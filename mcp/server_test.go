package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/sei-protocol/seictl/chaos"
)

func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := NewServer().Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	return res
}

func text(res *mcp.CallToolResult) string {
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func manifest(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %s", text(res))
	}
	var out ManifestOutput
	decodeStructured(t, res, &out)
	return out.Manifest
}

func decodeStructured(t *testing.T, res *mcp.CallToolResult, into any) {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("re-marshal structured output: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode structured output: %v\n%s", err, raw)
	}
}

// statusReason decodes the metav1.Status envelope an errored tool returns.
func statusReason(t *testing.T, res *mcp.CallToolResult) (reason, message string) {
	t.Helper()
	if !res.IsError {
		t.Fatalf("expected tool error, got success: %s", text(res))
	}
	var st struct {
		Reason  string `json:"reason"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(text(res)), &st); err != nil {
		t.Fatalf("error content is not a Status envelope: %v\n%s", err, text(res))
	}
	return st.Reason, st.Message
}

func TestListTools(t *testing.T) {
	cs := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		got[tool.Name] = tool
	}
	for _, want := range []string{"chaos_list", "chaos_render", "bench_render", "network_render", "node_render"} {
		tool, ok := got[want]
		if !ok {
			t.Errorf("tool %q not registered", want)
			continue
		}
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no input schema", want)
		}
	}
	if len(got) != 5 {
		t.Errorf("registered %d tools, want 5", len(got))
	}
	schema, _ := json.Marshal(got["chaos_render"].InputSchema)
	for _, req := range []string{`"fault"`, `"chainId"`, `"runId"`, `"namespace"`} {
		if !strings.Contains(string(schema), req) {
			t.Errorf("chaos_render schema missing %s:\n%s", req, schema)
		}
	}
}

func TestChaosList(t *testing.T) {
	cs := connect(t)
	res := call(t, cs, "chaos_list", nil)
	if res.IsError {
		t.Fatalf("chaos_list errored: %s", text(res))
	}
	var out ChaosListOutput
	decodeStructured(t, res, &out)
	want := chaos.Catalog()
	if len(out.Faults) != len(want) || len(want) == 0 {
		t.Fatalf("got %d faults, want %d", len(out.Faults), len(want))
	}
	for i := range want {
		if out.Faults[i] != want[i] {
			t.Errorf("fault %d: got %+v, want %+v", i, out.Faults[i], want[i])
		}
	}
}

func TestChaosRender(t *testing.T) {
	cs := connect(t)
	base := map[string]any{"chainId": "bench-a", "runId": "run1", "namespace": "eng-bob"}
	with := func(kv ...string) map[string]any {
		m := map[string]any{}
		for k, v := range base {
			m[k] = v
		}
		for i := 0; i < len(kv); i += 2 {
			m[kv[i]] = kv[i+1]
		}
		return m
	}

	t.Run("duration fault renders", func(t *testing.T) {
		out := manifest(t, call(t, cs, "chaos_render", with("fault", "network-latency", "duration", "2m")))
		for _, want := range []string{"kind: NetworkChaos", "name: network-latency-run1", "sei.io/harness-run: \"run1\"", "namespace: eng-bob"} {
			if !strings.Contains(out, want) {
				t.Errorf("manifest missing %q:\n%s", want, out)
			}
		}
	})
	t.Run("one-shot fault renders without duration", func(t *testing.T) {
		out := manifest(t, call(t, cs, "chaos_render", with("fault", "pod-failure")))
		if !strings.Contains(out, "kind: PodChaos") {
			t.Errorf("manifest:\n%s", out)
		}
	})

	failures := []struct {
		name string
		args map[string]any
		msg  string
	}{
		{"unknown fault", with("fault", "meteor", "duration", "1m"), "meteor"},
		{"one-shot with duration", with("fault", "pod-failure", "duration", "1m"), "one-shot"},
		{"duration missing", with("fault", "network-latency"), "duration"},
		{"duration unitless", with("fault", "network-latency", "duration", "2"), "missing unit"},
		{"duration zero", with("fault", "network-latency", "duration", "0s"), "positive"},
		{"duration negative", with("fault", "network-latency", "duration", "-1m"), "positive"},
		{"uppercase run id", with("fault", "network-latency", "duration", "1m", "runId", "Run1"), "run-id"},
		{"dotted chain id", with("fault", "network-latency", "duration", "1m", "chainId", "bench.a"), "chain-id"},
		{"run id too long", with("fault", "network-latency", "duration", "1m", "runId", strings.Repeat("r", 60)), "63"},
	}
	for _, tc := range failures {
		t.Run(tc.name, func(t *testing.T) {
			reason, msg := statusReason(t, call(t, cs, "chaos_render", tc.args))
			if reason != "BadRequest" {
				t.Errorf("reason = %q, want BadRequest", reason)
			}
			if !strings.Contains(msg, tc.msg) {
				t.Errorf("message %q does not mention %q", msg, tc.msg)
			}
		})
	}
}

func TestBenchRender(t *testing.T) {
	cs := connect(t)
	ok := map[string]any{
		"runId": "run1", "chainId": "bench-a", "image": "ghcr.io/x/seiload@sha256:abc",
		"profileConfigMap": "seiload-profile-run1", "durationMinutes": 10, "namespace": "eng-bob",
	}
	out := manifest(t, call(t, cs, "bench_render", ok))
	for _, want := range []string{"kind: Job", "name: seiload-run1", "sei.io/harness-run: \"run1\"", "seiload-profile-run1"} {
		if !strings.Contains(out, want) {
			t.Errorf("manifest missing %q:\n%s", want, out)
		}
	}

	bad := map[string]any{}
	for k, v := range ok {
		bad[k] = v
	}
	bad["durationMinutes"] = 0
	reason, msg := statusReason(t, call(t, cs, "bench_render", bad))
	if reason != "BadRequest" || !strings.Contains(msg, "positive") {
		t.Errorf("zero duration: reason=%q msg=%q", reason, msg)
	}
}

func TestNetworkAndNodeRender(t *testing.T) {
	cs := connect(t)

	out := manifest(t, call(t, cs, "network_render", map[string]any{
		"preset": "genesis-chain", "name": "bench-a", "namespace": "eng-bob", "chainId": "bench-a",
		"image": "ghcr.io/sei-protocol/sei:v1", "replicas": 4, "consensusEngine": "Autobahn", "evmOnly": true,
		"configValue": []string{"app.toml:giga_executor.enabled=true"},
	}))
	for _, want := range []string{"kind: SeiNetwork", "name: bench-a", "engine: Autobahn", "evmOnly: true", "replicas: 4", "giga_executor.enabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("network manifest missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "status:") {
		t.Errorf("rendered manifest carries status:\n%s", out)
	}

	reason, _ := statusReason(t, call(t, cs, "network_render", map[string]any{
		"preset": "genesis-chain", "name": "bench-a", "chainId": "bench-a", "image": "img", "evmOnly": true,
	}))
	if reason != "BadRequest" {
		t.Errorf("evmOnly without Autobahn: reason=%q", reason)
	}

	out = manifest(t, call(t, cs, "node_render", map[string]any{
		"preset": "rpc", "name": "bench-a-rpc-0", "namespace": "eng-bob", "chainId": "bench-a",
		"image": "ghcr.io/sei-protocol/sei:v1", "network": "bench-a",
	}))
	for _, want := range []string{"kind: SeiNode", "name: bench-a-rpc-0", "sei.io/seinetwork: bench-a"} {
		if !strings.Contains(out, want) {
			t.Errorf("node manifest missing %q:\n%s", want, out)
		}
	}

	reason, _ = statusReason(t, call(t, cs, "node_render", map[string]any{"preset": "nope", "name": "n"}))
	if reason != "BadRequest" {
		t.Errorf("unknown preset: reason=%q", reason)
	}
}
