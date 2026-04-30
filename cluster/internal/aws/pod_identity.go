package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
)

type PodIdentityArtifact struct {
	Kind          string // always "PodIdentityAssociation"
	AssociationID string
	RoleARN       string
	Action        string // "create" | "exists" | "would-create"
}

// PodIdentityBinding identifies one Pod Identity association uniquely
// via the (cluster, namespace, serviceAccount) tuple, plus the role to
// bind. This shape mirrors EKS's natural key.
type PodIdentityBinding struct {
	Cluster        string
	Namespace      string
	ServiceAccount string
	RoleARN        string
	Region         string
}

// EnsurePodIdentity creates the seictl SA association if it doesn't
// exist. EKS exposes no Get-by-tuple API, so we list-then-match.
// A pre-existing association bound to a different role is a hard
// failure — silently rebinding could grant the engineer access to
// the wrong S3 prefix.
func EnsurePodIdentity(ctx context.Context, b PodIdentityBinding, dryRun bool) (PodIdentityArtifact, *clioutput.Error) {
	cfg, err := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(b.Region))
	if err != nil {
		return PodIdentityArtifact{}, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"load AWS config: %v", err)
	}
	client := eks.NewFromConfig(cfg)

	existingID, existingRoleARN, err := findPodIdentity(ctx, client, b)
	if err != nil {
		return PodIdentityArtifact{}, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"list pod identity associations: %v", err)
	}
	if existingID != "" {
		if existingRoleARN != b.RoleARN {
			return PodIdentityArtifact{}, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
				"existing pod identity %s is bound to %s, want %s; manual remediation required",
				existingID, existingRoleARN, b.RoleARN)
		}
		return PodIdentityArtifact{
			Kind:          "PodIdentityAssociation",
			AssociationID: existingID,
			RoleARN:       existingRoleARN,
			Action:        "exists",
		}, nil
	}
	if dryRun {
		return PodIdentityArtifact{
			Kind:    "PodIdentityAssociation",
			RoleARN: b.RoleARN,
			Action:  "would-create",
		}, nil
	}

	out, cerr := client.CreatePodIdentityAssociation(ctx, &eks.CreatePodIdentityAssociationInput{
		ClusterName:    awssdk.String(b.Cluster),
		Namespace:      awssdk.String(b.Namespace),
		ServiceAccount: awssdk.String(b.ServiceAccount),
		RoleArn:        awssdk.String(b.RoleARN),
	})
	if cerr != nil {
		return PodIdentityArtifact{}, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"create pod identity association: %v", cerr)
	}
	return PodIdentityArtifact{
		Kind:          "PodIdentityAssociation",
		AssociationID: awssdk.ToString(out.Association.AssociationId),
		RoleARN:       awssdk.ToString(out.Association.RoleArn),
		Action:        "create",
	}, nil
}

// findPodIdentity returns the associationID + roleARN of the
// (cluster, namespace, sa) binding if one exists; empty strings if
// not. EKS guarantees at most one association per tuple, so we can
// take the first List result without further filtering.
func findPodIdentity(ctx context.Context, client *eks.Client, b PodIdentityBinding) (id, roleARN string, err error) {
	out, err := client.ListPodIdentityAssociations(ctx, &eks.ListPodIdentityAssociationsInput{
		ClusterName:    awssdk.String(b.Cluster),
		Namespace:      awssdk.String(b.Namespace),
		ServiceAccount: awssdk.String(b.ServiceAccount),
	})
	if err != nil {
		return "", "", err
	}
	for _, a := range out.Associations {
		desc, derr := client.DescribePodIdentityAssociation(ctx, &eks.DescribePodIdentityAssociationInput{
			ClusterName:   awssdk.String(b.Cluster),
			AssociationId: a.AssociationId,
		})
		if derr != nil {
			return "", "", derr
		}
		return awssdk.ToString(a.AssociationId), awssdk.ToString(desc.Association.RoleArn), nil
	}
	return "", "", nil
}
