package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	seiconfig "github.com/sei-protocol/sei-config"
)

// DefaultEndpoint is the local CometBFT RPC address.
var DefaultEndpoint = fmt.Sprintf("http://localhost:%d", seiconfig.PortRPC)

const defaultRequestTimeout = 500 * time.Millisecond

// StatusClient queries a CometBFT node's /status endpoint.
type StatusClient struct {
	endpoint   string
	httpClient *http.Client
}

// NodeStatus holds the fields we care about from CometBFT /status.
type NodeStatus struct {
	LatestBlockHeight int64
	CatchingUp        bool
}

// Endpoint returns the configured RPC endpoint.
func (c *StatusClient) Endpoint() string { return c.endpoint }

type statusResponse struct {
	SyncInfo struct {
		LatestBlockHeight string `json:"latest_block_height"`
		CatchingUp        bool   `json:"catching_up"`
	} `json:"sync_info"`
}

// NewStatusClient creates a client targeting the given RPC endpoint.
// Pass "" for the default localhost endpoint.
func NewStatusClient(endpoint string, httpClient *http.Client) *StatusClient {
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &StatusClient{endpoint: endpoint, httpClient: httpClient}
}

// Status queries the node and returns the parsed status.
func (c *StatusClient) Status(ctx context.Context) (*NodeStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint+"/status", nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}

	var rpcResp statusResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("decoding /status: %w", err)
	}

	h, err := strconv.ParseInt(rpcResp.SyncInfo.LatestBlockHeight, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("parsing latest_block_height %q: %w", rpcResp.SyncInfo.LatestBlockHeight, err)
	}

	return &NodeStatus{
		LatestBlockHeight: h,
		CatchingUp:        rpcResp.SyncInfo.CatchingUp,
	}, nil
}

// LatestHeight is a convenience wrapper returning just the height.
func (c *StatusClient) LatestHeight(ctx context.Context) (int64, error) {
	s, err := c.Status(ctx)
	if err != nil {
		return 0, err
	}
	return s.LatestBlockHeight, nil
}
