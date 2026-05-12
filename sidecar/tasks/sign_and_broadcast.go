// Package tasks — sign-tx family helper.
//
// SECURITY POSTURE NOTE — sei-protocol/seictl#163 / #165
//
// This file implements the shared sign-and-broadcast path used by all
// sign-tx task handlers (gov-vote in #163-A, plus gov-submit-proposal
// and gov-deposit in follow-up PRs). The handlers accept sign-and-
// broadcast requests over the sidecar's HTTP API, which is
// unauthenticated in Phase 1-3 of the governance-flow workstream.
// The sidecar binds 0.0.0.0:7777 in Phase 1-3; any caller with
// network reach to that port can submit governance txs as the
// validator's operator account by POSTing {type, params} to
// /v0/tasks. This is comparable to the seienv+SSH status-quo trust
// scope (anyone with the SSH key has equivalent power) but the K8s
// network blast radius is wider.
//
// Phase 4 (#165) fronts the sidecar with a kube-rbac-proxy sidecar
// container that terminates TLS on 0.0.0.0:8443, runs TokenReview
// + a single coarse SubjectAccessReview (create seinodetasks.sei.io)
// against the cluster API server regardless of task type, and proxies
// to the sidecar bound on 127.0.0.1:7777. The sidecar then trusts
// X-Remote-User on the loopback ingress. REMOVE THIS NOTICE when
// #165 lands.

package tasks

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode"

	rpchttp "github.com/sei-protocol/sei-chain/sei-tendermint/rpc/client/http"
	"github.com/sei-protocol/sei-chain/sei-tendermint/crypto/tmhash"

	"github.com/sei-protocol/sei-chain/sei-cosmos/client"
	"github.com/sei-protocol/sei-chain/sei-cosmos/client/tx"
	"github.com/sei-protocol/sei-chain/sei-cosmos/codec"
	codectypes "github.com/sei-protocol/sei-chain/sei-cosmos/codec/types"
	cryptocodec "github.com/sei-protocol/sei-chain/sei-cosmos/crypto/codec"
	sdk "github.com/sei-protocol/sei-chain/sei-cosmos/types"
	authtx "github.com/sei-protocol/sei-chain/sei-cosmos/x/auth/tx"
	authtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/auth/types"
	banktypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/bank/types"
	govtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/gov/types"
	stakingtypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/staking/types"
	upgradetypes "github.com/sei-protocol/sei-chain/sei-cosmos/x/upgrade/types"

	signingtypes "github.com/sei-protocol/sei-chain/sei-cosmos/types/tx/signing"

	"github.com/sei-protocol/seictl/sidecar/engine"
	"github.com/sei-protocol/seictl/sidecar/rpc"
	"github.com/sei-protocol/seilog"
)

var signTxLog = seilog.NewLogger("seictl", "task", "sign-tx")

// feeDenom is the only fee denom the sidecar will sign for. seienv
// historically used "sei" (e.g., `--fees 20sei` in vote.go:15) which
// seid rejects at parse time anyway — we make the rejection explicit
// and chain-confusion-proof. 1 SEI = 1_000_000 usei.
const feeDenom = "usei"

// inclusionPollTimeout caps how long we wait for /tx?hash=... to return
// a block-included response after BroadcastTxSync returns code=0.
// Beyond this the helper returns a non-Terminal result with IncludedAt
// nil; the caller may re-submit the same TaskID later to short-circuit
// onto the existing checkpoint and re-poll. Var (not const) so tests
// can shorten it without injecting a deadline-bearing context.
var inclusionPollTimeout = 60 * time.Second

// inclusionPollInterval is the gap between /tx?hash=... polls. The
// local seid is on loopback so this is cheap; we keep it lower than
// block time so first-included tx is reported promptly.
var inclusionPollInterval = 500 * time.Millisecond

// SignAndBroadcastInput is the shared input contract for every sign-tx
// handler. Msg is the typed cosmos-sdk message — e.g., *govtypes.MsgVote
// in this PR; *govtypes.MsgSubmitProposal / *govtypes.MsgDeposit in the
// follow-up PRs. Keeping Msg as sdk.Msg means the helper never grows a
// per-msg switch.
type SignAndBroadcastInput struct {
	ChainID string
	KeyName string
	Msg     sdk.Msg

	// Fees is a coin-string in usei. Non-usei denoms are rejected
	// Terminal to mirror the seienv `20sei` latent-bug fix.
	Fees string

	Gas    uint64
	Memo   string
	TaskID string
}

// SignAndBroadcastResult is the shared output contract. Per design
// Component C, gov-vote / gov-deposit / gov-submit-proposal each
// extend this with task-type-specific fields written by the handler
// AFTER this function returns.
type SignAndBroadcastResult struct {
	TxHash        string     `json:"txHash"`
	Height        int64      `json:"height"`
	Code          uint32     `json:"code"`
	Codespace     string     `json:"codespace,omitempty"`
	RawLog        string     `json:"rawLog,omitempty"`
	GasWanted     int64      `json:"gasWanted"`
	GasUsed       int64      `json:"gasUsed"`
	Sequence      uint64     `json:"sequence"`
	AccountNumber uint64     `json:"accountNumber"`
	ChainID       string     `json:"chainId"`
	BroadcastedAt time.Time  `json:"broadcastedAt"`
	// IncludedAt is nil when broadcast succeeded but inclusion polling
	// timed out. The tx may still land — the caller may re-submit the
	// same TaskID to short-circuit onto the existing checkpoint.
	IncludedAt *time.Time `json:"includedAt,omitempty"`
}

// TerminalError marks a sign-tx error as non-retryable: malformed input,
// chain-confusion, CheckTx rejection, missing key. The engine currently
// has no retry policy so this is a marker for the CLI and future code
// paths; we still want the distinction in the helper's contract so
// callers do not implement their own ad-hoc retry semantics on top.
type TerminalError struct {
	Err error
}

func (e *TerminalError) Error() string { return e.Err.Error() }
func (e *TerminalError) Unwrap() error { return e.Err }

// Terminal wraps err as TerminalError. Returns nil when err is nil.
func Terminal(err error) error {
	if err == nil {
		return nil
	}
	return &TerminalError{Err: err}
}

// IsTerminal reports whether err (or any wrapped error) is a TerminalError.
func IsTerminal(err error) bool {
	var t *TerminalError
	return errors.As(err, &t)
}

// txClient is the narrow seam the sign-tx helper uses for everything
// it does NOT control directly via local files or the rpc.Client:
// fetching the signer's account number+sequence, broadcasting tx
// bytes, and querying inclusion by tx hash. A package-private
// interface keeps the seam local to the sign-tx family rather than
// growing it on engine.ExecutionConfig.
type txClient interface {
	// AccountNumberSequence returns (accountNumber, sequence, err).
	// Order matches authtypes.AccountRetriever.GetAccountNumberSequence.
	// We ALWAYS refresh — never cache across retries — because a stale
	// sequence is the most common cause of CheckTx ErrWrongSequence and
	// because the cached value could mask a successful prior broadcast.
	AccountNumberSequence(ctx context.Context, fromAddr sdk.AccAddress) (uint64, uint64, error)

	// BroadcastSync returns (txResponse, err). resp.Code != 0 is a
	// CheckTx-level rejection; the helper treats it as Terminal.
	BroadcastSync(ctx context.Context, txBytes []byte) (*sdk.TxResponse, error)

	// QueryTx returns (response, found, err). found=false / err=nil is
	// "node has no record" — the caller distinguishes that from a
	// transport error.
	QueryTx(ctx context.Context, txHashHex string) (*sdk.TxResponse, bool, error)
}

// signTxClientFactory builds the production txClient. Exported as a
// var so tests can swap it; production wires the SDK Tendermint HTTP
// client against the local CometBFT RPC endpoint resolved from cfg.RPC.
var signTxClientFactory = newSDKTxClient

// newSDKTxClient constructs a txClient backed by the cosmos-sdk
// client.Context against the local seid RPC. We build it lazily per
// call so the keyring (passed in) is not retained across handlers.
func newSDKTxClient(cfg engine.ExecutionConfig, in SignAndBroadcastInput, fromAddr sdk.AccAddress) (txClient, error) {
	rpcURL := rpc.DefaultEndpoint
	if cfg.RPC != nil && cfg.RPC.Endpoint() != "" {
		rpcURL = cfg.RPC.Endpoint()
	}
	tmClient, err := rpchttp.New(rpcURL)
	if err != nil {
		return nil, fmt.Errorf("tendermint RPC client at %s: %w", rpcURL, err)
	}
	registry := newSignTxInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	txCfg := authtx.NewTxConfig(cdc, authtx.DefaultSignModes)
	clientCtx := client.Context{}.
		WithChainID(in.ChainID).
		WithCodec(cdc).
		WithInterfaceRegistry(registry).
		WithTxConfig(txCfg).
		WithKeyring(cfg.Keyring).
		WithClient(tmClient).
		WithFromAddress(fromAddr).
		WithFromName(in.KeyName).
		WithAccountRetriever(authtypes.AccountRetriever{}).
		WithBroadcastMode("sync")
	return &sdkTxClient{clientCtx: clientCtx, txCfg: txCfg}, nil
}

// sdkTxClient is the production txClient — a thin shim over the
// cosmos-sdk client.Context. Public methods carry the same docs as
// the interface.
type sdkTxClient struct {
	clientCtx client.Context
	txCfg     client.TxConfig
}

func (c *sdkTxClient) AccountNumberSequence(_ context.Context, fromAddr sdk.AccAddress) (uint64, uint64, error) {
	return authtypes.AccountRetriever{}.GetAccountNumberSequence(c.clientCtx, fromAddr)
}

func (c *sdkTxClient) BroadcastSync(_ context.Context, txBytes []byte) (*sdk.TxResponse, error) {
	return c.clientCtx.BroadcastTxSync(txBytes)
}

func (c *sdkTxClient) QueryTx(ctx context.Context, txHashHex string) (*sdk.TxResponse, bool, error) {
	if c.clientCtx.Client == nil {
		return nil, false, errors.New("tx client has no Tendermint RPC client")
	}
	hashBytes, err := hex.DecodeString(txHashHex)
	if err != nil {
		return nil, false, fmt.Errorf("decode tx hash %q: %w", txHashHex, err)
	}
	res, err := c.clientCtx.Client.Tx(ctx, hashBytes, false)
	if err != nil {
		// CometBFT returns an error "tx not found" when the node has no
		// record. We treat any "not found" substring as the "found=false"
		// signal; everything else is a real transport error.
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("query /tx?hash=%s: %w", txHashHex, err)
	}
	resp := &sdk.TxResponse{
		Height:    res.Height,
		TxHash:    txHashHex,
		Code:      res.TxResult.Code,
		Codespace: res.TxResult.Codespace,
		Data:      string(res.TxResult.Data),
		RawLog:    res.TxResult.Log,
		Info:      res.TxResult.Info,
		GasWanted: res.TxResult.GasWanted,
		GasUsed:   res.TxResult.GasUsed,
	}
	return resp, true, nil
}

// SignAndBroadcast is the entry point each sign-tx handler calls.
// The function is ordered to make the chain-confusion and denom
// guards effectively zero-cost when callers pass bad input — both
// run before we touch the keyring. The idempotency contract is
// described in docs/design/in-pod-governance-signing.md, Component C.
func SignAndBroadcast(ctx context.Context, cfg engine.ExecutionConfig, in SignAndBroadcastInput) (*SignAndBroadcastResult, error) {
	if err := validateInput(in); err != nil {
		return nil, Terminal(err)
	}
	if err := checkFeesDenom(in.Fees); err != nil {
		return nil, Terminal(err)
	}

	// Chain-confusion guard runs BEFORE we open the keyring — a stale
	// passphrase prompt or a hardware-key tap on the wrong chain is
	// the failure mode we are protecting against.
	if err := guardChainID(ctx, cfg.RPC, in.ChainID); err != nil {
		return nil, err // already Terminal-wrapped where appropriate
	}

	// Validate the engine wiring BEFORE touching the keyring — opening
	// the keyring on the file backend can prompt for the passphrase, and
	// a misconfigured checkpoint store should never reach that path.
	if cfg.Keyring == nil {
		return nil, Terminal(errors.New("keyring not configured: set SEI_KEYRING_BACKEND/SEI_KEYRING_PASSPHRASE on the sidecar"))
	}
	if cfg.Checkpoints == nil {
		return nil, errors.New("checkpoint store not configured")
	}

	info, err := cfg.Keyring.Key(in.KeyName)
	if err != nil {
		return nil, Terminal(fmt.Errorf("keyring entry %q: %w", in.KeyName, err))
	}
	fromAddr := info.GetAddress()

	tc, err := signTxClientFactory(cfg, in, fromAddr)
	if err != nil {
		return nil, err
	}

	return signAndBroadcast(ctx, cfg, tc, in, fromAddr)
}

// signAndBroadcast is the testable inner loop. Tests inject a fake
// txClient via signTxClientFactory; we keep ExecutionConfig and the
// chain-id guard at the public-entry layer above so the same inner
// loop is exercised by tests and production.
func signAndBroadcast(ctx context.Context, cfg engine.ExecutionConfig, tc txClient, in SignAndBroadcastInput, fromAddr sdk.AccAddress) (*SignAndBroadcastResult, error) {
	// Always fetch a fresh sequence — never reuse a cached value across
	// retries. A stale sequence is the most common CheckTx rejection
	// and could also mask a successfully landed prior broadcast.
	accNum, seq, err := tc.AccountNumberSequence(ctx, fromAddr)
	if err != nil {
		return nil, fmt.Errorf("account retrieve %s: %w", fromAddr.String(), err)
	}

	// On retry-after-broadcast: a checkpoint already exists from a
	// prior attempt. If the chain has the tx, we short-circuit to the
	// on-chain result. If not — and the sequence has not advanced —
	// we delete the checkpoint and fall through to re-sign with the
	// fresh sequence. If the sequence HAS advanced past the
	// persisted one and the chain still has no record, we have a
	// confused state (likely DeliverTx rejection); surface Terminal.
	if prior, lerr := cfg.Checkpoints.LoadCheckpoint(in.TaskID); lerr != nil {
		return nil, fmt.Errorf("load checkpoint: %w", lerr)
	} else if prior != nil {
		if prior.ChainID != in.ChainID {
			// A different chain on the same TaskID is itself a chain
			// confusion: refuse rather than overwrite.
			return nil, Terminal(fmt.Errorf("checkpoint chain mismatch: stored=%q requested=%q",
				prior.ChainID, in.ChainID))
		}
		resp, found, qerr := tc.QueryTx(ctx, prior.TxHash)
		if qerr != nil {
			return nil, fmt.Errorf("query prior tx %s: %w", prior.TxHash, qerr)
		}
		if found {
			signTxLog.Info("idempotent retry: tx already on chain",
				"taskId", in.TaskID, "txHash", prior.TxHash, "height", resp.Height)
			return resultFromTxResponse(resp, prior.AccountNumber, prior.Sequence, in.ChainID, prior.CreatedAt, timePtr(time.Now().UTC())), nil
		}
		if seq <= prior.Sequence {
			// Tx never landed AND the chain's sequence has not moved
			// past the one we used last time — safe to re-sign with
			// the same sequence. Drop the stale checkpoint first.
			signTxLog.Warn("idempotent retry: prior tx absent; re-signing at unchanged sequence",
				"taskId", in.TaskID, "priorTxHash", prior.TxHash, "sequence", seq)
			if derr := cfg.Checkpoints.DeleteCheckpoint(in.TaskID); derr != nil {
				return nil, fmt.Errorf("delete stale checkpoint: %w", derr)
			}
		} else {
			return nil, Terminal(fmt.Errorf(
				"checkpoint reports prior broadcast (hash=%s seq=%d) but chain has no record and sequence has advanced to %d; "+
					"manual investigation required (possible DeliverTx rejection)",
				prior.TxHash, prior.Sequence, seq))
		}
	}

	// Build the unsigned tx. The factory's WithFees() panics on parse
	// error; we already validated the denom up front via checkFeesDenom
	// so a panic here would indicate a bug, not user input.
	_, txCfg := makeSignTxCodec()
	factory := tx.Factory{}.
		WithChainID(in.ChainID).
		WithKeybase(cfg.Keyring).
		WithTxConfig(txCfg).
		WithAccountRetriever(authtypes.AccountRetriever{}).
		WithAccountNumber(accNum).
		WithSequence(seq).
		WithGas(in.Gas).
		WithFees(in.Fees).
		WithMemo(in.Memo).
		WithSignMode(signingtypes.SignMode_SIGN_MODE_DIRECT)

	builder, err := tx.BuildUnsignedTx(factory, in.Msg)
	if err != nil {
		return nil, Terminal(fmt.Errorf("build unsigned tx: %w", err))
	}
	if err := tx.Sign(factory, in.KeyName, builder, true); err != nil {
		return nil, fmt.Errorf("sign tx: %w", err)
	}
	txBytes, err := txCfg.TxEncoder()(builder.GetTx())
	if err != nil {
		return nil, fmt.Errorf("encode tx: %w", err)
	}

	// Compute the deterministic tx hash and persist the checkpoint
	// BEFORE broadcasting. If the process dies between this Save and
	// the next line, the next retry will see the checkpoint, query
	// the chain, and reconcile correctly.
	txHash := strings.ToUpper(hex.EncodeToString(tmhash.Sum(txBytes)))
	broadcastedAt := time.Now().UTC()
	cp := &engine.SignTxCheckpoint{
		TaskID:        in.TaskID,
		TxHash:        txHash,
		Sequence:      seq,
		AccountNumber: accNum,
		ChainID:       in.ChainID,
		CreatedAt:     broadcastedAt,
	}
	if err := cfg.Checkpoints.SaveCheckpoint(cp); err != nil {
		return nil, fmt.Errorf("persist checkpoint: %w", err)
	}

	signTxLog.Info("broadcasting tx",
		"taskId", in.TaskID, "txHash", txHash, "chainId", in.ChainID,
		"sequence", seq, "accountNumber", accNum, "gas", in.Gas, "fees", in.Fees)

	resp, err := tc.BroadcastSync(ctx, txBytes)
	if err != nil {
		return nil, fmt.Errorf("broadcast: %w", err)
	}
	if resp.Code != 0 {
		// CheckTx-level rejection. The tx never entered the mempool, so
		// the checkpoint we just persisted advertises a tx hash that
		// will never appear on-chain — an audit-confusing dangling
		// record. Delete it; an operator who fixes the config and
		// resubmits gets a fresh signing path at the unchanged sequence
		// (LoadCheckpoint returns nil, fall through to re-sign).
		if derr := cfg.Checkpoints.DeleteCheckpoint(in.TaskID); derr != nil {
			signTxLog.Warn("failed to delete checkpoint after CheckTx rejection",
				"taskId", in.TaskID, "err", derr)
		}
		return nil, Terminal(fmt.Errorf("checkTx rejected: code=%d codespace=%q log=%s",
			resp.Code, resp.Codespace, resp.RawLog))
	}

	included, perr := pollForInclusion(ctx, tc, txHash, inclusionPollTimeout, inclusionPollInterval)
	if perr != nil {
		// Caller-driven cancellation: don't synthesize a "completed"
		// result on the truncated task. Engine records this as failed;
		// next Submit with the same TaskID short-circuits via the
		// persisted checkpoint to the chain-query path.
		return nil, perr
	}
	out := resultFromTxResponse(resp, accNum, seq, in.ChainID, broadcastedAt, nil)
	if included != nil {
		now := time.Now().UTC()
		out = resultFromTxResponse(included, accNum, seq, in.ChainID, broadcastedAt, &now)
	}
	return out, nil
}

// validateInput enforces the param shape every sign-tx handler should
// have already enforced; we re-validate here so the helper is safe
// against handlers that forget.
func validateInput(in SignAndBroadcastInput) error {
	if in.ChainID == "" {
		return errors.New("chainId required")
	}
	if in.KeyName == "" {
		return errors.New("keyName required")
	}
	if in.Msg == nil {
		return errors.New("msg required")
	}
	if in.TaskID == "" {
		return errors.New("taskId required (engine should always populate this)")
	}
	if in.Gas == 0 {
		return errors.New("gas required (must be > 0)")
	}
	if in.Fees == "" {
		return errors.New("fees required")
	}
	if err := validateMemo(in.Memo); err != nil {
		return err
	}
	if err := in.Msg.ValidateBasic(); err != nil {
		return fmt.Errorf("msg.ValidateBasic: %w", err)
	}
	return nil
}

// maxMemoBytes matches Cosmos SDK's MaxMemoCharacters consensus cap.
// Memos that exceed this size would be rejected at CheckTx but only after
// the sidecar persisted a checkpoint and signed the tx — refuse at the
// admission boundary to avoid wasted state.
const maxMemoBytes = 256

// validateMemo rejects oversize memos and memos containing non-printable
// or control characters. The memo travels into on-chain event logs and
// the sidecar's task result; an attacker who controls it can otherwise
// inject ANSI sequences into audit pipelines or forge fake audit lines.
func validateMemo(memo string) error {
	if len(memo) > maxMemoBytes {
		return fmt.Errorf("memo length %d exceeds %d bytes", len(memo), maxMemoBytes)
	}
	for _, r := range memo {
		if r == '\t' || r == ' ' {
			continue
		}
		if !unicode.IsPrint(r) {
			return fmt.Errorf("memo contains non-printable character %U", r)
		}
	}
	return nil
}

// checkFeesDenom rejects any coin string whose denom is not "usei".
// This is the seienv `--fees 20sei` latent-bug guard: seid does NOT
// have a "sei" denom, and we never want to surface a parse error
// from deep inside the SDK Factory.WithFees panic path.
func checkFeesDenom(fees string) error {
	coins, err := sdk.ParseCoinsNormalized(fees)
	if err != nil {
		return fmt.Errorf("parse fees %q: %w", fees, err)
	}
	// ParseCoinsNormalized filters zero-amount entries, so "0usei" and
	// non-positive sums normalize to empty Coins. Reject both — a tx
	// with zero fee bypasses any min-gas-price contract the operator
	// thought they were paying.
	if len(coins) == 0 {
		return fmt.Errorf("fees %q resolves to zero or empty coins", fees)
	}
	if !coins.IsAllPositive() {
		return fmt.Errorf("fees %q contains non-positive amounts", fees)
	}
	for _, c := range coins {
		if c.Denom != feeDenom {
			return fmt.Errorf("fees %q: denom %q not permitted (only %q)", fees, c.Denom, feeDenom)
		}
	}
	return nil
}

// guardChainID rejects sign requests whose ChainID does not match BOTH
// the sidecar's SEI_CHAIN_ID env AND the local seid's actual /status
// NodeInfo.Network. The two checks defend against different operator
// errors:
//   - env mismatch: somebody redeployed the sidecar with the wrong env
//     but the pod is still running against the right chain (or v.v.)
//   - /status mismatch: the local seid was rehomed to a different
//     chain and the sidecar env hasn't been updated yet
//
// Both checks must pass before we touch the keyring.
func guardChainID(ctx context.Context, rpcClient *rpc.Client, chainID string) error {
	envChain := os.Getenv("SEI_CHAIN_ID")
	if envChain == "" {
		// Fail-fast: refusing to sign when the env contract is broken
		// is safer than silently signing for the chain we happen to be
		// connected to. The cross-reviewer Kubernetes-specialist
		// scenario "handler that breaks if SEI_CHAIN_ID isn't set" is
		// served by this branch's Terminal error.
		return Terminal(errors.New("SEI_CHAIN_ID not set on sidecar; refusing to sign"))
	}
	if chainID != envChain {
		return Terminal(fmt.Errorf("chain mismatch: params.chainId=%q sidecar.SEI_CHAIN_ID=%q", chainID, envChain))
	}
	if rpcClient == nil {
		return Terminal(errors.New("RPC client not configured; cannot verify /status.NodeInfo.Network"))
	}
	raw, err := rpcClient.Get(ctx, "/status")
	if err != nil {
		// Network/transport problem talking to the local seid — not
		// a chain mismatch per se but we still refuse to sign because
		// we cannot prove the chain identity.
		return fmt.Errorf("query local seid /status: %w", err)
	}
	var status rpc.StatusResult
	if err := json.Unmarshal(raw, &status); err != nil {
		return fmt.Errorf("decode /status: %w", err)
	}
	if status.NodeInfo.Network == "" {
		return Terminal(errors.New("/status.node_info.network is empty; refusing to sign"))
	}
	if status.NodeInfo.Network != chainID {
		return Terminal(fmt.Errorf("chain mismatch: params.chainId=%q node.network=%q", chainID, status.NodeInfo.Network))
	}
	return nil
}

// pollForInclusion polls the local seid /tx?hash=... until the tx is
// reported as included, the context is cancelled, or timeout elapses.
// Distinguishes three outcomes:
//   - tx found on chain: (resp, nil)
//   - timeout elapsed:   (nil,  nil)   — caller treats as "broadcast OK,
//                                         inclusion-confirmation deferred"
//   - ctx cancelled:     (nil,  ctx.Err()) — caller surfaces as failure so
//                                            the engine doesn't record a
//                                            truncated task as completed
func pollForInclusion(ctx context.Context, tc txClient, txHash string, timeout, interval time.Duration) (*sdk.TxResponse, error) {
	deadline := time.Now().Add(timeout)
	for {
		// Check cancellation + deadline BEFORE each network call so a
		// CLI-driven --timeout actually bounds the wait — the QueryTx
		// transport can itself stall on a hung seid.
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, nil
		}
		resp, found, err := tc.QueryTx(ctx, txHash)
		if err == nil && found {
			return resp, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// resultFromTxResponse normalizes either a /tx?hash=... result or a
// fresh BroadcastTxSync response into the helper's public shape.
// accountNumber/sequence/chainID come from the signing site; the
// remote responses do not echo them.
func resultFromTxResponse(resp *sdk.TxResponse, accNum, seq uint64, chainID string, broadcastedAt time.Time, includedAt *time.Time) *SignAndBroadcastResult {
	return &SignAndBroadcastResult{
		TxHash:        resp.TxHash,
		Height:        resp.Height,
		Code:          resp.Code,
		Codespace:     resp.Codespace,
		RawLog:        resp.RawLog,
		GasWanted:     resp.GasWanted,
		GasUsed:       resp.GasUsed,
		Sequence:      seq,
		AccountNumber: accNum,
		ChainID:       chainID,
		BroadcastedAt: broadcastedAt,
		IncludedAt:    includedAt,
	}
}

func timePtr(t time.Time) *time.Time { return &t }

// newSignTxInterfaceRegistry registers only the proto interfaces sign-tx
// handlers need today (gov v1beta1 + auth + bank + staking + crypto +
// upgrade). Adding more pulls in transitive dependencies (notably x/wasm
// via x/evm) that break CGO_ENABLED=0 builds.
//
// Upgrade types are registered here so that #163-B's MsgSubmitProposal-
// wrapping-SoftwareUpgradeProposal handler can pack the proposal Content
// without rebuilding a codec.
func newSignTxInterfaceRegistry() codectypes.InterfaceRegistry {
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	authtypes.RegisterInterfaces(registry)
	banktypes.RegisterInterfaces(registry)
	stakingtypes.RegisterInterfaces(registry)
	govtypes.RegisterInterfaces(registry)
	upgradetypes.RegisterInterfaces(registry)
	return registry
}

// makeSignTxCodec is a convenience wrapper used by the inner signing
// loop where we only need a TxConfig (and a Codec for handlers that
// want to marshal their result schema). Sharing the registry across
// callers in one call is fine because InterfaceRegistry is read-only
// after registration.
func makeSignTxCodec() (codec.Codec, client.TxConfig) {
	registry := newSignTxInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	txCfg := authtx.NewTxConfig(cdc, authtx.DefaultSignModes)
	return cdc, txCfg
}

