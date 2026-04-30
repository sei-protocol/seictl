package cluster

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sei-protocol/seictl/cluster/internal/clioutput"
	"github.com/sei-protocol/seictl/cluster/internal/kube"
)

func newSND(t *testing.T, name, role, chainID, ns string, replicas, ready int, phase, imageSHA string) unstructured.Unstructured {
	t.Helper()
	u := unstructured.Unstructured{}
	u.SetAPIVersion("sei.io/v1alpha1")
	u.SetKind("SeiNodeDeployment")
	u.SetName(name)
	u.SetNamespace(ns)
	u.SetLabels(map[string]string{
		"sei.io/chain-id":              chainID,
		"sei.io/role":                  role,
		"sei.io/engineer":              "bdc",
		"sei.io/bench-name":            strings.TrimPrefix(strings.TrimSuffix(chainID, "-rpc"), "bench-bdc-"),
		"app.kubernetes.io/part-of":    "seictl-bench",
		"app.kubernetes.io/managed-by": "seictl",
	})
	u.SetAnnotations(map[string]string{
		"sei.io/image-sha": imageSHA,
	})
	u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-3 * time.Minute)))
	_ = unstructured.SetNestedField(u.Object, int64(replicas), "spec", "replicas")
	_ = unstructured.SetNestedField(u.Object, int64(ready), "status", "readyReplicas")
	if phase != "" {
		_ = unstructured.SetNestedField(u.Object, phase, "status", "phase")
	}
	return u
}

func newJob(t *testing.T, name, chainID, ns string, active int, condComplete bool) unstructured.Unstructured {
	t.Helper()
	u := unstructured.Unstructured{}
	u.SetAPIVersion("batch/v1")
	u.SetKind("Job")
	u.SetName(name)
	u.SetNamespace(ns)
	u.SetLabels(map[string]string{
		"sei.io/chain-id":              chainID,
		"sei.io/engineer":              "bdc",
		"sei.io/bench-name":            strings.TrimPrefix(chainID, "bench-bdc-"),
		"app.kubernetes.io/part-of":    "seictl-bench",
		"app.kubernetes.io/managed-by": "seictl",
	})
	u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-3 * time.Minute)))
	_ = unstructured.SetNestedField(u.Object, int64(active), "status", "active")
	if condComplete {
		_ = unstructured.SetNestedSlice(u.Object, []any{
			map[string]any{"type": "Complete", "status": "True"},
		}, "status", "conditions")
	}
	return u
}

func TestRunBenchList(t *testing.T) {
	t.Run("aggregates SNDs and Job into one BenchSummary per chain-id", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		snds := []unstructured.Unstructured{
			newSND(t, "bench-bdc-demo", "validator", "bench-bdc-demo", "eng-bdc", 4, 3, "Running", "abc123"),
			newSND(t, "bench-bdc-demo-rpc", "rpc", "bench-bdc-demo", "eng-bdc", 1, 1, "", "abc123"),
		}
		jobs := []unstructured.Unstructured{
			newJob(t, "seiload-bench-bdc-demo", "bench-bdc-demo", "eng-bdc", 1, false),
		}

		var listCalls int
		deps := benchListDeps{
			identityPath: func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) {
				return &kube.Client{Namespace: "eng-bdc"}, nil
			},
			listFn: func(_ context.Context, _ *kube.Client, opts kube.ListOptions) ([]unstructured.Unstructured, *clioutput.Error) {
				listCalls++
				if !strings.Contains(opts.LabelSelector, "sei.io/engineer=bdc") {
					t.Errorf("missing engineer scope in selector: %q", opts.LabelSelector)
				}
				switch opts.Resources[0] {
				case "seinodedeployments.sei.io":
					return snds, nil
				case "jobs.batch":
					return jobs, nil
				}
				return nil, nil
			},
		}

		var buf bytes.Buffer
		err := runBenchList(context.Background(), benchListInput{}, &buf, deps)
		if err != nil {
			t.Fatalf("runBenchList: %v\nbody=%s", err, buf.String())
		}
		if listCalls != 2 {
			t.Errorf("listFn calls: got %d, want 2 (SNDs + Jobs)", listCalls)
		}

		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data benchListResult
		_ = json.Unmarshal(env.Data, &data)
		if len(data.Items) != 1 {
			t.Fatalf("items: got %d, want 1", len(data.Items))
		}
		s := data.Items[0]
		if s.ChainID != "bench-bdc-demo" || s.Name != "demo" || s.Namespace != "eng-bdc" || s.Owner != "bdc" {
			t.Errorf("identity fields: %+v", s)
		}
		if s.Phase != "Running" {
			t.Errorf("phase: %q", s.Phase)
		}
		if s.ValidatorsDesired != 4 || s.ValidatorsReady != 3 {
			t.Errorf("validator counts: %d/%d", s.ValidatorsReady, s.ValidatorsDesired)
		}
		if s.RPCDesired != 1 || s.RPCReady != 1 {
			t.Errorf("rpc counts: %d/%d", s.RPCReady, s.RPCDesired)
		}
		if s.LoadJobPhase != "Running" {
			t.Errorf("load job phase: %q", s.LoadJobPhase)
		}
		if s.ImageDigest != "abc123" {
			t.Errorf("image digest: %q", s.ImageDigest)
		}
		if s.AgeSeconds < 100 || s.AgeSeconds > 300 {
			t.Errorf("age (~3min): %d", s.AgeSeconds)
		}
	})

	t.Run("empty cluster returns empty items", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		var buf bytes.Buffer
		err := runBenchList(context.Background(), benchListInput{}, &buf, benchListDeps{
			identityPath: func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) {
				return &kube.Client{}, nil
			},
			listFn: func(context.Context, *kube.Client, kube.ListOptions) ([]unstructured.Unstructured, *clioutput.Error) {
				return nil, nil
			},
		})
		if err != nil {
			t.Fatalf("runBenchList: %v", err)
		}
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data benchListResult
		_ = json.Unmarshal(env.Data, &data)
		if len(data.Items) != 0 {
			t.Errorf("expected empty items; got %+v", data.Items)
		}
	})

	t.Run("completed job surfaces Succeeded phase", func(t *testing.T) {
		path := writeEngineerFile(t, "bdc")
		jobs := []unstructured.Unstructured{
			newJob(t, "seiload-bench-bdc-demo", "bench-bdc-demo", "eng-bdc", 0, true),
		}
		deps := benchListDeps{
			identityPath:  func() (string, error) { return path, nil },
			newKubeClient: func(kube.Options) (*kube.Client, *clioutput.Error) { return &kube.Client{}, nil },
			listFn: func(_ context.Context, _ *kube.Client, opts kube.ListOptions) ([]unstructured.Unstructured, *clioutput.Error) {
				if opts.Resources[0] == "jobs.batch" {
					return jobs, nil
				}
				return nil, nil
			},
		}
		var buf bytes.Buffer
		_ = runBenchList(context.Background(), benchListInput{}, &buf, deps)
		var env clioutput.Envelope
		_ = json.Unmarshal(buf.Bytes(), &env)
		var data benchListResult
		_ = json.Unmarshal(env.Data, &data)
		if len(data.Items) != 1 || data.Items[0].LoadJobPhase != "Succeeded" {
			t.Errorf("expected Succeeded; got %+v", data.Items)
		}
	})
}
