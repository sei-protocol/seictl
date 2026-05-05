package nodedeployment

import (
	"context"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/sei-protocol/seictl/internal/snd"
)

func listAction(ctx context.Context, c *cli.Command) error {
	allNS := c.Bool("all-namespaces")

	printer, err := makePrinter(c.String("output"))
	if err != nil {
		emitStatus(os.Stderr, usageError("%s", err.Error()))
		return cli.Exit("", 1)
	}

	var sel labels.Selector
	if raw := c.String("selector"); raw != "" {
		sel, err = labels.Parse(raw)
		if err != nil {
			emitStatus(os.Stderr, usageError("parse --selector: %s", err.Error()))
			return cli.Exit("", 1)
		}
	}

	kc := loadKubeconfig(c.String("kubeconfig"), c.String("namespace"))
	cfg, err := kc.RESTConfig()
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	listOpts := []client.ListOption{}
	if !allNS {
		ns, err := kc.Namespace()
		if err != nil {
			emitStatus(os.Stderr, err)
			return cli.Exit("", 1)
		}
		listOpts = append(listOpts, client.InNamespace(ns))
	}
	if sel != nil {
		listOpts = append(listOpts, client.MatchingLabelsSelector{Selector: sel})
	}

	kcli, err := newClient(cfg)
	if err != nil {
		emitStatus(os.Stderr, err)
		return cli.Exit("", 1)
	}

	list := snd.NewList()
	if err := kcli.List(ctx, list, listOpts...); err != nil {
		emitStatus(os.Stderr, fmt.Errorf("list SeiNodeDeployments: %w", err))
		return cli.Exit("", 1)
	}
	if err := printer.PrintObj(list, os.Stdout); err != nil {
		return fmt.Errorf("print: %w", err)
	}
	return nil
}

var listCmd = cli.Command{
	Name:  "list",
	Usage: "List SeiNodeDeployments",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Target namespace (defaults to kubeconfig context or in-cluster SA)",
		},
		&cli.BoolFlag{
			Name:    "all-namespaces",
			Aliases: []string{"A"},
			Usage:   "List across all namespaces; overrides --namespace",
		},
		&cli.StringFlag{
			Name:    "selector",
			Aliases: []string{"l"},
			Usage:   "Label selector (e.g. -l env=nightly,role=validator)",
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
	Action: listAction,
}
