// Package aws holds AWS-side helpers used by cluster-facing seictl
// commands.
package aws

import (
	"context"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
)

type Caller struct {
	Account      string
	Region       string
	PrincipalARN string
}

// GetCaller resolves the active AWS principal via STS GetCallerIdentity.
// Errors map to ExitIdentity / CatAWSUnavailable — this is a read of
// auth state, not a creation. When the failure is "no credentials
// resolvable" (vs e.g. permission denied), the message is prefixed with
// CredsHint() so the engineer sees the specific remediation.
func GetCaller(ctx context.Context) (*Caller, *clioutput.Error) {
	cfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatAWSUnavailable,
			"%s (load AWS config: %v)", CredsHint(), err)
	}
	if _, credsErr := cfg.Credentials.Retrieve(ctx); credsErr != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatAWSUnavailable,
			"%s (%v)", CredsHint(), credsErr)
	}
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, nil)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatAWSUnavailable,
			"sts get-caller-identity: %v", err)
	}
	return &Caller{
		Account:      awssdk.ToString(out.Account),
		Region:       cfg.Region,
		PrincipalARN: awssdk.ToString(out.Arn),
	}, nil
}
