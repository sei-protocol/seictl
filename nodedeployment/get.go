package nodedeployment

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sei-protocol/seictl/internal/snd"
)

func getAction(ctx context.Context, c *cli.Command) error {
	name := c.StringArg("name")
	if name == "" {
		emitStatus(os.Stderr, usageError("name argument required: seictl nd get <name>"))
		return cli.Exit("", 1)
	}

	printer, err := makePrinter(c.String("output"))
	if err != nil {
		emitStatus(os.Stderr, usageError("%s", err.Error()))
		return cli.Exit("", 1)
	}

	kc := loadKubeconfig(c.String("kubeconfig"), c.String("namespace"))
	cfg, err := kc.RESTConfig()
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}
	ns, err := kc.Namespace()
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	kcli, err := newClient(cfg)
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	obj := snd.New()
	if err := kcli.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, obj); err != nil {
		emitStatus(os.Stderr, fmt.Errorf("get SeiNodeDeployment %s/%s: %w", ns, name, err))
		return cli.Exit("", 1)
	}
	if err := printer.PrintObj(obj, os.Stdout); err != nil {
		return fmt.Errorf("print: %w", err)
	}
	return nil
}

var getCmd = cli.Command{
	Name:      "get",
	Usage:     "Read a SeiNodeDeployment by name",
	ArgsUsage: "<name>",
	Arguments: []cli.Argument{
		&cli.StringArg{Name: "name", UsageText: "metadata.name of the SeiNodeDeployment"},
	},
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Target namespace (defaults to kubeconfig context or in-cluster SA)",
		},
		&cli.StringFlag{
			Name:    "output",
			Aliases: []string{"o"},
			Usage:   "Output format: yaml (default) | json | name | jsonpath=<template>",
		},
		&cli.StringFlag{
			Name:    "kubeconfig",
			Sources: cli.EnvVars("KUBECONFIG"),
			Usage:   "Path to kubeconfig (honors KUBECONFIG colon-merge); defaults to $HOME/.kube/config or in-cluster",
		},
	},
	Action: getAction,
}
