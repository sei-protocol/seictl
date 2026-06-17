package shadow

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
)

// StateReader reads logical EVM state at a height. Its method set matches
// go-ethereum's *ethclient.Client, so a real client satisfies it directly; the
// shadow and canonical sides are two instances. A nil blockNumber means latest.
type StateReader interface {
	StorageAt(ctx context.Context, account common.Address, key common.Hash, blockNumber *big.Int) ([]byte, error)
	CodeAt(ctx context.Context, account common.Address, blockNumber *big.Int) ([]byte, error)
	NonceAt(ctx context.Context, account common.Address, blockNumber *big.Int) (uint64, error)
}

// TouchedAccount is the set of state a block touched for one address: the
// storage slots to compare, and whether the account's code and nonce should be
// checked. A KeySource produces these per block (e.g. from a prestate trace).
type TouchedAccount struct {
	Addr       common.Address
	Slots      []common.Hash
	CheckCode  bool
	CheckNonce bool
}

// KeySource yields the accounts (and their slots) a block touched, so Layer 2
// compares exactly the state real transactions read or wrote at that height.
type KeySource interface {
	TouchedAccounts(ctx context.Context, height int64) ([]TouchedAccount, error)
}

// compareState reads each touched key's logical value from both chains at the
// given height and records every mismatch. It fails closed: a read error on any
// key aborts with that error rather than reporting a partial (and so falsely
// clean) result.
func compareState(ctx context.Context, height int64, touched []TouchedAccount, shadow, canonical StateReader) (*Layer2Result, error) {
	blockNum := big.NewInt(height)
	res := &Layer2Result{}

	for _, acct := range touched {
		res.AccountsChecked++

		for _, slot := range acct.Slots {
			res.KeysChecked++
			s, err := shadow.StorageAt(ctx, acct.Addr, slot, blockNum)
			if err != nil {
				return nil, fmt.Errorf("shadow storage %s/%s: %w", acct.Addr.Hex(), slot.Hex(), err)
			}
			c, err := canonical.StorageAt(ctx, acct.Addr, slot, blockNum)
			if err != nil {
				return nil, fmt.Errorf("canonical storage %s/%s: %w", acct.Addr.Hex(), slot.Hex(), err)
			}
			if !bytes.Equal(leftPad32(s), leftPad32(c)) {
				res.Divergences = append(res.Divergences, StateDivergence{
					Kind: "storage", Addr: acct.Addr.Hex(), Slot: slot.Hex(),
					Shadow: hexStr(s), Canonical: hexStr(c),
				})
			}
		}

		if acct.CheckCode {
			res.KeysChecked++
			s, err := shadow.CodeAt(ctx, acct.Addr, blockNum)
			if err != nil {
				return nil, fmt.Errorf("shadow code %s: %w", acct.Addr.Hex(), err)
			}
			c, err := canonical.CodeAt(ctx, acct.Addr, blockNum)
			if err != nil {
				return nil, fmt.Errorf("canonical code %s: %w", acct.Addr.Hex(), err)
			}
			if !bytes.Equal(s, c) {
				res.Divergences = append(res.Divergences, StateDivergence{
					Kind: "code", Addr: acct.Addr.Hex(), Shadow: hexStr(s), Canonical: hexStr(c),
				})
			}
		}

		if acct.CheckNonce {
			res.KeysChecked++
			s, err := shadow.NonceAt(ctx, acct.Addr, blockNum)
			if err != nil {
				return nil, fmt.Errorf("shadow nonce %s: %w", acct.Addr.Hex(), err)
			}
			c, err := canonical.NonceAt(ctx, acct.Addr, blockNum)
			if err != nil {
				return nil, fmt.Errorf("canonical nonce %s: %w", acct.Addr.Hex(), err)
			}
			if s != c {
				res.Divergences = append(res.Divergences, StateDivergence{
					Kind: "nonce", Addr: acct.Addr.Hex(),
					Shadow: fmt.Sprintf("%d", s), Canonical: fmt.Sprintf("%d", c),
				})
			}
		}
	}

	return res, nil
}

// leftPad32 normalizes a storage value to 32 big-endian bytes so a trimmed RPC
// result compares equal to a zero-padded one. Values longer than 32 bytes are
// returned unchanged so the mismatch surfaces.
func leftPad32(b []byte) []byte {
	if len(b) >= 32 {
		return b
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func hexStr(b []byte) string { return "0x" + hex.EncodeToString(b) }
