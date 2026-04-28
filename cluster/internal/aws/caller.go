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
// auth state, not a creation.
func GetCaller(ctx context.Context) (*Caller, *clioutput.Error) {
	cfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatAWSUnavailable,
			"load AWS config: %v", err)
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
