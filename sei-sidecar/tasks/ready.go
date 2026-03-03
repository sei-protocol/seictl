package tasks

import (
	"context"

	"github.com/sei-protocol/seictl/sei-sidecar/engine"
)

// MarkReadyHandler returns a no-op TaskHandler. When it succeeds, the engine
// marks itself as ready.
func MarkReadyHandler() engine.TaskHandler {
	return func(_ context.Context, _ map[string]any) error {
		return nil
	}
}
