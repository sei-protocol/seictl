// Package clioutput defines the JSON output envelope for cluster-facing
// seictl subcommands and the exit-code / category enums that are part of
// the public CLI contract.
//
// The envelope mirrors Kubernetes `metav1.TypeMeta` (apiVersion + kind)
// so the Sei platform engineer audience reads results in the same shape
// they read CRDs. The same shape becomes the v2 MCP tool contract.
//
// Two-way doors:
//   - Adding new kinds → additive, never breaks consumers.
//   - Adding new fields to a kind → JSON consumers ignore unknown fields.
//   - Adding new error categories → already non-breaking.
//   - Breaking schema change → ship `seictl.sei.io/v2` alongside v1
//     (the standard K8s API-versioning migration).
package clioutput

import (
	"encoding/json"
	"fmt"
	"io"
)

// APIVersion is the stable group/version string emitted on every envelope.
// Breaking changes ship as `seictl.sei.io/v2` alongside v1, not as
// mutations to v1.
const APIVersion = "seictl.sei.io/v1"

// Kinds emitted by cluster-facing verbs. New verbs add a constant here
// rather than open-coding the string at the call site.
const (
	KindContextResult = "ContextResult"
	KindBenchUpResult = "BenchUpResult"
)

const (
	ExitSuccess  = 0
	ExitUsage    = 2
	ExitNotFound = 3
	ExitCluster  = 4
	ExitRBAC     = 5
	ExitBench    = 10
	ExitOnboard  = 20
	ExitIdentity = 40
)

const (
	CatImagePolicy     = "image-policy"
	CatImageResolution = "image-resolution"
	CatValidation      = "validation"
	CatNamespacePolicy = "namespace-policy"
	CatApplyFailed     = "apply-failed"
	CatNameCollision   = "name-collision"
	CatFinalizerStuck  = "finalizer-stuck"
	CatTemplateRender  = "template-render"

	CatAliasInvalid        = "alias-invalid"
	CatPlatformRepoMissing = "platform-repo-missing"
	CatWorkingTreeDirty    = "working-tree-dirty"
	CatGHUnauthenticated   = "gh-unauthenticated"
	CatPRCreateFailed      = "pr-create-failed"
	CatAWSCreateFailed     = "aws-create-failed"

	CatMalformed       = "malformed"
	CatMissing         = "missing"
	CatKubeconfigParse = "kubeconfig-parse"
	CatPermsLoose      = "perms-loose"
	CatAWSUnavailable  = "aws-unavailable"
)

type Envelope struct {
	APIVersion string          `json:"apiVersion"`
	Kind       string          `json:"kind"`
	Data       json.RawMessage `json:"data,omitempty"`
	Error      *ErrorBody      `json:"error,omitempty"`
}

type ErrorBody struct {
	Code     int    `json:"code"`
	Category string `json:"category"`
	Message  string `json:"message"`
	Detail   string `json:"detail,omitempty"`
}

// Error is the CLI-side typed failure. Carries enough to populate ErrorBody
// and choose the process exit code. Implements error.
type Error struct {
	Code     int
	Category string
	Message  string
	Detail   string
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Category, e.Message, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Category, e.Message)
}

func New(code int, category, message string) *Error {
	return &Error{Code: code, Category: category, Message: message}
}

func Newf(code int, category, format string, args ...any) *Error {
	return &Error{Code: code, Category: category, Message: fmt.Sprintf(format, args...)}
}

func (e *Error) WithDetail(detail string) *Error {
	e.Detail = detail
	return e
}

// Emit writes a success envelope to w. data is marshalled into the
// envelope's Data field.
func Emit(w io.Writer, kind string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshalling %s data: %w", kind, err)
	}
	env := Envelope{APIVersion: APIVersion, Kind: kind, Data: raw}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// EmitError writes an error envelope to w. Returns marshal errors only;
// callers should not propagate those back to the user.
func EmitError(w io.Writer, kind string, e *Error) error {
	env := Envelope{
		APIVersion: APIVersion,
		Kind:       kind,
		Error: &ErrorBody{
			Code:     e.Code,
			Category: e.Category,
			Message:  e.Message,
			Detail:   e.Detail,
		},
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}
