package workflow

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/sei-protocol/seictl/internal/cliutil"
)

// Complete (the terminal-success gate) is legal; Initializing (SeiNode vocab,
// not a workflow phase) errors Invalid at parse with the legal set.
func TestWorkflowWatch_PhaseVocab(t *testing.T) {
	if err := cliutil.ValidatePhase("Complete", workflowPhases); err != nil {
		t.Errorf("Complete must be a legal SeiNodeTaskWorkflow phase; got %v", err)
	}
	err := cliutil.ValidatePhase("Initializing", workflowPhases)
	if err == nil {
		t.Fatal("Initializing must be illegal for a workflow (SeiNode vocab, not a workflow phase)")
	}
	if !strings.Contains(err.Error(), "Complete") {
		t.Errorf("error must list the legal set incl. Complete; got %q", err.Error())
	}
}

// MatchPhase drives the kubectl-wait-compatible exit codes: Complete satisfies
// --until (exit 0), Failed is a terminal error carrying the plan's failure
// detail (nonzero), Running keeps streaming.
func TestWorkflowWatch_MatchPhase(t *testing.T) {
	complete := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"phase": "Complete"},
	}}
	ok, err := cliutil.MatchPhase(complete, phaseComplete)
	if err != nil || !ok {
		t.Errorf("Complete: got (ok=%v err=%v); want (true, nil)", ok, err)
	}

	running := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{"phase": "Running"},
	}}
	if ok, err := cliutil.MatchPhase(running, phaseComplete); ok || err != nil {
		t.Errorf("Running: got (ok=%v err=%v); want (false, nil) — keep streaming", ok, err)
	}

	failed := &unstructured.Unstructured{Object: map[string]interface{}{
		"status": map[string]interface{}{
			"phase": "Failed",
			"plan": map[string]interface{}{
				"failedTaskDetail": map[string]interface{}{"error": "reset-data: seid RPC is serving"},
			},
		},
	}}
	ok, err = cliutil.MatchPhase(failed, phaseComplete)
	if ok || err == nil {
		t.Fatalf("Failed: got (ok=%v err=%v); want (false, terminal error)", ok, err)
	}
	if !strings.Contains(err.Error(), "reset-data: seid RPC is serving") {
		t.Errorf("Failed error must carry the plan failure detail; got %q", err.Error())
	}
}
