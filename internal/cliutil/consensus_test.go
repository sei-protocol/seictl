package cliutil

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestApplyConsensus(t *testing.T) {
	cases := []struct {
		name        string
		engine      string
		evmOnly     bool
		wantEngine  string
		wantEvmOnly bool
		wantErr     string
	}{
		{name: "unset leaves spec.consensus absent"},
		{name: "exact", engine: "Autobahn", wantEngine: "Autobahn"},
		{name: "case-insensitive", engine: "tendermint", wantEngine: "Tendermint"},
		{name: "autobahn evm-only", engine: "autobahn", evmOnly: true, wantEngine: "Autobahn", wantEvmOnly: true},
		{name: "evm-only without engine", evmOnly: true, wantErr: "requires --consensus-engine Autobahn"},
		{name: "evm-only under tendermint", engine: "Tendermint", evmOnly: true, wantErr: "requires --consensus-engine Autobahn, got Tendermint"},
		{name: "unknown engine", engine: "Narwhal", wantErr: "not one of Tendermint, Autobahn"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := map[string]interface{}{"spec": map[string]interface{}{}}
			err := ApplyConsensus(root, tc.engine, tc.evmOnly)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			engine, found, _ := unstructured.NestedString(root, "spec", "consensus", "engine")
			if tc.wantEngine == "" {
				if _, present, _ := unstructured.NestedFieldNoCopy(root, "spec", "consensus"); present {
					t.Fatal("spec.consensus must stay absent")
				}
				return
			}
			if !found || engine != tc.wantEngine {
				t.Fatalf("engine: got %q, want %q", engine, tc.wantEngine)
			}
			evmOnly, present, _ := unstructured.NestedBool(root, "spec", "consensus", "evmOnly")
			if present != tc.wantEvmOnly || evmOnly != tc.wantEvmOnly {
				t.Fatalf("evmOnly: present=%v value=%v, want %v", present, evmOnly, tc.wantEvmOnly)
			}
		})
	}
}
