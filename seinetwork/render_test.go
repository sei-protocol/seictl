package seinetwork

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// T1 — network apply golden render: genesis-chain preset + the full flag set.
func TestRender_GenesisChainGolden(t *testing.T) {
	got, err := render(renderArgs{
		preset:           "genesis-chain",
		name:             "crater-lake-1",
		namespace:        "nightly",
		chainID:          "c1",
		image:            "i:1",
		replicas:         4,
		hasReps:          true,
		genesisAccounts:  []string{"a:100usei"},
		genesisOverrides: []string{"staking.params.unbonding_time=600s"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got.GroupVersionKind().Kind != "SeiNetwork" {
		t.Errorf("kind = %q; want SeiNetwork", got.GroupVersionKind().Kind)
	}
	if got.GetName() != "crater-lake-1" || got.GetNamespace() != "nightly" {
		t.Errorf("identity = %s/%s; want nightly/crater-lake-1", got.GetNamespace(), got.GetName())
	}
	// SeiNetwork has NO top-level chainId nor template — only spec.genesis.chainId.
	chainID, _, _ := unstructured.NestedString(got.Object, "spec", "genesis", "chainId")
	if chainID != "c1" {
		t.Errorf("spec.genesis.chainId = %q; want c1", chainID)
	}
	if _, found, _ := unstructured.NestedMap(got.Object, "spec", "template"); found {
		t.Errorf("spec.template present; SeiNetwork is not a template wrapper")
	}
	image, _, _ := unstructured.NestedString(got.Object, "spec", "image")
	if image != "i:1" {
		t.Errorf("spec.image = %q; want i:1", image)
	}
	replicas, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas")
	if replicas != 4 {
		t.Errorf("spec.replicas = %d; want 4", replicas)
	}
	accounts, found, _ := unstructured.NestedSlice(got.Object, "spec", "genesis", "accounts")
	if !found || len(accounts) != 1 {
		t.Fatalf("spec.genesis.accounts = %v; want 1 entry", accounts)
	}
	a0 := accounts[0].(map[string]interface{})
	if a0["address"] != "a" || a0["balance"] != "100usei" {
		t.Errorf("accounts[0] = %v; want {a, 100usei}", a0)
	}
	overrides, _, _ := unstructured.NestedMap(got.Object, "spec", "genesis", "overrides")
	if overrides["staking.params.unbonding_time"] != "600s" {
		t.Errorf("genesis override not written: %v", overrides)
	}
	// configOverrides from the preset survives.
	co, _, _ := unstructured.NestedStringMap(got.Object, "spec", "configOverrides")
	if co["network.rpc.pprof_listen_address"] != "0.0.0.0:6060" {
		t.Errorf("preset configOverrides lost: %v", co)
	}
	if got.GetAnnotations()["seictl.sei.io/preset"] != "genesis-chain" {
		t.Errorf("preset annotation missing: %v", got.GetAnnotations())
	}
}

func TestRender_PresetReplicasDefault4(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "genesis-chain",
		name:    "x",
		chainID: "c",
		image:   "i:1",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	replicas, found, _ := unstructured.NestedInt64(got.Object, "spec", "replicas")
	if !found || replicas != 4 {
		t.Errorf("replicas = %d found=%v; want preset default 4 (Q1: pin 4)", replicas, found)
	}
}

// resourceArgs is the minimal valid genesis-chain render, so a resource
// case only has to state the dimension it is exercising.
func resourceArgs() renderArgs {
	return renderArgs{
		preset:  "genesis-chain",
		name:    "x",
		chainID: "c",
		image:   "i:1",
	}
}

// assertRequests checks all three request dimensions plus the invariant
// that no render emits limits: the controller derives the memory limit
// from the request, and the CRD's CEL rejects a CPU limit outright.
func assertRequests(t *testing.T, u *unstructured.Unstructured, wantCPU, wantMemory, wantStorage string) {
	t.Helper()
	cpu, _, _ := unstructured.NestedString(u.Object, "spec", "resources", "requests", "cpu")
	if cpu != wantCPU {
		t.Errorf("spec.resources.requests.cpu = %q; want %q", cpu, wantCPU)
	}
	mem, _, _ := unstructured.NestedString(u.Object, "spec", "resources", "requests", "memory")
	if mem != wantMemory {
		t.Errorf("spec.resources.requests.memory = %q; want %q", mem, wantMemory)
	}
	stor, _, _ := unstructured.NestedString(u.Object, "spec", "dataVolume", "storage", "resources", "requests", "storage")
	if stor != wantStorage {
		t.Errorf("spec.dataVolume.storage.resources.requests.storage = %q; want %q", stor, wantStorage)
	}
	assertNoLimits(t, u.Object, "")
}

// assertNoLimits walks the whole rendered object rather than probing the
// two known paths, so a limits block that appears somewhere new still trips.
func assertNoLimits(t *testing.T, node interface{}, path string) {
	t.Helper()
	switch v := node.(type) {
	case map[string]interface{}:
		for k, child := range v {
			if k == "limits" {
				t.Errorf("limits present at %s.%s = %v; seictl must not emit limits", path, k, child)
			}
			assertNoLimits(t, child, path+"."+k)
		}
	case []interface{}:
		for i, child := range v {
			assertNoLimits(t, child, fmt.Sprintf("%s[%d]", path, i))
		}
	}
}

func TestRender_PresetResourceDefaults(t *testing.T) {
	got, err := render(resourceArgs())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	assertRequests(t, got, "4", "32Gi", "500Gi")
}

func TestRender_ResourceFlagOverride(t *testing.T) {
	args := resourceArgs()
	args.cpu, args.memory, args.storage = "16", "128Gi", "2000Gi"
	got, err := render(args)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	assertRequests(t, got, "16", "128Gi", "2000Gi")
}

// Each flag moves its own dimension and leaves the other two on the
// preset default — the layering claim in `network apply --help`.
func TestRender_PartialResourceOverride(t *testing.T) {
	cases := []struct {
		name                             string
		cpu, memory, storage             string
		wantCPU, wantMemory, wantStorage string
	}{
		{"cpu only", "8", "", "", "8", "32Gi", "500Gi"},
		{"memory only", "", "64Gi", "", "4", "64Gi", "500Gi"},
		{"storage only", "", "", "1000Gi", "4", "32Gi", "1000Gi"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := resourceArgs()
			args.cpu, args.memory, args.storage = tc.cpu, tc.memory, tc.storage
			got, err := render(args)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			assertRequests(t, got, tc.wantCPU, tc.wantMemory, tc.wantStorage)
		})
	}
}

// A quantity the apiserver would reject must fail here, not after the CR
// is committed, merged, and picked up by Flux.
func TestRender_RejectsInvalidQuantity(t *testing.T) {
	cases := []struct {
		name                 string
		cpu, memory, storage string
		want                 string
	}{
		{"cpu not a number", "abc", "", "", `--cpu "abc"`},
		{"memory wrong suffix", "", "32GB", "", `--memory "32GB"`},
		{"storage embedded space", "", "", "500 Gi", `--storage "500 Gi"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := resourceArgs()
			args.cpu, args.memory, args.storage = tc.cpu, tc.memory, tc.storage
			_, err := render(args)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q; want containing %q", err.Error(), tc.want)
			}
		})
	}
}

// --set can reach spec.resources.limits.cpu, which the CRD's CEL rejects
// at admission. Catch it locally instead.
func TestRender_RejectsCPULimitFromSet(t *testing.T) {
	args := resourceArgs()
	args.sets = []string{"spec.resources.limits.cpu=100m"}
	_, err := render(args)
	if err == nil {
		t.Fatal("expected error for --set spec.resources.limits.cpu")
	}
	if !strings.Contains(err.Error(), "spec.resources.limits.cpu") || !strings.Contains(err.Error(), "no CPU limit") {
		t.Errorf("err = %q; want it to name the path and say seid carries no CPU limit", err.Error())
	}
}

// The guard is deliberately narrow: seinode_types.go:154 PERMITS
// limits.memory when it equals requests.memory, so rejecting the whole
// limits block would forbid a spelling the CRD allows.
func TestRender_AllowsMemoryLimitFromSet(t *testing.T) {
	args := resourceArgs()
	args.sets = []string{"spec.resources.limits.memory=32Gi"}
	got, err := render(args)
	if err != nil {
		t.Fatalf("render with --set spec.resources.limits.memory: %v", err)
	}
	limit, _, _ := unstructured.NestedString(got.Object, "spec", "resources", "limits", "memory")
	if limit != "32Gi" {
		t.Errorf("spec.resources.limits.memory = %q; want 32Gi (allowed by the CRD)", limit)
	}
}

func TestRender_RequiredFlags(t *testing.T) {
	cases := []struct {
		name string
		args renderArgs
		want string
	}{
		{"missing preset", renderArgs{name: "x"}, "--preset is required"},
		{"missing name", renderArgs{preset: "genesis-chain"}, "name is required"},
		{"unknown preset", renderArgs{preset: "no-such", name: "x"}, "unknown preset"},
		{"without chain-id", renderArgs{preset: "genesis-chain", name: "x", image: "i:1"}, "requires .spec.genesis.chainId"},
		{"without image", renderArgs{preset: "genesis-chain", name: "x", chainID: "c"}, "requires .spec.image"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := render(tc.args)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q; want containing %q", err.Error(), tc.want)
			}
		})
	}
}

func TestRender_GenesisOverrideTypes(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "genesis-chain",
		name:    "x",
		chainID: "c",
		image:   "i:1",
		genesisOverrides: []string{
			`staking.params.unbonding_time=600s`,
			`bank.params.default_send_enabled=true`,
			`gov.params.voting_period_seconds=120`,
			`mint.params.inflation={"min":0.05,"max":0.2}`,
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	overrides, _, _ := unstructured.NestedMap(got.Object, "spec", "genesis", "overrides")
	if overrides["staking.params.unbonding_time"] != "600s" {
		t.Errorf("string override wrong: %v", overrides["staking.params.unbonding_time"])
	}
	if overrides["bank.params.default_send_enabled"] != true {
		t.Errorf("bool override wrong: %v", overrides["bank.params.default_send_enabled"])
	}
	if overrides["gov.params.voting_period_seconds"] != float64(120) {
		t.Errorf("number override wrong: %v", overrides["gov.params.voting_period_seconds"])
	}
	if m, ok := overrides["mint.params.inflation"].(map[string]interface{}); !ok || m["min"] != float64(0.05) {
		t.Errorf("object override wrong: %v", overrides["mint.params.inflation"])
	}
}

func TestRender_SetPrecedence(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "genesis-chain",
		name:    "x",
		chainID: "c",
		image:   "from-flag:1",
		sets:    []string{"spec.image=from-set:2"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	image, _, _ := unstructured.NestedString(got.Object, "spec", "image")
	if image != "from-set:2" {
		t.Errorf("image = %q; --set should beat --image", image)
	}
}

func TestRender_SetCannotRetargetMetadata(t *testing.T) {
	got, err := render(renderArgs{
		preset:    "genesis-chain",
		name:      "x",
		namespace: "nightly",
		chainID:   "c",
		image:     "i:1",
		sets:      []string{"metadata.namespace=kube-system", "metadata.name=hijacked"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got.GetNamespace() != "nightly" || got.GetName() != "x" {
		t.Errorf("identity = %s/%s; --set must not retarget", got.GetNamespace(), got.GetName())
	}
}

// urfave/cli's StringSliceFlag splits on ',' by default; assert the guard.
func TestApplyCmd_FlagsDoNotSplitOnComma(t *testing.T) {
	if !applyCmd.DisableSliceFlagSeparator {
		t.Fatal("applyCmd.DisableSliceFlagSeparator must be true")
	}
	var capturedGenesis, capturedSet []string
	origAction := applyCmd.Action
	applyCmd.Action = func(_ context.Context, c *cli.Command) error {
		capturedGenesis = c.StringSlice("genesis-account")
		capturedSet = c.StringSlice("set")
		return nil
	}
	t.Cleanup(func() { applyCmd.Action = origAction })

	err := applyCmd.Run(context.Background(), []string{
		"apply", "net",
		"--preset", "genesis-chain",
		"--chain-id", "net",
		"--image", "i:1",
		"--genesis-account", "sei1abc:1000usei,500uatom",
		"--set", "spec.configOverrides.evm.http_port=8545,foo",
	})
	if err != nil {
		t.Fatalf("apply Run: %v", err)
	}
	if len(capturedGenesis) != 1 || capturedGenesis[0] != "sei1abc:1000usei,500uatom" {
		t.Errorf("--genesis-account split by parser: %v", capturedGenesis)
	}
	if len(capturedSet) != 1 || capturedSet[0] != "spec.configOverrides.evm.http_port=8545,foo" {
		t.Errorf("--set split by parser: %v", capturedSet)
	}
}

func TestParseCascade(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"", "Foreground"},
		{"foreground", "Foreground"},
		{"background", "Background"},
		{"orphan", "Orphan"},
	} {
		got, err := parseCascade(tc.raw)
		if err != nil {
			t.Fatalf("parseCascade(%q): %v", tc.raw, err)
		}
		if string(*got) != tc.want {
			t.Errorf("parseCascade(%q) = %q; want %q", tc.raw, *got, tc.want)
		}
	}
	for _, bad := range []string{"async", "delete"} {
		if _, err := parseCascade(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestPresetNames(t *testing.T) {
	found := false
	for _, n := range presetNames() {
		if n == "genesis-chain" {
			found = true
		}
	}
	if !found {
		t.Errorf("preset genesis-chain not found in %v", presetNames())
	}
}
