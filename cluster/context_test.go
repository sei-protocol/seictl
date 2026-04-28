package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sei-protocol/seictl/cluster/internal/aws"
	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
)

const fixtureKubeconfig = `apiVersion: v1
kind: Config
current-context: harbor
contexts:
- name: harbor
  context:
    cluster: harbor-eks
    user: harbor-sso
    namespace: eng-bdc
clusters:
- name: harbor-eks
  cluster:
    server: https://harbor.example.com
users:
- name: harbor-sso
  user:
    token: fake
`

func writeKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kubeconfig.yaml")
	if err := os.WriteFile(path, []byte(fixtureKubeconfig), 0o600); err != nil {
		t.Fatalf("seed kubeconfig: %v", err)
	}
	return path
}

func TestRunContext(t *testing.T) {
	t.Run("renders populated envelope with stub deps", func(t *testing.T) {
		path := writeKubeconfig(t)

		stub := contextDeps{
			getCaller: func(context.Context) (*aws.Caller, *clioutput.Error) {
				return &aws.Caller{
					Account:      "189176372795",
					Region:       "eu-central-1",
					PrincipalARN: "arn:aws:sts::189176372795:assumed-role/eng/bdc",
				}, nil
			},
			identityPath: func() (string, error) {
				return "", errors.New("no identity in test")
			},
		}

		var buf bytes.Buffer
		if err := runContext(context.Background(), path, "", &buf, stub); err != nil {
			t.Fatalf("runContext: %v", err)
		}

		var env clioutput.Envelope
		if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Kind != "context.result" || env.Version != "v1" {
			t.Errorf("envelope: kind=%q version=%q", env.Kind, env.Version)
		}
		var data contextResult
		if err := json.Unmarshal(env.Data, &data); err != nil {
			t.Fatalf("data unmarshal: %v", err)
		}
		if data.KubeContext != "harbor" || data.Namespace != "eng-bdc" {
			t.Errorf("kube fields: %+v", data)
		}
		if data.AWSAccount != "189176372795" || data.AWSRegion != "eu-central-1" {
			t.Errorf("aws fields: %+v", data)
		}
		if data.Engineer != nil {
			t.Errorf("engineer should be nil when identityPath unreachable, got %+v", data.Engineer)
		}
	})

	t.Run("aws failure leaves aws fields empty", func(t *testing.T) {
		path := writeKubeconfig(t)

		stub := contextDeps{
			getCaller: func(context.Context) (*aws.Caller, *clioutput.Error) {
				return nil, clioutput.New(clioutput.ExitIdentity, clioutput.CatAWSUnavailable, "expired SSO")
			},
			identityPath: func() (string, error) {
				return "", errors.New("skip")
			},
		}

		var buf bytes.Buffer
		if err := runContext(context.Background(), path, "", &buf, stub); err != nil {
			t.Fatalf("runContext should not fail on AWS error: %v", err)
		}
		var env clioutput.Envelope
		if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		var data contextResult
		_ = json.Unmarshal(env.Data, &data)
		if data.AWSAccount != "" || data.AWSPrincipalARN != "" {
			t.Errorf("aws fields should be empty: %+v", data)
		}
	})

	t.Run("bad kubeconfig emits error envelope and ExitCoder", func(t *testing.T) {
		stub := contextDeps{
			getCaller: func(context.Context) (*aws.Caller, *clioutput.Error) {
				return nil, clioutput.New(clioutput.ExitIdentity, clioutput.CatAWSUnavailable, "skip")
			},
			identityPath: func() (string, error) { return "", errors.New("skip") },
		}

		var buf bytes.Buffer
		err := runContext(context.Background(), "/nonexistent/path", "", &buf, stub)
		if err == nil {
			t.Fatalf("expected exit-coder error")
		}
		// The error must be a cli.ExitCoder so main() can OS-exit with the right code.
		type exitCoder interface{ ExitCode() int }
		ec, ok := err.(exitCoder)
		if !ok {
			t.Fatalf("error must implement ExitCode(); got %T", err)
		}
		if ec.ExitCode() != clioutput.ExitIdentity {
			t.Errorf("exit code: got %d, want %d", ec.ExitCode(), clioutput.ExitIdentity)
		}
		var env clioutput.Envelope
		if err := json.Unmarshal(buf.Bytes(), &env); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if env.Error == nil || env.Error.Category != clioutput.CatKubeconfigParse {
			t.Errorf("expected kubeconfig-parse error envelope; got %+v", env.Error)
		}
	})
}
