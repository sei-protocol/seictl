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

// Field names mirror downstream env-var contracts (SEI_TENDERMINT_RPC,
// SEI_EVM_JSON_RPC) — rename = break two contracts.
type Endpoints struct {
	TendermintRpc []string `json:"tendermintRpc"`
	EvmJsonRpc    []string `json:"evmJsonRpc,omitempty"`
}

type chainUpResult struct {
	ChainID     string               `json:"chainId"`
	Name        string               `json:"name"`
	Namespace   string               `json:"namespace"`
	ImageRef    string               `json:"imageRef"`
	ImageDigest string               `json:"imageDigest"`
	Validators  int                  `json:"validators"`
	Endpoints   Endpoints            `json:"endpoints"`
	DryRun      bool                 `json:"dryRun"`
	Manifests   []render.ManifestRef `json:"manifests"`
	AppliedAt   *time.Time           `json:"appliedAt,omitempty"`
}

type chainUpInput struct {
	Image      string
	Name       string
	Validators int
	Apply      bool
	Kubeconfig string
	Context    string
}

type chainDeps struct {
	resolveDigest func(context.Context, string) (string, *clioutput.Error)
	identityPath  func() (string, error)
	newKubeClient func(kube.Options) (*kube.Client, *clioutput.Error)
	apply         func(ctx context.Context, kc *kube.Client, fieldOwner, namespace string, docs [][]byte) ([]kube.ApplyResult, *clioutput.Error)
	getCaller     func(context.Context) (*aws.Caller, *clioutput.Error)
}

var defaultChainDeps = chainDeps{
	resolveDigest: aws.ResolveDigest,
	identityPath:  identity.DefaultPath,
	newKubeClient: kube.New,
	apply:         applyToCluster,
	getCaller:     aws.GetCaller,
}

var ChainCmd = cli.Command{
	Name:  "chain",
	Usage: "Manage standalone validator chains on the harbor cluster",
	Commands: []*cli.Command{
		chainDownCmd,
		{
			Name:  "up",
			Usage: "Render or apply a validator-only SeiNodeDeployment",
			Flags: append(kubeconfigFlags(),
				&cli.StringFlag{Name: "image", Required: true, Usage: "ECR image ref"},
				&cli.StringFlag{Name: "name", Required: true, Usage: "Chain name"},
				&cli.IntFlag{Name: "validators", Required: true, Usage: "Validator count (1-21)"},
				&cli.BoolFlag{Name: "apply", Usage: "Server-side apply the rendered manifest"},
			),
			Action: func(ctx context.Context, command *cli.Command) error {
				return runChainUpCmd(ctx, chainUpInput{
					Image:      command.String("image"),
					Name:       command.String("name"),
					Validators: int(command.Int("validators")),
					Apply:      command.Bool("apply"),
					Kubeconfig: command.String("kubeconfig"),
					Context:    command.String("context"),
				}, os.Stdout, defaultChainDeps)
			},
		},
	},
}

func runChainUpCmd(ctx context.Context, in chainUpInput, out io.Writer, deps chainDeps) error {
	res, err := runChainUp(ctx, in, deps)
	if err != nil {
		return failChainUp(out, err)
	}
	if emitErr := clioutput.Emit(out, clioutput.KindChainUpResult, res); emitErr != nil {
		fmt.Fprintln(os.Stderr, emitErr)
		return cli.Exit("", 1)
	}
	return nil
}

func runChainUp(ctx context.Context, in chainUpInput, deps chainDeps) (chainUpResult, *clioutput.Error) {
	eng, idErr := loadEngineer(deps.identityPath)
	if idErr != nil {
		return chainUpResult{}, idErr
	}
	if e := validate.Image(in.Image); e != nil {
		return chainUpResult{}, e.ExitWith(clioutput.ExitBench)
	}
	if e := validate.Name(eng.Alias, in.Name); e != nil {
		return chainUpResult{}, e.ExitWith(clioutput.ExitBench)
	}
	if e := validate.Validators(in.Validators); e != nil {
		return chainUpResult{}, e.ExitWith(clioutput.ExitBench)
	}
	namespace := "eng-" + eng.Alias
	if e := validate.Namespace(namespace, eng.Alias); e != nil {
		return chainUpResult{}, e.ExitWith(clioutput.ExitBench)
	}
	if _, callerErr := deps.getCaller(ctx); callerErr != nil {
		return chainUpResult{}, callerErr
	}

	digest, dErr := deps.resolveDigest(ctx, in.Image)
	if dErr != nil {
		return chainUpResult{}, dErr
	}
	seidImage := digestPinned(in.Image, digest)
	if err := aws.AssertECRDigestRef(seidImage); err != nil {
		return chainUpResult{}, clioutput.Newf(clioutput.ExitBench, clioutput.CatImageResolution, "%v", err)
	}

	chainID := fmt.Sprintf("bench-%s-%s", eng.Alias, in.Name)
	digestShort := shortDigest(digest)

	docs, manifests, rerr := renderChainManifests(eng.Alias, in.Name, namespace, chainID, seidImage, digestShort, in.Validators)
	if rerr != nil {
		return chainUpResult{}, rerr
	}

	res := chainUpResult{
		ChainID:     chainID,
		Name:        in.Name,
		Namespace:   namespace,
		ImageRef:    in.Image,
		ImageDigest: digest,
		Validators:  in.Validators,
		Endpoints:   deriveChainEndpoints(chainID, namespace),
		DryRun:      !in.Apply,
		Manifests:   manifests,
	}

	if in.Apply {
		kc, kerr := deps.newKubeClient(kube.Options{Kubeconfig: in.Kubeconfig, Context: in.Context})
		if kerr != nil {
			return chainUpResult{}, kerr
		}
		applyResults, applyErr := deps.apply(ctx, kc, benchFieldOwner, namespace, docs)
		if applyErr != nil {
			return chainUpResult{}, applyErr
		}
		res.Manifests = mergeApplyResults(manifests, applyResults)
		now := time.Now().UTC()
		res.AppliedAt = &now
	}
	return res, nil
}

func renderChainManifests(alias, name, namespace, chainID, seidImage, digestShort string, validators int) ([][]byte, []render.ManifestRef, *clioutput.Error) {
	vars := map[string]string{
		"CHAIN_ID":           chainID,
		"NAMESPACE":          namespace,
		"ENGINEER_ALIAS":     alias,
		"NAME":               name,
		"SEID_IMAGE":         seidImage,
		"IMAGE_DIGEST_SHORT": digestShort,
		"VALIDATOR_COUNT":    strconv.Itoa(validators),
		"PART_OF":            "seictl",
	}
	chainYAML, e := renderEmbedded("chain.yaml", vars)
	if e != nil {
		return nil, nil, e
	}
	docs := render.SplitYAML(chainYAML)
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

func deriveChainEndpoints(chainID, namespace string) Endpoints {
	host := fmt.Sprintf("%s-internal.%s.svc.cluster.local", chainID, namespace)
	return Endpoints{
		TendermintRpc: []string{fmt.Sprintf("http://%s:%d", host, seiconfig.PortRPC)},
	}
}

func failChainUp(out io.Writer, e *clioutput.Error) error {
	_ = clioutput.EmitError(out, clioutput.KindChainUpResult, e)
	return cli.Exit("", e.Code)
}
