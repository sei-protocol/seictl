package tasks

import (
	"context"
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

// fakeTxClient is the in-memory txClient used by tests.
type fakeTxClient struct {
	mu sync.Mutex

	accountNumber uint64
	sequence      uint64
	accountErr    error

	broadcastResp *sdk.TxResponse
	broadcastErr  error
	broadcasts    int

	// queryDefault returns canned data for any hash (mirroring production
	// QueryTx). Nil returns not-found.
	queryDefault *sdk.TxResponse
	queryErr     error
	queryCalls   int
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
	if f.queryDefault != nil {
		resp := *f.queryDefault
		resp.TxHash = hash
		return &resp, true, nil
	}
	return nil, false, nil
}

// testKeyring returns a memory keyring with one entry under "node_admin".
func testKeyring(t *testing.T) (keyring.Keyring, sdk.AccAddress) {
	t.Helper()
	ensureBech32()
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

func makeMsgVote(t *testing.T, voter sdk.AccAddress) sdk.Msg {
	t.Helper()
	return govtypes.NewMsgVote(voter, 7, govtypes.OptionYes)
}

func newGuardCfg(t *testing.T, chainID string) (engine.ExecutionConfig, sdk.AccAddress) {
	t.Helper()
	t.Setenv("SEI_CHAIN_ID", chainID)
	srv := fakeStatusServer(t, chainID, 0)
	kb, addr := testKeyring(t)
	return engine.ExecutionConfig{
		Keyring: kb,
		RPC:     rpc.NewClient(srv.URL, nil),
	}, addr
}

// --- Tests -----------------------------------------------------------

func TestChainConfusionGuard_EnvMismatch(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{}

	_, err := signAndBroadcast(context.Background(), cfg, tc, SignAndBroadcastInput{
		ChainID: "wrong-chain",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000001",
	}, addr)
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
	t.Setenv("SEI_CHAIN_ID", "pacific-1")
	srv := fakeStatusServer(t, "atlantic-2", 0)
	kb, addr := testKeyring(t)
	cfg := engine.ExecutionConfig{
		Keyring: kb,
		RPC:     rpc.NewClient(srv.URL, nil),
	}

	_, err := signAndBroadcast(context.Background(), cfg, &fakeTxClient{}, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000002",
	}, addr)
	if !IsTerminal(err) {
		t.Fatalf("want Terminal status-mismatch error, got %v", err)
	}
	if !strings.Contains(err.Error(), "node.network") {
		t.Fatalf("error should reference node.network, got %q", err.Error())
	}
}

func TestChainConfusionGuard_MissingEnv(t *testing.T) {
	// t.Setenv("","") auto-restores after the test, unlike os.Unsetenv.
	t.Setenv("SEI_CHAIN_ID", "")
	srv := fakeStatusServer(t, "pacific-1", 0)
	kb, addr := testKeyring(t)
	cfg := engine.ExecutionConfig{
		Keyring: kb,
		RPC:     rpc.NewClient(srv.URL, nil),
	}

	_, err := signAndBroadcast(context.Background(), cfg, &fakeTxClient{}, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000003",
	}, addr)
	if !IsTerminal(err) {
		t.Fatalf("want Terminal env-not-set error, got %v", err)
	}
}

func TestFeesDenomGuard_RejectsNonUSei(t *testing.T) {
	cfg, addr := newGuardCfg(t, "pacific-1")

	_, err := signAndBroadcast(context.Background(), cfg, &fakeTxClient{}, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "20sei", // the seienv vote.go:15 latent bug
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000004",
	}, addr)
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
	cfg := engine.ExecutionConfig{
		Keyring: nil, // sidecar started without SEI_KEYRING_BACKEND
		RPC:     rpc.NewClient(srv.URL, nil),
	}

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
	cfg, _ := newGuardCfg(t, "pacific-1")

	_, err := SignAndBroadcast(context.Background(), cfg, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "ghost", // not in the keyring
		Msg:     govtypes.NewMsgVote(makeAddr(t), 7, govtypes.OptionYes),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000006",
	})
	if !IsTerminal(err) {
		t.Fatalf("want Terminal missing-key error, got %v", err)
	}
}

func TestAccountRetrieverFailure_Propagates(t *testing.T) {
	// Account-retrieve failure is NOT Terminal — may be a transient seid restart.
	cfg, addr := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{accountErr: errors.New("rpc dial: connection refused")}

	_, err := signAndBroadcast(context.Background(), cfg, tc, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000007",
	}, addr)
	if err == nil {
		t.Fatal("expected error from account retrieve")
	}
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

	_, err := signAndBroadcast(context.Background(), cfg, tc, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-000000000008",
	}, addr)
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

func TestInclusionPollingCtxCancel_ReturnsCtxErr(t *testing.T) {
	// ctx cancellation must surface so the engine records the task failed
	// (rather than synthesizing "completed" on a truncated run).
	cfg, addr := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{Code: 0, TxHash: "h", Height: 0},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := signAndBroadcast(ctx, cfg, tc, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-00000000000c",
	}, addr)
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected ctx-cancellation error, got: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("test ran too long: %v (polling should bail on ctx.Done)", time.Since(start))
	}
}

func TestInclusionPollingDeadline_ReturnsNonTerminalWithNilIncludedAt(t *testing.T) {
	// Poll deadline (no ctx cancel) is "broadcast OK, inclusion deferred":
	// non-Terminal success with IncludedAt=nil.
	cfg, addr := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{
		accountNumber: 17,
		sequence:      42,
		broadcastResp: &sdk.TxResponse{Code: 0, TxHash: "h", Height: 0},
	}

	prevTimeout, prevInterval := inclusionPollTimeout, inclusionPollInterval
	inclusionPollTimeout = 50 * time.Millisecond
	inclusionPollInterval = 10 * time.Millisecond
	t.Cleanup(func() {
		inclusionPollTimeout = prevTimeout
		inclusionPollInterval = prevInterval
	})

	res, err := signAndBroadcast(context.Background(), cfg, tc, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		TaskID:  "00000000-0000-0000-0000-00000000000d",
	}, addr)
	if err != nil {
		t.Fatalf("poll-deadline (no ctx cancel) should NOT be an error: %v", err)
	}
	if res.IncludedAt != nil {
		t.Fatal("IncludedAt must be nil when poll deadline elapsed without inclusion")
	}
}

func TestAppendTaskIDToMemo(t *testing.T) {
	const id = "00000000-0000-0000-0000-000000000001"
	cases := []struct {
		base string
		want string
	}{
		{"", "taskID=" + id},
		{"vote-rationale", "vote-rationale taskID=" + id},
	}
	for _, c := range cases {
		if got := appendTaskIDToMemo(c.base, id); got != c.want {
			t.Fatalf("appendTaskIDToMemo(%q,_) = %q, want %q", c.base, got, c.want)
		}
	}
	// Empty taskID leaves base unchanged.
	if got := appendTaskIDToMemo("base", ""); got != "base" {
		t.Fatalf("empty taskID changed memo: %q", got)
	}
}

func TestCallerSuppliedTaskIDInMemoRejected(t *testing.T) {
	// Caller cannot smuggle a "taskID=" tag into the memo — the audit
	// trail must contain exactly the engine-appended tag.
	cfg, addr := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{accountNumber: 17, sequence: 42}

	_, err := signAndBroadcast(context.Background(), cfg, tc, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		Memo:    "rationale taskID=forged-uuid",
		TaskID:  "00000000-0000-0000-0000-0000000000aa",
	}, addr)
	if !IsTerminal(err) || !strings.Contains(err.Error(), "taskID=") {
		t.Fatalf("want Terminal taskID-prefix rejection, got %v", err)
	}
}

func TestMemoCapEnforcedAfterTaskIDAppend(t *testing.T) {
	// Caller's base is under 256 bytes but base + taskID exceeds the cap.
	cfg, addr := newGuardCfg(t, "pacific-1")
	tc := &fakeTxClient{accountNumber: 17, sequence: 42}

	base := strings.Repeat("a", 240) // leaves 16 bytes; "taskID=<uuid>" is 7+36=43
	_, err := signAndBroadcast(context.Background(), cfg, tc, SignAndBroadcastInput{
		ChainID: "pacific-1",
		KeyName: "node_admin",
		Msg:     makeMsgVote(t, addr),
		Fees:    "4000usei",
		Gas:     200_000,
		Memo:    base,
		TaskID:  "00000000-0000-0000-0000-000000000099",
	}, addr)
	if !IsTerminal(err) || !strings.Contains(err.Error(), "memo length") {
		t.Fatalf("want Terminal memo-length error, got %v", err)
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
