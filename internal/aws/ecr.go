package aws

import (
	"context"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awscfg "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	"github.com/aws/aws-sdk-go-v2/service/ecr/types"

	"github.com/sei-protocol/seictl/internal/clioutput"
)

// ResolveDigest converts an ECR image reference (registry/repo:tag) into
// its sha256 digest. References already pinned to a digest are returned
// as-is without an ECR round-trip.
//
// The registry hostname is parsed for AWS account + region — the
// registry policy in internal/validate constrains it to a single
// account, but the hostname remains the source of truth so a future
// per-engineer-ECR rollout doesn't require touching this code.
func ResolveDigest(ctx context.Context, ref string) (string, *clioutput.Error) {
	host, repo, tag, digest, parseErr := parseImageRef(ref)
	if parseErr != nil {
		return "", parseErr
	}
	if digest != "" {
		return digest, nil
	}

	account, region, err := parseECRHost(host)
	if err != nil {
		return "", err
	}

	cfg, awsErr := awscfg.LoadDefaultConfig(ctx, awscfg.WithRegion(region))
	if awsErr != nil {
		return "", clioutput.Newf(clioutput.ExitBench, clioutput.CatImageResolution,
			"load AWS config: %v", awsErr)
	}
	out, awsErr := ecr.NewFromConfig(cfg).DescribeImages(ctx, &ecr.DescribeImagesInput{
		RegistryId:     awssdk.String(account),
		RepositoryName: awssdk.String(repo),
		ImageIds:       []types.ImageIdentifier{{ImageTag: awssdk.String(tag)}},
	})
	if awsErr != nil {
		return "", clioutput.Newf(clioutput.ExitBench, clioutput.CatImageResolution,
			"ecr describe-images %s:%s: %v", repo, tag, awsErr)
	}
	if len(out.ImageDetails) == 0 || out.ImageDetails[0].ImageDigest == nil {
		return "", clioutput.Newf(clioutput.ExitBench, clioutput.CatImageResolution,
			"ecr returned no digest for %s:%s", repo, tag)
	}
	return awssdk.ToString(out.ImageDetails[0].ImageDigest), nil
}

// parseImageRef splits a fully-qualified ECR ref into host, repo, tag,
// digest. Exactly one of (tag, digest) is populated. Caller must have
// already validated the registry policy via internal/validate.Image.
func parseImageRef(ref string) (host, repo, tag, digest string, err *clioutput.Error) {
	slash := strings.IndexByte(ref, '/')
	if slash < 0 {
		return "", "", "", "", clioutput.Newf(clioutput.ExitBench, clioutput.CatImageResolution,
			"image %q is missing registry hostname", ref)
	}
	host = ref[:slash]
	rest := ref[slash+1:]
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		digest = rest[at+1:]
		if !strings.HasPrefix(digest, "sha256:") {
			return "", "", "", "", clioutput.Newf(clioutput.ExitBench, clioutput.CatImageResolution,
				"digest %q must be sha256:...", digest)
		}
		return host, rest[:at], "", digest, nil
	}
	if colon := strings.IndexByte(rest, ':'); colon >= 0 {
		return host, rest[:colon], rest[colon+1:], "", nil
	}
	return "", "", "", "", clioutput.Newf(clioutput.ExitBench, clioutput.CatImageResolution,
		"image %q must specify a tag or digest", ref)
}

// parseECRHost extracts the AWS account and region from
// `<account>.dkr.ecr.<region>.amazonaws.com`.
func parseECRHost(host string) (account, region string, err *clioutput.Error) {
	parts := strings.Split(host, ".")
	if len(parts) != 6 || parts[1] != "dkr" || parts[2] != "ecr" || parts[4] != "amazonaws" || parts[5] != "com" {
		return "", "", clioutput.Newf(clioutput.ExitBench, clioutput.CatImageResolution,
			"hostname %q is not an ECR endpoint", host)
	}
	return parts[0], parts[3], nil
}

// AssertECRDigestRef returns an actionable error if ref is not a
// digest-pinned ECR reference. Used at render time to guarantee
// manifests never carry a tag.
func AssertECRDigestRef(ref string) error {
	_, _, _, digest, err := parseImageRef(ref)
	if err != nil {
		return fmt.Errorf("image %q is not a valid ECR ref: %s", ref, err.Message)
	}
	if digest == "" {
		return fmt.Errorf("image %q must be digest-pinned (got tag)", ref)
	}
	return nil
}
