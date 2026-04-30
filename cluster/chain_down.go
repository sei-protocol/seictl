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

type chainDownResult struct {
	Name      string               `json:"name"`
	ChainID   string               `json:"chainId"`
	Namespace string               `json:"namespace"`
	Resources []render.ManifestRef `json:"resources"`
	DryRun    bool                 `json:"dryRun"`
	DeletedAt *time.Time           `json:"deletedAt,omitempty"`
	Hint      string               `json:"hint,omitempty"`
}

type chainDownInput struct {
	Name       string
	Namespace  string
	Kubeconfig string
	Context    string
	DryRun     bool
}

type chainDownDeps struct {
	identityPath  func() (string, error)
	newKubeClient func(kube.Options) (*kube.Client, *clioutput.Error)
	deleteFn      func(ctx context.Context, kc *kube.Client, opts kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error)
	dryRunListFn  func(ctx context.Context, kc *kube.Client, opts kube.ListOptions) ([]kube.DeleteResult, *clioutput.Error)
}

var defaultChainDownDeps = chainDownDeps{
	identityPath:  identity.DefaultPath,
	newKubeClient: kube.New,
	deleteFn:      deleteFromCluster,
	dryRunListFn:  dryRunListFromCluster,
}

var chainDownCmd = &cli.Command{
	Name:  "down",
	Usage: "Tear down a chain by name (cascades to any rpc/load attached to the same chain-id)",
	Flags: append(kubeconfigFlags(),
		&cli.StringFlag{Name: "name", Required: true, Usage: "Chain name to tear down"},
		&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Usage: "Namespace (defaults to eng-<alias>)"},
		&cli.BoolFlag{Name: "dry-run", Usage: "List the resources that would be deleted without deleting them"},
	),
	Action: func(ctx context.Context, command *cli.Command) error {
		return runChainDown(ctx, chainDownInput{
			Name:       command.String("name"),
			Namespace:  command.String("namespace"),
			Kubeconfig: command.String("kubeconfig"),
			Context:    command.String("context"),
			DryRun:     command.Bool("dry-run"),
		}, os.Stdout, defaultChainDownDeps)
	},
}

func runChainDown(ctx context.Context, in chainDownInput, out io.Writer, deps chainDownDeps) error {
	eng, idErr := loadEngineer(deps.identityPath)
	if idErr != nil {
		return failChainDown(out, idErr)
	}
	if e := validate.Name(eng.Alias, in.Name); e != nil {
		return failChainDown(out, e.ExitWith(clioutput.ExitBench))
	}

	namespace := in.Namespace
	if namespace == "" {
		namespace = "eng-" + eng.Alias
	}
	if e := validate.Namespace(namespace, eng.Alias); e != nil {
		return failChainDown(out, e.ExitWith(clioutput.ExitBench))
	}

	chainID := fmt.Sprintf("bench-%s-%s", eng.Alias, in.Name)
	selector := fmt.Sprintf("sei.io/engineer=%s,sei.io/chain-id=%s", eng.Alias, chainID)

	kc, kerr := deps.newKubeClient(kube.Options{Kubeconfig: in.Kubeconfig, Context: in.Context})
	if kerr != nil {
		return failChainDown(out, kerr)
	}

	var (
		results []kube.DeleteResult
		dErr    *clioutput.Error
	)
	if in.DryRun {
		results, dErr = deps.dryRunListFn(ctx, kc, kube.ListOptions{
			Namespace:     namespace,
			Resources:     benchDownTargets,
			LabelSelector: selector,
		})
	} else {
		results, dErr = deps.deleteFn(ctx, kc, kube.DeleteOptions{
			Namespace:     namespace,
			Resources:     benchDownTargets,
			LabelSelector: selector,
		})
	}
	if dErr != nil {
		return failChainDown(out, dErr)
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

	res := chainDownResult{
		Name:      in.Name,
		ChainID:   chainID,
		Namespace: namespace,
		Resources: resources,
		DryRun:    in.DryRun,
	}
	switch {
	case in.DryRun:
		if len(results) == 0 {
			res.Hint = fmt.Sprintf("no resources match selector %q in namespace %q — chain may not exist or already be torn down", selector, namespace)
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
	if err := clioutput.Emit(out, clioutput.KindChainDownResult, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.Exit("", 1)
	}
	return nil
}

func failChainDown(out io.Writer, e *clioutput.Error) error {
	_ = clioutput.EmitError(out, clioutput.KindChainDownResult, e)
	return cli.Exit("", e.Code)
}
