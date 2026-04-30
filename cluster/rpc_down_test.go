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

func TestRunRPCDown(t *testing.T) {
	t.Run("dry-run uses chain-id + rpc-name + component=rpc selector", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		var capturedSelector string
		deps := rpcDownDeps{
			identityPath:  func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) { return &kube.Client{}, nil },
			dryRunListFn: func(_ context.Context, _ *kube.Client, opts kube.ListOptions) ([]kube.DeleteResult, *clioutput.Error) {
				capturedSelector = opts.LabelSelector
				return []kube.DeleteResult{
					{Kind: "SeiNodeDeployment", Name: "bench-bdc-qa-rpc-primary", Namespace: "eng-bdc", Action: "would-delete"},
				}, nil
			},
		}
		var buf bytes.Buffer
		_ = runRPCDown(context.Background(), rpcDownInput{ChainID: "bench-bdc-qa", Name: "primary", DryRun: true}, &buf, deps)
		want := "sei.io/engineer=bdc,sei.io/chain-id=bench-bdc-qa,sei.io/rpc-name=primary,app.kubernetes.io/component=rpc"
		if capturedSelector != want {
			t.Errorf("selector: got %q\nwant %q", capturedSelector, want)
		}
	})

	t.Run("dry-run with no matches surfaces hint", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		deps := rpcDownDeps{
			identityPath:  func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) { return &kube.Client{}, nil },
			dryRunListFn: func(context.Context, *kube.Client, kube.ListOptions) ([]kube.DeleteResult, *clioutput.Error) {
				return nil, nil
			},
		}
		var buf bytes.Buffer
		err := runRPCDown(context.Background(), rpcDownInput{ChainID: "bench-bdc-qa", Name: "primary", DryRun: true}, &buf, deps)
		if err != nil {
			t.Fatalf("runRPCDown: %v", err)
		}
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data rpcDownResult
		_ = json.Unmarshal(env.Data, &data)
		if !strings.Contains(data.Hint, "no resources match") {
			t.Errorf("expected hint about no matches; got %q", data.Hint)
		}
	})

	t.Run("delete sets DeletedAt when nothing is terminating", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		deps := rpcDownDeps{
			identityPath:  func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) { return &kube.Client{}, nil },
			deleteFn: func(context.Context, *kube.Client, kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error) {
				return []kube.DeleteResult{
					{Kind: "SeiNodeDeployment", Name: "bench-bdc-qa-rpc-primary", Namespace: "eng-bdc", Action: "deleted"},
				}, nil
			},
		}
		var buf bytes.Buffer
		err := runRPCDown(context.Background(), rpcDownInput{ChainID: "bench-bdc-qa", Name: "primary"}, &buf, deps)
		if err != nil {
			t.Fatalf("runRPCDown: %v", err)
		}
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data rpcDownResult
		_ = json.Unmarshal(env.Data, &data)
		if data.DeletedAt == nil {
			t.Errorf("DeletedAt should be set")
		}
	})

	t.Run("delete reports still-terminating in hint", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		deps := rpcDownDeps{
			identityPath:  func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) { return &kube.Client{}, nil },
			deleteFn: func(context.Context, *kube.Client, kube.DeleteOptions) ([]kube.DeleteResult, *clioutput.Error) {
				return []kube.DeleteResult{
					{Kind: "SeiNodeDeployment", Name: "bench-bdc-qa-rpc-primary", Namespace: "eng-bdc", Action: "still-terminating"},
				}, nil
			},
		}
		var buf bytes.Buffer
		_ = runRPCDown(context.Background(), rpcDownInput{ChainID: "bench-bdc-qa", Name: "primary"}, &buf, deps)
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data rpcDownResult
		_ = json.Unmarshal(env.Data, &data)
		if data.DeletedAt != nil {
			t.Errorf("DeletedAt should be nil when something still terminating")
		}
		if !strings.Contains(data.Hint, "still terminating") {
			t.Errorf("expected still-terminating hint; got %q", data.Hint)
		}
	})

	t.Run("rejects empty chain-id", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		deps := rpcDownDeps{
			identityPath:  func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) { return &kube.Client{}, nil },
		}
		var buf bytes.Buffer
		err := runRPCDown(context.Background(), rpcDownInput{ChainID: "", Name: "primary"}, &buf, deps)
		if err == nil {
			t.Fatalf("expected error")
		}
	})
}
