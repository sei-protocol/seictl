package cluster

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/urfave/cli/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/identity"
	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

// benchOwnerSelector scopes lists to objects this engineer owns.
// `app.kubernetes.io/part-of=seictl-bench` survives the rename if the
// engineer alias changes; `sei.io/engineer=<alias>` is the per-engineer
// IAM-aligned scope.
const benchPartOf = "seictl-bench"

type benchListResult struct {
	Items []benchSummary `json:"items"`
}

type benchSummary struct {
	ChainID           string `json:"chainId"`
	Name              string `json:"name"`
	Namespace         string `json:"namespace"`
	Owner             string `json:"owner"`
	Phase             string `json:"phase"`
	ValidatorsReady   int    `json:"validatorsReady"`
	ValidatorsDesired int    `json:"validatorsDesired"`
	RPCReady          int    `json:"rpcReady"`
	RPCDesired        int    `json:"rpcDesired"`
	LoadJobPhase      string `json:"loadJobPhase"`
	AgeSeconds        int64  `json:"ageSeconds"`
	ImageDigest       string `json:"imageDigest"`
}

type benchListInput struct {
	AllNamespaces bool
	Namespace     string
	Kubeconfig    string
	Context       string
}

type benchListDeps struct {
	identityPath  func() (string, error)
	newKubeClient func(kube.Options) (*kube.Client, *clioutput.Error)
	listFn        func(ctx context.Context, kc *kube.Client, opts kube.ListOptions) ([]unstructured.Unstructured, *clioutput.Error)
}

var defaultBenchListDeps = benchListDeps{
	identityPath:  identity.DefaultPath,
	newKubeClient: kube.New,
	listFn:        listFromCluster,
}

func listFromCluster(ctx context.Context, kc *kube.Client, opts kube.ListOptions) ([]unstructured.Unstructured, *clioutput.Error) {
	out, err := kc.List(ctx, opts)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitBench, clioutput.CatApplyFailed, "%v", err)
	}
	return out, nil
}

var benchListCmd = &cli.Command{
	Name:  "list",
	Usage: "Owner-scoped list of running benchmarks",
	Flags: append(kubeconfigFlags(),
		&cli.BoolFlag{Name: "all-namespaces", Aliases: []string{"A"}, Usage: "List across all namespaces"},
		&cli.StringFlag{Name: "namespace", Aliases: []string{"n"}, Usage: "Namespace override"},
	),
	Action: func(ctx context.Context, command *cli.Command) error {
		return runBenchList(ctx, benchListInput{
			AllNamespaces: command.Bool("all-namespaces"),
			Namespace:     command.String("namespace"),
			Kubeconfig:    command.String("kubeconfig"),
			Context:       command.String("context"),
		}, os.Stdout, defaultBenchListDeps)
	},
}

func runBenchList(ctx context.Context, in benchListInput, out io.Writer, deps benchListDeps) error {
	eng, idErr := loadEngineer(deps.identityPath)
	if idErr != nil {
		return failBenchList(out, idErr)
	}

	namespace := in.Namespace
	if namespace == "" {
		namespace = "eng-" + eng.Alias
	}
	selector := fmt.Sprintf("app.kubernetes.io/part-of=%s,sei.io/engineer=%s", benchPartOf, eng.Alias)

	kc, kerr := deps.newKubeClient(kube.Options{Kubeconfig: in.Kubeconfig, Context: in.Context})
	if kerr != nil {
		return failBenchList(out, kerr)
	}

	snds, lErr := deps.listFn(ctx, kc, kube.ListOptions{
		Namespace:     namespace,
		AllNamespaces: in.AllNamespaces,
		Resources:     []string{"seinodedeployments.sei.io"},
		LabelSelector: selector,
	})
	if lErr != nil {
		return failBenchList(out, lErr)
	}
	jobs, lErr := deps.listFn(ctx, kc, kube.ListOptions{
		Namespace:     namespace,
		AllNamespaces: in.AllNamespaces,
		Resources:     []string{"jobs.batch"},
		LabelSelector: selector,
	})
	if lErr != nil {
		return failBenchList(out, lErr)
	}

	res := benchListResult{Items: aggregateBenchSummaries(snds, jobs)}
	if err := clioutput.Emit(out, clioutput.KindBenchListResult, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.Exit("", 1)
	}
	return nil
}

// aggregateBenchSummaries groups SNDs + Jobs by chain-id (the
// `sei.io/chain-id` label) and produces one benchSummary per group.
// Validator vs. RPC SND is distinguished by the `sei.io/role` label.
func aggregateBenchSummaries(snds, jobs []unstructured.Unstructured) []benchSummary {
	type group struct {
		validator *unstructured.Unstructured
		rpc       *unstructured.Unstructured
		job       *unstructured.Unstructured
	}
	groups := map[string]*group{}

	for i := range snds {
		snd := &snds[i]
		chainID := snd.GetLabels()["sei.io/chain-id"]
		if chainID == "" {
			continue
		}
		g, ok := groups[chainID]
		if !ok {
			g = &group{}
			groups[chainID] = g
		}
		switch snd.GetLabels()["sei.io/role"] {
		case "validator":
			g.validator = snd
		case "rpc":
			g.rpc = snd
		}
	}
	for i := range jobs {
		j := &jobs[i]
		chainID := j.GetLabels()["sei.io/chain-id"]
		if chainID == "" {
			continue
		}
		g, ok := groups[chainID]
		if !ok {
			g = &group{}
			groups[chainID] = g
		}
		g.job = j
	}

	out := make([]benchSummary, 0, len(groups))
	for chainID, g := range groups {
		out = append(out, summarize(chainID, g.validator, g.rpc, g.job))
	}
	return out
}

func summarize(chainID string, validator, rpc, job *unstructured.Unstructured) benchSummary {
	summary := benchSummary{ChainID: chainID}

	primary := validator
	if primary == nil {
		primary = rpc
	}
	if primary == nil && job != nil {
		primary = job
	}
	if primary != nil {
		labels := primary.GetLabels()
		summary.Name = labels["sei.io/bench-name"]
		summary.Namespace = primary.GetNamespace()
		summary.Owner = labels["sei.io/engineer"]
		summary.ImageDigest = primary.GetAnnotations()["sei.io/image-sha"]
		ts := primary.GetCreationTimestamp().Time
		if !ts.IsZero() {
			summary.AgeSeconds = int64(time.Since(ts).Seconds())
		}
	}
	if validator != nil {
		summary.Phase, _, _ = unstructured.NestedString(validator.Object, "status", "phase")
		summary.ValidatorsDesired = intField(validator, "spec", "replicas")
		summary.ValidatorsReady = intField(validator, "status", "readyReplicas")
	}
	if rpc != nil {
		summary.RPCDesired = intField(rpc, "spec", "replicas")
		summary.RPCReady = intField(rpc, "status", "readyReplicas")
	}
	if job != nil {
		summary.LoadJobPhase = jobPhaseFor(job)
	}
	return summary
}

func intField(u *unstructured.Unstructured, fields ...string) int {
	v, _, _ := unstructured.NestedInt64(u.Object, fields...)
	return int(v)
}

// jobPhaseFor folds Job .status fields into a single phase string the
// engineer can act on. Mirrors what kubectl prints in `Jobs:` columns.
func jobPhaseFor(job *unstructured.Unstructured) string {
	conditions, _, _ := unstructured.NestedSlice(job.Object, "status", "conditions")
	for _, c := range conditions {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		ctype, _ := cm["type"].(string)
		cstatus, _ := cm["status"].(string)
		if cstatus != "True" {
			continue
		}
		if strings.EqualFold(ctype, "Complete") {
			return "Succeeded"
		}
		if strings.EqualFold(ctype, "Failed") {
			return "Failed"
		}
	}
	active := intField(job, "status", "active")
	if active > 0 {
		return "Running"
	}
	return "Pending"
}

func failBenchList(out io.Writer, e *clioutput.Error) error {
	_ = clioutput.EmitError(out, clioutput.KindBenchListResult, e)
	return cli.Exit("", e.Code)
}
