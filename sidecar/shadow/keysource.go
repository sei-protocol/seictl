package shadow

import (
	"context"
	"fmt"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	gethrpc "github.com/ethereum/go-ethereum/rpc"
)

// TraceKeySource derives a block's touched accounts and storage slots from a
// prestate trace (debug_traceBlockByNumber with the prestateTracer) on an EVM
// JSON-RPC endpoint. The prestate lists every account and slot the block's
// transactions accessed — exactly the set Layer 2 should compare. Requires the
// debug_ namespace enabled on the endpoint (a non-public, operator-owned node).
type TraceKeySource struct {
	client *gethrpc.Client
}

// NewTraceKeySource dials the EVM JSON-RPC endpoint used to fetch prestate
// traces. The same touched set applies to both chains (identical transactions),
// so a single endpoint (typically the canonical node) is sufficient.
func NewTraceKeySource(evmRPC string) (*TraceKeySource, error) {
	c, err := gethrpc.Dial(evmRPC)
	if err != nil {
		return nil, fmt.Errorf("dialing EVM RPC %q: %w", evmRPC, err)
	}
	return &TraceKeySource{client: c}, nil
}

// Close releases the underlying RPC connection.
func (t *TraceKeySource) Close() {
	if t.client != nil {
		t.client.Close()
	}
}

// prestateAccount is the subset of the prestateTracer's per-account output the
// key source needs: which slots were accessed, and whether the account carries
// code (a contract).
type prestateAccount struct {
	Storage map[common.Hash]common.Hash `json:"storage"`
	Code    hexutil.Bytes               `json:"code"`
}

// prestateTxTrace wraps one transaction's prestate tracer result.
type prestateTxTrace struct {
	Result map[common.Address]prestateAccount `json:"result"`
}

func (t *TraceKeySource) TouchedAccounts(ctx context.Context, height int64) ([]TouchedAccount, error) {
	var traces []prestateTxTrace
	blockArg := hexutil.EncodeUint64(uint64(height))
	cfg := map[string]any{"tracer": "prestateTracer"}
	if err := t.client.CallContext(ctx, &traces, "debug_traceBlockByNumber", blockArg, cfg); err != nil {
		return nil, fmt.Errorf("debug_traceBlockByNumber at height %d: %w", height, err)
	}
	return mergePrestateTraces(traces), nil
}

// mergePrestateTraces unions the per-transaction prestate results into one
// touched-account set per address (slots unioned; code checked when the trace
// shows the account carries code; nonce checked for every touched account).
func mergePrestateTraces(traces []prestateTxTrace) []TouchedAccount {
	slots := map[common.Address]map[common.Hash]struct{}{}
	hasCode := map[common.Address]bool{}
	for _, tx := range traces {
		for addr, acct := range tx.Result {
			if slots[addr] == nil {
				slots[addr] = map[common.Hash]struct{}{}
			}
			for slot := range acct.Storage {
				slots[addr][slot] = struct{}{}
			}
			if len(acct.Code) > 0 {
				hasCode[addr] = true
			}
		}
	}

	out := make([]TouchedAccount, 0, len(slots))
	for addr, slotSet := range slots {
		ta := TouchedAccount{Addr: addr, CheckCode: hasCode[addr], CheckNonce: true}
		for slot := range slotSet {
			ta.Slots = append(ta.Slots, slot)
		}
		out = append(out, ta)
	}
	return out
}

// StaticKeySource compares a fixed, curated set of accounts on every block — the
// fallback when prestate tracing (debug_) is unavailable. Coverage is sampled,
// not exact; pair it with a curated hot-contract / hot-account list.
type StaticKeySource struct {
	Accounts []TouchedAccount
}

func (s StaticKeySource) TouchedAccounts(_ context.Context, _ int64) ([]TouchedAccount, error) {
	return s.Accounts, nil
}
