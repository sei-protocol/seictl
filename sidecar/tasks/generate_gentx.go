package tasks

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seilog"
)

var gentxLog = seilog.NewLogger("seictl", "task", "generate-gentx")

const (
	gentxMarkerFile  = ".sei-sidecar-gentx-done"
	validatorKeyName = "validator"
)

// GentxGenerator runs the seid commands needed to produce a genesis
// transaction: keys add → add-genesis-account → gentx.
type GentxGenerator struct {
	homeDir string
	run     CommandRunner
}

// NewGentxGenerator creates a generator targeting the given home directory.
func NewGentxGenerator(homeDir string, runner CommandRunner) *GentxGenerator {
	if runner == nil {
		runner = DefaultCommandRunner
	}
	return &GentxGenerator{homeDir: homeDir, run: runner}
}

// Handler returns an engine.TaskHandler for the generate-gentx task type.
//
// Expected params:
//
//	{
//	  "chainId":        "my-chain",
//	  "stakingAmount":  "1000000usei",
//	  "accountBalance": "10000000usei",
//	  "genesisParams":  "" (optional, reserved for future genesis customization)
//	}
func (g *GentxGenerator) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		if markerExists(g.homeDir, gentxMarkerFile) {
			gentxLog.Debug("already completed, skipping")
			return nil
		}

		chainID, _ := params["chainId"].(string)
		if chainID == "" {
			return fmt.Errorf("generate-gentx: missing required param 'chainId'")
		}
		stakingAmount, _ := params["stakingAmount"].(string)
		if stakingAmount == "" {
			return fmt.Errorf("generate-gentx: missing required param 'stakingAmount'")
		}
		accountBalance, _ := params["accountBalance"].(string)
		if accountBalance == "" {
			return fmt.Errorf("generate-gentx: missing required param 'accountBalance'")
		}

		address, err := g.addValidatorKey(ctx)
		if err != nil {
			return err
		}

		if err := g.addGenesisAccount(ctx, address, accountBalance); err != nil {
			return err
		}

		if err := g.generateGentx(ctx, chainID, stakingAmount); err != nil {
			return err
		}

		gentxLog.Info("gentx generated", "address", address)
		return writeMarker(g.homeDir, gentxMarkerFile)
	}
}

// addValidatorKey creates a local key and returns its bech32 address.
func (g *GentxGenerator) addValidatorKey(ctx context.Context) (string, error) {
	gentxLog.Info("creating validator key")

	out, err := g.run(ctx, "seid", "keys", "add", validatorKeyName,
		"--keyring-backend", "test",
		"--home", g.homeDir,
		"--output", "json",
	)
	if err != nil {
		return "", fmt.Errorf("generate-gentx: keys add: %w", err)
	}

	var keyOutput struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(out, &keyOutput); err != nil {
		return "", fmt.Errorf("generate-gentx: parsing keys output: %w", err)
	}
	if keyOutput.Address == "" {
		return "", fmt.Errorf("generate-gentx: keys add returned empty address")
	}

	gentxLog.Info("validator key created", "address", keyOutput.Address)
	return keyOutput.Address, nil
}

func (g *GentxGenerator) addGenesisAccount(ctx context.Context, address, balance string) error {
	gentxLog.Info("adding genesis account", "address", address, "balance", balance)

	_, err := g.run(ctx, "seid", "add-genesis-account", address, balance,
		"--home", g.homeDir,
	)
	if err != nil {
		return fmt.Errorf("generate-gentx: add-genesis-account: %w", err)
	}
	return nil
}

func (g *GentxGenerator) generateGentx(ctx context.Context, chainID, stakingAmount string) error {
	gentxLog.Info("generating gentx", "chainId", chainID, "stakingAmount", stakingAmount)

	_, err := g.run(ctx, "seid", "gentx", validatorKeyName, stakingAmount,
		"--chain-id", chainID,
		"--keyring-backend", "test",
		"--home", g.homeDir,
	)
	if err != nil {
		return fmt.Errorf("generate-gentx: gentx: %w", err)
	}
	return nil
}
