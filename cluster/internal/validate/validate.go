// Package validate centralises input validation for cluster-facing
// seictl commands. Every side-effecting subcommand calls these helpers
// before any kubeconfig, AWS, or filesystem mutation.
//
// The regex shapes and the deny-list match docs/design/cluster-cli.md
// §Input validation. Image validation is policy-only — it does not
// resolve references to digests; that lives in internal/aws.
package validate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
)

const (
	AllowedRegistry   = "189176372795.dkr.ecr.us-east-2.amazonaws.com"
	AllowedRepoPrefix = "sei/"

	BenchSizeSmall  = "s"
	BenchSizeMedium = "m"
	BenchSizeLarge  = "l"

	BenchDurationMin = 1
	BenchDurationMax = 240

	maxK8sLabel = 63
)

var (
	aliasRe     = regexp.MustCompile(`^[a-z]([a-z0-9-]{0,28}[a-z0-9])?$`)
	nameRe      = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	namespaceRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

	aliasDenyList = map[string]struct{}{
		"kube-system":     {},
		"kube-public":     {},
		"kube-node-lease": {},
		"default":         {},
		"autobake":        {},
		"flux-system":     {},
		"istio-system":    {},
		"tide-agents":     {},
	}
)

func Alias(s string) *clioutput.Error {
	if !aliasRe.MatchString(s) {
		return clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAliasInvalid,
			"alias %q does not match %s", s, aliasRe)
	}
	if _, denied := aliasDenyList[s]; denied {
		return clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAliasInvalid,
			"alias %q is reserved", s)
	}
	return nil
}

// Name validates a benchmark name and enforces the combined
// `bench-<alias>-<name>` chain-id stays within K8s' 63-char label cap.
func Name(alias, name string) *clioutput.Error {
	if !nameRe.MatchString(name) {
		return clioutput.Newf(clioutput.ExitBench, clioutput.CatValidation,
			"name %q does not match %s", name, nameRe)
	}
	chainID := fmt.Sprintf("bench-%s-%s", alias, name)
	if len(chainID) > maxK8sLabel {
		return clioutput.Newf(clioutput.ExitBench, clioutput.CatValidation,
			"combined chain-id %q is %d chars, max %d", chainID, len(chainID), maxK8sLabel)
	}
	return nil
}

func Size(s string) *clioutput.Error {
	switch s {
	case BenchSizeSmall, BenchSizeMedium, BenchSizeLarge:
		return nil
	}
	return clioutput.Newf(clioutput.ExitBench, clioutput.CatValidation,
		"size %q is not one of s|m|l", s)
}

func DurationMinutes(n int) *clioutput.Error {
	if n < BenchDurationMin || n > BenchDurationMax {
		return clioutput.Newf(clioutput.ExitBench, clioutput.CatValidation,
			"duration %d minutes is out of range [%d, %d]", n, BenchDurationMin, BenchDurationMax)
	}
	return nil
}

// Namespace enforces RFC-1123 label shape. If alias is non-empty, also
// enforces the side-effecting-verb policy that the namespace equals
// `eng-<alias>`. Pass empty alias for read-only verbs.
func Namespace(ns, alias string) *clioutput.Error {
	if len(ns) > maxK8sLabel || !namespaceRe.MatchString(ns) {
		return clioutput.Newf(clioutput.ExitBench, clioutput.CatNamespacePolicy,
			"namespace %q is not a valid RFC-1123 label", ns)
	}
	if alias != "" {
		want := "eng-" + alias
		if ns != want {
			return clioutput.Newf(clioutput.ExitBench, clioutput.CatNamespacePolicy,
				"namespace must equal %q for engineer %q (got %q)", want, alias, ns)
		}
	}
	return nil
}

// Image enforces the registry policy (ECR-only, sei/* prefix, tag or
// digest required). Digest resolution lives in internal/aws.
func Image(ref string) *clioutput.Error {
	if ref == "" {
		return clioutput.New(clioutput.ExitBench, clioutput.CatImagePolicy,
			"image is required")
	}
	slash := strings.IndexByte(ref, '/')
	if slash < 0 {
		return clioutput.Newf(clioutput.ExitBench, clioutput.CatImagePolicy,
			"image %q is missing registry hostname", ref)
	}
	host := ref[:slash]
	if host != AllowedRegistry {
		return clioutput.Newf(clioutput.ExitBench, clioutput.CatImagePolicy,
			"image registry must be %s (got %s)", AllowedRegistry, host)
	}
	rest := ref[slash+1:]
	if !strings.HasPrefix(rest, AllowedRepoPrefix) {
		return clioutput.Newf(clioutput.ExitBench, clioutput.CatImagePolicy,
			"image repo must start with %q (got %q)", AllowedRepoPrefix, rest)
	}
	repoTail := rest[len(AllowedRepoPrefix):]
	if repoTail == "" {
		return clioutput.Newf(clioutput.ExitBench, clioutput.CatImagePolicy,
			"image %q is missing repo name after %q", ref, AllowedRepoPrefix)
	}
	if !strings.ContainsAny(repoTail, ":@") {
		return clioutput.Newf(clioutput.ExitBench, clioutput.CatImagePolicy,
			"image %q must specify a tag or digest", ref)
	}
	return nil
}
