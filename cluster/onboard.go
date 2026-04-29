// `seictl onboard` is harbor-only and single-account in v1; the
// constants below pin those assumptions. Multi-cluster / multi-account
// is a follow-up that would replace these with flags.

package cluster

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"text/template"

	"github.com/urfave/cli/v3"

	"github.com/sei-protocol/seictl/cluster/internal/aws"
	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/githubpr"
	"github.com/sei-protocol/seictl/cluster/internal/identity"
	"github.com/sei-protocol/seictl/cluster/internal/onboardmanifests"
	"github.com/sei-protocol/seictl/cluster/internal/onboardmanifests/aggregator"
	"github.com/sei-protocol/seictl/cluster/internal/validate"
)

const (
	onboardCluster = "harbor"
	onboardAccount = "189176372795"
	onboardRegion  = "eu-central-1"
)

//go:embed onboard_pr_body.tmpl
var onboardPRBodyTemplate string

type OnboardResult struct {
	Alias          string        `json:"alias"`
	IdentityPath   string        `json:"identityPath"`
	GeneratedFiles []string      `json:"generatedFiles"`
	Branch         string        `json:"branch,omitempty"`
	PRURL          string        `json:"prUrl,omitempty"`
	AWSResources   []AWSResource `json:"awsResources"`
	DryRun         bool          `json:"dryRun"`
}

// AWSResource action ∈ {create, exists, would-create}.
type AWSResource struct {
	Kind   string `json:"kind"`
	ARN    string `json:"arn"`
	Action string `json:"action"`
}

type onboardInput struct {
	Alias        string
	Name         string
	PlatformRepo string
	NoPR         bool
	Apply        bool
}

type onboardDeps struct {
	identityPath     func() (string, error)
	getCaller        func(context.Context) (*aws.Caller, *clioutput.Error)
	provisionIAM     func(ctx context.Context, scope aws.EngineerScope, dryRun bool) ([]aws.IAMArtifact, *clioutput.Error)
	podIdentity      func(ctx context.Context, b aws.PodIdentityBinding, dryRun bool) (aws.PodIdentityArtifact, *clioutput.Error)
	generateFiles    func(cell onboardmanifests.Cell) ([]onboardmanifests.File, error)
	updateAggregator func(repoPath, alias string) (aggregator.Result, error)
	checkGHAuth      func() error
	checkClean       func(repoPath string) error
	createPR         func(opts githubpr.Options) (*githubpr.Result, error)
	discoverRepo     func(start string) (string, error)
	writeIdentity    func(path string, eng identity.Engineer) *clioutput.Error
}

var defaultOnboardDeps = onboardDeps{
	identityPath:     identity.DefaultPath,
	getCaller:        aws.GetCaller,
	provisionIAM:     aws.ProvisionIAM,
	podIdentity:      aws.EnsurePodIdentity,
	generateFiles:    onboardmanifests.Generate,
	updateAggregator: aggregator.UpdateEngineers,
	checkGHAuth:      githubpr.CheckAuth,
	checkClean:       githubpr.CheckCleanTree,
	createPR:         githubpr.CreatePR,
	discoverRepo:     githubpr.DiscoverRepo,
	writeIdentity:    identity.Write,
}

var OnboardCmd = cli.Command{
	Name:  "onboard",
	Usage: "Provision a new engineer's harbor footprint (IAM + namespace cell)",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "alias", Required: true, Usage: "Engineer alias (matches namespace eng-<alias>)"},
		&cli.StringFlag{Name: "name", Usage: "Engineer display name (recorded in identity file)"},
		&cli.StringFlag{Name: "platform-repo", Usage: "Path to local sei-protocol/platform clone (default: walk up from CWD)"},
		&cli.BoolFlag{Name: "no-pr", Usage: "Generate manifests + AWS, skip the PR creation step"},
		&cli.BoolFlag{Name: "apply", Usage: "Perform the side effects; default is dry-run"},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		return runOnboard(ctx, onboardInput{
			Alias:        command.String("alias"),
			Name:         command.String("name"),
			PlatformRepo: command.String("platform-repo"),
			NoPR:         command.Bool("no-pr"),
			Apply:        command.Bool("apply"),
		}, os.Stdout, defaultOnboardDeps)
	},
}

func runOnboard(ctx context.Context, in onboardInput, out io.Writer, deps onboardDeps) error {
	if e := validate.Alias(in.Alias); e != nil {
		return failOnboard(out, e)
	}

	idPath, err := deps.identityPath()
	if err != nil {
		return failOnboard(out, clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMissing,
			"resolve identity path: %v", err))
	}
	if e := identityConsistent(idPath, in.Alias); e != nil {
		return failOnboard(out, e)
	}

	if _, callerErr := deps.getCaller(ctx); callerErr != nil {
		return failOnboard(out, callerErr)
	}

	repoPath := ""
	if !in.NoPR {
		path, repoErr := resolveRepo(in.PlatformRepo, deps.discoverRepo)
		if repoErr != nil {
			return failOnboard(out, repoErr)
		}
		repoPath = path
		if in.Apply {
			if err := deps.checkGHAuth(); err != nil {
				return failOnboard(out, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatGHUnauthenticated,
					"%v", err))
			}
			if err := deps.checkClean(repoPath); err != nil {
				return failOnboard(out, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatWorkingTreeDirty,
					"%v", err))
			}
			if err := refuseExistingAlias(repoPath, in.Alias); err != nil {
				return failOnboard(out, err)
			}
		}
	}

	dryRun := !in.Apply
	scope := aws.EngineerScope{
		Account: onboardAccount,
		Region:  onboardRegion,
		Cluster: onboardCluster,
		Alias:   in.Alias,
	}
	iamArts, iamErr := deps.provisionIAM(ctx, scope, dryRun)
	if iamErr != nil {
		return failOnboard(out, iamErr)
	}
	piArt, piErr := deps.podIdentity(ctx, aws.PodIdentityBinding{
		Cluster:        scope.Cluster,
		Namespace:      "eng-" + in.Alias,
		ServiceAccount: "bench-seiload",
		RoleARN:        scope.RoleARN(),
		Region:         scope.Region,
	}, dryRun)
	if piErr != nil {
		return failOnboard(out, piErr)
	}

	files, ferr := deps.generateFiles(onboardmanifests.Cell{Alias: in.Alias})
	if ferr != nil {
		return failOnboard(out, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatPRCreateFailed,
			"generate manifests: %v", ferr))
	}
	generatedPaths := make([]string, len(files))
	for i, f := range files {
		generatedPaths[i] = f.Path
	}

	var aggRes aggregator.Result
	if !in.NoPR {
		var aerr error
		aggRes, aerr = deps.updateAggregator(repoPath, in.Alias)
		if errors.Is(aerr, aggregator.ErrAggregatorMissing) {
			return failOnboard(out, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAggregatorMissing,
				"aggregator kustomization missing at clusters/harbor/engineers/kustomization.yaml; this is a one-time bootstrap that must be landed by hand (see sei-protocol/platform#249 for prior art)"))
		}
		if aerr != nil {
			return failOnboard(out, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatPRCreateFailed,
				"update aggregator: %v", aerr))
		}
		if aggRes.Added {
			generatedPaths = append(generatedPaths, aggRes.Path)
		}
	}

	res := OnboardResult{
		Alias:          in.Alias,
		IdentityPath:   idPath,
		GeneratedFiles: generatedPaths,
		AWSResources:   awsArtifactsToResources(iamArts, piArt),
		DryRun:         dryRun,
	}

	if in.Apply && !in.NoPR {
		body, berr := renderPRBody(in.Alias, scope, generatedPaths)
		if berr != nil {
			return failOnboard(out, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatPRCreateFailed,
				"render PR body: %v", berr))
		}
		fileBodies := map[string][]byte{}
		for _, f := range files {
			fileBodies[f.Path] = f.Content
		}
		if aggRes.Added {
			fileBodies[aggRes.Path] = aggRes.Content
		}
		prRes, perr := deps.createPR(githubpr.Options{
			RepoPath:      repoPath,
			Branch:        "seictl/onboard-" + in.Alias,
			BaseBranch:    "main",
			CommitMessage: fmt.Sprintf("feat(engineers): onboard %s cell", in.Alias),
			PRTitle:       fmt.Sprintf("onboard: engineer cell eng-%s", in.Alias),
			PRBody:        body,
			Files:         fileBodies,
		})
		if perr != nil {
			return failOnboard(out, clioutput.Newf(clioutput.ExitOnboard, clioutput.CatPRCreateFailed,
				"%v", perr))
		}
		res.Branch = prRes.Branch
		res.PRURL = prRes.URL
	}

	if in.Apply {
		eng := identity.Engineer{Alias: in.Alias, Name: in.Name}
		if e := deps.writeIdentity(idPath, eng); e != nil {
			return failOnboard(out, e)
		}
	}

	if err := clioutput.Emit(out, clioutput.KindOnboardResult, res); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return cli.Exit("", 1)
	}
	return nil
}

// identityConsistent enforces that a pre-existing engineer.json with a
// different alias blocks onboard. Matching alias is fine (idempotent
// rewrite happens later); missing file is fine (will be created).
func identityConsistent(path, alias string) *clioutput.Error {
	existing, err := identity.Read(path)
	if err == nil {
		if existing.Alias != alias {
			return clioutput.Newf(clioutput.ExitIdentity, clioutput.CatMalformed,
				"identity file at %s has alias %q, refusing to overwrite with %q; remove the file deliberately",
				path, existing.Alias, alias)
		}
		return nil
	}
	if err.Category == clioutput.CatMissing {
		return nil
	}
	return err
}

func resolveRepo(explicit string, discover func(string) (string, error)) (string, *clioutput.Error) {
	if explicit != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", clioutput.Newf(clioutput.ExitOnboard, clioutput.CatPlatformRepoMissing,
				"resolve --platform-repo: %v", err)
		}
		return abs, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", clioutput.Newf(clioutput.ExitOnboard, clioutput.CatPlatformRepoMissing,
			"resolve cwd: %v", err)
	}
	repo, err := discover(cwd)
	if err != nil {
		return "", clioutput.Newf(clioutput.ExitOnboard, clioutput.CatPlatformRepoMissing,
			"%v", err)
	}
	return repo, nil
}

// refuseExistingAlias fails closed if an engineer cell already exists
// at the target path. Mid-run idempotency for an in-progress onboard
// (same engineer re-running) is the createPR layer's job; this guard
// catches the cross-engineer case where Alice tries `--alias bob` and
// would otherwise clobber bob's cell directory.
func refuseExistingAlias(repoPath, alias string) *clioutput.Error {
	cellDir := filepath.Join(repoPath, "clusters", "harbor", "engineers", alias)
	if _, err := os.Stat(cellDir); err == nil {
		return clioutput.Newf(clioutput.ExitOnboard, clioutput.CatAliasInvalid,
			"alias %q already provisioned at %s; remove deliberately or pick a different alias",
			alias, cellDir)
	} else if !errors.Is(err, os.ErrNotExist) {
		return clioutput.Newf(clioutput.ExitOnboard, clioutput.CatPlatformRepoMissing,
			"stat %s: %v", cellDir, err)
	}
	return nil
}

func awsArtifactsToResources(iam []aws.IAMArtifact, pi aws.PodIdentityArtifact) []AWSResource {
	out := make([]AWSResource, 0, len(iam)+1)
	for _, a := range iam {
		out = append(out, AWSResource{Kind: a.Kind, ARN: a.ARN, Action: a.Action})
	}
	if pi.Kind != "" {
		arn := pi.AssociationID
		if arn == "" {
			arn = pi.RoleARN
		}
		out = append(out, AWSResource{Kind: pi.Kind, ARN: arn, Action: pi.Action})
	}
	return out
}

func renderPRBody(alias string, scope aws.EngineerScope, files []string) (string, error) {
	tmpl, err := template.New("pr-body").Parse(onboardPRBodyTemplate)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	err = tmpl.Execute(&buf, struct {
		Alias     string
		Files     []string
		PolicyARN string
		RoleARN   string
	}{alias, files, scope.PolicyARN(), scope.RoleARN()})
	if err != nil {
		return "", err
	}
	return buf.String(), nil
}

func failOnboard(out io.Writer, e *clioutput.Error) error {
	_ = clioutput.EmitError(out, clioutput.KindOnboardResult, e)
	return cli.Exit("", e.Code)
}
