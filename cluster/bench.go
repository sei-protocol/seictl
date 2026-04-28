package cluster

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/cluster/internal/aws"
	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/identity"
	"github.com/sei-protocol/seictl/cluster/internal/render"
	"github.com/sei-protocol/seictl/cluster/internal/validate"
	"github.com/sei-protocol/seictl/cluster/templates"
)

const (
	benchS3Bucket = "harbor-sei-autobake-results"

	// Vendored seiload image. Slice 3b will resolve to a digest at apply
	// time; v1 dry-run emits the tag for visibility, fails-closed against
	// non-ECR registries via internal/validate.Image.
	defaultSeiloadImage = "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/seiload:latest"
)

var benchSizes = map[string]struct {
	Validators int
	RPC        int
}{
	"s": {Validators: 4, RPC: 1},
	"m": {Validators: 10, RPC: 2},
	"l": {Validators: 21, RPC: 4},
}

type benchUpResult struct {
	ChainID      string               `json:"chainId"`
	Name         string               `json:"name"`
	Namespace    string               `json:"namespace"`
	ImageRef     string               `json:"imageRef"`
	ImageDigest  string               `json:"imageDigest"`
	Size         string               `json:"size"`
	Validators   int                  `json:"validators"`
	RPCNodes     int                  `json:"rpcNodes"`
	Duration     string               `json:"duration"`
	ResultsS3URI string               `json:"resultsS3Uri"`
	DryRun       bool                 `json:"dryRun"`
	Manifests    []render.ManifestRef `json:"manifests"`
	AppliedAt    *time.Time           `json:"appliedAt,omitempty"`
}

type benchUpInput struct {
	Image    string
	Name     string
	Size     string
	Duration int
}

type benchDeps struct {
	resolveDigest func(context.Context, string) (string, *clioutput.Error)
	identityPath  func() (string, error)
}

var defaultBenchDeps = benchDeps{
	resolveDigest: aws.ResolveDigest,
	identityPath:  identity.DefaultPath,
}

var BenchCmd = cli.Command{
	Name:  "bench",
	Usage: "Manage benchmark workloads on the harbor cluster",
	Commands: []*cli.Command{
		{
			Name:  "up",
			Usage: "Render (slice 3a) or apply (slice 3b) a benchmark workload",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "image", Required: true, Usage: "ECR image ref to bench"},
				&cli.StringFlag{Name: "name", Required: true, Usage: "Bench name (forms part of chain-id)"},
				&cli.StringFlag{Name: "size", Value: "s", Usage: "s|m|l"},
				&cli.IntFlag{Name: "duration", Value: 30, Usage: "Bench duration in minutes (1-240)"},
			},
			Action: func(ctx context.Context, command *cli.Command) error {
				return runBenchUp(ctx, benchUpInput{
					Image:    command.String("image"),
					Name:     command.String("name"),
					Size:     command.String("size"),
					Duration: int(command.Int("duration")),
				}, os.Stdout, defaultBenchDeps)
			},
		},
	},
}

func runBenchUp(ctx context.Context, in benchUpInput, out io.Writer, deps benchDeps) error {
	eng, idErr := loadEngineer(deps.identityPath)
	if idErr != nil {
		return failBenchUp(out, idErr)
	}

	if e := validate.Image(in.Image); e != nil {
		return failBenchUp(out, e)
	}
	if e := validate.Name(eng.Alias, in.Name); e != nil {
		return failBenchUp(out, e)
	}
	if e := validate.Size(in.Size); e != nil {
		return failBenchUp(out, e)
	}
	if e := validate.DurationMinutes(in.Duration); e != nil {
		return failBenchUp(out, e)
	}

	namespace := "eng-" + eng.Alias
	if e := validate.Namespace(namespace, eng.Alias); e != nil {
		return failBenchUp(out, e)
	}

	digest, dErr := deps.resolveDigest(ctx, in.Image)
	if dErr != nil {
		return failBenchUp(out, dErr)
	}
	seidImage := digestPinned(in.Image, digest)
	if err := aws.AssertECRDigestRef(seidImage); err != nil {
		return failBenchUp(out, clioutput.Newf(clioutput.ExitBench, clioutput.CatImageResolution,
			"%v", err))
	}

	chainID := fmt.Sprintf("bench-%s-%s", eng.Alias, in.Name)
	digestShort := shortDigest(digest)
	s3URI := fmt.Sprintf("s3://%s/%s/%s/%s/report.log", benchS3Bucket, chainID, digestShort, chainID)
	sizeProfile := benchSizes[in.Size]

	manifests, err := renderManifests(eng.Alias, in.Name, namespace, chainID, seidImage,
		digestShort, sizeProfile.Validators, sizeProfile.RPC, in.Duration)
	if err != nil {
		return failBenchUp(out, err)
	}

	res := benchUpResult{
		ChainID:      chainID,
		Name:         in.Name,
		Namespace:    namespace,
		ImageRef:     in.Image,
		ImageDigest:  digest,
		Size:         in.Size,
		Validators:   sizeProfile.Validators,
		RPCNodes:     sizeProfile.RPC,
		Duration:     fmt.Sprintf("%dm", in.Duration),
		ResultsS3URI: s3URI,
		DryRun:       true,
		Manifests:    manifests,
	}
	if err := clioutput.Emit(out, clioutput.KindBenchUpResult, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.Exit("", 1)
	}
	return nil
}

func loadEngineer(pathFn func() (string, error)) (*identity.Engineer, *clioutput.Error) {
	path, err := pathFn()
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMissing,
			"resolve identity path: %v", err)
	}
	return identity.Read(path)
}

// renderManifests builds the four-document YAML stream the bench
// produces (validator SND, RPC SND, seiload Job, profile ConfigMap)
// and extracts a ManifestRef per document.
func renderManifests(alias, name, namespace, chainID, seidImage, digestShort string,
	validators, rpcCount, durationMin int) ([]render.ManifestRef, *clioutput.Error) {
	vars := map[string]string{
		"CHAIN_ID":             chainID,
		"NAMESPACE":            namespace,
		"ENGINEER_ALIAS":       alias,
		"BENCH_NAME":           name,
		"SEID_IMAGE":           seidImage,
		"SEILOAD_IMAGE":        defaultSeiloadImage,
		"IMAGE_DIGEST_SHORT":   digestShort,
		"VALIDATOR_COUNT":      strconv.Itoa(validators),
		"RPC_COUNT":            strconv.Itoa(rpcCount),
		"JOB_DEADLINE_SECONDS": strconv.Itoa(durationMin * 60),
	}

	sndYAML, e := renderEmbedded("snd.yaml", vars)
	if e != nil {
		return nil, e
	}
	jobYAML, e := renderEmbedded("seiload-job.yaml", vars)
	if e != nil {
		return nil, e
	}

	profileBody, e := renderEmbedded("seiload-profile.json", map[string]string{
		"CHAIN_ID":      chainID,
		"RPC_ENDPOINTS": rpcEndpointsJSON(chainID, namespace, rpcCount),
	})
	if e != nil {
		return nil, e
	}

	cmVars := make(map[string]string, len(vars)+1)
	for k, v := range vars {
		cmVars[k] = v
	}
	cmVars["PROFILE_BODY_INDENTED"] = render.Indent(strings.TrimRight(string(profileBody), "\n"), "    ")
	cmYAML, e := renderEmbedded("profile-cm.yaml", cmVars)
	if e != nil {
		return nil, e
	}

	stream := bytes.Join([][]byte{sndYAML, jobYAML, cmYAML}, []byte("\n---\n"))
	var refs []render.ManifestRef
	for _, doc := range render.SplitYAML(stream) {
		ref, err := render.ExtractRef(doc)
		if err != nil {
			return nil, clioutput.Newf(clioutput.ExitBench, clioutput.CatTemplateRender,
				"extract manifest ref: %v", err)
		}
		ref.Action = "create"
		refs = append(refs, ref)
	}
	return refs, nil
}

func renderEmbedded(name string, vars map[string]string) ([]byte, *clioutput.Error) {
	raw, err := templates.FS.ReadFile(name)
	if err != nil {
		return nil, clioutput.Newf(clioutput.ExitBench, clioutput.CatTemplateRender,
			"read embedded template %s: %v", name, err)
	}
	return render.Render(raw, vars)
}

func failBenchUp(out io.Writer, e *clioutput.Error) error {
	_ = clioutput.EmitError(out, clioutput.KindBenchUpResult, e)
	return cli.Exit("", e.Code)
}

// digestPinned strips any tag or digest from ref and re-pins to the
// given digest, producing host/repo@sha256:....
func digestPinned(ref, digest string) string {
	slash := strings.IndexByte(ref, '/')
	host := ref[:slash]
	rest := ref[slash+1:]
	if at := strings.IndexByte(rest, '@'); at >= 0 {
		rest = rest[:at]
	} else if colon := strings.IndexByte(rest, ':'); colon >= 0 {
		rest = rest[:colon]
	}
	return host + "/" + rest + "@" + digest
}

// shortDigest returns the first 12 hex characters of a sha256 digest,
// matching the autobake S3 path partition convention.
func shortDigest(d string) string {
	i := strings.IndexByte(d, ':')
	if i < 0 || len(d) < i+1+12 {
		return d
	}
	return d[i+1 : i+1+12]
}

// rpcEndpointsJSON renders the JSON-array body for the seiload profile's
// `endpoints` field. RPC service DNS resolves at apply time; dry-run
// emits the predictable cluster-local form.
func rpcEndpointsJSON(chainID, namespace string, rpcCount int) string {
	var sb strings.Builder
	for i := 0; i < rpcCount; i++ {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("%q",
			fmt.Sprintf("http://%s-rpc.%s.svc.cluster.local:8545", chainID, namespace)))
	}
	return sb.String()
}
