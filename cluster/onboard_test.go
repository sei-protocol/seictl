package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sei-protocol/seictl/cluster/internal/aws"
	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/config"
	"github.com/sei-protocol/seictl/cluster/internal/githubpr"
	"github.com/sei-protocol/seictl/cluster/internal/onboardmanifests"
	"github.com/sei-protocol/seictl/cluster/internal/onboardmanifests/aggregator"
)

// onboardStubs builds a deps struct with all positive-path stubs.
// Tests override individual fields to exercise specific failure modes.
func onboardStubs(t *testing.T) (onboardDeps, string, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "seictl")
	cfgPath := filepath.Join(root, "config.json")
	repoPath := t.TempDir()
	// Create .git and clusters/harbor markers so DiscoverRepo would
	// resolve. Tests that exercise discovery go through deps.discoverRepo.
	_ = os.MkdirAll(filepath.Join(repoPath, ".git"), 0o755)
	_ = os.MkdirAll(filepath.Join(repoPath, "clusters", "harbor"), 0o755)

	deps := onboardDeps{
		configPath: func() (string, error) { return cfgPath, nil },
		getCaller: func(context.Context) (*aws.Caller, *clioutput.Error) {
			return &aws.Caller{Account: "189176372795", Region: "eu-central-1", PrincipalARN: "arn:aws:sts::...:bdc"}, nil
		},
		provisionIAM: func(_ context.Context, scope aws.EngineerScope, dryRun bool) ([]aws.IAMArtifact, *clioutput.Error) {
			action := "create"
			if dryRun {
				action = "would-create"
			}
			return []aws.IAMArtifact{
				{Kind: "Policy", ARN: scope.PolicyARN(), Action: action},
				{Kind: "Role", ARN: scope.RoleARN(), Action: action},
				{Kind: "Attachment", ARN: scope.PolicyARN(), Action: action},
			}, nil
		},
		podIdentity: func(_ context.Context, b aws.PodIdentityBinding, dryRun bool) (aws.PodIdentityArtifact, *clioutput.Error) {
			action := "create"
			if dryRun {
				action = "would-create"
			}
			return aws.PodIdentityArtifact{
				Kind:          "PodIdentityAssociation",
				AssociationID: "a-stub",
				RoleARN:       b.RoleARN,
				Action:        action,
			}, nil
		},
		generateFiles: onboardmanifests.Generate,
		updateAggregator: func(_, alias string) (aggregator.Result, error) {
			return aggregator.Result{
				Path:    "clusters/harbor/engineers/kustomization.yaml",
				Content: []byte("apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - " + alias + "\n"),
				Added:   true,
			}, nil
		},
		ensureUpToDate: func(string, string) error { return nil },
		checkGHAuth:    func() error { return nil },
		checkClean:     func(string) error { return nil },
		createPR: func(opts githubpr.Options) (*githubpr.Result, error) {
			return &githubpr.Result{Branch: opts.Branch, URL: "https://github.com/example/pr/1"}, nil
		},
		discoverRepo: func(string) (string, error) { return repoPath, nil },
		writeConfig:  config.Write,
	}
	return deps, cfgPath, repoPath
}

func TestRunOnboard_DryRunEmitsWouldCreate(t *testing.T) {
	deps, cfgPath, _ := onboardStubs(t)

	var buf bytes.Buffer
	err := runOnboard(context.Background(), onboardInput{Alias: "bdc"}, &buf, deps)
	if err != nil {
		t.Fatalf("runOnboard: %v\nbody=%s", err, buf.String())
	}

	var env clioutput.Envelope
	_ = json.Unmarshal(buf.Bytes(), &env)
	if env.Kind != clioutput.KindOnboardResult {
		t.Errorf("kind: %q", env.Kind)
	}
	var data OnboardResult
	_ = json.Unmarshal(env.Data, &data)
	if !data.DryRun {
		t.Errorf("DryRun should be true")
	}
	if data.PRURL != "" {
		t.Errorf("PR should not be opened on dry-run; got %q", data.PRURL)
	}
	for _, r := range data.AWSResources {
		if r.Action != "would-create" {
			t.Errorf("expected would-create on dry-run; got %s on %s", r.Action, r.Kind)
		}
	}
	// Config file should NOT be written on dry-run.
	if _, statErr := os.Stat(cfgPath); statErr == nil {
		t.Errorf("config file written on dry-run at %s", cfgPath)
	}
	if len(data.GeneratedFiles) != 4 {
		t.Errorf("generated files: got %d, want 4 (3 cell + aggregator)", len(data.GeneratedFiles))
	}
	var sawAggregator bool
	for _, p := range data.GeneratedFiles {
		if p == "clusters/harbor/engineers/kustomization.yaml" {
			sawAggregator = true
		}
	}
	if !sawAggregator {
		t.Errorf("aggregator path missing from generated files: %v", data.GeneratedFiles)
	}
}

func TestRunOnboard_ApplyHappyPath(t *testing.T) {
	deps, cfgPath, _ := onboardStubs(t)

	var buf bytes.Buffer
	err := runOnboard(context.Background(), onboardInput{Alias: "bdc", Apply: true}, &buf, deps)
	if err != nil {
		t.Fatalf("runOnboard: %v\nbody=%s", err, buf.String())
	}
	var env clioutput.Envelope
	_ = json.Unmarshal(buf.Bytes(), &env)
	var data OnboardResult
	_ = json.Unmarshal(env.Data, &data)

	if data.DryRun {
		t.Errorf("DryRun should be false on --apply")
	}
	if data.PRURL == "" {
		t.Errorf("PR URL should be set; got %+v", data)
	}
	if data.Branch == "" {
		t.Errorf("Branch should be set")
	}
	if data.Namespace != "eng-bdc" {
		t.Errorf("namespace: got %q, want eng-bdc", data.Namespace)
	}
	for _, r := range data.AWSResources {
		if r.Action != "create" {
			t.Errorf("expected create; got %s", r.Action)
		}
	}
	// Config file written.
	got, idErr := config.Read(cfgPath)
	if idErr != nil {
		t.Fatalf("config not written: %v", idErr)
	}
	if got.Alias != "bdc" || got.Namespace != "eng-bdc" {
		t.Errorf("config content: %+v", got)
	}
}

func TestRunOnboard_NoPRSkipsPRCreation(t *testing.T) {
	deps, _, _ := onboardStubs(t)
	prCalled := false
	deps.createPR = func(githubpr.Options) (*githubpr.Result, error) {
		prCalled = true
		return nil, nil
	}

	var buf bytes.Buffer
	err := runOnboard(context.Background(), onboardInput{Alias: "bdc", Apply: true, NoPR: true}, &buf, deps)
	if err != nil {
		t.Fatalf("runOnboard: %v\n%s", err, buf.String())
	}
	if prCalled {
		t.Errorf("createPR should not be called with --no-pr")
	}
	var env clioutput.Envelope
	_ = json.Unmarshal(buf.Bytes(), &env)
	var data OnboardResult
	_ = json.Unmarshal(env.Data, &data)
	if data.PRURL != "" {
		t.Errorf("PR URL should be empty with --no-pr")
	}
}

func TestRunOnboard_RejectsBadAlias(t *testing.T) {
	deps, _, _ := onboardStubs(t)
	var buf bytes.Buffer
	err := runOnboard(context.Background(), onboardInput{Alias: "Bad-Alias"}, &buf, deps)
	if err == nil || !strings.Contains(buf.String(), "alias-invalid") {
		t.Errorf("expected alias-invalid; got %s", buf.String())
	}
}

func TestRunOnboard_RejectsConflictingIdentity(t *testing.T) {
	deps, cfgPath, _ := onboardStubs(t)
	if e := config.Write(cfgPath, config.Config{Alias: "alice", Namespace: "eng-alice"}); e != nil {
		t.Fatalf("seed: %v", e)
	}
	var buf bytes.Buffer
	err := runOnboard(context.Background(), onboardInput{Alias: "bdc"}, &buf, deps)
	if err == nil {
		t.Fatalf("expected rejection")
	}
	if !strings.Contains(buf.String(), "alice") {
		t.Errorf("error should name conflicting alias; got %s", buf.String())
	}
}

func TestRunOnboard_RejectsSquattedAlias(t *testing.T) {
	deps, _, repoPath := onboardStubs(t)
	if err := os.MkdirAll(filepath.Join(repoPath, "clusters", "harbor", "engineers", "bdc"), 0o755); err != nil {
		t.Fatalf("seed squatter: %v", err)
	}
	var buf bytes.Buffer
	err := runOnboard(context.Background(), onboardInput{Alias: "bdc", Apply: true}, &buf, deps)
	if err == nil || !strings.Contains(buf.String(), "alias-invalid") {
		t.Errorf("expected alias-invalid for squatter; got %s", buf.String())
	}
}

func TestRunOnboard_PropagatesGHAuthFailure(t *testing.T) {
	deps, _, _ := onboardStubs(t)
	deps.checkGHAuth = func() error { return errors.New("not logged in") }
	var buf bytes.Buffer
	err := runOnboard(context.Background(), onboardInput{Alias: "bdc", Apply: true}, &buf, deps)
	if err == nil || !strings.Contains(buf.String(), "gh-unauthenticated") {
		t.Errorf("expected gh-unauthenticated; got %s", buf.String())
	}
}

func TestRunOnboard_RefusesWrongAWSAccount(t *testing.T) {
	deps, _, _ := onboardStubs(t)
	deps.getCaller = func(context.Context) (*aws.Caller, *clioutput.Error) {
		return &aws.Caller{Account: "999999999999", Region: "eu-central-1", PrincipalARN: "arn:aws:sts::999999999999:assumed-role/foo"}, nil
	}
	iamCalled := false
	deps.provisionIAM = func(context.Context, aws.EngineerScope, bool) ([]aws.IAMArtifact, *clioutput.Error) {
		iamCalled = true
		return nil, nil
	}

	var buf bytes.Buffer
	err := runOnboard(context.Background(), onboardInput{Alias: "bdc", Apply: true}, &buf, deps)
	if err == nil {
		t.Fatalf("expected error, got success: %s", buf.String())
	}
	out := buf.String()
	if !strings.Contains(out, clioutput.CatWrongAccount) {
		t.Errorf("expected category %q; got %s", clioutput.CatWrongAccount, out)
	}
	if !strings.Contains(out, "999999999999") {
		t.Errorf("error should name actual account; got %s", out)
	}
	if !strings.Contains(out, onboardAccount) {
		t.Errorf("error should name expected account; got %s", out)
	}
	if iamCalled {
		t.Errorf("provisionIAM must not be called when caller account is wrong")
	}
}

func TestRunOnboard_PropagatesIAMFailureWithAWSCreateFailed(t *testing.T) {
	deps, _, _ := onboardStubs(t)
	deps.provisionIAM = func(context.Context, aws.EngineerScope, bool) ([]aws.IAMArtifact, *clioutput.Error) {
		return nil, clioutput.New(clioutput.ExitOnboard, clioutput.CatAWSCreateFailed, "permission denied")
	}
	var buf bytes.Buffer
	err := runOnboard(context.Background(), onboardInput{Alias: "bdc", Apply: true}, &buf, deps)
	if err == nil || !strings.Contains(buf.String(), "aws-create-failed") {
		t.Errorf("expected aws-create-failed; got %s", buf.String())
	}
}

func TestRunOnboard_StaleBaseFailsBeforeAggregatorRead(t *testing.T) {
	deps, _, _ := onboardStubs(t)
	deps.ensureUpToDate = func(string, string) error {
		return errors.New("local HEAD is 3 commit(s) behind origin/main; run `git pull origin main` first")
	}
	aggregatorCalled := false
	deps.updateAggregator = func(string, string) (aggregator.Result, error) {
		aggregatorCalled = true
		return aggregator.Result{}, nil
	}
	var buf bytes.Buffer
	err := runOnboard(context.Background(), onboardInput{Alias: "bdc", Apply: true}, &buf, deps)
	if err == nil || !strings.Contains(buf.String(), "base-branch-stale") {
		t.Errorf("expected base-branch-stale; got %s", buf.String())
	}
	if !strings.Contains(buf.String(), "git pull origin main") {
		t.Errorf("error should tell engineer to pull; got %s", buf.String())
	}
	if aggregatorCalled {
		t.Errorf("updateAggregator should not run when base is stale")
	}
}

func TestRunOnboard_AggregatorAddedIsIncludedInPRFiles(t *testing.T) {
	deps, _, _ := onboardStubs(t)
	deps.updateAggregator = func(_, alias string) (aggregator.Result, error) {
		return aggregator.Result{
			Path:    "clusters/harbor/engineers/kustomization.yaml",
			Content: []byte("synthetic-aggregator-body-for-" + alias),
			Added:   true,
		}, nil
	}
	var captured githubpr.Options
	deps.createPR = func(opts githubpr.Options) (*githubpr.Result, error) {
		captured = opts
		return &githubpr.Result{Branch: opts.Branch, URL: "https://github.com/example/pr/1"}, nil
	}
	var buf bytes.Buffer
	if err := runOnboard(context.Background(), onboardInput{Alias: "bdc", Apply: true}, &buf, deps); err != nil {
		t.Fatalf("runOnboard: %v\n%s", err, buf.String())
	}
	body, ok := captured.Files["clusters/harbor/engineers/kustomization.yaml"]
	if !ok {
		t.Fatalf("aggregator path not in PR file map; got keys: %v", keys(captured.Files))
	}
	if !bytes.Contains(body, []byte("synthetic-aggregator-body-for-bdc")) {
		t.Errorf("aggregator content not propagated; got %s", body)
	}
}

func TestRunOnboard_AggregatorIdempotentSkipsPRFile(t *testing.T) {
	deps, _, _ := onboardStubs(t)
	deps.updateAggregator = func(string, string) (aggregator.Result, error) {
		return aggregator.Result{
			Path:    "clusters/harbor/engineers/kustomization.yaml",
			Content: []byte("unchanged"),
			Added:   false,
		}, nil
	}
	var captured githubpr.Options
	deps.createPR = func(opts githubpr.Options) (*githubpr.Result, error) {
		captured = opts
		return &githubpr.Result{Branch: opts.Branch, URL: "https://github.com/example/pr/1"}, nil
	}
	var buf bytes.Buffer
	if err := runOnboard(context.Background(), onboardInput{Alias: "bdc", Apply: true}, &buf, deps); err != nil {
		t.Fatalf("runOnboard: %v\n%s", err, buf.String())
	}
	if _, present := captured.Files["clusters/harbor/engineers/kustomization.yaml"]; present {
		t.Errorf("aggregator path should be omitted when Added=false; got keys: %v", keys(captured.Files))
	}
	var env clioutput.Envelope
	_ = json.Unmarshal(buf.Bytes(), &env)
	var data OnboardResult
	_ = json.Unmarshal(env.Data, &data)
	for _, p := range data.GeneratedFiles {
		if p == "clusters/harbor/engineers/kustomization.yaml" {
			t.Errorf("aggregator path should not appear in GeneratedFiles when Added=false")
		}
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
