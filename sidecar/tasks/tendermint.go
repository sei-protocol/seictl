package tasks

// tendermintStatusResponse is the minimal shape of the Tendermint /status JSON response,
// shared by peer discovery (node ID) and state sync (latest height).
type tendermintStatusResponse struct {
	NodeInfo struct {
		ID string `json:"id"`
	} `json:"node_info"`
	SyncInfo struct {
		LatestBlockHeight string `json:"latest_block_height"`
	} `json:"sync_info"`
}

// tendermintBlockResponse is the minimal shape of the Tendermint /block JSON response.
type tendermintBlockResponse struct {
	BlockID struct {
		Hash string `json:"hash"`
	} `json:"block_id"`
}
