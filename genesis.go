package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/urfave/cli/v3"
)

var genesisCmd = cli.Command{
	Name:  "genesis",
	Usage: "Manage Sei genesis JSON file",
	Commands: []*cli.Command{
		{
			Name:                   "patch",
			Usage:                  "Apply a merge-patch to the Sei genesis JSON file",
			MutuallyExclusiveFlags: []cli.MutuallyExclusiveFlags{outputterFlags},
			Arguments: []cli.Argument{
				&cli.StringArg{
					Name:        "file",
					Destination: &destinations.genesis.patch.file,
					Config: cli.StringConfig{
						TrimSpace: true,
					},
				},
			},
			Action: func(ctx context.Context, command *cli.Command) error {
				var patchBytes []byte
				if destinations.genesis.patch.file == "" {
					scanner := bufio.NewScanner(os.Stdin)
					scanner.Scan()
					if err := scanner.Err(); err != nil {
						return fmt.Errorf("reading input from stdin: %w", err)
					}
					patchBytes = []byte(scanner.Text())
				} else {
					var err error
					patchBytes, err = os.ReadFile(destinations.genesis.patch.file)
					if err != nil {
						return fmt.Errorf("reading patch file: %w", err)
					}
				}

				patch := make(map[string]any)
				if err := json.Unmarshal(patchBytes, &patch); err != nil {
					return fmt.Errorf("parsing patch: %w", err)
				}

				genesisPath := filepath.Join(destinations.home, "config", "genesis.json")
				genesisBytes, err := os.ReadFile(genesisPath)
				if err != nil {
					return fmt.Errorf("reading genesis file: %w", err)
				}
				genesis := make(map[string]any)
				if err := json.Unmarshal(genesisBytes, &genesis); err != nil {
					return fmt.Errorf("parsing genesis: %w", err)
				}

				patchedGenesis := mergePatch(genesis, patch)

				var prettyPatchedGenesis bytes.Buffer
				encoder := json.NewEncoder(&prettyPatchedGenesis)
				encoder.SetIndent("", "  ")
				if err := encoder.Encode(patchedGenesis); err != nil {
					return fmt.Errorf("marshalling patched genesis: %w", err)
				}
				return output(genesisPath, prettyPatchedGenesis)
			},
		},
	},
}
