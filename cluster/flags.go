package cluster

import "github.com/urfave/cli/v3"

// kubeconfigFlags is the shared set of cluster-target flags used by
// every cluster-facing verb that touches kubeconfig.
func kubeconfigFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{
			Name:    "kubeconfig",
			Sources: cli.EnvVars("KUBECONFIG"),
			Usage:   "Path to kubeconfig file",
		},
		&cli.StringFlag{
			Name:  "context",
			Usage: "Kubeconfig context to use",
		},
	}
}
