package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/identity"
	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

const benchTestDigest = "sha256:1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcd"

func writeEngineerFile(t *testing.T, alias string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "seictl")
	path := filepath.Join(root, "engineer.json")
	if err := identity.Write(path, identity.Engineer{Alias: alias, Name: "Test"}); err != nil {
		t.Fatalf("seed identity: %+v", err)
	}
	return path
}

func stubBenchDeps(t *testing.T, alias string) benchDeps {
	t.Helper()
	path := writeEngineerFile(t, alias)
	return benchDeps{
		resolveDigest: func(context.Context, string) (string, *clioutput.Error) {
			return benchTestDigest, nil
		},
		identityPath: func() (string, error) { return path, nil },
		apply: func(context.Context, kube.Options, string, [][]byte) ([]kube.ApplyResult, *clioutput.Error) {
			t.Fatalf("apply should not be called on dry-run path")
			return nil, nil
		},
	}
}

func TestRunBenchUp(t *testing.T) {
	t.Run("dry-run emits BenchUpResult envelope for size s", func(t *testing.T) {
		var buf bytes.Buffer
		err := runBenchUp(context.Background(), benchUpInput{
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1.2.3",
			Name:     "demo",
			Size:     "s",
			Duration: 5,
		}, &buf, stubBenchDeps(t, "bdc"))
		if err != nil {
			t.Fatalf("runBenchUp: %v\nbody=%s", err, buf.String())
		}

		var env clioutput.Envelope
		if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
			t.Fatalf("envelope unmarshal: %v\n%s", err, buf.String())
		}
		if env.Kind != clioutput.KindBenchUpResult || env.APIVersion != clioutput.APIVersion {
			t.Errorf("envelope: kind=%q apiVersion=%q", env.Kind, env.APIVersion)
		}
		if env.Error != nil {
			t.Fatalf("error body should be nil; got %+v\nbody=%s", env.Error, buf.String())
		}
		var data benchUpResult
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("data unmarshal: %v", err)
		}

		if data.ChainID != "bench-bdc-demo" {
			t.Errorf("chainId: %q", data.ChainID)
		}
		if data.Namespace != "eng-bdc" {
			t.Errorf("namespace: %q", data.Namespace)
		}
		if data.ImageDigest != benchTestDigest {
			t.Errorf("digest: %q", data.ImageDigest)
		}
		if data.Validators != 4 || data.RPCNodes != 1 {
			t.Errorf("size profile s: validators=%d rpc=%d", data.Validators, data.RPCNodes)
		}
		if !data.DryRun {
			t.Errorf("dryRun should be true")
		}
		// Schema: s3://harbor-validation-results/<namespace>/<job>/<run>/...
		want := "s3://harbor-validation-results/eng-bdc/evm-transfer/demo/report.log"
		if data.ResultsS3URI != want {
			t.Errorf("s3 uri: got %q, want %q", data.ResultsS3URI, want)
		}
		if len(data.Manifests) != 4 {
			t.Fatalf("expected 4 manifests (validator SND, rpc SND, seiload Job, profile CM); got %d", len(data.Manifests))
		}

		// Spot-check kinds and namespacing — every manifest must live in eng-bdc.
		seenKinds := map[string]bool{}
		for _, m := range data.Manifests {
			seenKinds[m.Kind] = true
			if m.Namespace != "eng-bdc" {
				t.Errorf("manifest %s/%s namespace: got %q, want eng-bdc", m.Kind, m.Name, m.Namespace)
			}
			if m.Action != "create" {
				t.Errorf("dry-run action: got %q", m.Action)
			}
		}
		if !seenKinds["SeiNodeDeployment"] || !seenKinds["Job"] || !seenKinds["ConfigMap"] {
			t.Errorf("expected kinds {SeiNodeDeployment,Job,ConfigMap}; got %v", seenKinds)
		}
	})

	t.Run("size m yields 10 validators / 2 rpc", func(t *testing.T) {
		var buf bytes.Buffer
		err := runBenchUp(context.Background(), benchUpInput{
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:     "load",
			Size:     "m",
			Duration: 30,
		}, &buf, stubBenchDeps(t, "bdc"))
		if err != nil {
			t.Fatalf("runBenchUp: %v", err)
		}
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data benchUpResult
		_ = json.Unmarshal(env.Data, &data)
		if data.Validators != 10 || data.RPCNodes != 2 {
			t.Errorf("size m: %d / %d", data.Validators, data.RPCNodes)
		}
	})

	t.Run("rejects non-ECR image", func(t *testing.T) {
		var buf bytes.Buffer
		err := runBenchUp(context.Background(), benchUpInput{
			Image:    "docker.io/sei/sei-chain:v1",
			Name:     "demo",
			Size:     "s",
			Duration: 30,
		}, &buf, stubBenchDeps(t, "bdc"))
		if err == nil {
			t.Fatalf("expected error")
		}
		exitCoder, ok := err.(interface{ ExitCode() int })
		if !ok || exitCoder.ExitCode() != clioutput.ExitBench {
			t.Errorf("exit code: %v", err)
		}
		if !strings.Contains(buf.String(), "image-policy") {
			t.Errorf("expected image-policy category; got %s", buf.String())
		}
	})

	t.Run("rejects bad size", func(t *testing.T) {
		var buf bytes.Buffer
		err := runBenchUp(context.Background(), benchUpInput{
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:     "demo",
			Size:     "xl",
			Duration: 30,
		}, &buf, stubBenchDeps(t, "bdc"))
		if err == nil || !strings.Contains(buf.String(), "validation") {
			t.Errorf("expected validation error; got err=%v body=%s", err, buf.String())
		}
	})

	t.Run("propagates digest-resolution errors", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		deps := benchDeps{
			resolveDigest: func(context.Context, string) (string, *clioutput.Error) {
				return "", clioutput.New(clioutput.ExitBench, clioutput.CatImageResolution, "ecr unavailable")
			},
			identityPath: func() (string, error) { return path, nil },
		}
		var buf bytes.Buffer
		err := runBenchUp(context.Background(), benchUpInput{
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:     "demo",
			Size:     "s",
			Duration: 5,
		}, &buf, deps)
		if err == nil || !strings.Contains(buf.String(), "image-resolution") {
			t.Errorf("expected image-resolution error; got %s", buf.String())
		}
	})

	t.Run("missing identity surfaces typed error", func(t *testing.T) {
		deps := benchDeps{
			resolveDigest: func(context.Context, string) (string, *clioutput.Error) {
				return benchTestDigest, nil
			},
			identityPath: func() (string, error) { return "", errors.New("home unset") },
		}
		var buf bytes.Buffer
		err := runBenchUp(context.Background(), benchUpInput{
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:     "demo",
			Size:     "s",
			Duration: 5,
		}, &buf, deps)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !strings.Contains(buf.String(), `"category": "missing"`) {
			t.Errorf("expected missing identity error; got %s", buf.String())
		}
	})

	t.Run("apply merges ApplyResult actions and sets appliedAt", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		var capturedDocs [][]byte
		deps := benchDeps{
			resolveDigest: func(context.Context, string) (string, *clioutput.Error) {
				return benchTestDigest, nil
			},
			identityPath: func() (string, error) { return path, nil },
			apply: func(_ context.Context, opts kube.Options, fieldOwner string, docs [][]byte) ([]kube.ApplyResult, *clioutput.Error) {
				if fieldOwner != benchFieldOwner {
					t.Errorf("field owner: got %q, want %q", fieldOwner, benchFieldOwner)
				}
				capturedDocs = docs
				out := make([]kube.ApplyResult, len(docs))
				for i := range docs {
					out[i] = kube.ApplyResult{
						Kind:      "stub",
						Name:      "stub",
						Namespace: "stub",
						Action:    "update",
					}
				}
				return out, nil
			},
		}

		var buf bytes.Buffer
		err := runBenchUp(context.Background(), benchUpInput{
			Image:      "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1.2.3",
			Name:       "demo",
			Size:       "s",
			Duration:   5,
			Apply:      true,
			Kubeconfig: "/tmp/kube",
			Context:    "harbor",
		}, &buf, deps)
		if err != nil {
			t.Fatalf("runBenchUp: %v\nbody=%s", err, buf.String())
		}
		if len(capturedDocs) != 4 {
			t.Errorf("apply received %d docs, want 4", len(capturedDocs))
		}

		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data benchUpResult
		_ = json.Unmarshal(env.Data, &data)

		if data.DryRun {
			t.Errorf("dryRun should be false on --apply")
		}
		if data.AppliedAt == nil {
			t.Errorf("appliedAt should be set on --apply")
		}
		for _, m := range data.Manifests {
			if m.Action != "update" {
				t.Errorf("manifest action: got %q, want update (from stub)", m.Action)
			}
		}
	})

	t.Run("apply propagates kubeconfig errors with ExitIdentity", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		deps := benchDeps{
			resolveDigest: func(context.Context, string) (string, *clioutput.Error) {
				return benchTestDigest, nil
			},
			identityPath: func() (string, error) { return path, nil },
			apply: func(context.Context, kube.Options, string, [][]byte) ([]kube.ApplyResult, *clioutput.Error) {
				return nil, clioutput.New(clioutput.ExitIdentity, clioutput.CatKubeconfigParse, "no kubeconfig")
			},
		}
		var buf bytes.Buffer
		err := runBenchUp(context.Background(), benchUpInput{
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:     "demo",
			Size:     "s",
			Duration: 5,
			Apply:    true,
		}, &buf, deps)
		if err == nil {
			t.Fatalf("expected error")
		}
		ec, ok := err.(interface{ ExitCode() int })
		if !ok || ec.ExitCode() != clioutput.ExitIdentity {
			t.Errorf("exit code: got %v, want %d", err, clioutput.ExitIdentity)
		}
		if !strings.Contains(buf.String(), "kubeconfig-parse") {
			t.Errorf("expected kubeconfig-parse category; got %s", buf.String())
		}
	})

	t.Run("apply propagates SSA failures with CatApplyFailed", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		deps := benchDeps{
			resolveDigest: func(context.Context, string) (string, *clioutput.Error) {
				return benchTestDigest, nil
			},
			identityPath: func() (string, error) { return path, nil },
			apply: func(context.Context, kube.Options, string, [][]byte) ([]kube.ApplyResult, *clioutput.Error) {
				return nil, clioutput.New(clioutput.ExitBench, clioutput.CatApplyFailed, "API server says no")
			},
		}
		var buf bytes.Buffer
		err := runBenchUp(context.Background(), benchUpInput{
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:     "demo",
			Size:     "s",
			Duration: 5,
			Apply:    true,
		}, &buf, deps)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !strings.Contains(buf.String(), "apply-failed") {
			t.Errorf("expected apply-failed category; got %s", buf.String())
		}
	})
}

func TestDigestPinned(t *testing.T) {
	tests := map[string]struct {
		ref, digest, want string
	}{
		"tag": {
			ref:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			digest: benchTestDigest,
			want:   "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain@" + benchTestDigest,
		},
		"already digested": {
			ref:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain@sha256:" + strings.Repeat("a", 64),
			digest: benchTestDigest,
			want:   "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain@" + benchTestDigest,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := digestPinned(tc.ref, tc.digest); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestShortDigest(t *testing.T) {
	if got := shortDigest("sha256:1234567890abcdef1234567890abcdef"); got != "1234567890ab" {
		t.Errorf("got %q", got)
	}
	if got := shortDigest("not-a-digest"); got != "not-a-digest" {
		t.Errorf("non-prefixed pass-through: %q", got)
	}
}
