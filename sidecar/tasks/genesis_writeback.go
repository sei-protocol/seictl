package tasks

import (
	"encoding/json"
	"fmt"

	tmtypes "github.com/sei-protocol/sei-chain/sei-tendermint/types"

	"github.com/sei-protocol/sei-chain/sei-cosmos/codec"
	authtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/auth/types"
	banktypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/bank/types"
	"github.com/sei-protocol/sei-chain/sei-cosmos/x/genutil"
)

// writeBackAuthAndBank serializes mutated auth + bank state and exports
// the genesis file. `prefix` flavors error messages with the calling task.
func writeBackAuthAndBank(
	prefix string,
	cdc codec.Codec,
	genFile string,
	genDoc *tmtypes.GenesisDoc,
	appState map[string]json.RawMessage,
	authGenState authtypes.GenesisState,
	accs authtypes.GenesisAccounts,
	bankGenState *banktypes.GenesisState,
) error {
	accs = authtypes.SanitizeGenesisAccounts(accs)
	genAccs, err := authtypes.PackAccounts(accs)
	if err != nil {
		return fmt.Errorf("%s: packing accounts: %w", prefix, err)
	}
	authGenState.Accounts = genAccs
	authStateBz, err := cdc.MarshalAsJSON(&authGenState)
	if err != nil {
		return fmt.Errorf("%s: marshaling auth state: %w", prefix, err)
	}
	appState[authtypes.ModuleName] = authStateBz

	bankGenState.Balances = banktypes.SanitizeGenesisBalances(bankGenState.Balances)
	bankStateBz, err := cdc.MarshalAsJSON(bankGenState)
	if err != nil {
		return fmt.Errorf("%s: marshaling bank state: %w", prefix, err)
	}
	appState[banktypes.ModuleName] = bankStateBz

	appStateJSON, err := json.Marshal(appState)
	if err != nil {
		return fmt.Errorf("%s: marshaling app state: %w", prefix, err)
	}
	genDoc.AppState = appStateJSON

	if err := genutil.ExportGenesisFile(genDoc, genFile); err != nil {
		return fmt.Errorf("%s: writing genesis: %w", prefix, err)
	}
	return nil
}
