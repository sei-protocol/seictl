package tasks

import (
	"context"

	"github.com/sei-protocol/platform/sei-sidecar/engine"
)

// MarkReadyHandler returns a TaskHandler that signals bootstrap completion.
// The actual ready flag is set by Engine.DrainUpdates when it sees a successful
// TaskMarkReady result — this handler just needs to succeed.
func MarkReadyHandler() engine.TaskHandler {
	return func(_ context.Context, _ map[string]any) error {
		return nil
	}
}
