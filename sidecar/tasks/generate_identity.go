package tasks

import (
	"context"
	"fmt"

	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seilog"
)

var identityLog = seilog.NewLogger("seictl", "task", "generate-identity")

const identityMarkerFile = ".sei-sidecar-identity-done"

// IdentityGenerator runs `seid init` to create the validator identity
// (node_key.json, priv_validator_key.json, initial genesis.json).
type IdentityGenerator struct {
	homeDir string
	run     CommandRunner
}

// NewIdentityGenerator creates a generator targeting the given home directory.
func NewIdentityGenerator(homeDir string, runner CommandRunner) *IdentityGenerator {
	if runner == nil {
		runner = DefaultCommandRunner
	}
	return &IdentityGenerator{homeDir: homeDir, run: runner}
}

// Handler returns an engine.TaskHandler for the generate-identity task type.
//
// Expected params: {"chainId": "...", "moniker": "..."}
func (g *IdentityGenerator) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		if markerExists(g.homeDir, identityMarkerFile) {
			identityLog.Debug("already completed, skipping")
			return nil
		}

		chainID, _ := params["chainId"].(string)
		if chainID == "" {
			return fmt.Errorf("generate-identity: missing required param 'chainId'")
		}
		moniker, _ := params["moniker"].(string)
		if moniker == "" {
			return fmt.Errorf("generate-identity: missing required param 'moniker'")
		}

		identityLog.Info("running seid init", "chainId", chainID, "moniker", moniker)

		_, err := g.run(ctx, "seid", "init", moniker,
			"--chain-id", chainID,
			"--home", g.homeDir,
		)
		if err != nil {
			return fmt.Errorf("generate-identity: seid init: %w", err)
		}

		identityLog.Info("identity generated", "moniker", moniker)
		return writeMarker(g.homeDir, identityMarkerFile)
	}
}
