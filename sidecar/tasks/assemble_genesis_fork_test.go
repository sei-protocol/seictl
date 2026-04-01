package tasks

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForkRequest_Validate(t *testing.T) {
	tests := []struct {
		name    string
		req     AssembleGenesisForkRequest
		wantErr string
	}{
		{
			name:    "missing sourceChainId",
			req:     AssembleGenesisForkRequest{ChainID: "fork-1", AccountBalance: "1usei", Namespace: "ns", Nodes: []AssembleNodeEntry{{Name: "n"}}},
			wantErr: "sourceChainId",
		},
		{
			name:    "missing chainId",
			req:     AssembleGenesisForkRequest{SourceChainID: "pacific-1", AccountBalance: "1usei", Namespace: "ns", Nodes: []AssembleNodeEntry{{Name: "n"}}},
			wantErr: "chainId",
		},
		{
			name:    "missing accountBalance",
			req:     AssembleGenesisForkRequest{SourceChainID: "pacific-1", ChainID: "fork-1", Namespace: "ns", Nodes: []AssembleNodeEntry{{Name: "n"}}},
			wantErr: "accountBalance",
		},
		{
			name:    "missing namespace",
			req:     AssembleGenesisForkRequest{SourceChainID: "pacific-1", ChainID: "fork-1", AccountBalance: "1usei", Nodes: []AssembleNodeEntry{{Name: "n"}}},
			wantErr: "namespace",
		},
		{
			name:    "empty nodes",
			req:     AssembleGenesisForkRequest{SourceChainID: "pacific-1", ChainID: "fork-1", AccountBalance: "1usei", Namespace: "ns"},
			wantErr: "nodes",
		},
		{
			name: "valid",
			req:  AssembleGenesisForkRequest{SourceChainID: "pacific-1", ChainID: "fork-1", AccountBalance: "1usei", Namespace: "ns", Nodes: []AssembleNodeEntry{{Name: "n"}}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.validate()
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not contain %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestForkRequest_NodeNames(t *testing.T) {
	req := AssembleGenesisForkRequest{
		Nodes: []AssembleNodeEntry{{Name: "a"}, {Name: "b"}, {Name: "c"}},
	}
	names := req.nodeNames()
	if len(names) != 3 || names[0] != "a" || names[1] != "b" || names[2] != "c" {
		t.Errorf("nodeNames() = %v, want [a b c]", names)
	}
}

func TestForkAssembler_MarkerIdempotency(t *testing.T) {
	homeDir := t.TempDir()
	if err := writeMarker(homeDir, forkAssembleMarkerFile); err != nil {
		t.Fatal(err)
	}

	assembler := NewGenesisForkAssembler(homeDir, "bucket", "region", nil, nil)
	handler := assembler.Handler()

	err := handler(context.Background(), map[string]any{
		"sourceChainId":  "pacific-1",
		"chainId":        "fork-1",
		"accountBalance": "1usei",
		"namespace":      "ns",
		"nodes":          []any{map[string]any{"name": "n"}},
	})
	if err != nil {
		t.Fatalf("expected nil (marker skip), got: %v", err)
	}
}

func TestForkAssembler_DownloadExportedState(t *testing.T) {
	homeDir := t.TempDir()

	exportedGenesis := `{"chain_id":"pacific-1","initial_height":"100","app_state":{}}`

	s3Objects := &mockS3GetObject{objects: map[string][]byte{
		"pacific-1/exported-state.json": []byte(exportedGenesis),
	}}
	s3Factory := func(_ context.Context, _ string) (S3GetObjectAPI, error) {
		return s3Objects, nil
	}

	assembler := NewGenesisForkAssembler(homeDir, "bucket", "region", s3Factory, nil)

	if err := assembler.downloadExportedState(context.Background(), "pacific-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	genFile := filepath.Join(homeDir, "config", "genesis.json")
	data, err := os.ReadFile(genFile)
	if err != nil {
		t.Fatalf("genesis.json not written: %v", err)
	}
	if !bytes.Contains(data, []byte("pacific-1")) {
		t.Errorf("genesis.json does not contain expected chain ID")
	}
}

func TestForkAssembler_RewriteChainMeta(t *testing.T) {
	homeDir := t.TempDir()
	configDir := filepath.Join(homeDir, "config")
	os.MkdirAll(configDir, 0o755)

	// Write a valid Tendermint genesis doc format.
	genesis := `{
		"chain_id": "pacific-1",
		"genesis_time": "2024-01-01T00:00:00Z",
		"initial_height": "100",
		"consensus_params": {
			"block": {"max_bytes": "22020096", "max_gas": "-1"},
			"evidence": {"max_age_num_blocks": "100000", "max_age_duration": "172800000000000", "max_bytes": "1048576"},
			"validator": {"pub_key_types": ["ed25519"]},
			"version": {}
		},
		"validators": [],
		"app_hash": "",
		"app_state": {}
	}`
	os.WriteFile(filepath.Join(configDir, "genesis.json"), []byte(genesis), 0o644)

	assembler := NewGenesisForkAssembler(homeDir, "bucket", "region", nil, nil)
	if err := assembler.rewriteChainMeta("fork-test-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	rewritten, _ := os.ReadFile(filepath.Join(configDir, "genesis.json"))
	var doc map[string]any
	json.Unmarshal(rewritten, &doc)

	if doc["chain_id"] != "fork-test-1" {
		t.Errorf("chain_id = %v, want fork-test-1", doc["chain_id"])
	}
	if doc["validators"] != nil {
		t.Errorf("validators should be nil/empty, got %v", doc["validators"])
	}
}

func TestForkAssembler_StripValidatorState(t *testing.T) {
	homeDir := t.TempDir()
	configDir := filepath.Join(homeDir, "config")
	os.MkdirAll(configDir, 0o755)

	stakingState := map[string]any{
		"params":               map[string]any{"bond_denom": "usei"},
		"validators":           []any{map[string]any{"operator": "val1"}},
		"delegations":          []any{map[string]any{"delegator": "del1"}},
		"last_total_power":     "100",
		"last_validator_powers": []any{map[string]any{"address": "val1"}},
	}
	slashingState := map[string]any{
		"params":        map[string]any{"signed_blocks_window": "100"},
		"signing_infos": []any{map[string]any{"address": "val1"}},
		"missed_blocks": []any{map[string]any{"address": "val1"}},
	}
	evidenceState := map[string]any{
		"evidence": []any{map[string]any{"type": "duplicate_vote"}},
	}
	distributionState := map[string]any{
		"params":                              map[string]any{"community_tax": "0.02"},
		"outstanding_rewards":                 []any{map[string]any{"val": "val1"}},
		"validator_accumulated_commissions":   []any{map[string]any{"val": "val1"}},
		"validator_historical_rewards":        []any{map[string]any{"val": "val1"}},
		"validator_current_rewards":           []any{map[string]any{"val": "val1"}},
		"delegator_starting_infos":            []any{map[string]any{"del": "del1"}},
		"validator_slash_events":              []any{map[string]any{"val": "val1"}},
	}

	appState := map[string]any{
		"staking":      stakingState,
		"slashing":     slashingState,
		"distribution": distributionState,
		"evidence":     evidenceState,
		"bank":         map[string]any{"balances": []any{}},
	}
	appStateJSON, _ := json.Marshal(appState)

	genesis := map[string]any{
		"chain_id":       "test",
		"genesis_time":   "2024-01-01T00:00:00Z",
		"initial_height": "1",
		"app_state":      json.RawMessage(appStateJSON),
	}
	data, _ := json.Marshal(genesis)
	os.WriteFile(filepath.Join(configDir, "genesis.json"), data, 0o644)

	assembler := NewGenesisForkAssembler(homeDir, "bucket", "region", nil, nil)
	if err := assembler.stripValidatorState(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	stripped, _ := os.ReadFile(filepath.Join(configDir, "genesis.json"))
	var doc map[string]json.RawMessage
	json.Unmarshal(stripped, &doc)

	var as map[string]json.RawMessage
	json.Unmarshal(doc["app_state"], &as)

	// Staking: validators stripped, params preserved
	var staking map[string]json.RawMessage
	json.Unmarshal(as["staking"], &staking)
	if string(staking["validators"]) != "[]" {
		t.Errorf("staking.validators = %s, want []", staking["validators"])
	}
	if string(staking["delegations"]) != "[]" {
		t.Errorf("staking.delegations = %s, want []", staking["delegations"])
	}
	// Params should be preserved
	var params map[string]any
	json.Unmarshal(staking["params"], &params)
	if params["bond_denom"] != "usei" {
		t.Errorf("staking.params.bond_denom lost, got %v", params["bond_denom"])
	}

	// Slashing: signing_infos stripped, params preserved
	var slashing map[string]json.RawMessage
	json.Unmarshal(as["slashing"], &slashing)
	if string(slashing["signing_infos"]) != "[]" {
		t.Errorf("slashing.signing_infos = %s, want []", slashing["signing_infos"])
	}

	// Evidence: cleared
	var evidence map[string]json.RawMessage
	json.Unmarshal(as["evidence"], &evidence)
	if string(evidence["evidence"]) != "[]" {
		t.Errorf("evidence.evidence = %s, want []", evidence["evidence"])
	}

	// Distribution: validator records stripped, params preserved
	var dist map[string]json.RawMessage
	json.Unmarshal(as["distribution"], &dist)
	if string(dist["outstanding_rewards"]) != "[]" {
		t.Errorf("distribution.outstanding_rewards = %s, want []", dist["outstanding_rewards"])
	}

	// Bank: untouched
	var bank map[string]json.RawMessage
	json.Unmarshal(as["bank"], &bank)
	if string(bank["balances"]) != "[]" {
		t.Errorf("bank should be untouched, balances = %s", bank["balances"])
	}
}

func TestForkAssembler_SetModuleFields_MissingModule(t *testing.T) {
	appState := map[string]json.RawMessage{
		"bank": json.RawMessage(`{"balances":[]}`),
	}
	// Should not panic on missing module
	setModuleFields(appState, "nonexistent", map[string]json.RawMessage{
		"field": json.RawMessage(`[]`),
	})
	if _, ok := appState["nonexistent"]; ok {
		t.Error("should not create missing module")
	}
}

func TestForkAssembler_DownloadGentxFiles(t *testing.T) {
	homeDir := t.TempDir()
	os.MkdirAll(filepath.Join(homeDir, "config"), 0o755)

	s3Objects := &mockS3GetObject{objects: map[string][]byte{
		"fork-1/val-0/gentx.json": []byte(`{"gentx":"val0"}`),
		"fork-1/val-1/gentx.json": []byte(`{"gentx":"val1"}`),
	}}
	s3Factory := func(_ context.Context, _ string) (S3GetObjectAPI, error) {
		return s3Objects, nil
	}

	assembler := NewGenesisForkAssembler(homeDir, "bucket", "region", s3Factory, nil)
	if err := assembler.downloadGentxFiles(context.Background(), "fork-1", []string{"val-0", "val-1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	gentxDir := filepath.Join(homeDir, "config", "gentx")
	for _, name := range []string{"val-0", "val-1"} {
		path := filepath.Join(gentxDir, name+"-gentx.json")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("gentx file not found: %s", path)
		}
	}
}

func TestForkAssembler_UploadGenesis(t *testing.T) {
	homeDir := t.TempDir()
	configDir := filepath.Join(homeDir, "config")
	os.MkdirAll(configDir, 0o755)
	os.WriteFile(filepath.Join(configDir, "genesis.json"), []byte(`{"chain_id":"fork-1"}`), 0o644)

	mock := newMockS3Uploader()
	assembler := NewGenesisForkAssembler(homeDir, "bucket", "region", nil, mockUploaderFactory(mock))

	if err := assembler.uploadGenesis(context.Background(), "fork-1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	key := "bucket/fork-1/genesis.json"
	if _, ok := mock.uploads[key]; !ok {
		t.Errorf("expected upload at %q, got keys: %v", key, mapKeys(mock.uploads))
	}
}

func TestForkAssembler_UploadPeers(t *testing.T) {
	homeDir := t.TempDir()

	s3Objects := &mockS3GetObject{objects: map[string][]byte{
		"fork-1/val-0/identity.json": []byte(`{"nodeId":"node-id-0"}`),
		"fork-1/val-1/identity.json": []byte(`{"nodeId":"node-id-1"}`),
	}}
	s3Factory := func(_ context.Context, _ string) (S3GetObjectAPI, error) {
		return s3Objects, nil
	}

	mock := newMockS3Uploader()
	assembler := NewGenesisForkAssembler(homeDir, "bucket", "region", s3Factory, mockUploaderFactory(mock))

	if err := assembler.uploadPeers(context.Background(), "fork-1", "default", []string{"val-0", "val-1"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	key := "bucket/fork-1/peers.json"
	data, ok := mock.uploads[key]
	if !ok {
		t.Fatalf("expected upload at %q, got keys: %v", key, mapKeys(mock.uploads))
	}

	var peers []map[string]string
	json.Unmarshal(data, &peers)
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers, got %d", len(peers))
	}
	if peers[0]["nodeId"] != "node-id-0" {
		t.Errorf("peer[0].nodeId = %q, want node-id-0", peers[0]["nodeId"])
	}
}

func mapKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
