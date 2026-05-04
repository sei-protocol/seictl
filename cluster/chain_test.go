package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"errors"

	"github.com/sei-protocol/seictl/cluster/internal/aws"
	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

var _ = aws.Caller{} // keep package referenced if other usages drop

const chainTestDigest = "sha256:abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

func stubChainDeps(t *testing.T, alias string) chainDeps {
	t.Helper()
	path := writeConfigFile(t, alias)
	return chainDeps{
		resolveDigest: func(context.Context, string) (string, *clioutput.Error) { return chainTestDigest, nil },
		configPath:    func() (string, error) { return path, nil },
		apply: func(context.Context, *kube.Client, string, string, [][]byte) ([]kube.ApplyResult, *clioutput.Error) {
			t.Fatalf("apply should not be called on dry-run path")
			return nil, nil
		},
		getCaller: okCaller,
	}
}

func TestRunChainUp(t *testing.T) {
	t.Run("dry-run emits ChainUpResult envelope", func(t *testing.T) {
		var buf bytes.Buffer
		err := runChainUpCmd(context.Background(), chainUpInput{
			Image:      "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1.2.3",
			Name:       "qa",
			Validators: 4,
		}, &buf, stubChainDeps(t, "bdc"))
		if err != nil {
			t.Fatalf("runChainUpCmd: %v\nbody=%s", err, buf.String())
		}

		var env clioutput.Envelope
		if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
			t.Fatalf("envelope unmarshal: %v\n%s", err, buf.String())
		}
		if env.Kind != clioutput.KindChainUpResult || env.APIVersion != clioutput.APIVersion {
			t.Errorf("envelope: kind=%q apiVersion=%q", env.Kind, env.APIVersion)
		}
		if env.Error != nil {
			t.Fatalf("error body should be nil; got %+v", env.Error)
		}

		var data chainUpResult
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("data unmarshal: %v", err)
		}
		if data.ChainID != "bench-bdc-qa" {
			t.Errorf("chainId: %q", data.ChainID)
		}
		if data.Namespace != "eng-bdc" {
			t.Errorf("namespace: %q", data.Namespace)
		}
		if data.Validators != 4 {
			t.Errorf("validators: %d", data.Validators)
		}
		if !data.DryRun {
			t.Errorf("dryRun should be true")
		}

		wantTM := "http://bench-bdc-qa-internal.eng-bdc.svc.cluster.local:26657"
		if len(data.Endpoints.TendermintRpc) != 1 || data.Endpoints.TendermintRpc[0] != wantTM {
			t.Errorf("tendermintRpc: got %v, want [%s]", data.Endpoints.TendermintRpc, wantTM)
		}
		if len(data.Endpoints.EvmJsonRpc) != 0 {
			t.Errorf("evmJsonRpc should be empty for chain up (validators don't serve EVM RPC); got %v", data.Endpoints.EvmJsonRpc)
		}
		if bytes.Contains(buf.Bytes(), []byte("prefundedAccounts")) {
			t.Errorf("envelope should omit prefundedAccounts when no --prefund passed: %s", buf.String())
		}

		if len(data.Manifests) != 1 {
			t.Fatalf("expected 1 manifest (validator SND only); got %d", len(data.Manifests))
		}
		m := data.Manifests[0]
		if m.Kind != "SeiNodeDeployment" || m.Name != data.ChainID || m.Namespace != "eng-bdc" {
			t.Errorf("manifest: %+v", m)
		}
		if m.Action != "create" {
			t.Errorf("dry-run action: %q", m.Action)
		}
	})

	t.Run("rejects validators outside [1,21]", func(t *testing.T) {
		for _, n := range []int{0, 22, -1} {
			var buf bytes.Buffer
			err := runChainUpCmd(context.Background(), chainUpInput{
				Image:      "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
				Name:       "qa",
				Validators: n,
			}, &buf, stubChainDeps(t, "bdc"))
			if err == nil || !strings.Contains(buf.String(), "validation") {
				t.Errorf("validators=%d should fail validation; got err=%v body=%s", n, err, buf.String())
			}
		}
	})

	t.Run("rejects non-ECR image", func(t *testing.T) {
		var buf bytes.Buffer
		err := runChainUpCmd(context.Background(), chainUpInput{
			Image:      "docker.io/sei/sei-chain:v1",
			Name:       "qa",
			Validators: 4,
		}, &buf, stubChainDeps(t, "bdc"))
		if err == nil || !strings.Contains(buf.String(), "image-policy") {
			t.Errorf("expected image-policy error; got %s", buf.String())
		}
	})

	t.Run("apply happy path sets appliedAt", func(t *testing.T) {
		path := writeConfigFile(t, "bdc")
		deps := chainDeps{
			resolveDigest: func(context.Context, string) (string, *clioutput.Error) { return chainTestDigest, nil },
			configPath:    func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) {
				return &kube.Client{ContextName: "harbor", Namespace: "eng-bdc"}, nil
			},
			apply: func(_ context.Context, _ *kube.Client, fieldOwner, namespace string, docs [][]byte) ([]kube.ApplyResult, *clioutput.Error) {
				if fieldOwner != benchFieldOwner {
					t.Errorf("field owner: got %q, want %q", fieldOwner, benchFieldOwner)
				}
				if namespace != "eng-bdc" {
					t.Errorf("namespace: %q", namespace)
				}
				if len(docs) != 1 {
					t.Errorf("expected 1 doc (validator SND); got %d", len(docs))
				}
				out := make([]kube.ApplyResult, len(docs))
				for i := range docs {
					out[i] = kube.ApplyResult{Kind: "SeiNodeDeployment", Name: "stub", Namespace: "eng-bdc", Action: "create"}
				}
				return out, nil
			},
			getCaller: okCaller,
		}
		var buf bytes.Buffer
		err := runChainUpCmd(context.Background(), chainUpInput{
			Image:      "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:       "qa",
			Validators: 4,
			Apply:      true,
		}, &buf, deps)
		if err != nil {
			t.Fatalf("runChainUpCmd: %v\n%s", err, buf.String())
		}
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data chainUpResult
		_ = json.Unmarshal(env.Data, &data)
		if data.DryRun {
			t.Errorf("dryRun should be false on --apply")
		}
		if data.AppliedAt == nil {
			t.Errorf("appliedAt should be set on --apply")
		}
	})

	t.Run("missing identity surfaces typed error", func(t *testing.T) {
		deps := chainDeps{
			resolveDigest: func(context.Context, string) (string, *clioutput.Error) { return chainTestDigest, nil },
			configPath:    func() (string, error) { return "", errors.New("home unset") },
		}
		var buf bytes.Buffer
		err := runChainUpCmd(context.Background(), chainUpInput{
			Image:      "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:       "qa",
			Validators: 4,
		}, &buf, deps)
		if err == nil {
			t.Fatalf("expected error")
		}
		if !strings.Contains(buf.String(), `"category": "missing"`) {
			t.Errorf("expected missing identity error; got %s", buf.String())
		}
	})

	t.Run("propagates digest-resolution errors", func(t *testing.T) {
		path := writeConfigFile(t, "bdc")
		deps := chainDeps{
			resolveDigest: func(context.Context, string) (string, *clioutput.Error) {
				return "", clioutput.New(clioutput.ExitBench, clioutput.CatImageResolution, "ecr unavailable")
			},
			configPath: func() (string, error) { return path, nil },
			getCaller:  okCaller,
		}
		var buf bytes.Buffer
		err := runChainUpCmd(context.Background(), chainUpInput{
			Image:      "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:       "qa",
			Validators: 4,
		}, &buf, deps)
		if err == nil || !strings.Contains(buf.String(), "image-resolution") {
			t.Errorf("expected image-resolution error; got %s", buf.String())
		}
	})

	t.Run("rendered manifest has chain component label", func(t *testing.T) {
		var buf bytes.Buffer
		_ = runChainUpCmd(context.Background(), chainUpInput{
			Image:      "189176372795.dkr.ecr.us-east-2.amazonaws.com/sei/sei-chain:v1",
			Name:       "qa",
			Validators: 4,
		}, &buf, stubChainDeps(t, "bdc"))

		docs, _, err := renderChainManifests("bdc", "qa", "eng-bdc", "bench-bdc-qa", "img@sha256:0", "0123456789ab", 4, nil)
		if err != nil {
			t.Fatalf("render: %v", err)
		}
		if len(docs) != 1 {
			t.Fatalf("expected 1 doc; got %d", len(docs))
		}
		body := string(docs[0])
		for _, want := range []string{
			"app.kubernetes.io/component: chain",
			"sei.io/role: validator",
			"sei.io/chain-id: bench-bdc-qa",
			"sei.io/engineer: bdc",
			"sei.io/bench-name: qa",
		} {
			if !strings.Contains(body, want) {
				t.Errorf("rendered doc missing label %q\n--- doc ---\n%s", want, body)
			}
		}

		// rpc.yaml's peer selector targets sei.io/role=validator on
		// pod labels; this template MUST keep that label on the pod
		// template metadata or RPC peering silently breaks.
		podSection := body[strings.Index(body, "  template:"):]
		if !strings.Contains(podSection, "sei.io/role: validator") {
			t.Errorf("validator pod template must carry sei.io/role: validator (load-bearing for rpc peer discovery)\n%s", podSection)
		}
	})
}
