package tasks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	authtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/auth/types"
	banktypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/bank/types"
	genutiltypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/genutil/types"
)

// minimalGenesis writes a Tendermint genesis.json with empty app_state;
// GetGenesisStateFromAppState defaults absent module keys, so this is
// enough for auth+bank append tests.
func minimalGenesis(t *testing.T, homeDir string) string {
	t.Helper()
	configDir := filepath.Join(homeDir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	genFile := filepath.Join(configDir, "genesis.json")
	body := `{
		"chain_id": "test-chain-1",
		"genesis_time": "2026-01-01T00:00:00Z",
		"initial_height": "1",
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
	if err := os.WriteFile(genFile, []byte(body), 0o644); err != nil {
		t.Fatalf("write genesis: %v", err)
	}
	return genFile
}

func readBankBalances(t *testing.T, genFile string) []banktypes.Balance {
	t.Helper()
	cdc, _ := makeCodec()
	appState, _, err := genutiltypes.GenesisStateFromGenFile(genFile)
	if err != nil {
		t.Fatalf("reading genesis: %v", err)
	}
	bank := banktypes.GetGenesisStateFromAppState(cdc, appState)
	return bank.Balances
}

func readAuthAccountAddrs(t *testing.T, genFile string) []string {
	t.Helper()
	cdc, _ := makeCodec()
	appState, _, err := genutiltypes.GenesisStateFromGenFile(genFile)
	if err != nil {
		t.Fatalf("reading genesis: %v", err)
	}
	auth := authtypes.GetGenesisStateFromAppState(cdc, appState)
	accs, err := authtypes.UnpackAccounts(auth.Accounts)
	if err != nil {
		t.Fatalf("unpacking accounts: %v", err)
	}
	out := make([]string, 0, len(accs))
	for _, a := range accs {
		out = append(out, a.GetAddress().String())
	}
	return out
}

func TestAddExternalGenesisAccounts_NoOpOnEmpty(t *testing.T) {
	homeDir := t.TempDir()
	genFile := minimalGenesis(t, homeDir)
	mtimeBefore := mustModTime(t, genFile)

	a := NewGenesisAssembler(homeDir, "bucket", "region", "test-chain-1", nil, nil)
	if err := a.addExternalGenesisAccounts(nil); err != nil {
		t.Fatalf("nil accounts: %v", err)
	}
	if err := a.addExternalGenesisAccounts([]GenesisAccountEntry{}); err != nil {
		t.Fatalf("empty accounts: %v", err)
	}

	if mustModTime(t, genFile) != mtimeBefore {
		t.Errorf("genesis.json was rewritten on no-op input")
	}
}

func TestAddExternalGenesisAccounts_AppendsBalanceAndAccount(t *testing.T) {
	homeDir := t.TempDir()
	genFile := minimalGenesis(t, homeDir)

	a := NewGenesisAssembler(homeDir, "bucket", "region", "test-chain-1", nil, nil)
	addr := "sei1zg69v7y6hn00qy352euf40x77qfrg4nclsjzp9"
	bal := "1000000000usei"

	err := a.addExternalGenesisAccounts([]GenesisAccountEntry{{Address: addr, Balance: bal}})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	balances := readBankBalances(t, genFile)
	found := false
	for _, b := range balances {
		if b.Address == addr {
			found = true
			if b.Coins.String() != "1000000000usei" {
				t.Errorf("balance: got %s, want %s", b.Coins.String(), bal)
			}
			break
		}
	}
	if !found {
		t.Errorf("address %s not found in bank.balances; got %v", addr, balances)
	}

	addrs := readAuthAccountAddrs(t, genFile)
	if !containsAddr(addrs, addr) {
		t.Errorf("address %s not found in auth.accounts; got %v", addr, addrs)
	}
}

func TestAddExternalGenesisAccounts_CollisionHardFails(t *testing.T) {
	homeDir := t.TempDir()
	_ = minimalGenesis(t, homeDir)

	a := NewGenesisAssembler(homeDir, "bucket", "region", "test-chain-1", nil, nil)
	addr := "sei1zg69v7y6hn00qy352euf40x77qfrg4nclsjzp9"

	// First add succeeds.
	if err := a.addExternalGenesisAccounts([]GenesisAccountEntry{{Address: addr, Balance: "1usei"}}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	// Second add of the same address must error.
	err := a.addExternalGenesisAccounts([]GenesisAccountEntry{{Address: addr, Balance: "999usei"}})
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	if !strings.Contains(err.Error(), "collides") {
		t.Errorf("collision error message: got %q, want substring 'collides'", err.Error())
	}
}

func TestAddExternalGenesisAccounts_RejectsDuplicateInSameBatch(t *testing.T) {
	homeDir := t.TempDir()
	_ = minimalGenesis(t, homeDir)

	a := NewGenesisAssembler(homeDir, "bucket", "region", "test-chain-1", nil, nil)
	addr := "sei1zg69v7y6hn00qy352euf40x77qfrg4nclsjzp9"
	err := a.addExternalGenesisAccounts([]GenesisAccountEntry{
		{Address: addr, Balance: "1usei"},
		{Address: addr, Balance: "2usei"},
	})
	if err == nil {
		t.Fatal("expected duplicate-in-batch error, got nil")
	}
	if !strings.Contains(err.Error(), "collides") {
		t.Errorf("error: got %q, want substring 'collides'", err.Error())
	}
}

func TestAddExternalGenesisAccounts_CollidesWithPreSeeded(t *testing.T) {
	// Mirrors the production case where a validator-derived account
	// (added by addMissingGenesisAccounts) collides with an external
	// account on the same address. Seed via the codec API directly,
	// not via the function under test, to prove collision detection
	// does not depend on which path populated auth.accounts.
	homeDir := t.TempDir()
	genFile := minimalGenesis(t, homeDir)
	addr := "sei1zg69v7y6hn00qy352euf40x77qfrg4nclsjzp9"
	seedAuthAccount(t, genFile, addr)

	a := NewGenesisAssembler(homeDir, "bucket", "region", "test-chain-1", nil, nil)
	err := a.addExternalGenesisAccounts([]GenesisAccountEntry{{Address: addr, Balance: "1usei"}})
	if err == nil {
		t.Fatal("expected collision against pre-seeded account, got nil")
	}
	if !strings.Contains(err.Error(), "collides") {
		t.Errorf("error: got %q, want substring 'collides'", err.Error())
	}
}

// seedAuthAccount writes a single bech32 entry into auth.accounts on the
// genesis file via the same codec path the production code uses.
func seedAuthAccount(t *testing.T, genFile, bech32 string) {
	t.Helper()
	cdc, _ := makeCodec()
	ensureBech32()
	appState, genDoc, err := genutiltypes.GenesisStateFromGenFile(genFile)
	if err != nil {
		t.Fatalf("reading genesis: %v", err)
	}
	authGenState := authtypes.GetGenesisStateFromAppState(cdc, appState)
	accs, err := authtypes.UnpackAccounts(authGenState.Accounts)
	if err != nil {
		t.Fatalf("unpacking: %v", err)
	}
	addr, err := sdk.AccAddressFromBech32(bech32)
	if err != nil {
		t.Fatalf("parsing addr: %v", err)
	}
	accs = append(accs, authtypes.NewBaseAccount(addr, nil, 0, 0))
	bank := banktypes.GetGenesisStateFromAppState(cdc, appState)
	if err := writeBackAuthAndBank("test-seed", cdc, genFile, genDoc, appState, authGenState, accs, bank); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestAddExternalGenesisAccounts_RejectsBadBech32(t *testing.T) {
	homeDir := t.TempDir()
	_ = minimalGenesis(t, homeDir)

	a := NewGenesisAssembler(homeDir, "bucket", "region", "test-chain-1", nil, nil)
	err := a.addExternalGenesisAccounts([]GenesisAccountEntry{{Address: "not-bech32", Balance: "1usei"}})
	if err == nil {
		t.Fatal("expected bech32 error, got nil")
	}
}

func TestAddExternalGenesisAccounts_RejectsBadBalance(t *testing.T) {
	homeDir := t.TempDir()
	_ = minimalGenesis(t, homeDir)

	a := NewGenesisAssembler(homeDir, "bucket", "region", "test-chain-1", nil, nil)
	err := a.addExternalGenesisAccounts([]GenesisAccountEntry{{
		Address: "sei1zg69v7y6hn00qy352euf40x77qfrg4nclsjzp9",
		Balance: "not-a-coin",
	}})
	if err == nil {
		t.Fatal("expected balance parse error, got nil")
	}
}

func TestForkAddExternalGenesisAccounts_UpdatesSupply(t *testing.T) {
	homeDir := t.TempDir()
	genFile := minimalGenesis(t, homeDir)

	a := NewGenesisForkAssembler(homeDir, "bucket", "region", nil, nil)
	addr := "sei1zg69v7y6hn00qy352euf40x77qfrg4nclsjzp9"
	err := a.addExternalGenesisAccounts([]GenesisAccountEntry{{Address: addr, Balance: "5000usei"}})
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	cdc, _ := makeCodec()
	appState, _, err := genutiltypes.GenesisStateFromGenFile(genFile)
	if err != nil {
		t.Fatalf("reading genesis: %v", err)
	}
	bank := banktypes.GetGenesisStateFromAppState(cdc, appState)

	// Supply must equal totalSupply or bank.InitGenesis panics
	// (sei-cosmos/x/bank/keeper/genesis.go:48).
	if bank.Supply.AmountOf("usei").String() != "5000" {
		t.Errorf("supply usei: got %s, want 5000", bank.Supply.AmountOf("usei").String())
	}
}

func TestForkAddExternalGenesisAccounts_CollisionHardFails(t *testing.T) {
	homeDir := t.TempDir()
	_ = minimalGenesis(t, homeDir)

	a := NewGenesisForkAssembler(homeDir, "bucket", "region", nil, nil)
	addr := "sei1zg69v7y6hn00qy352euf40x77qfrg4nclsjzp9"

	if err := a.addExternalGenesisAccounts([]GenesisAccountEntry{{Address: addr, Balance: "1usei"}}); err != nil {
		t.Fatalf("first add: %v", err)
	}
	err := a.addExternalGenesisAccounts([]GenesisAccountEntry{{Address: addr, Balance: "999usei"}})
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
}

func mustModTime(t *testing.T, path string) int64 {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	return st.ModTime().UnixNano()
}

func containsAddr(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}
