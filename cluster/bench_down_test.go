package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

func TestRunBenchDown(t *testing.T) {
	t.Run("emits BenchDownResult with per-resource actions and appliedAt", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		var capturedSelector string
		deps := benchDownDeps{
			identityPath: func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) {
				return &kube.Client{Namespace: "eng-bdc"}, nil
			},
			deleteFn: func(_ context.Context, _ *kube.Client, opts kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error) {
				capturedSelector = opts.LabelSelector
				return []kube.DeleteResult{
					{Kind: "SeiNodeDeployment", Name: "bench-bdc-demo", Namespace: "eng-bdc", Action: "deleted"},
					{Kind: "SeiNodeDeployment", Name: "bench-bdc-demo-rpc", Namespace: "eng-bdc", Action: "deleted"},
					{Kind: "Job", Name: "seiload-bench-bdc-demo", Namespace: "eng-bdc", Action: "deleted"},
					{Kind: "ConfigMap", Name: "seiload-profile-bench-bdc-demo", Namespace: "eng-bdc", Action: "not-found"},
				}, nil
			},
		}
		var buf bytes.Buffer
		err := runBenchDown(context.Background(), benchDownInput{Name: "demo"}, &buf, deps)
		if err != nil {
			t.Fatalf("runBenchDown: %v\nbody=%s", err, buf.String())
		}
		if capturedSelector != "sei.io/engineer=bdc,sei.io/bench-name=demo" {
			t.Errorf("selector: got %q", capturedSelector)
		}
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		if env.Kind != clioutput.KindBenchDownResult {
			t.Errorf("kind: %q", env.Kind)
		}
		var data benchDownResult
		_ = json.Unmarshal(env.Data, &data)
		if data.ChainID != "bench-bdc-demo" || data.Namespace != "eng-bdc" {
			t.Errorf("identity fields: %+v", data)
		}
		if len(data.Resources) != 4 {
			t.Errorf("resources: got %d, want 4", len(data.Resources))
		}
		if data.DeletedAt == nil {
			t.Errorf("deletedAt should be set")
		}
		// Per-action wiring round-trips correctly.
		seen := map[string]int{}
		for _, r := range data.Resources {
			seen[r.Action]++
		}
		if seen["deleted"] != 3 || seen["not-found"] != 1 {
			t.Errorf("action distribution: %+v", seen)
		}
	})

	t.Run("rejects bad name with validation category", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		var buf bytes.Buffer
		err := runBenchDown(context.Background(), benchDownInput{Name: "Bad-Name"}, &buf, benchDownDeps{
			identityPath: func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) {
				t.Fatalf("kube client should not be constructed for invalid name")
				return nil, nil
			},
			deleteFn: nil,
		})
		if err == nil || !strings.Contains(buf.String(), "validation") {
			t.Errorf("expected validation error; got %s", buf.String())
		}
	})

	t.Run("propagates kubeconfig errors with ExitIdentity", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		var buf bytes.Buffer
		err := runBenchDown(context.Background(), benchDownInput{Name: "demo"}, &buf, benchDownDeps{
			identityPath: func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) {
				return nil, clioutput.New(clioutput.ExitIdentity, clioutput.CatKubeconfigParse, "no kubeconfig")
			},
		})
		if err == nil {
			t.Fatalf("expected error")
		}
		ec, ok := err.(interface{ ExitCode() int })
		if !ok || ec.ExitCode() != clioutput.ExitIdentity {
			t.Errorf("exit code: got %v, want %d", err, clioutput.ExitIdentity)
		}
	})

	t.Run("propagates delete errors with finalizer-stuck category", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		var buf bytes.Buffer
		err := runBenchDown(context.Background(), benchDownInput{Name: "demo"}, &buf, benchDownDeps{
			identityPath: func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) {
				return &kube.Client{}, nil
			},
			deleteFn: func(context.Context, *kube.Client, kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error) {
				return nil, clioutput.New(clioutput.ExitBench, clioutput.CatFinalizerStuck, "PVC stuck")
			},
		})
		if err == nil || !strings.Contains(buf.String(), "finalizer-stuck") {
			t.Errorf("expected finalizer-stuck; got %s", buf.String())
		}
	})

	t.Run("missing identity surfaces typed error", func(t *testing.T) {
		var buf bytes.Buffer
		err := runBenchDown(context.Background(), benchDownInput{Name: "demo"}, &buf, benchDownDeps{
			identityPath: func() (string, error) { return "", errors.New("home unset") },
		})
		if err == nil || !strings.Contains(buf.String(), `"category": "missing"`) {
			t.Errorf("expected missing identity; got %s", buf.String())
		}
	})
}
