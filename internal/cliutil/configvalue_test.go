package cliutil

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestParseConfigValue(t *testing.T) {
	cases := []struct {
		name    string
		expr    string
		want    map[string]interface{}
		wantErr string
	}{
		{"bool", "config.toml:evm-only=true", map[string]interface{}{"fileName": "config.toml", "key": "evm-only", "value": true}, ""},
		{"integer stays int64", "config.toml:p2p.max_num_inbound_peers=9007199254740993", map[string]interface{}{"fileName": "config.toml", "key": "p2p.max_num_inbound_peers", "value": int64(9007199254740993)}, ""},
		{"float", "app.toml:x.ratio=0.25", map[string]interface{}{"fileName": "app.toml", "key": "x.ratio", "value": 0.25}, ""},
		{"numbers inside array", "app.toml:x.list=[1,2.5]", map[string]interface{}{"fileName": "app.toml", "key": "x.list", "value": []interface{}{int64(1), 2.5}}, ""},
		{"nested key bool", "app.toml:giga_executor.occ_enabled=false", map[string]interface{}{"fileName": "app.toml", "key": "giga_executor.occ_enabled", "value": false}, ""},
		{"bare string", "app.toml:state-store.sc-write-mode=async", map[string]interface{}{"fileName": "app.toml", "key": "state-store.sc-write-mode", "value": "async"}, ""},
		{"quoted numeric string", `config.toml:consensus.timeout_commit="400ms"`, map[string]interface{}{"fileName": "config.toml", "key": "consensus.timeout_commit", "value": "400ms"}, ""},
		{"array", `app.toml:evm.enabled_legacy_sei_apis=["a","b"]`, map[string]interface{}{"fileName": "app.toml", "key": "evm.enabled_legacy_sei_apis", "value": []interface{}{"a", "b"}}, ""},
		{"value containing equals", "app.toml:x.y=a=b", map[string]interface{}{"fileName": "app.toml", "key": "x.y", "value": "a=b"}, ""},
		{"missing colon", "config.toml=true", nil, "missing ':'"},
		{"missing equals", "config.toml:evm-only", nil, "missing '='"},
		{"non toml file", "autobahn.json:foo=1", nil, "only TOML files"},
		{"bad key", "config.toml:evm..only=true", nil, "dotted TOML path"},
		{"empty value", "config.toml:evm-only=", nil, "empty value"},
		{"null", "config.toml:evm-only=null", nil, "rejected by the CRD"},
		{"nested null", `app.toml:t={"a":null}`, nil, "nested null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConfigValue(tc.expr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestApplyConfigValues_MergesByFileAndKey(t *testing.T) {
	root := map[string]interface{}{
		"spec": map[string]interface{}{
			"configValues": []interface{}{
				map[string]interface{}{"fileName": "config.toml", "key": "evm-only", "value": false},
				map[string]interface{}{"fileName": "app.toml", "key": "evm.http_port", "value": int64(8545)},
			},
		},
	}
	err := ApplyConfigValues(root, []string{
		"config.toml:evm-only=true",
		"app.toml:giga_executor.enabled=true",
	}, "spec", "configValues")
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	got := root["spec"].(map[string]interface{})["configValues"].([]interface{})
	want := []interface{}{
		map[string]interface{}{"fileName": "config.toml", "key": "evm-only", "value": true},
		map[string]interface{}{"fileName": "app.toml", "key": "evm.http_port", "value": int64(8545)},
		map[string]interface{}{"fileName": "app.toml", "key": "giga_executor.enabled", "value": true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v\nwant %#v", got, want)
	}
}

func TestApplyConfigValues_RejectsOverMaxItems(t *testing.T) {
	exprs := make([]string, 0, MaxConfigValues+1)
	for i := 0; i <= MaxConfigValues; i++ {
		exprs = append(exprs, fmt.Sprintf("app.toml:k%d=1", i))
	}
	root := map[string]interface{}{"spec": map[string]interface{}{}}
	err := ApplyConfigValues(root, exprs, "spec", "configValues")
	if err == nil || !strings.Contains(err.Error(), "at most 100") {
		t.Fatalf("want max-items error, got %v", err)
	}
}

func TestApplyConfigValues_RejectsPreexistingDuplicate(t *testing.T) {
	root := map[string]interface{}{"spec": map[string]interface{}{"configValues": []interface{}{
		map[string]interface{}{"fileName": "app.toml", "key": "a.b", "value": int64(1)},
		map[string]interface{}{"fileName": "app.toml", "key": "a.b", "value": int64(2)},
	}}}
	err := ApplyConfigValues(root, []string{"app.toml:c.d=true"}, "spec", "configValues")
	if err == nil || !strings.Contains(err.Error(), "app.toml:a.b more than once") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestApplyConfigValues_NoopWhenEmpty(t *testing.T) {
	root := map[string]interface{}{"spec": map[string]interface{}{}}
	if err := ApplyConfigValues(root, nil, "spec", "configValues"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, found := root["spec"].(map[string]interface{})["configValues"]; found {
		t.Fatal("empty flag list must not create spec.configValues")
	}
}
