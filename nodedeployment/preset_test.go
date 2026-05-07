package nodedeployment

import (
	"context"
	"strings"
	"testing"

	"github.com/urfave/cli/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestRender_GenesisChain(t *testing.T) {
	got, err := render(renderArgs{
		preset:    "genesis-chain",
		name:      "crater-lake-1",
		namespace: "nightly",
		chainID:   "crater-lake-1",
		image:     "ghcr.io/sei-protocol/sei:v6.4.0",
		replicas:  3,
		hasReps:   true,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if got.GroupVersionKind().Kind != "SeiNodeDeployment" {
		t.Errorf("kind = %q; want SeiNodeDeployment", got.GroupVersionKind().Kind)
	}
	if got.GetName() != "crater-lake-1" {
		t.Errorf("name = %q; want crater-lake-1", got.GetName())
	}
	if got.GetNamespace() != "nightly" {
		t.Errorf("namespace = %q; want nightly", got.GetNamespace())
	}
	chainID, found, _ := unstructured.NestedString(got.Object, "spec", "genesis", "chainId")
	if !found || chainID != "crater-lake-1" {
		t.Errorf("spec.genesis.chainId = %q; want crater-lake-1", chainID)
	}
	tmplChainID, found, _ := unstructured.NestedString(got.Object, "spec", "template", "spec", "chainId")
	if !found || tmplChainID != "crater-lake-1" {
		t.Errorf("spec.template.spec.chainId = %q; want crater-lake-1 (--chain-id must hit both paths for genesis-chain)", tmplChainID)
	}
	image, _, _ := unstructured.NestedString(got.Object, "spec", "template", "spec", "image")
	if image != "ghcr.io/sei-protocol/sei:v6.4.0" {
		t.Errorf("image = %q; want overridden", image)
	}
	replicas, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas")
	if replicas != 3 {
		t.Errorf("replicas = %d; want 3 (overrode preset's 4)", replicas)
	}
	if got.GetAnnotations()["seictl.sei.io/preset"] != "genesis-chain" {
		t.Errorf("preset annotation missing or wrong: %v", got.GetAnnotations())
	}
	if got.GetAnnotations()["seictl.sei.io/version"] == "" {
		t.Errorf("version annotation missing")
	}
}

func TestRender_RPCChainIDDoesNotTouchGenesis(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "rpc-fleet",
		chainID: "pacific-1",
		image:   "img:1",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	tmplChainID, _, _ := unstructured.NestedString(got.Object, "spec", "template", "spec", "chainId")
	if tmplChainID != "pacific-1" {
		t.Errorf("spec.template.spec.chainId = %q; want pacific-1", tmplChainID)
	}
	if _, found, _ := unstructured.NestedString(got.Object, "spec", "genesis", "chainId"); found {
		t.Errorf("spec.genesis.chainId set on rpc preset; should only appear on genesis-chain")
	}
}

func TestRender_TemplateLabels(t *testing.T) {
	cases := []struct {
		preset string
		role   string
	}{
		{"genesis-chain", "validator"},
		{"rpc", "node"},
	}
	for _, tc := range cases {
		t.Run(tc.preset, func(t *testing.T) {
			got, err := render(renderArgs{
				preset:  tc.preset,
				name:    "demo",
				chainID: "bench-x",
				image:   "img:1",
			})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			labels, _, _ := unstructured.NestedStringMap(got.Object, "spec", "template", "metadata", "labels")
			if labels["sei.io/chain"] != "bench-x" {
				t.Errorf("template.labels[sei.io/chain] = %q; want bench-x", labels["sei.io/chain"])
			}
			if labels["sei.io/role"] != tc.role {
				t.Errorf("template.labels[sei.io/role] = %q; want %s", labels["sei.io/role"], tc.role)
			}
		})
	}
}

func TestRender_RPCAutoWiresPeers(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "rpc-fleet",
		chainID: "bench-nightly-12345",
		image:   "img:1",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	peers, found, _ := unstructured.NestedSlice(got.Object, "spec", "template", "spec", "peers")
	if !found || len(peers) != 1 {
		t.Fatalf("expected one peer source, got %v (found=%v)", peers, found)
	}
	peer := peers[0].(map[string]interface{})
	selector, _, _ := unstructured.NestedStringMap(peer, "label", "selector")
	if selector["sei.io/chain"] != "bench-nightly-12345" {
		t.Errorf("peers[0].label.selector[sei.io/chain] = %q; want bench-nightly-12345", selector["sei.io/chain"])
	}
	if _, present := selector["sei.io/role"]; present {
		t.Errorf("peers[0].label.selector has sei.io/role; v1 matches on chain-id only (uniqueness does the work)")
	}
}

func TestRender_GenesisChainOmitsPeers(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "genesis-chain",
		name:    "validators",
		chainID: "bench-x",
		image:   "img:1",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if _, found, _ := unstructured.NestedSlice(got.Object, "spec", "template", "spec", "peers"); found {
		t.Errorf("genesis-chain should not auto-wire peers; validators bootstrap from genesis ceremony")
	}
}

func TestRender_RPCPresetDefaults(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "rpc-fleet",
		chainID: "pacific-1",
		image:   "ghcr.io/sei-protocol/sei:v6.4.0",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	replicas, found, _ := unstructured.NestedInt64(got.Object, "spec", "replicas")
	if !found || replicas != 2 {
		t.Errorf("replicas = %d found=%v; want preset default 2", replicas, found)
	}
	fullNode, found, _ := unstructured.NestedMap(got.Object, "spec", "template", "spec", "fullNode")
	if !found || fullNode == nil {
		t.Errorf("expected spec.template.spec.fullNode to be set by rpc preset")
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
		{
			"genesis-chain without chain-id",
			renderArgs{preset: "genesis-chain", name: "x", image: "img:1"},
			"requires .spec.genesis.chainId",
		},
		{
			"genesis-chain without image",
			renderArgs{preset: "genesis-chain", name: "x", chainID: "c"},
			"requires .spec.template.spec.image",
		},
		{
			"rpc without chain-id",
			renderArgs{preset: "rpc", name: "x", image: "img:1"},
			"requires .spec.template.spec.chainId",
		},
		{
			"rpc without image",
			renderArgs{preset: "rpc", name: "x", chainID: "pacific-1"},
			"requires .spec.template.spec.image",
		},
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

func TestRender_GenesisAccountFlag(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "genesis-chain",
		name:    "qa-test",
		chainID: "qa-test",
		image:   "img:1",
		genesisAccounts: []string{
			"sei1abc:1000000000usei",
			"0xdef:500000000usei,2000uusdc",
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	accounts, found, _ := unstructured.NestedSlice(got.Object, "spec", "genesis", "accounts")
	if !found || len(accounts) != 2 {
		t.Fatalf("expected 2 accounts, got %v (found=%v)", accounts, found)
	}
	a0 := accounts[0].(map[string]interface{})
	if a0["address"] != "sei1abc" {
		t.Errorf("accounts[0].address = %v; want sei1abc", a0["address"])
	}
	if a0["balance"] != "1000000000usei" {
		t.Errorf("accounts[0].balance = %v; want 1000000000usei", a0["balance"])
	}
	a1 := accounts[1].(map[string]interface{})
	if a1["address"] != "0xdef" {
		t.Errorf("accounts[1].address = %v; want 0xdef", a1["address"])
	}
	if a1["balance"] != "500000000usei,2000uusdc" {
		t.Errorf("accounts[1].balance = %v; want 500000000usei,2000uusdc", a1["balance"])
	}
}

func TestRender_GenesisAccountRejectsNonGenesisPreset(t *testing.T) {
	_, err := render(renderArgs{
		preset:          "rpc",
		name:            "rpc-fleet",
		chainID:         "pacific-1",
		image:           "img:1",
		genesisAccounts: []string{"sei1abc:1000usei"},
	})
	if err == nil || !strings.Contains(err.Error(), "only valid with --preset genesis-chain") {
		t.Errorf("expected 'only valid with --preset genesis-chain' error, got %v", err)
	}
}

func TestRender_GenesisAccountSetCanOverride(t *testing.T) {
	got, err := render(renderArgs{
		preset:          "genesis-chain",
		name:            "x",
		chainID:         "c",
		image:           "img:1",
		genesisAccounts: []string{"sei1abc:1000usei"},
		sets:            []string{"spec.genesis.accountBalance=2000usei"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	accounts, _, _ := unstructured.NestedSlice(got.Object, "spec", "genesis", "accounts")
	if len(accounts) != 1 {
		t.Errorf("--genesis-account should still produce its entry; got len=%d", len(accounts))
	}
	bal, _, _ := unstructured.NestedString(got.Object, "spec", "genesis", "accountBalance")
	if bal != "2000usei" {
		t.Errorf("--set after --genesis-account should set accountBalance; got %q", bal)
	}
}

func TestParseGenesisAccount_Errors(t *testing.T) {
	cases := []struct {
		entry string
		want  string
	}{
		{"no-colon", "missing ':'"},
		{":1000usei", "empty address"},
		{"sei1abc:", "empty balance"},
	}
	for _, tc := range cases {
		t.Run(tc.entry, func(t *testing.T) {
			_, err := render(renderArgs{
				preset:          "genesis-chain",
				name:            "x",
				chainID:         "c",
				image:           "img:1",
				genesisAccounts: []string{tc.entry},
			})
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q; want containing %q", err.Error(), tc.want)
			}
		})
	}
}

func TestRender_SetOverrides(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "x",
		chainID: "pacific-1",
		sets: []string{
			"spec.template.spec.image=custom:tag",
			"spec.replicas=5",
			"spec.template.spec.statefulFlag=true",
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	image, _, _ := unstructured.NestedString(got.Object, "spec", "template", "spec", "image")
	if image != "custom:tag" {
		t.Errorf("image = %q; want custom:tag", image)
	}
	reps, _, _ := unstructured.NestedInt64(got.Object, "spec", "replicas")
	if reps != 5 {
		t.Errorf("replicas = %d; want 5", reps)
	}
	flag, _, _ := unstructured.NestedBool(got.Object, "spec", "template", "spec", "statefulFlag")
	if !flag {
		t.Errorf("statefulFlag = false; want true")
	}
}

func TestRender_SetPrecedence(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "x",
		chainID: "pacific-1",
		image:   "from-flag:1",
		sets:    []string{"spec.template.spec.image=from-set:2"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	image, _, _ := unstructured.NestedString(got.Object, "spec", "template", "spec", "image")
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
		image:     "img:1",
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

func TestApplySet_Errors(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"no-equals", "missing '='"},
		{"=value", "empty path"},
		{"a..b=v", "empty segment"},
		{"a[=v", "without closing"},
		{"a[]=v", "empty list index"},
		{"a[abc]=v", "malformed list index"},
		{"a[-1]=v", "negative list index"},
		{"[0]=v", "without preceding key"},
		{"foo=null", "value 'null' is not supported"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			err := applySet(map[string]interface{}{}, tc.expr)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q; want containing %q", err.Error(), tc.want)
			}
		})
	}
}

func TestApplySet_ListIndex_AppendIntoEmptyList(t *testing.T) {
	root := map[string]interface{}{}
	if err := applySet(root, "spec.genesis.accounts[0].address=sei1abc"); err != nil {
		t.Fatalf("applySet: %v", err)
	}
	if err := applySet(root, "spec.genesis.accounts[0].balance=1000usei"); err != nil {
		t.Fatalf("applySet: %v", err)
	}
	accounts, _, _ := unstructured.NestedSlice(root, "spec", "genesis", "accounts")
	if len(accounts) != 1 {
		t.Fatalf("len(accounts) = %d; want 1", len(accounts))
	}
	a0, ok := accounts[0].(map[string]interface{})
	if !ok {
		t.Fatalf("accounts[0] is %T; want map", accounts[0])
	}
	if a0["address"] != "sei1abc" {
		t.Errorf("accounts[0].address = %v; want sei1abc", a0["address"])
	}
	if a0["balance"] != "1000usei" {
		t.Errorf("accounts[0].balance = %v; want 1000usei", a0["balance"])
	}
}

func TestApplySet_ListIndex_AppendSequential(t *testing.T) {
	root := map[string]interface{}{}
	exprs := []string{
		"spec.genesis.accounts[0].address=sei1aaa",
		"spec.genesis.accounts[0].balance=100usei",
		"spec.genesis.accounts[1].address=sei1bbb",
		"spec.genesis.accounts[1].balance=200usei",
	}
	for _, e := range exprs {
		if err := applySet(root, e); err != nil {
			t.Fatalf("applySet(%q): %v", e, err)
		}
	}
	accounts, _, _ := unstructured.NestedSlice(root, "spec", "genesis", "accounts")
	if len(accounts) != 2 {
		t.Fatalf("len(accounts) = %d; want 2", len(accounts))
	}
	a1 := accounts[1].(map[string]interface{})
	if a1["address"] != "sei1bbb" || a1["balance"] != "200usei" {
		t.Errorf("accounts[1] = %v; want {address=sei1bbb, balance=200usei}", a1)
	}
}

func TestApplySet_ListIndex_SetOnExisting(t *testing.T) {
	root := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"peers": []interface{}{
						map[string]interface{}{
							"label": map[string]interface{}{
								"original": "value",
							},
						},
					},
				},
			},
		},
	}
	if err := applySet(root, "spec.template.spec.peers[0].label.original=overridden"); err != nil {
		t.Fatalf("applySet: %v", err)
	}
	peers, _, _ := unstructured.NestedSlice(root, "spec", "template", "spec", "peers")
	p0 := peers[0].(map[string]interface{})
	label := p0["label"].(map[string]interface{})
	if label["original"] != "overridden" {
		t.Errorf("peers[0].label.original = %v; want overridden", label["original"])
	}
}

func TestApplySet_ListIndex_SparseRejected(t *testing.T) {
	root := map[string]interface{}{}
	err := applySet(root, "spec.accounts[2].address=foo")
	if err == nil {
		t.Fatalf("expected error for sparse index, got nil")
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Errorf("err = %v; want containing 'out of range'", err)
	}
	if !strings.Contains(err.Error(), ".accounts[2]") {
		t.Errorf("err = %v; want containing failing segment '.accounts[2]'", err)
	}
}

func TestApplySet_ListIndex_LeafScalar(t *testing.T) {
	root := map[string]interface{}{}
	if err := applySet(root, "tags[0]=alpha"); err != nil {
		t.Fatalf("applySet: %v", err)
	}
	if err := applySet(root, "tags[1]=beta"); err != nil {
		t.Fatalf("applySet: %v", err)
	}
	tags, _, _ := unstructured.NestedSlice(root, "tags")
	if len(tags) != 2 || tags[0] != "alpha" || tags[1] != "beta" {
		t.Errorf("tags = %v; want [alpha beta]", tags)
	}
}

func TestApplySet_ListIndex_TypeMismatchSurfaces(t *testing.T) {
	root := map[string]interface{}{
		"spec": map[string]interface{}{
			"accounts": "not-a-list",
		},
	}
	err := applySet(root, "spec.accounts[0]=foo")
	if err == nil {
		t.Fatalf("expected error for non-list field, got nil")
	}
	if !strings.Contains(err.Error(), "expects list") {
		t.Errorf("err = %v; want containing 'expects list'", err)
	}
}

func TestRender_OverrideFlag(t *testing.T) {
	got, err := render(renderArgs{
		preset:  "rpc",
		name:    "qa-test",
		chainID: "qa-test",
		image:   "img:1",
		overrides: []string{
			"evm.enabled_legacy_sei_apis=sei_getLogs,sei_getBlockByNumber",
			"tx_index.indexer=kv",
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	overrides, found, _ := unstructured.NestedStringMap(got.Object, "spec", "template", "spec", "overrides")
	if !found {
		t.Fatal("overrides map not found")
	}
	if got := overrides["evm.enabled_legacy_sei_apis"]; got != "sei_getLogs,sei_getBlockByNumber" {
		t.Errorf("evm.enabled_legacy_sei_apis = %q; want %q", got, "sei_getLogs,sei_getBlockByNumber")
	}
	if got := overrides["tx_index.indexer"]; got != "kv" {
		t.Errorf("tx_index.indexer = %q; want %q", got, "kv")
	}
}

func TestApplyOverride_PreservesExistingMap(t *testing.T) {
	root := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": map[string]interface{}{
				"spec": map[string]interface{}{
					"overrides": map[string]interface{}{
						"chain.moniker": "preset-default",
					},
				},
			},
		},
	}
	if err := applyOverride(root, "evm.enabled_legacy_sei_apis=sei_getLogs"); err != nil {
		t.Fatalf("applyOverride: %v", err)
	}
	overrides, _, _ := unstructured.NestedStringMap(root, "spec", "template", "spec", "overrides")
	if overrides["chain.moniker"] != "preset-default" {
		t.Errorf("preset-supplied entry was clobbered: chain.moniker = %q", overrides["chain.moniker"])
	}
	if overrides["evm.enabled_legacy_sei_apis"] != "sei_getLogs" {
		t.Errorf("override not written: evm.enabled_legacy_sei_apis = %q", overrides["evm.enabled_legacy_sei_apis"])
	}
}

func TestApplyOverride_ValueWithEquals(t *testing.T) {
	root := map[string]interface{}{}
	if err := applyOverride(root, "chain.min_gas_prices=0.1usei,0.5=stake"); err != nil {
		t.Fatalf("applyOverride: %v", err)
	}
	overrides, _, _ := unstructured.NestedStringMap(root, "spec", "template", "spec", "overrides")
	if got := overrides["chain.min_gas_prices"]; got != "0.1usei,0.5=stake" {
		t.Errorf("value with embedded '=' not preserved: got %q", got)
	}
}

// TestApplyCmd_FlagsDoNotSplitOnComma exercises the actual urfave/cli flag
// parser end-to-end. urfave/cli's StringSliceFlag splits values on ','
// unless DisableSliceFlagSeparator is set on the Command. Without this,
// --override evm.enabled_legacy_sei_apis=a,b would arrive as ["...=a", "b"].
// Same latent bug applied to --set (e.g. multi-denom min_gas_prices) and
// --genesis-account (multi-denom balances). All three are guarded by the
// command-level DisableSliceFlagSeparator: true.
func TestApplyCmd_FlagsDoNotSplitOnComma(t *testing.T) {
	if !applyCmd.DisableSliceFlagSeparator {
		t.Fatal("applyCmd.DisableSliceFlagSeparator must be true; otherwise comma-bearing flag values get split by urfave/cli")
	}

	var capturedOverride, capturedSet, capturedGenesis []string
	origAction := applyCmd.Action
	applyCmd.Action = func(_ context.Context, c *cli.Command) error {
		capturedOverride = c.StringSlice("override")
		capturedSet = c.StringSlice("set")
		capturedGenesis = c.StringSlice("genesis-account")
		return nil
	}
	t.Cleanup(func() { applyCmd.Action = origAction })

	err := applyCmd.Run(context.Background(), []string{
		"apply", "qa-rpc",
		"--preset", "rpc",
		"--chain-id", "qa-rpc",
		"--image", "img:1",
		"--override", "evm.enabled_legacy_sei_apis=sei_getLogs,sei_getBlockByNumber",
		"--set", "spec.template.spec.image=foo,bar",
		"--genesis-account", "sei1abc:1000usei,500uatom",
	})
	if err != nil {
		t.Fatalf("apply Run: %v", err)
	}

	if len(capturedOverride) != 1 ||
		capturedOverride[0] != "evm.enabled_legacy_sei_apis=sei_getLogs,sei_getBlockByNumber" {
		t.Errorf("--override split by parser: %v", capturedOverride)
	}
	if len(capturedSet) != 1 || capturedSet[0] != "spec.template.spec.image=foo,bar" {
		t.Errorf("--set split by parser: %v", capturedSet)
	}
	if len(capturedGenesis) != 1 || capturedGenesis[0] != "sei1abc:1000usei,500uatom" {
		t.Errorf("--genesis-account split by parser: %v", capturedGenesis)
	}
}

func TestApplyOverride_Errors(t *testing.T) {
	cases := []struct {
		expr string
		want string
	}{
		{"no-equals", "missing '='"},
		{"=value", "empty key"},
	}
	for _, tc := range cases {
		t.Run(tc.expr, func(t *testing.T) {
			err := applyOverride(map[string]interface{}{}, tc.expr)
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q; want containing %q", err.Error(), tc.want)
			}
		})
	}
}

func TestParseValue(t *testing.T) {
	cases := map[string]interface{}{
		"":       "",
		"true":   true,
		"false":  false,
		"42":     int64(42),
		"-1":     int64(-1),
		"abc":    "abc",
		"v1.2.3": "v1.2.3",
		"truex":  "truex",
		"42x":    "42x",
	}
	for in, want := range cases {
		if got := parseValue(in); got != want {
			t.Errorf("parseValue(%q) = %v (%T); want %v (%T)", in, got, got, want, want)
		}
	}
}

func TestPresetNames(t *testing.T) {
	names := presetNames()
	if len(names) < 2 {
		t.Fatalf("expected at least 2 presets, got %v", names)
	}
	want := map[string]bool{"genesis-chain": false, "rpc": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("preset %q not found in %v", n, names)
		}
	}
}
