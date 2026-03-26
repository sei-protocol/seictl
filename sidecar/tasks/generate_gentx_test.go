package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mockGentxRunner(homeDir string) (CommandRunner, *[][]string) {
	var calls [][]string
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		cmd := append([]string{name}, args...)
		calls = append(calls, cmd)

		if name == "seid" && len(args) > 0 && args[0] == "keys" {
			resp, _ := json.Marshal(map[string]string{
				"address": "sei1testaddr",
			})
			return resp, nil
		}
		return nil, nil
	}
	return runner, &calls
}

func TestGentxGenerator_FullFlow(t *testing.T) {
	homeDir := t.TempDir()
	runner, calls := mockGentxRunner(homeDir)

	handler := NewGentxGenerator(homeDir, runner).Handler()
	err := handler(context.Background(), map[string]any{
		"chainId":        "test-chain-1",
		"stakingAmount":  "1000000usei",
		"accountBalance": "10000000usei",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(*calls) != 3 {
		t.Fatalf("expected 3 seid commands, got %d: %v", len(*calls), *calls)
	}

	keysCmd := (*calls)[0]
	if !contains(keysCmd, "keys") || !contains(keysCmd, "add") || !contains(keysCmd, "validator") {
		t.Errorf("first command should be keys add: %v", keysCmd)
	}

	addAcctCmd := (*calls)[1]
	if !contains(addAcctCmd, "add-genesis-account") || !contains(addAcctCmd, "sei1testaddr") {
		t.Errorf("second command should be add-genesis-account with address: %v", addAcctCmd)
	}

	gentxCmd := (*calls)[2]
	if !contains(gentxCmd, "gentx") || !contains(gentxCmd, "1000000usei") {
		t.Errorf("third command should be gentx with staking amount: %v", gentxCmd)
	}
}

func TestGentxGenerator_Idempotent(t *testing.T) {
	homeDir := t.TempDir()
	runner, calls := mockGentxRunner(homeDir)

	handler := NewGentxGenerator(homeDir, runner).Handler()
	params := map[string]any{
		"chainId":        "test-chain-1",
		"stakingAmount":  "1000000usei",
		"accountBalance": "10000000usei",
	}

	if err := handler(context.Background(), params); err != nil {
		t.Fatalf("first call: %v", err)
	}
	firstCallCount := len(*calls)

	if err := handler(context.Background(), params); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if len(*calls) != firstCallCount {
		t.Fatalf("expected no new commands on second call, got %d total", len(*calls))
	}
}

func TestGentxGenerator_MissingParams(t *testing.T) {
	runner, _ := mockGentxRunner(t.TempDir())
	handler := NewGentxGenerator(t.TempDir(), runner).Handler()

	tests := []struct {
		name   string
		params map[string]any
	}{
		{"missing chainId", map[string]any{"stakingAmount": "1", "accountBalance": "1"}},
		{"missing stakingAmount", map[string]any{"chainId": "c", "accountBalance": "1"}},
		{"missing accountBalance", map[string]any{"chainId": "c", "stakingAmount": "1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := handler(context.Background(), tt.params)
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestGentxGenerator_KeysAddFailure(t *testing.T) {
	homeDir := t.TempDir()
	runner := func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "seid" && len(args) > 0 && args[0] == "keys" {
			return nil, fmt.Errorf("keyring error")
		}
		return nil, nil
	}

	handler := NewGentxGenerator(homeDir, runner).Handler()
	err := handler(context.Background(), map[string]any{
		"chainId": "c", "stakingAmount": "1", "accountBalance": "1",
	})
	if err == nil {
		t.Fatal("expected error when keys add fails")
	}

	if _, statErr := os.Stat(filepath.Join(homeDir, gentxMarkerFile)); statErr == nil {
		t.Fatal("marker should not exist after failure")
	}
}

func contains(s []string, substr string) bool {
	for _, v := range s {
		if strings.Contains(v, substr) {
			return true
		}
	}
	return false
}
