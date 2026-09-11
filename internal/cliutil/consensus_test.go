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
			if tc.wantEngine == "" && !tc.wantEvmOnly {
				if _, present, _ := unstructured.NestedFieldNoCopy(root, "spec", "consensus"); present {
					t.Fatal("spec.consensus must stay absent")
				}
				return
			}
			engine, _, _ := unstructured.NestedString(root, "spec", "consensus", "engine")
			if engine != tc.wantEngine {
				t.Fatalf("engine: got %q, want %q", engine, tc.wantEngine)
			}
			evmOnly, present, _ := unstructured.NestedBool(root, "spec", "consensus", "evmOnly")
			if present != tc.wantEvmOnly || evmOnly != tc.wantEvmOnly {
				t.Fatalf("evmOnly: present=%v value=%v, want %v", present, evmOnly, tc.wantEvmOnly)
			}
		})
	}
}

func TestValidateConsensus(t *testing.T) {
	cases := []struct {
		name      string
		consensus map[string]interface{}
		wantErr   string
	}{
		{name: "absent"},
		{name: "engine only", consensus: map[string]interface{}{"engine": "Autobahn"}},
		{name: "evmOnly false under tendermint", consensus: map[string]interface{}{"engine": "Tendermint", "evmOnly": false}},
		{name: "evmOnly under autobahn", consensus: map[string]interface{}{"engine": "Autobahn", "evmOnly": true}},
		{name: "evmOnly without engine", consensus: map[string]interface{}{"evmOnly": true}, wantErr: `spec.consensus.engine is ""`},
		{name: "evmOnly under tendermint via --set", consensus: map[string]interface{}{"engine": "Tendermint", "evmOnly": true}, wantErr: `spec.consensus.engine is "Tendermint"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec := map[string]interface{}{}
			if tc.consensus != nil {
				spec["consensus"] = tc.consensus
			}
			err := ValidateConsensus(map[string]interface{}{"spec": spec})
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
			}
		})
	}
}
