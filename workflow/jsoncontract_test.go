package workflow

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"testing"
)

// runJQ pipes objJSON through `jq <args>` and returns stdout + exit-ok. Skips
// when jq is absent so the suite stays green in minimal environments.
func runJQ(t *testing.T, objJSON []byte, args ...string) (string, bool) {
	t.Helper()
	if _, err := exec.LookPath("jq"); err != nil {
		t.Skip("jq not on PATH; skipping JSON-contract jq assertion")
	}
	cmd := exec.Command("jq", args...)
	cmd.Stdin = bytes.NewReader(objJSON)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.String(), err == nil
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}

// The rendered CR must satisfy the SeiNodeTaskWorkflow schema's locked field
// paths: spec.kind, spec.target.nodeRef.name, spec.stateSync.{configPatch,
// rpcServers}. Asserted through jq on the exact JSON the apiserver receives.
func TestJSONContract_StateSyncRender(t *testing.T) {
	obj, err := render(renderArgs{
		preset:      "state-sync",
		name:        "n-state-sync",
		namespace:   "sei",
		target:      "chaos-rpc-0",
		configPatch: writeConfigPatch(t, evmSSSplitPatch),
		rpcServers:  []string{"a:26657", "b:26657"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	j, err := json.Marshal(obj.Object)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	cases := []struct {
		name   string
		filter string
		want   string
	}{
		{"spec.kind", ".spec.kind", "StateSync"},
		{"spec.target.nodeRef.name", ".spec.target.nodeRef.name", "chaos-rpc-0"},
		{"configPatch nested value", `.spec.stateSync.configPatch."app.toml"."state-store"."evm-ss-split"`, "true"},
		{"rpcServers[0]", ".spec.stateSync.rpcServers[0]", "a:26657"},
		{"rpcServers length", ".spec.stateSync.rpcServers | length", "2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, ok := runJQ(t, j, "-er", tc.filter)
			if !ok {
				t.Fatalf("jq -e failed for %s (path must be present)", tc.filter)
			}
			if got := trimNL(out); got != tc.want {
				t.Errorf("%s = %q; want %q", tc.filter, got, tc.want)
			}
		})
	}
}

// workflowStatusFixture is a Running workflow whose status carries the plan the
// watch path reads (status.phase + status.plan.tasks) — the exact shape
// `workflow watch`/`state-sync` consume.
func workflowStatusFixture() map[string]interface{} {
	return map[string]interface{}{
		"apiVersion": "sei.io/v1alpha1",
		"kind":       "SeiNodeTaskWorkflow",
		"metadata":   map[string]interface{}{"name": "chaos-rpc-0-state-sync", "namespace": "sei"},
		"spec": map[string]interface{}{
			"kind":   "StateSync",
			"target": map[string]interface{}{"nodeRef": map[string]interface{}{"name": "chaos-rpc-0"}},
		},
		"status": map[string]interface{}{
			"phase": "Running",
			"plan": map[string]interface{}{
				"phase": "Active",
				"tasks": []interface{}{
					map[string]interface{}{"type": "mark-not-ready", "status": "Complete"},
					map[string]interface{}{"type": "reset-data", "status": "Running"},
				},
			},
		},
	}
}

// The watch consumer reads .status.phase and iterates .status.plan.tasks[] for
// per-step progress; lock both paths.
func TestJSONContract_WorkflowStatusForWatch(t *testing.T) {
	j, err := json.Marshal(workflowStatusFixture())
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	if out, ok := runJQ(t, j, "-er", ".status.phase"); !ok || trimNL(out) != "Running" {
		t.Errorf(".status.phase = %q (ok=%v); want Running", trimNL(out), ok)
	}
	if out, ok := runJQ(t, j, "-er", ".status.plan.tasks | length"); !ok || trimNL(out) != "2" {
		t.Errorf(".status.plan.tasks length = %q (ok=%v); want 2", trimNL(out), ok)
	}
	if out, ok := runJQ(t, j, "-er", ".status.plan.tasks[1].type"); !ok || trimNL(out) != "reset-data" {
		t.Errorf(".status.plan.tasks[1].type = %q (ok=%v); want reset-data", trimNL(out), ok)
	}
}
