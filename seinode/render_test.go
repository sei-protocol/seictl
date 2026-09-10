package seinode

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sei-protocol/seictl/internal/cliutil"
)

// T2 — node apply golden render: rpc preset + --chain-id/--image/--network.
func TestRender_RPCGolden(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "chaos-rpc-0",
		chainID: "c1",
		image:   "i:1",
		network: "netX",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got.GroupVersionKind().Kind != "SeiNode" {
		t.Errorf("kind = %q; want SeiNode", got.GroupVersionKind().Kind)
	}
	if got.GetName() != "chaos-rpc-0" {
		t.Errorf("name = %q; want chaos-rpc-0", got.GetName())
	}
	chainID, _, _ := unstructured.NestedString(got.Object, "spec", "chainId")
	if chainID != "c1" {
		t.Errorf("spec.chainId = %q; want c1", chainID)
	}
	image, _, _ := unstructured.NestedString(got.Object, "spec", "image")
	if image != "i:1" {
		t.Errorf("spec.image = %q; want i:1", image)
	}
	if _, found, _ := unstructured.NestedMap(got.Object, "spec", "fullNode"); !found {
		t.Errorf("spec.fullNode not set by rpc preset")
	}
	selector, _, _ := selectorOf(t, got)
	if selector["sei.io/seinetwork"] != "netX" {
		t.Errorf("peers[0].label.selector[sei.io/seinetwork] = %q; want netX", selector["sei.io/seinetwork"])
	}
	// SeiNode is flat — there must be NO spec.template wrapper or spec.replicas.
	if _, found, _ := unstructured.NestedMap(got.Object, "spec", "template"); found {
		t.Errorf("spec.template present; SeiNode is flat, no template wrapper")
	}
	if _, found, _ := unstructured.NestedInt64(got.Object, "spec", "replicas"); found {
		t.Errorf("spec.replicas present; SeiNode has no replicas field")
	}
}

func selectorOf(t *testing.T, u *unstructured.Unstructured) (map[string]string, bool, error) {
	t.Helper()
	peers, found, err := unstructured.NestedSlice(u.Object, "spec", "peers")
	if err != nil || !found || len(peers) == 0 {
		t.Fatalf("expected at least one peer source, found=%v err=%v", found, err)
	}
	peer := peers[0].(map[string]interface{})
	return unstructured.NestedStringMap(peer, "label", "selector")
}

// resourceArgs is the minimal valid rpc render, so a resource case only
// has to state the dimension it is exercising.
func resourceArgs() renderArgs {
	return renderArgs{
		preset:  "rpc",
		name:    "rpc-0",
		chainID: "c1",
		image:   "i:1",
		network: "netX",
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
// preset default — the layering claim in `node apply --help`.
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
			if !strings.Contains(err.Error(), "not a valid Kubernetes quantity") {
				t.Errorf("err = %q; want it rejected as unparseable, not for being non-positive", err.Error())
			}
		})
	}
}

// Zero and negative parse cleanly but the CRD's CEL requires positive
// values (seinode_types.go:153 requests, :196 storage), so they must
// fail here rather than at a post-merge reconcile.
func TestRender_RejectsNonPositiveQuantity(t *testing.T) {
	cases := []struct {
		name                 string
		cpu, memory, storage string
		want                 string
	}{
		{"cpu negative", "-1", "", "", `--cpu "-1"`},
		{"cpu zero", "0", "", "", `--cpu "0"`},
		{"memory negative", "", "-5Gi", "", `--memory "-5Gi"`},
		{"memory zero", "", "0Gi", "", `--memory "0Gi"`},
		{"storage negative", "", "", "-5Gi", `--storage "-5Gi"`},
		{"storage zero", "", "", "0Gi", `--storage "0Gi"`},
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
			if !strings.Contains(err.Error(), "must be positive") {
				t.Errorf("err = %q; want it rejected for not being positive, not as unparseable", err.Error())
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

// T3 — peer-wiring: --network sets exactly sei.io/seinetwork, NOT
// sei.io/chain or sei.io/nodedeployment. Guards the §3 one-way decision.
func TestRender_PeerWiringKey(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "rpc-0",
		chainID: "bench-nightly-12345",
		image:   "i:1",
		network: "bench-nightly-12345",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	selector, _, _ := selectorOf(t, got)
	if _, present := selector["sei.io/seinetwork"]; !present {
		t.Errorf("selector missing sei.io/seinetwork; got %v", selector)
	}
	if _, present := selector["sei.io/chain"]; present {
		t.Errorf("selector has sei.io/chain; the split surface binds network identity, NOT chain")
	}
	if _, present := selector["sei.io/nodedeployment"]; present {
		t.Errorf("selector has sei.io/nodedeployment; that frozen key is slated for retirement")
	}
	if len(selector) != 1 {
		t.Errorf("selector = %v; want exactly one key (sei.io/seinetwork)", selector)
	}
}

// T4 — no --network and no --set spec.peers => Invalid with guidance.
func TestRender_RequiresPeerSource(t *testing.T) {
	_, err := render(renderArgs{
		preset:  "rpc",
		name:    "rpc-0",
		chainID: "c1",
		image:   "i:1",
	})
	if err == nil {
		t.Fatalf("expected error when neither --network nor --set spec.peers given")
	}
	if !strings.Contains(err.Error(), "--network") || !strings.Contains(err.Error(), "spec.peers") {
		t.Errorf("err = %q; want guidance naming --network and spec.peers", err.Error())
	}
}

// T4 (cont.) — an explicit --set spec.peers satisfies the requirement
// even without --network.
func TestRender_ExplicitPeerSetSatisfiesRequirement(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "rpc-0",
		chainID: "c1",
		image:   "i:1",
		sets:    []string{"spec.peers[0].label.selector.sei.io/role=validator"},
	})
	if err != nil {
		t.Fatalf("render with explicit --set spec.peers: %v", err)
	}
	// No --network => no sei.io/seinetwork object label (T-new contract).
	if _, present := got.GetLabels()["sei.io/seinetwork"]; present {
		t.Errorf("sei.io/seinetwork stamped without --network; got %v", got.GetLabels())
	}
}

// T-new — object-label stamping (Defect A producer side). --network X
// renders metadata.labels {sei.io/seinetwork=X, sei.io/role=node};
// without --network only sei.io/role=node.
func TestRender_ObjectLabels(t *testing.T) {
	withNet, err := render(renderArgs{
		preset:  "rpc",
		name:    "rpc-0",
		chainID: "c1",
		image:   "i:1",
		network: "netX",
	})
	if err != nil {
		t.Fatalf("render with --network: %v", err)
	}
	labels := withNet.GetLabels()
	if labels["sei.io/role"] != "node" {
		t.Errorf("labels[sei.io/role] = %q; want node", labels["sei.io/role"])
	}
	if labels["sei.io/seinetwork"] != "netX" {
		t.Errorf("labels[sei.io/seinetwork] = %q; want netX", labels["sei.io/seinetwork"])
	}

	withoutNet, err := render(renderArgs{
		preset:  "rpc",
		name:    "rpc-0",
		chainID: "c1",
		image:   "i:1",
		sets:    []string{"spec.peers[0].label.selector.sei.io/role=validator"},
	})
	if err != nil {
		t.Fatalf("render without --network: %v", err)
	}
	labels = withoutNet.GetLabels()
	if labels["sei.io/role"] != "node" {
		t.Errorf("labels[sei.io/role] = %q; want node (unconditional)", labels["sei.io/role"])
	}
	if _, present := labels["sei.io/seinetwork"]; present {
		t.Errorf("labels[sei.io/seinetwork] present without --network; got %v", labels)
	}
}

// T5 — node apply has no --replicas flag (SeiNode has no replicas field).
func TestApplyCmd_NoReplicasFlag(t *testing.T) {
	for _, f := range applyCmd.Flags {
		for _, name := range f.Names() {
			if name == "replicas" {
				t.Fatalf("node apply must not expose --replicas; SeiNode has no spec.replicas")
			}
		}
	}
}

func TestRender_RequiredFlags(t *testing.T) {
	cases := []struct {
		name string
		args renderArgs
		want string
	}{
		{"missing preset", renderArgs{name: "x", network: "n"}, "--preset is required"},
		{"missing name", renderArgs{preset: "rpc", network: "n"}, "name is required"},
		{"unknown preset", renderArgs{preset: "no-such", name: "x", network: "n"}, "unknown preset"},
		{"rpc without chain-id", renderArgs{preset: "rpc", name: "x", image: "i:1", network: "n"}, "requires .spec.chainId"},
		{"rpc without image", renderArgs{preset: "rpc", name: "x", chainID: "c", network: "n"}, "requires .spec.image"},
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

func TestRender_SetPrecedence(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "x",
		chainID: "pacific-1",
		image:   "from-flag:1",
		network: "netX",
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
		preset:    "rpc",
		name:      "x",
		namespace: "nightly",
		chainID:   "pacific-1",
		image:     "i:1",
		network:   "netX",
		sets: []string{
			"metadata.namespace=kube-system",
			"metadata.name=hijacked",
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got.GetNamespace() != "nightly" {
		t.Errorf("namespace = %q; --set must not retarget post-resolution", got.GetNamespace())
	}
	if got.GetName() != "x" {
		t.Errorf("name = %q; --set must not rename", got.GetName())
	}
}

func TestRender_OverrideFlag(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "qa",
		chainID: "qa",
		image:   "i:1",
		network: "netX",
		overrides: []string{
			"evm.enabled_legacy_sei_apis=sei_getLogs,sei_getBlockByNumber",
			"tx_index.indexer=kv",
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	overrides, found, _ := unstructured.NestedStringMap(got.Object, "spec", "overrides")
	if !found {
		t.Fatal("spec.overrides not found")
	}
	if overrides["evm.enabled_legacy_sei_apis"] != "sei_getLogs,sei_getBlockByNumber" {
		t.Errorf("override not preserved: %q", overrides["evm.enabled_legacy_sei_apis"])
	}
	if overrides["tx_index.indexer"] != "kv" {
		t.Errorf("override not written: %q", overrides["tx_index.indexer"])
	}
	// The preset's pprof override must survive alongside the added keys.
	if overrides["network.rpc.pprof_listen_address"] != "0.0.0.0:6060" {
		t.Errorf("preset override clobbered: %v", overrides)
	}
}

func TestRender_ExternalAddress(t *testing.T) {
	got, err := render(renderArgs{
		preset:          "rpc",
		name:            "sentry",
		chainID:         "c1",
		image:           "i:1",
		network:         "netX",
		externalAddress: "1.2.3.4:26656",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	ext, _, _ := unstructured.NestedString(got.Object, "spec", "externalAddress")
	if ext != "1.2.3.4:26656" {
		t.Errorf("spec.externalAddress = %q; want 1.2.3.4:26656", ext)
	}
}

// urfave/cli's StringSliceFlag splits on ',' by default; assert the guard.
func TestApplyCmd_FlagsDoNotSplitOnComma(t *testing.T) {
	if !applyCmd.DisableSliceFlagSeparator {
		t.Fatal("applyCmd.DisableSliceFlagSeparator must be true")
	}

	var capturedOverride, capturedSet []string
	origAction := applyCmd.Action
	applyCmd.Action = func(_ context.Context, c *cli.Command) error {
		capturedOverride = c.StringSlice("override")
		capturedSet = c.StringSlice("set")
		return nil
	}
	t.Cleanup(func() { applyCmd.Action = origAction })

	err := applyCmd.Run(context.Background(), []string{
		"apply", "qa-rpc",
		"--preset", "rpc",
		"--chain-id", "qa-rpc",
		"--image", "i:1",
		"--network", "netX",
		"--override", "evm.enabled_legacy_sei_apis=sei_getLogs,sei_getBlockByNumber",
		"--set", "spec.image=foo,bar",
	})
	if err != nil {
		t.Fatalf("apply Run: %v", err)
	}
	if len(capturedOverride) != 1 || capturedOverride[0] != "evm.enabled_legacy_sei_apis=sei_getLogs,sei_getBlockByNumber" {
		t.Errorf("--override split by parser: %v", capturedOverride)
	}
	if len(capturedSet) != 1 || capturedSet[0] != "spec.image=foo,bar" {
		t.Errorf("--set split by parser: %v", capturedSet)
	}
}

func TestParseCascade(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", "Foreground"},
		{"foreground", "Foreground"},
		{"background", "Background"},
		{"orphan", "Orphan"},
	}
	for _, tc := range cases {
		got, err := parseCascade(tc.raw)
		if err != nil {
			t.Fatalf("parseCascade(%q): %v", tc.raw, err)
		}
		if string(*got) != tc.want {
			t.Errorf("parseCascade(%q) = %q; want %q", tc.raw, *got, tc.want)
		}
	}
	for _, bad := range []string{"async", "FOREGROUND", "delete"} {
		if _, err := parseCascade(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestPresetNames(t *testing.T) {
	names := presetNames()
	found := false
	for _, n := range names {
		if n == "rpc" {
			found = true
		}
	}
	if !found {
		t.Errorf("preset rpc not found in %v", names)
	}
}

// --- storage performance (Spec 001 Req 3) ---

const vacPath = "spec.dataVolume.storage.volumeAttributesClassName"

func vacNameOf(t *testing.T, u *unstructured.Unstructured) (string, bool) {
	t.Helper()
	name, found, err := unstructured.NestedString(u.Object, "spec", "dataVolume", "storage", "volumeAttributesClassName")
	if err != nil {
		t.Fatalf("read %s: %v", vacPath, err)
	}
	return name, found
}

// The standard tier is the absence of the field, not an empty string or a
// name meaning "default" — the PVC must carry no volumeAttributesClassName
// at all so the gp3 StorageClass supplies the baseline (DR-001:81-88).
func TestRender_StandardTierOmitsTheField(t *testing.T) {
	got, err := render(resourceArgs())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if name, found := vacNameOf(t, got); found {
		t.Errorf("%s = %q; want the field absent for the standard tier", vacPath, name)
	}
}

// Req 3.2, in the one direction that is correct: the operator supplies
// the pair, the render carries the class name that encodes it.
func TestRender_SupportedPairResolvesToClassName(t *testing.T) {
	args := resourceArgs()
	args.iops, args.throughput = "10000", "750"
	got, err := render(args)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	name, found := vacNameOf(t, got)
	if !found {
		t.Fatalf("%s absent; want it set from the supplied pair", vacPath)
	}
	if name != "sei-gp3-performance-v1" {
		t.Errorf("%s = %q; want sei-gp3-performance-v1", vacPath, name)
	}
}

func TestRender_RejectsUnsupportedPair(t *testing.T) {
	args := resourceArgs()
	args.iops, args.throughput = "16000", "1000"
	_, err := render(args)
	if err == nil {
		t.Fatal("render = nil; want an unsupported pair refused locally")
	}
	for _, want := range []string{"10000", "750", "sei-gp3-performance-v1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %q; want it to name %q from the supported set", err.Error(), want)
		}
	}
}

// The gp3 ratio ceiling, at the render rather than at provision time. The
// apiserver accepts this CR; the PVC then fails to provision and the pod
// sits Pending, with a create-only field so the remedy is a new chain.
func TestRender_RejectsPerformanceTierOnUndersizedVolume(t *testing.T) {
	args := resourceArgs()
	args.iops, args.throughput = "10000", "750"
	args.storage = "10Gi"
	_, err := render(args)
	if err == nil {
		t.Fatal("render = nil; want 10Gi refused for the 10000-IOPS offering")
	}
	if !strings.Contains(err.Error(), "20Gi") {
		t.Errorf("err = %q; want it to name the 20Gi floor", err.Error())
	}
}

// The floor is checked against the size that actually landed, so --set
// reaching the size path is caught the same way --storage is.
func TestRender_RejectsPerformanceTierUndersizedViaSet(t *testing.T) {
	args := resourceArgs()
	args.iops, args.throughput = "10000", "750"
	args.sets = []string{"spec.dataVolume.storage.resources.requests.storage=10Gi"}
	_, err := render(args)
	if err == nil {
		t.Fatal("render = nil; want --set of an undersized volume refused")
	}
	if !strings.Contains(err.Error(), "20Gi") {
		t.Errorf("err = %q; want it to name the 20Gi floor", err.Error())
	}
}

// --set must not smuggle in a class name no supported pair resolves to,
// mirroring the spec.resources.limits.cpu guard.
func TestRender_RejectsUnsupportedClassNameViaSet(t *testing.T) {
	args := resourceArgs()
	args.sets = []string{"spec.dataVolume.storage.volumeAttributesClassName=sei-gp3-performance-v2"}
	_, err := render(args)
	if err == nil {
		t.Fatal("render = nil; want --set of an unsupported class name refused")
	}
	if !strings.Contains(err.Error(), "sei-gp3-performance-v2") {
		t.Errorf("err = %q; want it to name the rejected class", err.Error())
	}
}

// The other half of the tier-minimum pinning (see
// TestStoragePerformanceOffering_MinSizeGiBPerTier in internal/cliutil):
// every supported tier must fit the preset's documented default footprint
// with no --storage at all. A retune that raised a tier's floor past the
// preset default would leave the documented default silently unusable
// with that tier, and this is where that shows up.
func TestRender_PerformanceTierFitsPresetDefault(t *testing.T) {
	for _, o := range cliutil.StoragePerformanceOfferings() {
		t.Run(o.ClassName, func(t *testing.T) {
			args := resourceArgs()
			args.iops = strconv.FormatInt(o.IOPS, 10)
			args.throughput = strconv.FormatInt(o.Throughput, 10)
			got, err := render(args)
			if err != nil {
				t.Fatalf("render with the preset default size and %s: %v\n"+
					"the tier now needs at least %dGi; either the preset default or the tier moved",
					o.ClassName, err, o.MinSizeGiB())
			}
			name, found := vacNameOf(t, got)
			if !found || name != o.ClassName {
				t.Errorf("%s = %q (found=%v); want %q", vacPath, name, found, o.ClassName)
			}
		})
	}
}
