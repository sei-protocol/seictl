// Package clioutput defines the JSON output envelope for cluster-facing
// seictl subcommands and the exit-code / category enums that are part of
// the public CLI contract.
//
// The shape here is the v2 MCP tool contract per docs/design/cluster-cli.md
// — bumping Version is a breaking change for the sei-platform-engineer skill.
package clioutput

import (
	"encoding/json"
	"fmt"
	"io"
)

const EnvelopeVersion = "v1"

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
	Kind    string          `json:"kind"`
	Version string          `json:"version"`
	Data    json.RawMessage `json:"data,omitempty"`
	Error   *ErrorBody      `json:"error,omitempty"`
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
	env := Envelope{Kind: kind, Version: EnvelopeVersion, Data: raw}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// EmitError writes an error envelope to w. Returns marshal errors only;
// callers should not propagate those back to the user.
func EmitError(w io.Writer, kind string, e *Error) error {
	env := Envelope{
		Kind:    kind,
		Version: EnvelopeVersion,
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
