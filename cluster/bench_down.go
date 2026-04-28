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
	DeletedAt *time.Time           `json:"deletedAt,omitempty"`
}

type benchDownInput struct {
	Name       string
	Namespace  string
	Kubeconfig string
	Context    string
}

type benchDownDeps struct {
	identityPath  func() (string, error)
	newKubeClient func(kube.Options) (*kube.Client, *clioutput.Error)
	deleteFn      func(ctx context.Context, kc *kube.Client, opts kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error)
}

var defaultBenchDownDeps = benchDownDeps{
	identityPath:  identity.DefaultPath,
	newKubeClient: kube.New,
	deleteFn:      deleteFromCluster,
}

func deleteFromCluster(ctx context.Context, kc *kube.Client, opts kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error) {
	results, err := kc.Delete(ctx, opts)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitBench, clioutput.CatFinalizerStuck, "%v", err)
	}
	return results, nil
}

var benchDownCmd = &cli.Command{
	Name:  "down",
	Usage: "Tear down a benchmark by name",
	Flags: append(kubeconfigFlags(),
		&cli.StringFlag{Name: "name", Required: true, Usage: "Bench name to tear down"},
		&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Usage: "Namespace (defaults to eng-<alias>)"},
	),
	Action: func(ctx context.Context, command *cli.Command) error {
		return runBenchDown(ctx, benchDownInput{
			Name:       command.String("name"),
			Namespace:  command.String("namespace"),
			Kubeconfig: command.String("kubeconfig"),
			Context:    command.String("context"),
		}, os.Stdout, defaultBenchDownDeps)
	},
}

func runBenchDown(ctx context.Context, in benchDownInput, out io.Writer, deps benchDownDeps) error {
	eng, idErr := loadEngineer(deps.identityPath)
	if idErr != nil {
		return failBenchDown(out, idErr)
	}
	if e := validate.Name(eng.Alias, in.Name); e != nil {
		return failBenchDown(out, e)
	}

	namespace := in.Namespace
	if namespace == "" {
		namespace = "eng-" + eng.Alias
	}
	if e := validate.Namespace(namespace, eng.Alias); e != nil {
		return failBenchDown(out, e)
	}

	chainID := fmt.Sprintf("bench-%s-%s", eng.Alias, in.Name)
	selector := fmt.Sprintf("sei.io/engineer=%s,sei.io/bench-name=%s", eng.Alias, in.Name)

	kc, kerr := deps.newKubeClient(kube.Options{Kubeconfig: in.Kubeconfig, Context: in.Context})
	if kerr != nil {
		return failBenchDown(out, kerr)
	}

	results, dErr := deps.deleteFn(ctx, kc, kube.DeleteOptions{
		Namespace:     namespace,
		Resources:     benchDownTargets,
		LabelSelector: selector,
	})
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

	now := time.Now().UTC()
	res := benchDownResult{
		Name:      in.Name,
		ChainID:   chainID,
		Namespace: namespace,
		Resources: resources,
		DeletedAt: &now,
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
