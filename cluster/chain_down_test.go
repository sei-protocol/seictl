package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

func stubChainDownDeps(t *testing.T, alias string) chainDownDeps {
	t.Helper()
	path := writeEngineerFile(t, alias)
	return chainDownDeps{
		identityPath:  func() (string, error) { return path, nil },
		newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) { return &kube.Client{}, nil },
		deleteFn: func(context.Context, *kube.Client, kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error) {
			t.Fatalf("deleteFn should not be called on dry-run path")
			return nil, nil
		},
		dryRunListFn: func(context.Context, *kube.Client, kube.ListOptions) ([]kube.DeleteResult, *clioutput.Error) {
			return nil, nil
		},
	}
}

func TestRunChainDown(t *testing.T) {
	t.Run("dry-run with no matches surfaces hint", func(t *testing.T) {
		deps := stubChainDownDeps(t, "bdc")
		var buf bytes.Buffer
		err := runChainDown(context.Background(), chainDownInput{Name: "qa", DryRun: true}, &buf, deps)
		if err != nil {
			t.Fatalf("runChainDown: %v", err)
		}
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		if env.Kind != clioutput.KindChainDownResult {
			t.Errorf("kind: %q", env.Kind)
		}
		var data chainDownResult
		_ = json.Unmarshal(env.Data, &data)
		if data.ChainID != "bench-bdc-qa" {
			t.Errorf("chainId: %q", data.ChainID)
		}
		if !data.DryRun {
			t.Errorf("dryRun should be true")
		}
		if !strings.Contains(data.Hint, "no resources match") {
			t.Errorf("expected hint about no matches; got %q", data.Hint)
		}
	})

	t.Run("dry-run uses chain-id selector", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		var capturedSelector string
		deps := chainDownDeps{
			identityPath:  func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) { return &kube.Client{}, nil },
			dryRunListFn: func(_ context.Context, _ *kube.Client, opts kube.ListOptions) ([]kube.DeleteResult, *clioutput.Error) {
				capturedSelector = opts.LabelSelector
				return []kube.DeleteResult{
					{Kind: "SeiNodeDeployment", Name: "bench-bdc-qa", Namespace: "eng-bdc", Action: "would-delete"},
				}, nil
			},
		}
		var buf bytes.Buffer
		_ = runChainDown(context.Background(), chainDownInput{Name: "qa", DryRun: true}, &buf, deps)
		want := "sei.io/engineer=bdc,sei.io/chain-id=bench-bdc-qa"
		if capturedSelector != want {
			t.Errorf("selector: got %q, want %q", capturedSelector, want)
		}
	})

	t.Run("delete sets DeletedAt when nothing is terminating", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		deps := chainDownDeps{
			identityPath:  func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) { return &kube.Client{}, nil },
			deleteFn: func(context.Context, *kube.Client, kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error) {
				return []kube.DeleteResult{
					{Kind: "SeiNodeDeployment", Name: "bench-bdc-qa", Namespace: "eng-bdc", Action: "deleted"},
				}, nil
			},
		}
		var buf bytes.Buffer
		err := runChainDown(context.Background(), chainDownInput{Name: "qa"}, &buf, deps)
		if err != nil {
			t.Fatalf("runChainDown: %v", err)
		}
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data chainDownResult
		_ = json.Unmarshal(env.Data, &data)
		if data.DeletedAt == nil {
			t.Errorf("DeletedAt should be set")
		}
		if data.Hint != "" {
			t.Errorf("Hint should be empty when nothing terminating; got %q", data.Hint)
		}
	})

	t.Run("delete reports still-terminating in hint", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		deps := chainDownDeps{
			identityPath:  func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) { return &kube.Client{}, nil },
			deleteFn: func(context.Context, *kube.Client, kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error) {
				return []kube.DeleteResult{
					{Kind: "SeiNodeDeployment", Name: "bench-bdc-qa", Namespace: "eng-bdc", Action: "still-terminating"},
				}, nil
			},
		}
		var buf bytes.Buffer
		_ = runChainDown(context.Background(), chainDownInput{Name: "qa"}, &buf, deps)
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data chainDownResult
		_ = json.Unmarshal(env.Data, &data)
		if data.DeletedAt != nil {
			t.Errorf("DeletedAt should be nil when something still terminating")
		}
		if !strings.Contains(data.Hint, "still terminating") {
			t.Errorf("expected still-terminating hint; got %q", data.Hint)
		}
	})

	t.Run("rejects bad name", func(t *testing.T) {
		deps := stubChainDownDeps(t, "bdc")
		var buf bytes.Buffer
		err := runChainDown(context.Background(), chainDownInput{Name: "Bad-Name"}, &buf, deps)
		if err == nil {
			t.Fatalf("expected error")
		}
	})
}
