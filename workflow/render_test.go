package workflow

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// writeConfigPatch writes a config-patch YAML fixture and returns its path.
func writeConfigPatch(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "patch.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config-patch fixture: %v", err)
	}
	return p
}

// evmSSSplitPatch is the STO-624 config patch: app.toml [state-store]
// evm-ss-split = true.
const evmSSSplitPatch = `
app.toml:
  state-store:
    evm-ss-split: true
`

func TestRender_StateSyncGolden(t *testing.T) {
	got, err := render(renderArgs{
		preset:      "state-sync",
		name:        "pacific-1-rpc-0-state-sync",
		namespace:   "pacific-1",
		target:      "pacific-1-rpc-0",
		configPatch: writeConfigPatch(t, evmSSSplitPatch),
		rpcServers:  []string{"a.example:26657", "b.example:26657"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}

	if k := got.GroupVersionKind().Kind; k != "SeiNodeTaskWorkflow" {
		t.Errorf("kind = %q; want SeiNodeTaskWorkflow", k)
	}
	if got.GetName() != "pacific-1-rpc-0-state-sync" {
		t.Errorf("name = %q; want pacific-1-rpc-0-state-sync", got.GetName())
	}
	if got.GetNamespace() != "pacific-1" {
		t.Errorf("namespace = %q; want pacific-1", got.GetNamespace())
	}

	recipe, _, _ := unstructured.NestedString(got.Object, "spec", "kind")
	if recipe != "StateSync" {
		t.Errorf("spec.kind = %q; want StateSync", recipe)
	}
	target, _, _ := unstructured.NestedString(got.Object, "spec", "target", "nodeRef", "name")
	if target != "pacific-1-rpc-0" {
		t.Errorf("spec.target.nodeRef.name = %q; want pacific-1-rpc-0", target)
	}

	servers, found, _ := unstructured.NestedStringSlice(got.Object, "spec", "stateSync", "rpcServers")
	if !found || len(servers) != 2 || servers[0] != "a.example:26657" || servers[1] != "b.example:26657" {
		t.Errorf("spec.stateSync.rpcServers = %v (found=%v); want [a.example:26657 b.example:26657]", servers, found)
	}

	// configPatch nests exactly file -> section -> {key: value}.
	split, found, err := unstructured.NestedBool(got.Object, "spec", "stateSync", "configPatch", "app.toml", "state-store", "evm-ss-split")
	if err != nil || !found {
		t.Fatalf("spec.stateSync.configPatch[app.toml][state-store][evm-ss-split] not found (found=%v err=%v)", found, err)
	}
	if !split {
		t.Errorf("evm-ss-split = %v; want true", split)
	}
}

// The minimal render (target only) still produces a valid StateSync workflow:
// stateSync present (CEL requires it) with no recipe params.
func TestRender_MinimalStateSync(t *testing.T) {
	got, err := render(renderArgs{preset: "state-sync", name: "n-state-sync", target: "n"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, found, _ := unstructured.NestedMap(got.Object, "spec", "stateSync"); !found {
		t.Error("spec.stateSync must be present for kind=StateSync (CEL requires it)")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(got.Object, "spec", "stateSync", "configPatch"); found {
		t.Error("spec.stateSync.configPatch should be absent when --config-patch is not given")
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(got.Object, "spec", "stateSync", "rpcServers"); found {
		t.Error("spec.stateSync.rpcServers should be absent when --rpc-servers is not given")
	}
}

func TestRender_MissingTargetIsUsageError(t *testing.T) {
	_, err := render(renderArgs{preset: "state-sync", name: "n-state-sync"})
	if err == nil {
		t.Fatal("expected a usage error when --target is missing")
	}
}

func TestRender_UnknownPreset(t *testing.T) {
	_, err := render(renderArgs{preset: "nope", name: "n", target: "n"})
	if err == nil {
		t.Fatal("expected an error for an unknown preset")
	}
}

func TestRender_RequirePhaseOverride(t *testing.T) {
	got, err := render(renderArgs{preset: "state-sync", name: "n-state-sync", target: "n", requirePhase: "Running"})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	rp, _, _ := unstructured.NestedString(got.Object, "spec", "target", "requirePhase")
	if rp != "Running" {
		t.Errorf("spec.target.requirePhase = %q; want Running", rp)
	}
}
