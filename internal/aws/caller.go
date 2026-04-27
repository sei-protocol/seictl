// Package aws holds the AWS-side helpers used by cluster-facing seictl
// commands. Slice 2 lands GetCaller for `seictl context`; ECR digest
// resolution and IAM/Pod-Identity helpers land in subsequent slices.
package aws

import (
	"context"

	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/sei-protocol/seictl/internal/clioutput"
)

type Caller struct {
	Account      string
	Region       string
	PrincipalARN string
}

// GetCaller resolves the engineer's AWS principal via STS. Errors map to
// ExitOnboard / CatAWSCreateFailed; `seictl context` treats this as
// best-effort and renders empty AWS fields if the engineer is not
// SSO-authenticated, but the typed error is preserved for callers that
// need to fail closed (e.g. `onboard --apply`).
func GetCaller(ctx context.Context) (*Caller, *clioutput.Error) {
	cfg, err := awscfg.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"load AWS config: %v", err)
	}
	out, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed,
			"sts get-caller-identity: %v", err)
	}
	return &Caller{
		Account:      derefStr(out.Account),
		Region:       cfg.Region,
		PrincipalARN: derefStr(out.Arn),
	}, nil
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
