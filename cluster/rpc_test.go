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

const rpcTestDigest = "sha256:fedcba0987654321fedcba0987654321fedcba0987654321fedcba0987654321"

func stubRPCDeps(t *testing.T, alias string) rpcDeps {
	t.Helper()
	path := writeEngineerFile(t, alias)
	return rpcDeps{
		resolveDigest: func(context.Context, string) (string, *clioutput.Error) { return rpcTestDigest, nil },
		identityPath:  func() (string, error) { return path, nil },
		apply: func(context.Context, *kube.Client, string, string, [][]byte) ([]kube.ApplyResult, *clioutput.Error) {
			t.Fatalf("apply should not be called on dry-run path")
			return nil, nil
		},
		getCaller: okCaller,
	}
}

func TestRunRPCUp(t *testing.T) {
	t.Run("dry-run emits RPCUpResult envelope with both endpoint types", func(t *testing.T) {
		var buf bytes.Buffer
		err := runRPCUpCmd(context.Background(), rpcUpInput{
			ChainID:  "bench-bdc-qa",
			Name:     "default",
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1.2.3",
			Replicas: 2,
		}, &buf, stubRPCDeps(t, "bdc"))
		if err != nil {
			t.Fatalf("runRPCUpCmd: %v\n%s", err, buf.String())
		}

		var env clioutput.Envelope
		if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
			t.Fatalf("envelope unmarshal: %v\n%s", err, buf.String())
		}
		if env.Kind != clioutput.KindRPCUpResult || env.APIVersion != clioutput.APIVersion {
			t.Errorf("envelope: kind=%q apiVersion=%q", env.Kind, env.APIVersion)
		}
		if env.Error != nil {
			t.Fatalf("error body: %+v", env.Error)
		}

		var data rpcUpResult
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("data unmarshal: %v", err)
		}
		if data.ChainID != "bench-bdc-qa" || data.Name != "default" || data.Replicas != 2 {
			t.Errorf("envelope fields: %+v", data)
		}
		if !data.DryRun {
			t.Errorf("dryRun should be true")
		}

		wantTM := "http://bench-bdc-qa-rpc-default-internal.eng-bdc.svc.cluster.local:26657"
		wantEVM := "http://bench-bdc-qa-rpc-default-internal.eng-bdc.svc.cluster.local:8545"
		if len(data.Endpoints.TendermintRpc) != 1 || data.Endpoints.TendermintRpc[0] != wantTM {
			t.Errorf("tendermintRpc: got %v, want [%s]", data.Endpoints.TendermintRpc, wantTM)
		}
		if len(data.Endpoints.EvmJsonRpc) != 1 || data.Endpoints.EvmJsonRpc[0] != wantEVM {
			t.Errorf("evmJsonRpc: got %v, want [%s]", data.Endpoints.EvmJsonRpc, wantEVM)
		}

		if len(data.Manifests) != 1 {
			t.Fatalf("expected 1 manifest; got %d", len(data.Manifests))
		}
		m := data.Manifests[0]
		if m.Kind != "SeiNodeDeployment" || m.Name != "bench-bdc-qa-rpc-default" {
			t.Errorf("manifest: %+v", m)
		}
	})

	t.Run("multi-fleet naming — different --name yields different SND name", func(t *testing.T) {
		for _, fleetName := range []string{"qa-test", "shadow", "long"} {
			var buf bytes.Buffer
			err := runRPCUpCmd(context.Background(), rpcUpInput{
				ChainID:  "bench-bdc-foo",
				Name:     fleetName,
				Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
				Replicas: 2,
			}, &buf, stubRPCDeps(t, "bdc"))
			if err != nil {
				t.Fatalf("runRPCUpCmd(%s): %v", fleetName, err)
			}
			var env clioutput.Envelope
			_ = json.Unmarshal(buf.Bytes(), &env)
			var data rpcUpResult
			_ = json.Unmarshal(env.Data, &data)
			wantName := "bench-bdc-foo-rpc-" + fleetName
			if data.Manifests[0].Name != wantName {
				t.Errorf("multi-fleet name: got %q, want %q", data.Manifests[0].Name, wantName)
			}
		}
	})

	t.Run("rejects empty chain-id", func(t *testing.T) {
		var buf bytes.Buffer
		err := runRPCUpCmd(context.Background(), rpcUpInput{
			ChainID:  "",
			Name:     "default",
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Replicas: 2,
		}, &buf, stubRPCDeps(t, "bdc"))
		if err == nil || !strings.Contains(buf.String(), "validation") {
			t.Errorf("expected validation error; got %s", buf.String())
		}
	})

	t.Run("rejects replicas outside [1,21]", func(t *testing.T) {
		for _, n := range []int{0, 22, -1} {
			var buf bytes.Buffer
			err := runRPCUpCmd(context.Background(), rpcUpInput{
				ChainID:  "bench-bdc-qa",
				Name:     "default",
				Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
				Replicas: n,
			}, &buf, stubRPCDeps(t, "bdc"))
			if err == nil || !strings.Contains(buf.String(), "validation") {
				t.Errorf("replicas=%d should fail validation; got %s", n, buf.String())
			}
		}
	})

	t.Run("rejects non-ECR image", func(t *testing.T) {
		var buf bytes.Buffer
		err := runRPCUpCmd(context.Background(), rpcUpInput{
			ChainID:  "bench-bdc-qa",
			Name:     "default",
			Image:    "docker.io/sei/sei-chain:v1",
			Replicas: 2,
		}, &buf, stubRPCDeps(t, "bdc"))
		if err == nil || !strings.Contains(buf.String(), "image-policy") {
			t.Errorf("expected image-policy error; got %s", buf.String())
		}
	})

	t.Run("apply happy path sets appliedAt", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		deps := rpcDeps{
			resolveDigest: func(context.Context, string) (string, *clioutput.Error) { return rpcTestDigest, nil },
			identityPath:  func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) {
				return &kube.Client{ContextName: "harbor", Namespace: "eng-bdc"}, nil
			},
			apply: func(_ context.Context, _ *kube.Client, fieldOwner, namespace string, docs [][]byte) ([]kube.ApplyResult, *clioutput.Error) {
				if fieldOwner != benchFieldOwner {
					t.Errorf("field owner: got %q", fieldOwner)
				}
				if len(docs) != 1 {
					t.Errorf("expected 1 doc; got %d", len(docs))
				}
				return []kube.ApplyResult{{Kind: "SeiNodeDeployment", Name: "stub", Namespace: "eng-bdc", Action: "create"}}, nil
			},
			getCaller: okCaller,
		}
		var buf bytes.Buffer
		err := runRPCUpCmd(context.Background(), rpcUpInput{
			ChainID:  "bench-bdc-qa",
			Name:     "default",
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Replicas: 2,
			Apply:    true,
		}, &buf, deps)
		if err != nil {
			t.Fatalf("runRPCUpCmd: %v\n%s", err, buf.String())
		}
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data rpcUpResult
		_ = json.Unmarshal(env.Data, &data)
		if data.AppliedAt == nil {
			t.Errorf("appliedAt should be set on --apply")
		}
	})

	t.Run("missing identity surfaces typed error", func(t *testing.T) {
		deps := rpcDeps{
			resolveDigest: func(context.Context, string) (string, *clioutput.Error) { return rpcTestDigest, nil },
			identityPath:  func() (string, error) { return "", errors.New("home unset") },
		}
		var buf bytes.Buffer
		err := runRPCUpCmd(context.Background(), rpcUpInput{
			ChainID:  "bench-bdc-qa",
			Name:     "default",
			Image:    "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Replicas: 2,
		}, &buf, deps)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !strings.Contains(buf.String(), `"category": "missing"`) {
			t.Errorf("expected missing identity error; got %s", buf.String())
		}
	})

	t.Run("rendered manifest has rpc component label and peer selector", func(t *testing.T) {
		docs, _, err := renderRPCManifests("bdc", "bench-bdc-qa", "default", "eng-bdc", "img@sha256:0", "0123456789ab", 2)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if len(docs) != 1 {
			t.Fatalf("expected 1 doc; got %d", len(docs))
		}
		body := string(docs[0])
		for _, want := range []string{
			"app.kubernetes.io/component: rpc",
			"app.kubernetes.io/part-of: seictl",
			"sei.io/role: rpc",
			"sei.io/chain-id: bench-bdc-qa",
			"sei.io/rpc-name: default",
			"sei.io/engineer: bdc",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("rendered doc missing label %q", want)
			}
		}
		for _, want := range []string{
			"sei.io/chain-id: bench-bdc-qa",
			"sei.io/role: validator",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("rendered doc missing peer selector %q", want)
			}
		}
	})
}
