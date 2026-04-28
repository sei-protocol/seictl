package clioutput

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEmit_Success(t *testing.T) {
	type sample struct {
		Foo string `json:"foo"`
		Bar int    `json:"bar"`
	}

	var buf bytes.Buffer
	if err := Emit(&buf, "TestKind", sample{Foo: "hello", Bar: 42}); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Kind != "TestKind" {
		t.Errorf("kind: got %q, want %q", env.Kind, "TestKind")
	}
	if env.APIVersion != APIVersion {
		t.Errorf("apiVersion: got %q, want %q", env.APIVersion, APIVersion)
	}
	if env.Error != nil {
		t.Errorf("error body should be nil on success, got %+v", env.Error)
	}
	var got sample
	if err := json.Unmarshal(env.Data, &got); err != nil {
		t.Fatalf("data unmarshal: %v", err)
	}
	if got.Foo != "hello" || got.Bar != 42 {
		t.Errorf("data round-trip: got %+v, want {hello 42}", got)
	}
}

func TestEmitError_Round_Trip(t *testing.T) {
	var buf bytes.Buffer
	cliErr := New(ExitBench, CatImagePolicy, "image registry not allowed").WithDetail("got: docker.io/foo")
	if err := EmitError(&buf, KindBenchUpResult, cliErr); err != nil {
		t.Fatalf("EmitError returned error: %v", err)
	}

	var env Envelope
	if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Kind != KindBenchUpResult {
		t.Errorf("kind: got %q", env.Kind)
	}
	if env.APIVersion != APIVersion {
		t.Errorf("apiVersion: got %q", env.APIVersion)
	}
	if env.Error == nil {
		t.Fatalf("error body should be populated")
	}
	if env.Error.Code != ExitBench {
		t.Errorf("code: got %d, want %d", env.Error.Code, ExitBench)
	}
	if env.Error.Category != CatImagePolicy {
		t.Errorf("category: got %q", env.Error.Category)
	}
	if env.Error.Message != "image registry not allowed" {
		t.Errorf("message: got %q", env.Error.Message)
	}
	if env.Error.Detail != "got: docker.io/foo" {
		t.Errorf("detail: got %q", env.Error.Detail)
	}
	if len(env.Data) != 0 {
		t.Errorf("data should be empty on failure, got %s", env.Data)
	}
}

func TestError_ErrorString(t *testing.T) {
	tests := map[string]struct {
		err  *Error
		want string
	}{
		"no detail": {
			err:  New(ExitIdentity, CatMissing, "engineer.json not found"),
			want: "missing: engineer.json not found",
		},
		"with detail": {
			err:  New(ExitBench, CatImagePolicy, "registry not allowed").WithDetail("got: docker.io"),
			want: "image-policy: registry not allowed (got: docker.io)",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewf(t *testing.T) {
	e := Newf(ExitOnboard, CatAliasInvalid, "alias %q failed regex", "BAD")
	if !strings.Contains(e.Message, `"BAD"`) {
		t.Errorf("Newf did not format args: %q", e.Message)
	}
	if e.Code != ExitOnboard {
		t.Errorf("code: got %d", e.Code)
	}
}

func TestEnvelopeContract_Stable(t *testing.T) {
	// APIVersion and Kind constants are part of the public contract
	// for the sei-platform-engineer skill / future MCP layer. Pin them
	// so accidental renames fail loudly. Bumping is a breaking change
	// — ship `seictl.sei.io/v2` alongside, don't mutate v1.
	if APIVersion != "seictl.sei.io/v1" {
		t.Errorf("APIVersion: got %q, want %q", APIVersion, "seictl.sei.io/v1")
	}
	want := map[string]string{
		"KindContextResult": "ContextResult",
		"KindBenchUpResult": "BenchUpResult",
	}
	got := map[string]string{
		"KindContextResult": KindContextResult,
		"KindBenchUpResult": KindBenchUpResult,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q, want %q", k, got[k], v)
		}
	}
}

func TestExitCodes_Stable(t *testing.T) {
	// Exit codes are part of the CLI contract per LLD §Exit codes.
	// This test pins them so accidental renumbering fails loudly.
	want := map[string]int{
		"ExitSuccess":  0,
		"ExitUsage":    2,
		"ExitNotFound": 3,
		"ExitCluster":  4,
		"ExitRBAC":     5,
		"ExitBench":    10,
		"ExitOnboard":  20,
		"ExitIdentity": 40,
	}
	got := map[string]int{
		"ExitSuccess":  ExitSuccess,
		"ExitUsage":    ExitUsage,
		"ExitNotFound": ExitNotFound,
		"ExitCluster":  ExitCluster,
		"ExitRBAC":     ExitRBAC,
		"ExitBench":    ExitBench,
		"ExitOnboard":  ExitOnboard,
		"ExitIdentity": ExitIdentity,
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %d, want %d", k, got[k], v)
		}
	}
}
