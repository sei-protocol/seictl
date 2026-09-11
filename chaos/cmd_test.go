package chaos

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sei-protocol/sei-k8s-controller/harness/faults"
)

func TestRender(t *testing.T) {
	base := faults.Params{ChainID: "bench-a", RunID: "r1", Namespace: "eng-x"}
	cases := []struct {
		name     string
		fault    string
		duration string
		runID    string
		chainID  string
		wantErr  string
		wantSubs []string
	}{
		{name: "duration fault", fault: "network-partition", duration: "5m",
			wantSubs: []string{"kind: NetworkChaos", "name: network-partition-r1", "namespace: eng-x", `duration: "5m"`, `["bench-a-0"]`}},
		{name: "one-shot fault", fault: "pod-failure",
			wantSubs: []string{"kind: PodChaos", "sei.io/harness-run: \"r1\""}},
		{name: "one-shot rejects duration", fault: "pod-failure", duration: "5m", wantErr: "one-shot"},
		{name: "duration fault needs duration", fault: "cpu-stress", wantErr: "needs --duration"},
		{name: "unitless duration is refused", fault: "cpu-stress", duration: "10", wantErr: "missing unit"},
		{name: "non-positive duration is refused", fault: "cpu-stress", duration: "-5m", wantErr: "must be positive"},
		{name: "unknown fault", fault: "nope", duration: "5m", wantErr: "nope"},
		{name: "missing fault lists catalog", wantErr: strings.Join(faults.Names(), ", ")},
		{name: "run-id must be a DNS-1123 label", fault: "cpu-stress", duration: "5m", runID: "Run_1", wantErr: "--run-id"},
		{name: "chain-id must be a DNS-1123 label", fault: "cpu-stress", duration: "5m", chainID: "bench.a", wantErr: "--chain-id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.Duration = tc.duration
			if tc.runID != "" {
				p.RunID = tc.runID
			}
			if tc.chainID != "" {
				p.ChainID = tc.chainID
			}
			out, err := Render(tc.fault, p)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("want error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range tc.wantSubs {
				if !strings.Contains(string(out), s) {
					t.Errorf("output missing %q:\n%s", s, out)
				}
			}
		})
	}
}

func TestList(t *testing.T) {
	var text bytes.Buffer
	if err := list(&text, "text"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(text.String()), "\n")
	if len(lines) != len(faults.Catalog) {
		t.Fatalf("text: %d lines, want %d", len(lines), len(faults.Catalog))
	}
	if !strings.Contains(text.String(), "pod-failure") || !strings.Contains(text.String(), "one-shot") {
		t.Errorf("text listing lacks one-shot marker:\n%s", text.String())
	}

	var js bytes.Buffer
	if err := list(&js, "json"); err != nil {
		t.Fatal(err)
	}
	var got []map[string]any
	if err := json.Unmarshal(js.Bytes(), &got); err != nil {
		t.Fatalf("json: %v", err)
	}
	if len(got) != len(faults.Catalog) {
		t.Fatalf("json: %d entries, want %d", len(got), len(faults.Catalog))
	}
	for i, f := range faults.Catalog {
		for key, want := range map[string]any{"name": f.Name, "kind": f.Kind, "oneShot": f.OneShot, "meshWide": f.MeshWide, "summary": f.Summary} {
			if got[i][key] != want {
				t.Errorf("json[%d].%s = %v, want %v", i, key, got[i][key], want)
			}
		}
	}

	var none bytes.Buffer
	err := list(&none, "xml")
	if err == nil || !strings.Contains(err.Error(), "--output") {
		t.Fatalf("bad format: want usage error, got %v", err)
	}
	if none.Len() != 0 {
		t.Errorf("bad format wrote to the output writer: %q", none.String())
	}
}
