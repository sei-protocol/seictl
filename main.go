package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/urfave/cli/v3"
)

var (
	destinations = struct {
		home      string
		outputter struct {
			output  string
			inPlace bool
		}
		config struct {
			patch struct {
				file string
			}
			target string
		}
		genesis struct {
			patch struct {
				file string
			}
		}
		patch struct {
			file   string
			target string
		}
		await struct {
			timeout   time.Duration
			validator struct {
				address string
			}
		}
	}{}

	seictlCmd = cli.Command{
		Name: "seictl",
		Commands: []*cli.Command{
			&configCmd,
			&genesisCmd,
			&patchCmd,
			&awaitCmd,
		},
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "home",
				Sources:     cli.EnvVars("SEI_HOME"),
				Destination: &destinations.home,
				TakesFile:   true,
				Config: cli.StringConfig{
					TrimSpace: true,
				},
				Action: func(ctx context.Context, command *cli.Command, s string) error {
					return command.Set("home", filepath.Clean(s))
				},
			},
		},
	}
)

func main() {
	if err := seictlCmd.Run(context.Background(), os.Args); err != nil {
		log.Fatal(err)
	}
}
