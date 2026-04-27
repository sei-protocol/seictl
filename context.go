package main

import (
	"context"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/internal/aws"
	"github.com/sei-protocol/seictl/internal/clioutput"
	"github.com/sei-protocol/seictl/internal/identity"
	"github.com/sei-protocol/seictl/internal/kube"
)

const contextResultKind = "context.result"

type contextResult struct {
	KubeContext     string             `json:"kubeContext"`
	Cluster         string             `json:"cluster"`
	Server          string             `json:"server"`
	Namespace       string             `json:"namespace"`
	AWSAccount      string             `json:"awsAccount"`
	AWSRegion       string             `json:"awsRegion"`
	AWSPrincipalARN string             `json:"awsPrincipalArn"`
	Engineer        *identity.Engineer `json:"engineer,omitempty"`
}

var contextCmd = cli.Command{
	Name:  "context",
	Usage: "Print cluster + identity ground truth as a JSON envelope",
	Flags: clusterFlags(),
	Action: func(ctx context.Context, command *cli.Command) error {
		return runContext(ctx, command, os.Stdout)
	},
}

func runContext(ctx context.Context, command *cli.Command, out io.Writer) error {
	kc, kerr := kube.New(kube.Options{
		Kubeconfig: command.String("kubeconfig"),
		Context:    command.String("context"),
		Namespace:  command.String("namespace"),
	})
	if kerr != nil {
		_ = clioutput.EmitError(out, contextResultKind, kerr)
		return cli.Exit("", kerr.Code)
	}

	res := contextResult{
		KubeContext: kc.ContextName,
		Cluster:     kc.ClusterName,
		Server:      kc.ClusterServer,
		Namespace:   kc.Namespace,
	}

	// AWS is best-effort: SSO sessions expire and engineers run `context`
	// to diagnose that. Render empty AWS fields rather than failing.
	if caller, awsErr := aws.GetCaller(ctx); awsErr == nil {
		res.AWSAccount = caller.Account
		res.AWSRegion = caller.Region
		res.AWSPrincipalARN = caller.PrincipalARN
	}

	if eng, idErr := readEngineerOptional(); idErr != nil {
		_ = clioutput.EmitError(out, contextResultKind, idErr)
		return cli.Exit("", idErr.Code)
	} else if eng != nil {
		res.Engineer = eng
	}

	if err := clioutput.Emit(out, contextResultKind, res); err != nil {
		return cli.Exit(err.Error(), clioutput.ExitIdentity)
	}
	return nil
}

// readEngineerOptional returns the engineer record if present and well-formed,
// nil if the file is simply absent (a valid pre-onboard state), or a typed
// error for malformed / loose-perms cases that the engineer must fix.
func readEngineerOptional() (*identity.Engineer, *clioutput.Error) {
	path, err := identity.DefaultPath()
	if err != nil {
		return nil, nil
	}
	eng, idErr := identity.Read(path)
	if idErr == nil {
		return eng, nil
	}
	if idErr.Category == clioutput.CatMissing {
		return nil, nil
	}
	return nil, idErr
}

func clusterFlags() []cli.Flag {
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
		&cli.StringFlag{
			Name:    "namespace",
			Aliases: []string{"n"},
			Usage:   "Namespace to operate in",
		},
	}
}
