package bench

import (
	"strings"
	"testing"

	"github.com/sei-protocol/sei-k8s-controller/harness/bench"
	"sigs.k8s.io/yaml"
)

func TestRender(t *testing.T) {
	base := bench.Params{RunID: "exp-42", ChainID: "bench-a", Image: "seiload@sha256:abc", DurationMinutes: 10, ProfileCM: "seiload-profile-exp-42"}
	cases := []struct {
		name    string
		mutate  func(*bench.Params)
		wantErr string
		check   func(t *testing.T, job map[string]any)
	}{
		{name: "defaults", mutate: func(*bench.Params) {},
			check: func(t *testing.T, job map[string]any) {
				meta := job["metadata"].(map[string]any)
				if meta["name"] != "seiload-exp-42" {
					t.Errorf("name = %v", meta["name"])
				}
				if _, has := meta["namespace"]; has {
					t.Errorf("namespace should be omitted when empty: %v", meta["namespace"])
				}
				if got := meta["labels"].(map[string]any)["sei.io/harness-run"]; got != "exp-42" {
					t.Errorf("harness-run label = %v", got)
				}
				spec := job["spec"].(map[string]any)
				if got := spec["activeDeadlineSeconds"]; got != float64((10+15)*60) {
					t.Errorf("activeDeadlineSeconds = %v, want duration+15m", got)
				}
			}},
		{name: "namespace and deadline override", mutate: func(p *bench.Params) { p.Namespace = "eng-x"; p.DeadlineSeconds = 99 },
			check: func(t *testing.T, job map[string]any) {
				if got := job["metadata"].(map[string]any)["namespace"]; got != "eng-x" {
					t.Errorf("namespace = %v", got)
				}
				if got := job["spec"].(map[string]any)["activeDeadlineSeconds"]; got != float64(99) {
					t.Errorf("activeDeadlineSeconds = %v", got)
				}
			}},
		{name: "zero duration", mutate: func(p *bench.Params) { p.DurationMinutes = 0 }, wantErr: "positive number of minutes"},
		{name: "run-id must be a DNS-1123 label", mutate: func(p *bench.Params) { p.RunID = "Exp_42" }, wantErr: "--run-id"},
		{name: "chain-id must be a DNS-1123 label", mutate: func(p *bench.Params) { p.ChainID = "bench.a" }, wantErr: "--chain-id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mutate(&p)
			out, err := render(p)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var job map[string]any
			if err := yaml.Unmarshal(out, &job); err != nil {
				t.Fatalf("output is not YAML: %v\n%s", err, out)
			}
			if job["kind"] != "Job" {
				t.Fatalf("kind = %v", job["kind"])
			}
			tc.check(t, job)
		})
	}
}
