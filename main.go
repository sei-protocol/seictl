package main

import (
	"context"
	"log"
	"os"
	"path/filepath"

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
	}{}

	seictlCmd = cli.Command{
		Name: "seictl",
		Commands: []*cli.Command{
			&configCmd,
			&genesisCmd,
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
