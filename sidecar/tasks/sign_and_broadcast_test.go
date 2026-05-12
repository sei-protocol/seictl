package tasks

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sei-protocol/sei-chain/sei-cosmos/crypto/hd"
	"github.com/sei-protocol/sei-chain/sei-cosmos/crypto/keyring"
	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	govtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/types"

	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/rpc"
)

// fakeTxClient is the in-memory txClient used by tests. Each field is
// either a function hook (set by tests that want explicit control) or
// a state-tracking counter the test asserts on.
type fakeTxClient struct {
	mu sync.Mutex

	accountNumber uint64
	sequence      uint64
	accountErr    error

	broadcastResp *sdk.TxResponse
	broadcastErr  error
	broadcasts    int

	// queryTx[hash] returns the canned response. queryNotFound[hash]
	// forces found=false even when queryDefault is set. Otherwise
	// queryDefault applies (matches "any hash is on chain"); a nil
	// default and absent map entry returns not-found.
	queryTx       map[string]*sdk.TxResponse
	queryNotFound map[string]bool
	queryDefault  *sdk.TxResponse
	queryErr      error
	queryCalls    int
}

func (f *fakeTxClient) AccountNumberSequence(_ context.Context, _ sdk.AccAddress) (uint64, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.accountNumber, f.sequence, f.accountErr
}

func (f *fakeTxClient) BroadcastSync(_ context.Context, _ []byte) (*sdk.TxResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broadcasts++
	if f.broadcastErr != nil {
		return nil, f.broadcastErr
	}
	return f.broadcastResp, nil
}

func (f *fakeTxClient) QueryTx(_ context.Context, hash string) (*sdk.TxResponse, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queryCalls++
	if f.queryErr != nil {
		return nil, false, f.queryErr
	}
	if f.queryNotFound[hash] {
		return nil, false, nil
	}
	if r, ok := f.queryTx[hash]; ok {
		return r, true, nil
	}
	if f.queryDefault != nil {
		// Echo the queried hash so the result schema is well-formed.
		// Production sdkTxClient.QueryTx does the same.
		resp := *f.queryDefault
		resp.TxHash = hash
		return &resp, true, nil
	}
	return nil, false, nil
}

// in-memory CheckpointStore for tests
type memCheckpoints struct {
	mu sync.Mutex
	m  map[string]*engine.SignTxCheckpoint
}

func newMemCheckpoints() *memCheckpoints {
	return &memCheckpoints{m: map[string]*engine.SignTxCheckpoint{}}
}

func (s *memCheckpoints) SaveCheckpoint(c *engine.SignTxCheckpoint) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cc := *c
	s.m[c.TaskID] = &cc
	return nil
}

func (s *memCheckpoints) LoadCheckpoint(id string) (*engine.SignTxCheckpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.m[id]; ok {
		cc := *c
		return &cc, nil
	}
	return nil, nil
}

func (s *memCheckpoints) DeleteCheckpoint(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
	return nil
}

// testKeyring returns a keyring containing one entry under "node_admin"
// derived from a stable mnemonic so tests don't carry secret material.
func testKeyring(t *testing.T) (keyring.Keyring, sdk.AccAddress) {
	t.Helper()
	ensureBech32() // sei prefixes; same as gentx tests
	kb, err := keyring.New("seictl-tests", keyring.BackendMemory, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	info, _, err := kb.NewMnemonic("node_admin", keyring.English, sdk.GetConfig().GetFullBIP44Path(), "", hd.Secp256k1)
	if err != nil {
		t.Fatalf("NewMnemonic: %v", err)
	}
	return kb, info.GetAddress()
}

// fakeStatusServer returns an httptest server that responds to /status
// with the given chain id. Optional respCode overrides the HTTP status.
func fakeStatusServer(t *testing.T, network string, respCode int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		if respCode != 0 {
			w.WriteHeader(respCode)
			return
		}
		// Seid's flat response shape (no JSON-RPC envelope).
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"node_info":{"id":"abc","network":"` + network + `"},"sync_info":{"latest_block_height":"100","catching_up":false}}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// makeMsgVote builds a valid MsgVote payload that ValidateBasic accepts.
func makeMsgVote(t *testing.T, voter sdk.AccAddress) sdk.Msg {
	t.Helper()
	return govtypes.NewMsgVote(voter, 7, govtypes.OptionYes)
}

// installFactory swaps signTxClientFactory for the test and restores
// it on cleanup. Use the returned setter to replace the fake mid-test.
func installFactory(t *testing.T, tc txClient) {
	t.Helper()
	prev := signTxClientFactory
	signTxClientFactory = func(_ engine.ExecutionConfig, _ SignAndBroadcastInput, _ sdk.AccAddress) (txClient, error) {
		return tc, nil
	}
	t.Cleanup(func() { signTxClientFactory = prev })
}

func newGuardCfg(t *testing.T, chainID string) (engine.ExecutionConfig, sdk.AccAddress) {
	t.Helper()
	t.Setenv("SEI_CHAIN_ID", chainID)
	srv := fakeStatusServer(t, chainID, 0)
	kb, addr := testKeyring(t)
	return engine.ExecutionConfig{
		Keyring:     kb,
		RPC:         rpc.NewClient(srv.URL, nil),
		Checkpoints: newMemCheckpoints(),
	}, addr
}

// --- Tests -----------------------------------------------------------

func TestChainConfusionGuard_EnvMismatch(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")
	// Use a fake txClient that should never be hit — we expect the
	// guard to short-circuit BEFORE the txClient is invoked.
	tc := &fakeTxClient{}
	installFactory(t, tc)

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "wrong-chain",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000001",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal env-mismatch error, got %v", err)
	}
	if !strings.Contains(err.Error(), "SEI_CHAIN_ID") {
		t.Fatalf("error should reference SEI_CHAIN_ID, got %q", err.Error())
	}
	if tc.broadcasts != 0 {
		t.Fatalf("broadcast must not run on guard failure; saw %d", tc.broadcasts)
	}
}

func TestChainConfusionGuard_StatusMismatch(t *testing.T) {
	// SEI_CHAIN_ID matches params but /status reports a different network.
	t.Setenv("SEI_CHAIN_ID", "pacific-1")
	srv := fakeStatusServer(t, "atlantic-2", 0)
	kb, addr := testKeyring(t)
	cfg := engine.ExecutionConfig{
		Keyring:     kb,
		RPC:         rpc.NewClient(srv.URL, nil),
		Checkpoints: newMemCheckpoints(),
	}
	installFactory(t, &fakeTxClient{})

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000002",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal status-mismatch error, got %v", err)
	}
	if !strings.Contains(err.Error(), "node.network") {
		t.Fatalf("error should reference node.network, got %q", err.Error())
	}
}

func TestChainConfusionGuard_MissingEnv(t *testing.T) {
	// t.Setenv with empty value exercises the same code path as an
	// unset env var AND auto-restores after the test, unlike a bare
	// os.Unsetenv which would leak across other tests in the package.
	t.Setenv("SEI_CHAIN_ID", "")
	srv := fakeStatusServer(t, "pacific-1", 0)
	kb, addr := testKeyring(t)
	cfg := engine.ExecutionConfig{
		Keyring:     kb,
		RPC:         rpc.NewClient(srv.URL, nil),
		Checkpoints: newMemCheckpoints(),
	}
	installFactory(t, &fakeTxClient{})

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000003",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal env-not-set error, got %v", err)
	}
}

func TestFeesDenomGuard_RejectsNonUSei(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")
	installFactory(t, &fakeTxClient{})

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "20sei", // the seienv vote.go:15 latent bug
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000004",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal denom error, got %v", err)
	}
	if !strings.Contains(err.Error(), "usei") {
		t.Fatalf("error should mention usei, got %q", err.Error())
	}
}

func TestMissingKeyring_ReturnsTerminal(t *testing.T) {
	t.Setenv("SEI_CHAIN_ID", "pacific-1")
	srv := fakeStatusServer(t, "pacific-1", 0)
	// Keyring is nil — simulates a sidecar started without
	// SEI_KEYRING_BACKEND.
	cfg := engine.ExecutionConfig{
		Keyring:     nil,
		RPC:         rpc.NewClient(srv.URL, nil),
		Checkpoints: newMemCheckpoints(),
	}
	installFactory(t, &fakeTxClient{})

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     govtypes.NewMsgVote(makeAddr(t), 7, govtypes.OptionYes),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000005",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal missing-keyring error, got %v", err)
	}
}

func TestMissingKeyEntry_ReturnsTerminal(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")
	installFactory(t, &fakeTxClient{})

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "ghost", // not in the keyring
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000006",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal missing-key error, got %v", err)
	}
}

func TestAccountRetrieverFailure_Propagates(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{accountErr: errors.New("rpc dial: connection refused")}
	installFactory(t, tc)

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000007",
	})
	if err == nil {
		t.Fatal("expected error from account retrieve")
	}
	// Account-retrieve failure is NOT Terminal — it might be a
	// transient seid restart.
	if IsTerminal(err) {
		t.Fatalf("account-retrieve transport errors should be retryable; got Terminal %v", err)
	}
	if !strings.Contains(err.Error(), "account retrieve") {
		t.Fatalf("error should mention account retrieve, got %q", err.Error())
	}
}

func TestBroadcastCheckTxFailure_ReturnsTerminal(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{
			Code:      11,
			Codespace: "sdk",
			RawLog:    "insufficient fees",
		},
	}
	installFactory(t, tc)

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000008",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal checkTx error, got %v", err)
	}
	if !strings.Contains(err.Error(), "checkTx") {
		t.Fatalf("error should mention checkTx, got %q", err.Error())
	}
	if tc.broadcasts != 1 {
		t.Fatalf("expected 1 broadcast, got %d", tc.broadcasts)
	}
}

func TestIdempotency_ChainHasTxShortCircuitsRetry(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")

	// First call: broadcast succeeds; inclusion poll returns immediately
	// via queryDefault so the test does not block on the 60s polling
	// timeout.
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{Code: 0, TxHash: "set-after-encode", Height: 100},
		queryDefault:  &sdk.TxResponse{Code: 0, Height: 101, GasUsed: 100000, GasWanted: 200000},
	}
	installFactory(t, tc)

	in := SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000009",
	}
	res, err := SignAndBroadcast(context.Background(), cfg, in)
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if res.TxHash == "" {
		t.Fatal("first call: empty txHash")
	}
	firstHash := res.TxHash
	firstBroadcasts := tc.broadcasts

	// Second call with the SAME taskID: the checkpoint exists. Reset
	// queryDefault and use the per-hash table to assert that the
	// short-circuit reads the PERSISTED hash, not a different one.
	tc.queryDefault = nil
	tc.queryTx = map[string]*sdk.TxResponse{
		firstHash: {Code: 0, TxHash: firstHash, Height: 101, GasUsed: 100000, GasWanted: 200000},
	}
	res2, err := SignAndBroadcast(context.Background(), cfg, in)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if res2.TxHash != firstHash {
		t.Fatalf("retry produced different hash: %q vs %q", res2.TxHash, firstHash)
	}
	if tc.broadcasts != firstBroadcasts {
		t.Fatalf("retry triggered an extra broadcast: %d > %d", tc.broadcasts, firstBroadcasts)
	}
}

func TestIdempotency_AdvancedSequenceWithoutTx_Terminal(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")

	// Pre-populate a checkpoint as if a prior call broadcast at seq 42.
	cps := cfg.Checkpoints.(*memCheckpoints)
	_ = cps.SaveCheckpoint(&engine.SignTxCheckpoint{
		TaskID:        "00000000-0000-0000-0000-00000000000a",
		TxHash:        "STALEHASH",
		Sequence:      42,
		AccountNumber: 17,
		ChainID:       "pacific-1",
		CreatedAt:     time.Now().UTC(),
	})

	// Now the chain reports the signer has moved to sequence 43, but
	// /tx?hash=STALEHASH returns not-found. That's the bad state the
	// design calls out: tx may have been DeliverTx-rejected.
	tc := &fakeTxClient{accountNumber: 17, sequence: 43}
	installFactory(t, tc)

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-00000000000a",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal advanced-sequence error, got %v", err)
	}
	if !strings.Contains(err.Error(), "DeliverTx") {
		t.Fatalf("error should call out DeliverTx investigation, got %q", err.Error())
	}
	if tc.broadcasts != 0 {
		t.Fatal("must NOT re-broadcast when sequence advanced past prior checkpoint")
	}
}

func TestIdempotency_UnchangedSequence_AbsentTxRecovers(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")
	cps := cfg.Checkpoints.(*memCheckpoints)
	_ = cps.SaveCheckpoint(&engine.SignTxCheckpoint{
		TaskID:        "00000000-0000-0000-0000-00000000000b",
		TxHash:        "STALEHASH",
		Sequence:      42,
		AccountNumber: 17,
		ChainID:       "pacific-1",
		CreatedAt:     time.Now().UTC(),
	})

	// Sequence has NOT advanced. Stale checkpoint should be dropped,
	// re-sign at seq 42, broadcast once. queryDefault makes inclusion
	// polling return immediately so the test does not block on the
	// 60s timeout; queryNotFound forces the STALEHASH lookup to fail
	// the prior-tx check.
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{Code: 0, TxHash: "freshhash", Height: 200},
		queryDefault:  &sdk.TxResponse{Code: 0, Height: 200},
		queryNotFound: map[string]bool{"STALEHASH": true},
	}
	installFactory(t, tc)

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-00000000000b",
	})
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if tc.broadcasts != 1 {
		t.Fatalf("expected one fresh broadcast, got %d", tc.broadcasts)
	}
}

func TestInclusionPollingCtxCancel_ReturnsCtxErr(t *testing.T) {
	// Ctx cancellation during inclusion poll MUST surface as an error
	// so the engine records the task as failed rather than synthesizing
	// a "completed" record on a truncated run. Next Submit with the
	// same TaskID short-circuits via the persisted checkpoint.
	cfg, addr := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{Code: 0, TxHash: "h", Height: 0},
	}
	installFactory(t, tc)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := SignAndBroadcast(ctx, cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-00000000000c",
	})
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected ctx-cancellation error, got: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("test ran too long: %v (polling should bail on ctx.Done)", time.Since(start))
	}
}

func TestInclusionPollingDeadline_ReturnsNonTerminalWithNilIncludedAt(t *testing.T) {
	// Poll-deadline exceeded (without ctx cancellation) is the "broadcast
	// OK, inclusion-confirmation deferred" case — non-Terminal success
	// with IncludedAt=nil. Operator can re-submit the same TaskID to
	// re-poll via the persisted checkpoint.
	cfg, addr := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{Code: 0, TxHash: "h", Height: 0},
	}
	installFactory(t, tc)

	// Shorten the poll budget so the test runs fast; restore on cleanup.
	prevTimeout, prevInterval := inclusionPollTimeout, inclusionPollInterval
	inclusionPollTimeout = 50 * time.Millisecond
	inclusionPollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		inclusionPollTimeout = prevTimeout
		inclusionPollInterval = prevInterval
	})

	res, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-00000000000d",
	})
	if err != nil {
		t.Fatalf("poll-deadline (no ctx cancel) should NOT be an error: %v", err)
	}
	if res.IncludedAt != nil {
		t.Fatal("IncludedAt must be nil when poll deadline elapsed without inclusion")
	}
}

func TestSaveCheckpoint_RunsBeforeBroadcast(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")
	// Force a broadcast error so we can inspect whether the checkpoint
	// was nevertheless persisted before broadcast was attempted.
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastErr:  errors.New("rpc network down"),
	}
	installFactory(t, tc)

	taskID := "00000000-0000-0000-0000-00000000000d"
	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  taskID,
	})
	if err == nil {
		t.Fatal("expected broadcast error")
	}
	got, err := cfg.Checkpoints.LoadCheckpoint(taskID)
	if err != nil || got == nil {
		t.Fatalf("checkpoint must persist before broadcast attempt; got %v err=%v", got, err)
	}
	if got.Sequence != 42 || got.AccountNumber != 17 || got.ChainID != "pacific-1" {
		t.Fatalf("checkpoint shape wrong: %+v", got)
	}
	if _, err := hex.DecodeString(got.TxHash); err != nil {
		t.Fatalf("checkpoint hash should be hex: %q (%v)", got.TxHash, err)
	}
}

// makeAddr returns a syntactically valid sei-bech32 AccAddress without
// constructing a keyring entry. Used by guards that fail before any
// keyring lookup.
func makeAddr(t *testing.T) sdk.AccAddress {
	t.Helper()
	ensureBech32()
	return sdk.AccAddress([]byte("aaaaaaaaaaaaaaaaaaaa"))
}
