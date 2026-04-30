package cluster

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/urfave/cli/v3"

	seiconfig "github.com/sei-protocol/sei-config"

	"github.com/sei-protocol/seictl/cluster/internal/aws"
	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/identity"
	"github.com/sei-protocol/seictl/cluster/internal/kube"
	"github.com/sei-protocol/seictl/cluster/internal/render"
	"github.com/sei-protocol/seictl/cluster/internal/validate"
)


type rpcUpResult struct {
	ChainID     string               `json:"chainId"`
	RPCName     string               `json:"rpcName"`
	Namespace   string               `json:"namespace"`
	ImageRef    string               `json:"imageRef"`
	ImageDigest string               `json:"imageDigest"`
	Replicas    int                  `json:"replicas"`
	Endpoints   Endpoints            `json:"endpoints"`
	DryRun      bool                 `json:"dryRun"`
	Manifests   []render.ManifestRef `json:"manifests"`
	AppliedAt   *time.Time           `json:"appliedAt,omitempty"`
}

type rpcUpInput struct {
	ChainID    string
	Name       string
	Image      string
	Replicas   int
	Apply      bool
	Kubeconfig string
	Context    string
}

type rpcDeps struct {
	resolveDigest func(context.Context, string) (string, *clioutput.Error)
	identityPath  func() (string, error)
	newKubeClient func(kube.Options) (*kube.Client, *clioutput.Error)
	apply         func(ctx context.Context, kc *kube.Client, fieldOwner, namespace string, docs [][]byte) ([]kube.ApplyResult, *clioutput.Error)
	getCaller     func(context.Context) (*aws.Caller, *clioutput.Error)
}

var defaultRPCDeps = rpcDeps{
	resolveDigest: aws.ResolveDigest,
	identityPath:  identity.DefaultPath,
	newKubeClient: kube.New,
	apply:         applyToCluster,
	getCaller:     aws.GetCaller,
}

var RPCCmd = cli.Command{
	Name:  "rpc",
	Usage: "Manage RPC fleets peering with an existing chain",
	Commands: []*cli.Command{
		rpcDownCmd,
		{
			Name:  "up",
			Usage: "Render or apply an RPC fleet against a named chain",
			Flags: append(kubeconfigFlags(),
				&cli.StringFlag{Name: "chain", Required: true, Usage: "Chain ID"},
				&cli.StringFlag{Name: "name", Required: true, Usage: "RPC fleet name"},
				&cli.StringFlag{Name: "image", Required: true, Usage: "ECR image ref"},
				&cli.IntFlag{Name: "replicas", Required: true, Usage: "RPC replica count (1-21)"},
				&cli.BoolFlag{Name: "apply", Usage: "Server-side apply the rendered manifest"},
			),
			Action: func(ctx context.Context, command *cli.Command) error {
				return runRPCUpCmd(ctx, rpcUpInput{
					ChainID:    command.String("chain"),
					Name:       command.String("name"),
					Image:      command.String("image"),
					Replicas:   int(command.Int("replicas")),
					Apply:      command.Bool("apply"),
					Kubeconfig: command.String("kubeconfig"),
					Context:    command.String("context"),
				}, os.Stdout, defaultRPCDeps)
			},
		},
	},
}

func runRPCUpCmd(ctx context.Context, in rpcUpInput, out io.Writer, deps rpcDeps) error {
	res, err := runRPCUp(ctx, in, deps)
	if err != nil {
		return failRPCUp(out, err)
	}
	if emitErr := clioutput.Emit(out, clioutput.KindRPCUpResult, res); emitErr != nil {
		fmt.Fprintln(os.Stderr, emitErr)
		return cli.Exit("", 1)
	}
	return nil
}

func runRPCUp(ctx context.Context, in rpcUpInput, deps rpcDeps) (rpcUpResult, *clioutput.Error) {
	eng, idErr := loadEngineer(deps.identityPath)
	if idErr != nil {
		return rpcUpResult{}, idErr
	}
	if e := validate.ChainID(in.ChainID); e != nil {
		return rpcUpResult{}, e.ExitWith(clioutput.ExitBench)
	}
	if e := validate.Image(in.Image); e != nil {
		return rpcUpResult{}, e.ExitWith(clioutput.ExitBench)
	}
	if e := validate.Name(eng.Alias, in.Name); e != nil {
		return rpcUpResult{}, e.ExitWith(clioutput.ExitBench)
	}
	if e := validate.RPCReplicas(in.Replicas); e != nil {
		return rpcUpResult{}, e.ExitWith(clioutput.ExitBench)
	}
	namespace := "eng-" + eng.Alias
	if e := validate.Namespace(namespace, eng.Alias); e != nil {
		return rpcUpResult{}, e.ExitWith(clioutput.ExitBench)
	}
	if _, callerErr := deps.getCaller(ctx); callerErr != nil {
		return rpcUpResult{}, callerErr
	}

	digest, dErr := deps.resolveDigest(ctx, in.Image)
	if dErr != nil {
		return rpcUpResult{}, dErr
	}
	seidImage := digestPinned(in.Image, digest)
	if err := aws.AssertECRDigestRef(seidImage); err != nil {
		return rpcUpResult{}, clioutput.Newf(clioutput.ExitBench, clioutput.CatImageResolution, "%v", err)
	}

	digestShort := shortDigest(digest)
	docs, manifests, rerr := renderRPCManifests(eng.Alias, in.ChainID, in.Name, namespace, seidImage, digestShort, in.Replicas)
	if rerr != nil {
		return rpcUpResult{}, rerr
	}

	res := rpcUpResult{
		ChainID:     in.ChainID,
		RPCName:     in.Name,
		Namespace:   namespace,
		ImageRef:    in.Image,
		ImageDigest: digest,
		Replicas:    in.Replicas,
		Endpoints:   deriveRPCEndpoints(in.ChainID, in.Name, namespace),
		DryRun:      !in.Apply,
		Manifests:   manifests,
	}

	if in.Apply {
		kc, kerr := deps.newKubeClient(kube.Options{Kubeconfig: in.Kubeconfig, Context: in.Context})
		if kerr != nil {
			return rpcUpResult{}, kerr
		}
		applyResults, applyErr := deps.apply(ctx, kc, benchFieldOwner, namespace, docs)
		if applyErr != nil {
			return rpcUpResult{}, applyErr
		}
		res.Manifests = mergeApplyResults(manifests, applyResults)
		now := time.Now().UTC()
		res.AppliedAt = &now
	}
	return res, nil
}

func renderRPCManifests(alias, chainID, name, namespace, seidImage, digestShort string, replicas int) ([][]byte, []render.ManifestRef, *clioutput.Error) {
	vars := map[string]string{
		"CHAIN_ID":           chainID,
		"RPC_NAME":           name,
		"NAMESPACE":          namespace,
		"ENGINEER_ALIAS":     alias,
		"SEID_IMAGE":         seidImage,
		"IMAGE_DIGEST_SHORT": digestShort,
		"RPC_COUNT":          strconv.Itoa(replicas),
		"PART_OF":            "seictl",
	}
	rpcYAML, e := renderEmbedded("rpc.yaml", vars)
	if e != nil {
		return nil, nil, e
	}
	docs := render.SplitYAML(rpcYAML)
	refs := make([]render.ManifestRef, 0, len(docs))
	for _, doc := range docs {
		ref, err := render.ExtractRef(doc)
		if err != nil {
			return nil, nil, clioutput.Newf(clioutput.ExitBench, clioutput.CatTemplateRender, "extract manifest ref: %v", err)
		}
		ref.Action = "create"
		refs = append(refs, ref)
	}
	return docs, refs, nil
}

func deriveRPCEndpoints(chainID, name, namespace string) Endpoints {
	host := fmt.Sprintf("%s-rpc-%s-internal.%s.svc.cluster.local", chainID, name, namespace)
	return Endpoints{
		TendermintRpc: []string{fmt.Sprintf("http://%s:%d", host, seiconfig.PortRPC)},
		EvmJsonRpc:    []string{fmt.Sprintf("http://%s:%d", host, seiconfig.PortEVMHTTP)},
	}
}

func failRPCUp(out io.Writer, e *clioutput.Error) error {
	_ = clioutput.EmitError(out, clioutput.KindRPCUpResult, e)
	return cli.Exit("", e.Code)
}
