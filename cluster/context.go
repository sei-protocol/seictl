package cluster

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/cluster/internal/aws"
	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/identity"
	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

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

// contextDeps lets tests stub the AWS and identity reads.
type contextDeps struct {
	getCaller    func(context.Context) (*aws.Caller, *clioutput.Error)
	identityPath func() (string, error)
}

var defaultContextDeps = contextDeps{
	getCaller:    aws.GetCaller,
	identityPath: identity.DefaultPath,
}

var ContextCmd = cli.Command{
	Name:  "context",
	Usage: "Print cluster + identity ground truth as a JSON envelope",
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

	// AWS is best-effort: SSO sessions expire and engineers run `context`
	// to diagnose that.
	if caller, awsErr := deps.getCaller(ctx); awsErr == nil {
		res.AWSAccount = caller.Account
		res.AWSRegion = caller.Region
		res.AWSPrincipalARN = caller.PrincipalARN
	}

	if path, err := deps.identityPath(); err == nil {
		eng, idErr := identity.Read(path)
		if idErr != nil && idErr.Category != clioutput.CatMissing {
			_ = clioutput.EmitError(out, clioutput.KindContextResult, idErr)
			return cli.Exit("", idErr.Code)
		}
		if eng != nil {
			res.Engineer = eng
		}
	}

	if err := clioutput.Emit(out, clioutput.KindContextResult, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.Exit("", 1)
	}
	return nil
}
