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

// benchDownTargets enumerates the resource types Apply creates.
// Order matters for cascade reasoning; foreground propagation handles
// child-cleanup within each kind.
var benchDownTargets = []string{
	"seinodedeployments.sei.io",
	"jobs.batch",
	"configmaps",
}

type benchDownResult struct {
	Name      string               `json:"name"`
	ChainID   string               `json:"chainId"`
	Namespace string               `json:"namespace"`
	Resources []render.ManifestRef `json:"resources"`
	DryRun    bool                 `json:"dryRun"`
	DeletedAt *time.Time           `json:"deletedAt,omitempty"`
	Hint      string               `json:"hint,omitempty"`
}

type benchDownInput struct {
	Name       string
	Namespace  string
	Kubeconfig string
	Context    string
	DryRun     bool
}

type benchDownDeps struct {
	identityPath  func() (string, error)
	newKubeClient func(kube.Options) (*kube.Client, *clioutput.Error)
	deleteFn      func(ctx context.Context, kc *kube.Client, opts kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error)
	dryRunListFn  func(ctx context.Context, kc *kube.Client, opts kube.ListOptions) ([]kube.DeleteResult, *clioutput.Error)
}

var defaultBenchDownDeps = benchDownDeps{
	identityPath:  identity.DefaultPath,
	newKubeClient: kube.New,
	deleteFn:      deleteFromCluster,
	dryRunListFn:  dryRunListFromCluster,
}

func deleteFromCluster(ctx context.Context, kc *kube.Client, opts kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error) {
	results, err := kc.Delete(ctx, opts)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitBench, clioutput.CatFinalizerStuck, "%v", err)
	}
	return results, nil
}

func dryRunListFromCluster(ctx context.Context, kc *kube.Client, opts kube.ListOptions) ([]kube.DeleteResult, *clioutput.Error) {
	objs, err := kc.List(ctx, opts)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitBench, clioutput.CatApplyFailed, "%v", err)
	}
	out := make([]kube.DeleteResult, len(objs))
	for i, o := range objs {
		out[i] = kube.DeleteResult{
			Kind:      o.GetKind(),
			Name:      o.GetName(),
			Namespace: o.GetNamespace(),
			Action:    "would-delete",
		}
	}
	return out, nil
}

var benchDownCmd = &cli.Command{
	Name:  "down",
	Usage: "Tear down a benchmark by name",
	Flags: append(kubeconfigFlags(),
		&cli.StringFlag{Name: "name", Required: true, Usage: "Bench name to tear down"},
		&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Usage: "Namespace (defaults to eng-<alias>)"},
		&cli.BoolFlag{Name: "dry-run", Usage: "List the resources that would be deleted without deleting them"},
	),
	Action: func(ctx context.Context, command *cli.Command) error {
		return runBenchDown(ctx, benchDownInput{
			Name:       command.String("name"),
			Namespace:  command.String("namespace"),
			Kubeconfig: command.String("kubeconfig"),
			Context:    command.String("context"),
			DryRun:     command.Bool("dry-run"),
		}, os.Stdout, defaultBenchDownDeps)
	},
}

func runBenchDown(ctx context.Context, in benchDownInput, out io.Writer, deps benchDownDeps) error {
	eng, idErr := loadEngineer(deps.identityPath)
	if idErr != nil {
		return failBenchDown(out, idErr)
	}
	if e := validate.Name(eng.Alias, in.Name); e != nil {
		return failBenchDown(out, e.ExitWith(clioutput.ExitBench))
	}

	namespace := in.Namespace
	if namespace == "" {
		namespace = "eng-" + eng.Alias
	}
	if e := validate.Namespace(namespace, eng.Alias); e != nil {
		return failBenchDown(out, e.ExitWith(clioutput.ExitBench))
	}

	chainID := fmt.Sprintf("bench-%s-%s", eng.Alias, in.Name)
	selector := fmt.Sprintf("sei.io/engineer=%s,sei.io/bench-name=%s", eng.Alias, in.Name)

	kc, kerr := deps.newKubeClient(kube.Options{Kubeconfig: in.Kubeconfig, Context: in.Context})
	if kerr != nil {
		return failBenchDown(out, kerr)
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
		return failBenchDown(out, dErr)
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

	res := benchDownResult{
		Name:      in.Name,
		ChainID:   chainID,
		Namespace: namespace,
		Resources: resources,
		DryRun:    in.DryRun,
	}
	switch {
	case in.DryRun:
		if len(results) == 0 {
			res.Hint = fmt.Sprintf("no resources match selector %q in namespace %q — bench may not exist or already be torn down", selector, namespace)
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
			res.Hint = fmt.Sprintf("%d resource(s) still terminating; wait before re-running `bench up` with the same name", terminating)
		}
	}
	if err := clioutput.Emit(out, clioutput.KindBenchDownResult, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.Exit("", 1)
	}
	return nil
}

func failBenchDown(out io.Writer, e *clioutput.Error) error {
	_ = clioutput.EmitError(out, clioutput.KindBenchDownResult, e)
	return cli.Exit("", e.Code)
}
