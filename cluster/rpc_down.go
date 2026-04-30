package cluster

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/identity"
	"github.com/sei-protocol/seictl/cluster/internal/kube"
	"github.com/sei-protocol/seictl/cluster/internal/render"
	"github.com/sei-protocol/seictl/cluster/internal/validate"
)

var rpcDownTargets = []string{"seinodedeployments.sei.io"}

type rpcDownResult struct {
	ChainID   string               `json:"chainId"`
	RPCName   string               `json:"rpcName"`
	Namespace string               `json:"namespace"`
	Resources []render.ManifestRef `json:"resources"`
	DryRun    bool                 `json:"dryRun"`
	DeletedAt *time.Time           `json:"deletedAt,omitempty"`
	Hint      string               `json:"hint,omitempty"`
}

type rpcDownInput struct {
	ChainID    string
	Name       string
	Namespace  string
	Kubeconfig string
	Context    string
	DryRun     bool
}

type rpcDownDeps struct {
	identityPath  func() (string, error)
	newKubeClient func(kube.Options) (*kube.Client, *clioutput.Error)
	deleteFn      func(ctx context.Context, kc *kube.Client, opts kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error)
	dryRunListFn  func(ctx context.Context, kc *kube.Client, opts kube.ListOptions) ([]kube.DeleteResult, *clioutput.Error)
}

var defaultRPCDownDeps = rpcDownDeps{
	identityPath:  identity.DefaultPath,
	newKubeClient: kube.New,
	deleteFn:      deleteFromCluster,
	dryRunListFn:  dryRunListFromCluster,
}

var rpcDownCmd = &cli.Command{
	Name:  "down",
	Usage: "Tear down an RPC fleet by chain + name",
	Flags: append(kubeconfigFlags(),
		&cli.StringFlag{Name: "chain", Required: true, Usage: "Chain ID"},
		&cli.StringFlag{Name: "name", Required: true, Usage: "RPC fleet name"},
		&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Usage: "Namespace override"},
		&cli.BoolFlag{Name: "dry-run", Usage: "List resources that would be deleted without deleting them"},
	),
	Action: func(ctx context.Context, command *cli.Command) error {
		return runRPCDown(ctx, rpcDownInput{
			ChainID:    command.String("chain"),
			Name:       command.String("name"),
			Namespace:  command.String("namespace"),
			Kubeconfig: command.String("kubeconfig"),
			Context:    command.String("context"),
			DryRun:     command.Bool("dry-run"),
		}, os.Stdout, defaultRPCDownDeps)
	},
}

func runRPCDown(ctx context.Context, in rpcDownInput, out io.Writer, deps rpcDownDeps) error {
	eng, idErr := loadEngineer(deps.identityPath)
	if idErr != nil {
		return failRPCDown(out, idErr)
	}
	if e := validate.ChainID(in.ChainID); e != nil {
		return failRPCDown(out, e.ExitWith(clioutput.ExitBench))
	}
	if e := validate.Name(eng.Alias, in.Name); e != nil {
		return failRPCDown(out, e.ExitWith(clioutput.ExitBench))
	}

	namespace := in.Namespace
	if namespace == "" {
		namespace = "eng-" + eng.Alias
	}
	if e := validate.Namespace(namespace, eng.Alias); e != nil {
		return failRPCDown(out, e.ExitWith(clioutput.ExitBench))
	}

	selector := fmt.Sprintf("sei.io/engineer=%s,sei.io/chain-id=%s,sei.io/rpc-name=%s,app.kubernetes.io/component=rpc",
		eng.Alias, in.ChainID, in.Name)

	kc, kerr := deps.newKubeClient(kube.Options{Kubeconfig: in.Kubeconfig, Context: in.Context})
	if kerr != nil {
		return failRPCDown(out, kerr)
	}

	var (
		results []kube.DeleteResult
		dErr    *clioutput.Error
	)
	if in.DryRun {
		results, dErr = deps.dryRunListFn(ctx, kc, kube.ListOptions{
			Namespace:     namespace,
			Resources:     rpcDownTargets,
			LabelSelector: selector,
		})
	} else {
		results, dErr = deps.deleteFn(ctx, kc, kube.DeleteOptions{
			Namespace:     namespace,
			Resources:     rpcDownTargets,
			LabelSelector: selector,
		})
	}
	if dErr != nil {
		return failRPCDown(out, dErr)
	}

	resources := make([]render.ManifestRef, len(results))
	for i, r := range results {
		resources[i] = render.ManifestRef{
			Kind:      r.Kind,
			Name:      r.Name,
			Namespace: r.Namespace,
			Action:    r.Action,
		}
	}

	res := rpcDownResult{
		ChainID:   in.ChainID,
		RPCName:   in.Name,
		Namespace: namespace,
		Resources: resources,
		DryRun:    in.DryRun,
	}
	switch {
	case in.DryRun:
		if len(results) == 0 {
			res.Hint = fmt.Sprintf("no resources match selector %q in namespace %q — rpc fleet may not exist or already be torn down", selector, namespace)
		}
	default:
		terminating := 0
		for _, r := range results {
			if r.Action == "still-terminating" {
				terminating++
			}
		}
		if terminating == 0 {
			now := time.Now().UTC()
			res.DeletedAt = &now
		} else {
			res.Hint = fmt.Sprintf("%d resource(s) still terminating; wait before re-running with the same name", terminating)
		}
	}
	if err := clioutput.Emit(out, clioutput.KindRPCDownResult, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.Exit("", 1)
	}
	return nil
}

func failRPCDown(out io.Writer, e *clioutput.Error) error {
	_ = clioutput.EmitError(out, clioutput.KindRPCDownResult, e)
	return cli.Exit("", e.Code)
}
