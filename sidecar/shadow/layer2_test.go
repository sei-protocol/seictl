package shadow

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/sei-protocol/seictl/sidecar/rpc"
)

// mockKeySource returns a fixed set of touched accounts (or an error).
type mockKeySource struct {
	accts []TouchedAccount
	err   error
}

func (m mockKeySource) TouchedAccounts(_ context.Context, _ int64) ([]TouchedAccount, error) {
	return m.accts, m.err
}

// mockState is a controllable StateReader: maps hold the value each side returns
// for a key, errOn forces a read error for a kind so the fail-closed path can be
// exercised.
type mockState struct {
	storage map[string][]byte // addr.Hex()|slot.Hex() -> value
	code    map[string][]byte // addr.Hex() -> bytecode
	nonce   map[string]uint64 // addr.Hex() -> nonce
	errOn   string            // "storage" | "code" | "nonce"
}

func newMockState() *mockState {
	return &mockState{
		storage: map[string][]byte{},
		code:    map[string][]byte{},
		nonce:   map[string]uint64{},
	}
}

func (m *mockState) StorageAt(_ context.Context, account common.Address, key common.Hash, _ *big.Int) ([]byte, error) {
	if m.errOn == "storage" {
		return nil, errors.New("injected storage error")
	}
	return m.storage[account.Hex()+"|"+key.Hex()], nil
}

func (m *mockState) CodeAt(_ context.Context, account common.Address, _ *big.Int) ([]byte, error) {
	if m.errOn == "code" {
		return nil, errors.New("injected code error")
	}
	return m.code[account.Hex()], nil
}

func (m *mockState) NonceAt(_ context.Context, account common.Address, _ *big.Int) (uint64, error) {
	if m.errOn == "nonce" {
		return 0, errors.New("injected nonce error")
	}
	return m.nonce[account.Hex()], nil
}

var (
	testAddr = common.HexToAddress("0x00000000000000000000000000000000000000aa")
	testSlot = common.HexToHash("0x01")
)

func storageKey(a common.Address, s common.Hash) string { return a.Hex() + "|" + s.Hex() }

func TestCompareState_AllMatch(t *testing.T) {
	shadow, canonical := newMockState(), newMockState()
	shadow.storage[storageKey(testAddr, testSlot)] = leftPad32([]byte{0x2a})
	canonical.storage[storageKey(testAddr, testSlot)] = []byte{0x2a} // trimmed; must normalize-equal
	shadow.code[testAddr.Hex()] = []byte{0x60, 0x60}
	canonical.code[testAddr.Hex()] = []byte{0x60, 0x60}
	shadow.nonce[testAddr.Hex()] = 5
	canonical.nonce[testAddr.Hex()] = 5

	touched := []TouchedAccount{{Addr: testAddr, Slots: []common.Hash{testSlot}, CheckCode: true, CheckNonce: true}}
	res, err := compareState(context.Background(), 100, touched, shadow, canonical)
	if err != nil {
		t.Fatalf("compareState: %v", err)
	}
	if len(res.Divergences) != 0 {
		t.Errorf("expected no divergences, got %+v", res.Divergences)
	}
	if res.AccountsChecked != 1 || res.KeysChecked != 3 {
		t.Errorf("counts: accounts=%d keys=%d, want 1/3", res.AccountsChecked, res.KeysChecked)
	}
}

func TestCompareState_Mismatches(t *testing.T) {
	shadow, canonical := newMockState(), newMockState()
	shadow.storage[storageKey(testAddr, testSlot)] = leftPad32([]byte{0x2a})
	canonical.storage[storageKey(testAddr, testSlot)] = leftPad32([]byte{0x2b}) // storage differs
	shadow.code[testAddr.Hex()] = []byte{0x60}
	canonical.code[testAddr.Hex()] = []byte{0x61} // code differs
	shadow.nonce[testAddr.Hex()] = 7
	canonical.nonce[testAddr.Hex()] = 8 // nonce differs

	touched := []TouchedAccount{{Addr: testAddr, Slots: []common.Hash{testSlot}, CheckCode: true, CheckNonce: true}}
	res, err := compareState(context.Background(), 100, touched, shadow, canonical)
	if err != nil {
		t.Fatalf("compareState: %v", err)
	}
	if len(res.Divergences) != 3 {
		t.Fatalf("expected 3 divergences, got %d: %+v", len(res.Divergences), res.Divergences)
	}
	kinds := map[string]bool{}
	for _, d := range res.Divergences {
		kinds[d.Kind] = true
		if d.Addr != testAddr.Hex() {
			t.Errorf("divergence addr = %s, want %s", d.Addr, testAddr.Hex())
		}
	}
	for _, want := range []string{"storage", "code", "nonce"} {
		if !kinds[want] {
			t.Errorf("missing %s divergence", want)
		}
	}
}

func TestCompareState_FailsClosed(t *testing.T) {
	shadow, canonical := newMockState(), newMockState()
	canonical.errOn = "storage"
	touched := []TouchedAccount{{Addr: testAddr, Slots: []common.Hash{testSlot}}}
	if _, err := compareState(context.Background(), 100, touched, shadow, canonical); err == nil {
		t.Error("expected fail-closed error when a side cannot be read")
	}
}

// End-to-end through CompareBlock: migration mode (AppHash expected to differ),
// Layer 1 receipts match, but Layer 2 logical state diverges -> DivergenceLayer 2.
func TestCompareBlock_Layer2_StateDivergence(t *testing.T) {
	txs := []rpc.TxResult{
		{Code: 0, GasUsed: "100", GasWanted: "200", Log: "ok", Events: json.RawMessage(`[]`)},
	}
	shadowSrv := rpcServer("SHADOW_APPHASH", "SAME_RESULTS", txs)
	defer shadowSrv.Close()
	canonicalSrv := rpcServer("CANON_APPHASH", "SAME_RESULTS", txs)
	defer canonicalSrv.Close()

	shadowState, canonState := newMockState(), newMockState()
	shadowState.storage[storageKey(testAddr, testSlot)] = leftPad32([]byte{0x01})
	canonState.storage[storageKey(testAddr, testSlot)] = leftPad32([]byte{0x02})
	ks := mockKeySource{accts: []TouchedAccount{{Addr: testAddr, Slots: []common.Hash{testSlot}}}}

	comp := NewComparator(shadowSrv.URL, canonicalSrv.URL,
		WithMigrationMode(), WithLayer2(shadowState, canonState, ks))
	result, err := comp.CompareBlock(context.Background(), 100)
	if err != nil {
		t.Fatalf("CompareBlock: %v", err)
	}

	if result.Match {
		t.Error("expected divergence: Layer 2 logical state differs")
	}
	if result.DivergenceLayer == nil || *result.DivergenceLayer != 2 {
		t.Errorf("expected divergence layer 2, got %v", result.DivergenceLayer)
	}
	if result.Layer2 == nil || len(result.Layer2.Divergences) != 1 {
		t.Errorf("expected 1 Layer2 divergence, got %+v", result.Layer2)
	}
}

// When Layer 2 logical state matches, a migration shadow is a clean match even
// though its AppHash differs (Layer 0) by design.
func TestCompareBlock_Layer2_CleanMatch(t *testing.T) {
	txs := []rpc.TxResult{
		{Code: 0, GasUsed: "100", GasWanted: "200", Log: "ok", Events: json.RawMessage(`[]`)},
	}
	shadowSrv := rpcServer("SHADOW_APPHASH", "SAME_RESULTS", txs)
	defer shadowSrv.Close()
	canonicalSrv := rpcServer("CANON_APPHASH", "SAME_RESULTS", txs)
	defer canonicalSrv.Close()

	shadowState, canonState := newMockState(), newMockState()
	shadowState.storage[storageKey(testAddr, testSlot)] = leftPad32([]byte{0x42})
	canonState.storage[storageKey(testAddr, testSlot)] = leftPad32([]byte{0x42})
	ks := mockKeySource{accts: []TouchedAccount{{Addr: testAddr, Slots: []common.Hash{testSlot}}}}

	comp := NewComparator(shadowSrv.URL, canonicalSrv.URL,
		WithMigrationMode(), WithLayer2(shadowState, canonState, ks))
	result, err := comp.CompareBlock(context.Background(), 100)
	if err != nil {
		t.Fatalf("CompareBlock: %v", err)
	}

	if !result.Match {
		t.Errorf("expected clean match, got divergence at layer %v", result.DivergenceLayer)
	}
	if result.Layer2 == nil || result.Layer2.AccountsChecked != 1 {
		t.Errorf("expected Layer2 populated with 1 account checked, got %+v", result.Layer2)
	}
}
