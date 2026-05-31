package serve

import (
	"fmt"

	"github.com/holiman/uint256"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// HeaderToRLP / HeaderFromRLP are the FAITHFUL, fork-aware header wire for the
// serving RPC. They encode the exact canonical field list keccak256-hashed by
// block.Header.rlpHash (15 mandatory fields + the fork-cumulative optional tail:
// BaseFee, WithdrawalsHash, {BlobGasUsed, ExcessBlobGas, ParentBeaconRoot},
// RequestsHash), so a client RLP-decoding the wire reconstructs a Header whose
// Hash() recomputes to the real Ethereum block hash — INDEPENDENTLY, without
// trusting a server-supplied hash. This is what the parentHash trust chain needs.
//
// The proto block.Header.Marshal path cannot be used here: it panics on a nil
// BaseFee (pre-London) and, on decode, maps nil→&0, which would make rlpHash
// include a phantom zero baseFee → wrong hash → broken chain. RLP is the only
// transport that preserves the fork-aware optional-field presence exactly.
func HeaderToRLP(h *block.Header) ([]byte, error) {
	if h == nil || h.Number == nil || h.Difficulty == nil {
		return nil, fmt.Errorf("serve: header missing Number/Difficulty")
	}
	enc := []interface{}{
		h.ParentHash, h.UncleHash, h.Coinbase, h.Root, h.TxHash, h.ReceiptHash,
		h.Bloom, h.Difficulty, h.Number, h.GasLimit, h.GasUsed, h.Time, h.Extra,
		h.MixDigest, h.Nonce,
	}
	if h.BaseFee != nil {
		enc = append(enc, h.BaseFee)
	}
	if h.WithdrawalsHash != nil {
		enc = append(enc, h.WithdrawalsHash)
	}
	if h.BlobGasUsed != nil {
		enc = append(enc, h.BlobGasUsed)
	}
	if h.ExcessBlobGas != nil {
		enc = append(enc, h.ExcessBlobGas)
	}
	if h.ParentBeaconRoot != nil {
		enc = append(enc, h.ParentBeaconRoot)
	}
	if h.RequestsHash != nil {
		enc = append(enc, h.RequestsHash)
	}
	return rlp.EncodeToBytes(enc)
}

// HeaderFromRLP decodes the canonical header RLP back into a *block.Header. The
// optional tail is fork-cumulative, so the field count disambiguates which
// optionals are present (15=pre-London, 16=+BaseFee, 17=+WithdrawalsHash,
// 20=+Cancun trio, 21=+RequestsHash).
func HeaderFromRLP(b []byte) (*block.Header, error) {
	var f []rlp.RawValue
	if err := rlp.DecodeBytes(b, &f); err != nil {
		return nil, fmt.Errorf("serve: header rlp: %w", err)
	}
	if len(f) < 15 {
		return nil, fmt.Errorf("serve: header has %d fields, need >= 15", len(f))
	}
	h := new(block.Header)
	get := func(i int, v interface{}) error { return rlp.DecodeBytes(f[i], v) }
	if err := firstErr(
		get(0, &h.ParentHash), get(1, &h.UncleHash), get(2, &h.Coinbase),
		get(3, &h.Root), get(4, &h.TxHash), get(5, &h.ReceiptHash),
		get(6, &h.Bloom), get(9, &h.GasLimit), get(10, &h.GasUsed),
		get(11, &h.Time), get(12, &h.Extra), get(13, &h.MixDigest), get(14, &h.Nonce),
	); err != nil {
		return nil, err
	}
	var diff, num uint256.Int
	if err := firstErr(get(7, &diff), get(8, &num)); err != nil {
		return nil, err
	}
	h.Difficulty, h.Number = &diff, &num

	if len(f) >= 16 {
		var bf uint256.Int
		if err := get(15, &bf); err != nil {
			return nil, err
		}
		h.BaseFee = &bf
	}
	if len(f) >= 17 {
		var wh types.Hash
		if err := get(16, &wh); err != nil {
			return nil, err
		}
		h.WithdrawalsHash = &wh
	}
	if len(f) >= 20 {
		var bgu, ebg uint64
		var pbr types.Hash
		if err := firstErr(get(17, &bgu), get(18, &ebg), get(19, &pbr)); err != nil {
			return nil, err
		}
		h.BlobGasUsed, h.ExcessBlobGas, h.ParentBeaconRoot = &bgu, &ebg, &pbr
	}
	if len(f) >= 21 {
		var rh types.Hash
		if err := get(20, &rh); err != nil {
			return nil, err
		}
		h.RequestsHash = &rh
	}
	return h, nil
}

func firstErr(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}
