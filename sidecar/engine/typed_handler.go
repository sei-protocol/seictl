package engine

import (
	"context"
	"encoding/json"
	"fmt"
)

// TypedHandler wraps a function that accepts typed params into a
// TaskHandler. The map[string]any params are marshaled to JSON and
// unmarshaled into the typed struct T, giving handlers compile-time
// type safety without changing the engine's dispatch mechanism.
func TypedHandler[T any](fn func(ctx context.Context, params T) error) TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		data, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("marshaling params: %w", err)
		}
		var typed T
		if err := json.Unmarshal(data, &typed); err != nil {
			return fmt.Errorf("parsing params: %w", err)
		}
		return fn(ctx, typed)
	}
}
