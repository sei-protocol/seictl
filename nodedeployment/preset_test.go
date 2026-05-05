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
		image:     "ghcr.io/sei-protocol/sei:v0.1.2",
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
	image, _, _ := unstructured.NestedString(got.Object, "spec", "template", "spec", "image")
	if image != "ghcr.io/sei-protocol/sei:v0.1.2" {
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

func TestRender_RPCPresetDefaults(t *testing.T) {
	got, err := render(renderArgs{
		preset: "rpc",
		name:   "rpc-fleet",
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
		{"missing name", renderArgs{preset: "genesis-chain"}, "--name is required"},
		{"unknown preset", renderArgs{preset: "no-such", name: "x"}, "unknown preset"},
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

func TestRender_SetOverrides(t *testing.T) {
	got, err := render(renderArgs{
		preset: "rpc",
		name:   "x",
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
		preset: "rpc",
		name:   "x",
		image:  "from-flag:1",
		sets:   []string{"spec.template.spec.image=from-set:2"},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	image, _, _ := unstructured.NestedString(got.Object, "spec", "template", "spec", "image")
	if image != "from-set:2" {
		t.Errorf("image = %q; --set should beat --image", image)
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
