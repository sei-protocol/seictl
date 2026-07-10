package workflow

import (
	"strings"
	"testing"
)

// preflightPhaseError refuses a re-run only over a terminal same-named workflow;
// absent or non-terminal phases pass (first run, or join an in-progress run).
func TestPreflightPhaseError(t *testing.T) {
	for _, phase := range []string{"", "Pending", "Running"} {
		if err := preflightPhaseError("sei", "n-state-sync", phase); err != nil {
			t.Errorf("phase %q: expected no refusal, got %v", phase, err)
		}
	}

	complete := preflightPhaseError("sei", "n-state-sync", phaseComplete)
	if complete == nil {
		t.Fatal("Complete must be refused")
	}
	if !strings.Contains(strings.ToLower(complete.Error()), "delete") {
		t.Errorf("Complete refusal must guide to delete; got %q", complete.Error())
	}
	if !strings.Contains(complete.Error(), "--name") {
		t.Errorf("Complete refusal must offer --name; got %q", complete.Error())
	}

	failed := preflightPhaseError("sei", "n-state-sync", phaseFailed)
	if failed == nil {
		t.Fatal("Failed must be refused")
	}
	if !strings.Contains(failed.Error(), forceDeleteAnnotation) {
		t.Errorf("Failed refusal must name the force-delete annotation; got %q", failed.Error())
	}
	if !strings.Contains(failed.Error(), "holds the node") {
		t.Errorf("Failed refusal must explain the node stays held; got %q", failed.Error())
	}
}
