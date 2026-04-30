package cluster

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/cluster/internal/aws"
	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/config"
	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

type contextResult struct {
	KubeContext     string         `json:"kubeContext"`
	Cluster         string         `json:"cluster"`
	Server          string         `json:"server"`
	Namespace       string         `json:"namespace"`
	AWSAccount      string         `json:"awsAccount"`
	AWSRegion       string         `json:"awsRegion"`
	AWSPrincipalARN string         `json:"awsPrincipalArn"`
	Config          *config.Config `json:"config,omitempty"`
}

// contextDeps lets tests stub the AWS and config reads.
type contextDeps struct {
	getCaller  func(context.Context) (*aws.Caller, *clioutput.Error)
	configPath func() (string, error)
}

var defaultContextDeps = contextDeps{
	getCaller:  aws.GetCaller,
	configPath: config.DefaultPath,
}

var ContextCmd = cli.Command{
	Name:  "context",
	Usage: "Print cluster + config ground truth as a JSON envelope",
	Flags: kubeconfigFlags(),
	Action: func(ctx context.Context, command *cli.Command) error {
		return runContext(ctx, command.String("kubeconfig"), command.String("context"), os.Stdout, defaultContextDeps)
	},
}

func runContext(ctx context.Context, kubeconfig, kubeContext string, out io.Writer, deps contextDeps) error {
	kc, kerr := kube.New(kube.Options{Kubeconfig: kubeconfig, Context: kubeContext})
	if kerr != nil {
		_ = clioutput.EmitError(out, clioutput.KindContextResult, kerr)
		return cli.Exit("", kerr.Code)
	}

	res := contextResult{
		KubeContext: kc.ContextName,
		Cluster:     kc.ClusterName,
		Server:      kc.ClusterServer,
		Namespace:   kc.Namespace,
	}

	// AWS is required: every side-effecting verb needs it, so failing
	// here saves the engineer from running through more verbs that all
	// fail in different shapes. The error message carries the specific
	// remediation hint via aws.CredsHint.
	caller, awsErr := deps.getCaller(ctx)
	if awsErr != nil {
		_ = clioutput.EmitError(out, clioutput.KindContextResult, awsErr)
		return cli.Exit("", awsErr.Code)
	}
	res.AWSAccount = caller.Account
	res.AWSRegion = caller.Region
	res.AWSPrincipalARN = caller.PrincipalARN

	if path, err := deps.configPath(); err == nil {
		cfg, idErr := config.Read(path)
		if idErr != nil && idErr.Category != clioutput.CatMissing {
			_ = clioutput.EmitError(out, clioutput.KindContextResult, idErr)
			return cli.Exit("", idErr.Code)
		}
		if cfg != nil {
			res.Config = cfg
		}
	}

	if err := clioutput.Emit(out, clioutput.KindContextResult, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.Exit("", 1)
	}
	return nil
}
