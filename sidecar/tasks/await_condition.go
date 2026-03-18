package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sei-protocol/seictl/sidecar/actions"
	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/rpc"
	"github.com/sei-protocol/seilog"
)

var awaitLog = seilog.NewLogger("seictl", "task", "await-condition")

const (
	conditionHeight = "height"
	actionSIGTERM   = "SIGTERM_SEID"

	heightPollInterval = 100 * time.Millisecond
)

// ConditionWaiter polls a local node until a condition is met, then
// optionally executes a post-condition action.
type ConditionWaiter struct {
	rpc *rpc.StatusClient
}

// NewConditionWaiter creates a ConditionWaiter. Pass nil for the default RPC client.
func NewConditionWaiter(rpcClient *rpc.StatusClient) *ConditionWaiter {
	if rpcClient == nil {
		rpcClient = rpc.NewStatusClient("", nil)
	}
	return &ConditionWaiter{rpc: rpcClient}
}

// Handler returns an engine.TaskHandler for the await-condition task type.
func (w *ConditionWaiter) Handler() engine.TaskHandler {
	return func(ctx context.Context, params map[string]any) error {
		condition, ok := params["condition"].(string)
		if !ok || condition == "" {
			return fmt.Errorf("condition is required (got %T)", params["condition"])
		}
		action, _ := params["action"].(string)

		switch condition {
		case conditionHeight:
			if err := w.awaitHeight(ctx, params); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown condition %q", condition)
		}

		if action == "" {
			return nil
		}
		return w.executeAction(ctx, action)
	}
}

func (w *ConditionWaiter) awaitHeight(ctx context.Context, params map[string]any) error {
	targetHeight, err := parseTargetHeight(params)
	if err != nil {
		return err
	}

	awaitLog.Info("awaiting height", "target", targetHeight, "rpc", w.rpc.Endpoint())

	var rpcHealthy bool
	var loggedInitialWait bool
	ticker := time.NewTicker(heightPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		height, err := w.rpc.LatestHeight(ctx)
		if err != nil {
			if rpcHealthy {
				awaitLog.Warn("rpc became unavailable", "err", err)
				rpcHealthy = false
			} else if !loggedInitialWait {
				awaitLog.Info("waiting for rpc to become available", "err", err)
				loggedInitialWait = true
			}
			continue
		}
		if !rpcHealthy {
			awaitLog.Info("rpc available", "height", height)
			rpcHealthy = true
		}

		if height >= targetHeight {
			awaitLog.Info("target height reached", "current", height, "target", targetHeight)
			return nil
		}
	}
}

func parseTargetHeight(params map[string]any) (int64, error) {
	switch v := params["targetHeight"].(type) {
	case float64:
		h := int64(v)
		if h <= 0 {
			return 0, fmt.Errorf("targetHeight must be > 0, got %d", h)
		}
		return h, nil
	case int64:
		if v <= 0 {
			return 0, fmt.Errorf("targetHeight must be > 0, got %d", v)
		}
		return v, nil
	case json.Number:
		h, err := v.Int64()
		if err != nil {
			return 0, fmt.Errorf("parsing targetHeight: %w", err)
		}
		if h <= 0 {
			return 0, fmt.Errorf("targetHeight must be > 0, got %d", h)
		}
		return h, nil
	default:
		return 0, fmt.Errorf("targetHeight is required (got %T)", params["targetHeight"])
	}
}

func (w *ConditionWaiter) executeAction(ctx context.Context, action string) error {
	switch action {
	case actionSIGTERM:
		return actions.GracefulStop(ctx, nil, "seid", actions.DefaultGracePeriod)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}
