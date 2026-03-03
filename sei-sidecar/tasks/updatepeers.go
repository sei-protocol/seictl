package tasks

import (
	"context"
	"fmt"
	"strings"
	"syscall"

	"github.com/sei-protocol/platform/sei-sidecar/engine"
)

// UpdatePeersHandler returns a TaskHandler that patches persistent_peers
// in config.toml and sends SIGHUP to seid to trigger a config reload.
func UpdatePeersHandler(homeDir string) engine.TaskHandler {
	return func(_ context.Context, params map[string]any) error {
		peers, err := extractPeersList(params)
		if err != nil {
			return err
		}

		patcher := NewConfigPatcher(homeDir)
		if err := patcher.PatchConfig(context.Background(), PatchSet{Peers: peers}); err != nil {
			return fmt.Errorf("patching persistent_peers: %w", err)
		}

		if err := SignalSeidFn(syscall.SIGHUP); err != nil {
			return fmt.Errorf("sending SIGHUP to seid: %w", err)
		}

		return nil
	}
}

func extractPeersList(params map[string]any) ([]string, error) {
	raw, ok := params["peers"]
	if !ok {
		return nil, fmt.Errorf("update-peers: missing required param 'peers'")
	}

	switch v := raw.(type) {
	case []string:
		return v, nil
	case []any:
		peers := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("update-peers: peers must be a list of strings")
			}
			peers = append(peers, s)
		}
		return peers, nil
	case string:
		return strings.Split(v, ","), nil
	default:
		return nil, fmt.Errorf("update-peers: peers must be a list of strings")
	}
}
