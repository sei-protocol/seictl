// IAM artifacts seictl creates land under path /seictl/ for IAM-console
// discoverability (`aws iam list-policies --path-prefix /seictl/`) and
// for scoped-down caller policies (e.g. `iam:PassRole` conditioned on
// `Resource: arn:aws:iam::*:role/seictl/*`). Per-engineer policies and
// roles share their name — harbor-workload-eng-<alias> — and are
// tagged with ManagedBy=seictl + Engineer=<alias> so drift on re-run
// is detectable.

package aws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
)

const (
	IAMPath                    = "/seictl/"
	validationResultsBucket    = "harbor-validation-results"
	validationResultsBucketARN = "arn:aws:s3:::" + validationResultsBucket
)

type EngineerScope struct {
	Account string
	Region  string
	Cluster string
	Alias   string
}

func (e EngineerScope) PolicyName() string {
	return fmt.Sprintf("%s-workload-eng-%s", e.Cluster, e.Alias)
}

func (e EngineerScope) RoleName() string {
	return e.PolicyName()
}

func (e EngineerScope) PolicyARN() string {
	return fmt.Sprintf("arn:aws:iam::%s:policy%s%s", e.Account, IAMPath, e.PolicyName())
}

func (e EngineerScope) RoleARN() string {
	return fmt.Sprintf("arn:aws:iam::%s:role%s%s", e.Account, IAMPath, e.RoleName())
}

type IAMArtifact struct {
	Kind   string // "Policy" | "Role" | "Attachment"
	ARN    string
	Action string // "create" | "exists" | "would-create"
}

// ProvisionIAM is idempotent: re-running on a fully-onboarded engineer
// returns all "exists" actions and performs no mutation.
func ProvisionIAM(ctx context.Context, scope EngineerScope, dryRun bool) ([]IAMArtifact, *clioutput.Error) {
	cfg, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(scope.Region))
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"load AWS config: %v", err)
	}
	client := iam.NewFromConfig(cfg)

	policyDoc, err := json.Marshal(seictlPolicyDocument(scope))
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"marshal policy doc: %v", err)
	}
	trustDoc, err := json.Marshal(podIdentityTrustPolicy(scope))
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"marshal trust policy: %v", err)
	}

	var artifacts []IAMArtifact
	policyArt, perr := ensurePolicy(ctx, client, scope, string(policyDoc), dryRun)
	if perr != nil {
		return artifacts, perr
	}
	artifacts = append(artifacts, policyArt)

	roleArt, rerr := ensureRole(ctx, client, scope, string(trustDoc), dryRun)
	if rerr != nil {
		return artifacts, rerr
	}
	artifacts = append(artifacts, roleArt)

	attArt, aerr := ensureAttachment(ctx, client, scope, dryRun)
	if aerr != nil {
		return artifacts, aerr
	}
	artifacts = append(artifacts, attArt)

	return artifacts, nil
}

func ensurePolicy(ctx context.Context, c *iam.Client, scope EngineerScope, doc string, dryRun bool) (IAMArtifact, *clioutput.Error) {
	arn := scope.PolicyARN()
	got, err := c.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: awssdk.String(arn)})
	if err == nil && got != nil {
		return IAMArtifact{Kind: "Policy", ARN: arn, Action: "exists"}, nil
	}
	if !isNotFound(err) {
		return IAMArtifact{}, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"get policy %s: %v", arn, err)
	}
	if dryRun {
		return IAMArtifact{Kind: "Policy", ARN: arn, Action: "would-create"}, nil
	}
	out, cerr := c.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     awssdk.String(scope.PolicyName()),
		Path:           awssdk.String(IAMPath),
		PolicyDocument: awssdk.String(doc),
		Description:    awssdk.String(fmt.Sprintf("seictl-managed: permissions for engineer %s", scope.Alias)),
		Tags:           seictlTags(scope),
	})
	if cerr != nil {
		return IAMArtifact{}, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"create policy %s: %v", scope.PolicyName(), cerr)
	}
	return IAMArtifact{Kind: "Policy", ARN: awssdk.ToString(out.Policy.Arn), Action: "create"}, nil
}

func ensureRole(ctx context.Context, c *iam.Client, scope EngineerScope, trust string, dryRun bool) (IAMArtifact, *clioutput.Error) {
	got, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: awssdk.String(scope.RoleName())})
	if err == nil && got != nil {
		return IAMArtifact{Kind: "Role", ARN: awssdk.ToString(got.Role.Arn), Action: "exists"}, nil
	}
	if !isNotFound(err) {
		return IAMArtifact{}, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"get role %s: %v", scope.RoleName(), err)
	}
	if dryRun {
		return IAMArtifact{Kind: "Role", ARN: scope.RoleARN(), Action: "would-create"}, nil
	}
	out, cerr := c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 awssdk.String(scope.RoleName()),
		Path:                     awssdk.String(IAMPath),
		AssumeRolePolicyDocument: awssdk.String(trust),
		Description:              awssdk.String(fmt.Sprintf("seictl-managed: Pod Identity role for workload-service-account SA, engineer %s", scope.Alias)),
		Tags:                     seictlTags(scope),
	})
	if cerr != nil {
		return IAMArtifact{}, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"create role %s: %v", scope.RoleName(), cerr)
	}
	return IAMArtifact{Kind: "Role", ARN: awssdk.ToString(out.Role.Arn), Action: "create"}, nil
}

func ensureAttachment(ctx context.Context, c *iam.Client, scope EngineerScope, dryRun bool) (IAMArtifact, *clioutput.Error) {
	listOut, err := c.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{
		RoleName: awssdk.String(scope.RoleName()),
	})
	if err == nil && listOut != nil {
		for _, p := range listOut.AttachedPolicies {
			if awssdk.ToString(p.PolicyArn) == scope.PolicyARN() {
				return IAMArtifact{Kind: "Attachment", ARN: scope.PolicyARN(), Action: "exists"}, nil
			}
		}
	} else if err != nil && !isNotFound(err) {
		return IAMArtifact{}, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"list role attachments: %v", err)
	}
	if dryRun {
		return IAMArtifact{Kind: "Attachment", ARN: scope.PolicyARN(), Action: "would-create"}, nil
	}
	_, aerr := c.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{
		RoleName:  awssdk.String(scope.RoleName()),
		PolicyArn: awssdk.String(scope.PolicyARN()),
	})
	if aerr != nil {
		return IAMArtifact{}, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"attach role policy: %v", aerr)
	}
	return IAMArtifact{Kind: "Attachment", ARN: scope.PolicyARN(), Action: "create"}, nil
}

func seictlTags(scope EngineerScope) []iamtypes.Tag {
	return []iamtypes.Tag{
		{Key: awssdk.String("ManagedBy"), Value: awssdk.String("seictl")},
		{Key: awssdk.String("Engineer"), Value: awssdk.String(scope.Alias)},
		{Key: awssdk.String("Component"), Value: awssdk.String("workload")},
		{Key: awssdk.String("Cluster"), Value: awssdk.String(scope.Cluster)},
	}
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NoSuchEntity", "NoSuchEntityException", "NotFound":
			return true
		}
	}
	return false
}

func seictlPolicyDocument(scope EngineerScope) map[string]any {
	prefix := fmt.Sprintf("eng-%s/*", scope.Alias)
	return map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Sid":      "ResultsListBucket",
				"Effect":   "Allow",
				"Action":   []string{"s3:ListBucket"},
				"Resource": validationResultsBucketARN,
				"Condition": map[string]any{
					"StringLike": map[string]any{"s3:prefix": []string{prefix}},
				},
			},
			{
				"Sid":      "ResultsWriteScopedToEngineerPrefix",
				"Effect":   "Allow",
				"Action":   []string{"s3:PutObject"},
				"Resource": fmt.Sprintf("%s/%s", validationResultsBucketARN, prefix),
			},
		},
	}
}

// podIdentityTrustPolicy uses Pod Identity's pods.eks.amazonaws.com
// principal. sts:TagSession is required (Pod Identity propagates pod
// metadata as session tags). SourceAccount + SourceArn are
// confused-deputy guards.
func podIdentityTrustPolicy(scope EngineerScope) map[string]any {
	sourceArn := fmt.Sprintf("arn:aws:eks:%s:%s:podidentityassociation/%s/*",
		scope.Region, scope.Account, scope.Cluster)
	return map[string]any{
		"Version": "2012-10-17",
		"Statement": []map[string]any{
			{
				"Sid":       "EKSPodIdentity",
				"Effect":    "Allow",
				"Principal": map[string]any{"Service": "pods.eks.amazonaws.com"},
				"Action":    []string{"sts:AssumeRole", "sts:TagSession"},
				"Condition": map[string]any{
					"StringEquals": map[string]any{"aws:SourceAccount": scope.Account},
					"ArnLike":      map[string]any{"aws:SourceArn": sourceArn},
				},
			},
		},
	}
}
