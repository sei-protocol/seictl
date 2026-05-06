package nodedeployment

import (
	"strings"
	"testing"

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
		{"a[0]=v", "list-index syntax"},
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
