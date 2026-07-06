package engine

// TxMarker is the pre-broadcast idempotency record for a sign-tx task. It is
// engine-owned metadata read during crash recovery — distinct from a handler's
// TaskResult.Result (which is the handler's output, read by the controller).
// It carries the fully signed tx bytes so a re-run re-broadcasts the identical
// tx rather than re-signing at a refreshed sequence (which would produce a
// different tx and risk a double submit).
type TxMarker struct {
	TaskID        string
	TxHash        string
	TxBytes       []byte
	AccountNumber uint64
	Sequence      uint64
	ChainID       string
}

// Checkpointer persists a TxMarker durably before a broadcast side effect and
// retrieves it on re-execution. The Save MUST be durable before the caller
// broadcasts, so a crash between broadcast and result-persist re-adopts the
// in-flight tx instead of signing a second one. Nil when the engine has no
// durable store; sign-tx handlers then broadcast without a marker and log that
// the crash-idempotency guard is disabled.
type Checkpointer interface {
	SaveTxMarker(m *TxMarker) error
	// GetTxMarker returns (nil, nil) when no marker exists for taskID.
	GetTxMarker(taskID string) (*TxMarker, error)
}
