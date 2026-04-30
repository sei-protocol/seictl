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

// Error is a validation failure carrying the CLI category and message.
// Callers attach the exit code at the call site via ExitWith — validation
// owns the WHAT (this input is invalid, here is why); the caller owns the
// WHEN/WHERE (which verb is failing, hence which exit code).
type Error struct {
	Category string
	Message  string
}

func (e *Error) Error() string { return e.Category + ": " + e.Message }

func (e *Error) ExitWith(code int) *clioutput.Error {
	return clioutput.New(code, e.Category, e.Message)
}

func newErr(category, format string, args ...any) *Error {
	return &Error{Category: category, Message: fmt.Sprintf(format, args...)}
}

func Alias(s string) *Error {
	if !aliasRe.MatchString(s) {
		return newErr(clioutput.CatAliasInvalid, "alias %q does not match %s", s, aliasRe)
	}
	if _, denied := aliasDenyList[s]; denied {
		return newErr(clioutput.CatAliasInvalid, "alias %q is reserved", s)
	}
	return nil
}

// Name validates a benchmark name and enforces the combined
// `bench-<alias>-<name>` chain-id stays within K8s' 63-char label cap.
func Name(alias, name string) *Error {
	if !nameRe.MatchString(name) {
		return newErr(clioutput.CatValidation, "name %q does not match %s", name, nameRe)
	}
	chainID := fmt.Sprintf("bench-%s-%s", alias, name)
	if len(chainID) > maxK8sLabel {
		return newErr(clioutput.CatValidation,
			"combined chain-id %q is %d chars, max %d", chainID, len(chainID), maxK8sLabel)
	}
	return nil
}

func Size(s string) *Error {
	switch s {
	case BenchSizeSmall, BenchSizeMedium, BenchSizeLarge:
		return nil
	}
	return newErr(clioutput.CatValidation, "size %q is not one of s|m|l", s)
}

func DurationMinutes(n int) *Error {
	if n < BenchDurationMin || n > BenchDurationMax {
		return newErr(clioutput.CatValidation,
			"duration %d minutes is out of range [%d, %d]", n, BenchDurationMin, BenchDurationMax)
	}
	return nil
}

func Validators(n int) *Error {
	if n < 1 || n > 21 {
		return newErr(clioutput.CatValidation,
			"validator count %d is out of range [1, 21]", n)
	}
	return nil
}

func RPCReplicas(n int) *Error {
	if n < 1 || n > 21 {
		return newErr(clioutput.CatValidation,
			"rpc replica count %d is out of range [1, 21]", n)
	}
	return nil
}

// ChainID enforces a non-empty bech32-friendly chain identifier.
// Engineers usually pass the value emitted by `chain up`'s envelope.
func ChainID(s string) *Error {
	if s == "" {
		return newErr(clioutput.CatValidation, "chain-id must not be empty")
	}
	return nil
}

// Namespace enforces RFC-1123 label shape. Convention enforcement
// (e.g. namespace = "eng-<alias>" for engineer cells) lives in
// `seictl onboard` — verbs read namespace verbatim from config so
// non-engineer flows can operate against arbitrary namespaces.
func Namespace(ns string) *Error {
	if len(ns) > maxK8sLabel || !namespaceRe.MatchString(ns) {
		return newErr(clioutput.CatNamespacePolicy,
			"namespace %q is not a valid RFC-1123 label", ns)
	}
	return nil
}

// Image enforces the registry policy (ECR-only, sei/* prefix, tag or
// digest required). Digest resolution lives in internal/aws.
func Image(ref string) *Error {
	if ref == "" {
		return newErr(clioutput.CatImagePolicy, "image is required")
	}
	slash := strings.IndexByte(ref, '/')
	if slash < 0 {
		return newErr(clioutput.CatImagePolicy, "image %q is missing registry hostname", ref)
	}
	host := ref[:slash]
	if host != AllowedRegistry {
		return newErr(clioutput.CatImagePolicy,
			"image registry must be %s (got %s)", AllowedRegistry, host)
	}
	rest := ref[slash+1:]
	if !strings.HasPrefix(rest, AllowedRepoPrefix) {
		return newErr(clioutput.CatImagePolicy,
			"image repo must start with %q (got %q)", AllowedRepoPrefix, rest)
	}
	repoTail := rest[len(AllowedRepoPrefix):]
	if repoTail == "" {
		return newErr(clioutput.CatImagePolicy,
			"image %q is missing repo name after %q", ref, AllowedRepoPrefix)
	}
	if !strings.ContainsAny(repoTail, ":@") {
		return newErr(clioutput.CatImagePolicy, "image %q must specify a tag or digest", ref)
	}
	return nil
}
