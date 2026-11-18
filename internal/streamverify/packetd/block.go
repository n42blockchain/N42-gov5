// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package packetd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/params"
)

// ErrNotYet is returned by FetchBlock for a block the node does not have yet.
var ErrNotYet = errors.New("packetd: block not available yet")

// PermanentError marks a failure that retrying the same block cannot fix
// (the node's header does not hash to its own block hash, a transaction gov5
// cannot decode, an execution that disagrees with the header). The follower
// drops such a block with an error and moves on; anything else is retried.
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

func permanent(format string, a ...any) error {
	return &PermanentError{Err: fmt.Errorf(format, a...)}
}

// IsPermanent reports whether err is a PermanentError.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

// Block is a block of the followed node as gov5 types.
type Block struct {
	Hash   types.Hash
	Header *block.Header
	Txs    transaction.Transactions
	// Source names how the header and transactions were obtained
	// ("raw-block", "raw-header", "json-header").
	Source string
}

// rpcBlock is the part of eth_getBlockByNumber (without transaction bodies)
// the follower uses, plus every header field for the JSON fallback.
type rpcBlock struct {
	Hash         string            `json:"hash"`
	Transactions []json.RawMessage `json:"transactions"`

	ParentHash          string  `json:"parentHash"`
	Sha3Uncles          string  `json:"sha3Uncles"`
	Miner               string  `json:"miner"`
	StateRoot           string  `json:"stateRoot"`
	TransactionsRoot    string  `json:"transactionsRoot"`
	ReceiptsRoot        string  `json:"receiptsRoot"`
	LogsBloom           string  `json:"logsBloom"`
	Difficulty          string  `json:"difficulty"`
	Number              string  `json:"number"`
	GasLimit            string  `json:"gasLimit"`
	GasUsed             string  `json:"gasUsed"`
	Timestamp           string  `json:"timestamp"`
	ExtraData           string  `json:"extraData"`
	MixHash             string  `json:"mixHash"`
	Nonce               string  `json:"nonce"`
	BaseFeePerGas       *string `json:"baseFeePerGas"`
	WithdrawalsRoot     *string `json:"withdrawalsRoot"`
	BlobGasUsed         *string `json:"blobGasUsed"`
	ExcessBlobGas       *string `json:"excessBlobGas"`
	ParentBeaconRoot    *string `json:"parentBeaconBlockRoot"`
	RequestsHash        *string `json:"requestsHash"`
	BlockAccessListHash *string `json:"blockAccessListHash"`
	SlotNumber          *string `json:"slotNumber"`
	MobileRegistryRoot  *string `json:"mobileRegistryRoot"`
}

// BlockNumber returns the node's head (eth_blockNumber).
func BlockNumber(ctx context.Context, c *Client) (uint64, error) {
	var s string
	if err := c.Call(ctx, &s, "eth_blockNumber"); err != nil {
		return 0, err
	}
	return hexutil.DecodeUint64(s)
}

// BlockHashByNumber returns the canonical hash at n, or ErrNotYet.
func BlockHashByNumber(ctx context.Context, c *Client, n uint64) (types.Hash, error) {
	var b *rpcBlock
	if err := c.Call(ctx, &b, "eth_getBlockByNumber", hexutil.EncodeUint64(n), false); err != nil {
		return types.Hash{}, err
	}
	if b == nil || b.Hash == "" {
		return types.Hash{}, ErrNotYet
	}
	return types.HexToHash(b.Hash), nil
}

// FetchBlock returns block n of the node as gov5 types: the header whose
// Hash() is the node's block hash (checked; a mismatch is permanent) and the
// transactions with their senders recovered and set (a gov5 packet carries
// `from`, which the phone trusts).
//
// The header and transactions come from debug_getRawBlock when the node
// serves the debug namespace; otherwise the header from debug_getRawHeader,
// or failing that from the eth_getBlockByNumber JSON, and each transaction
// from eth_getRawTransactionByBlockHashAndIndex.
func FetchBlock(ctx context.Context, c *Client, cfg *params.ChainConfig, n uint64) (*Block, error) {
	var jb *rpcBlock
	if err := c.Call(ctx, &jb, "eth_getBlockByNumber", hexutil.EncodeUint64(n), false); err != nil {
		return nil, err
	}
	if jb == nil || jb.Hash == "" {
		return nil, ErrNotYet
	}
	out := &Block{Hash: types.HexToHash(jb.Hash)}
	var rawTxs [][]byte

	var raw hexutil.Bytes
	err := c.Call(ctx, &raw, "debug_getRawBlock", out.Hash.Hex())
	switch {
	case err == nil && len(raw) > 0:
		hdr, txs, derr := decodeEthBlock(raw)
		if derr != nil {
			return nil, permanent("block %d %v: raw block: %v", n, out.Hash, derr)
		}
		out.Header, rawTxs, out.Source = hdr, txs, "raw-block"
	case err != nil && !IsMethodNotFound(err):
		return nil, fmt.Errorf("debug_getRawBlock %d: %w", n, err)
	default:
		var rawHdr hexutil.Bytes
		herr := c.Call(ctx, &rawHdr, "debug_getRawHeader", out.Hash.Hex())
		switch {
		case herr == nil && len(rawHdr) > 0:
			var h block.Header
			if derr := rlp.DecodeBytes(rawHdr, &h); derr != nil {
				return nil, permanent("block %d %v: raw header: %v", n, out.Hash, derr)
			}
			out.Header, out.Source = &h, "raw-header"
		case herr != nil && !IsMethodNotFound(herr):
			return nil, fmt.Errorf("debug_getRawHeader %d: %w", n, herr)
		default:
			h, jerr := headerFromJSON(jb)
			if jerr != nil {
				return nil, permanent("block %d %v: json header: %v", n, out.Hash, jerr)
			}
			out.Header, out.Source = h, "json-header"
		}
		rawTxs = make([][]byte, len(jb.Transactions))
		b := make([]BatchElem, len(jb.Transactions))
		res := make([]hexutil.Bytes, len(jb.Transactions))
		for i := range b {
			b[i] = BatchElem{Method: "eth_getRawTransactionByBlockHashAndIndex", Params: []any{out.Hash.Hex(), hexutil.EncodeUint64(uint64(i))}, Result: &res[i]}
		}
		if err := c.Batch(ctx, b); err != nil {
			return nil, fmt.Errorf("raw transactions of %d: %w", n, err)
		}
		for i := range b {
			if b[i].Err != nil || len(res[i]) == 0 {
				return nil, fmt.Errorf("raw transaction %d of block %d: %v", i, n, b[i].Err)
			}
			rawTxs[i] = res[i]
		}
	}

	if got := out.Header.Hash(); got != out.Hash {
		return nil, permanent("block %d: the %s header hashes to %v, the node reports %v", n, out.Source, got, out.Hash)
	}
	if out.Header.Number == nil || out.Header.Number.Uint64() != n {
		return nil, permanent("block %d: header number %v", n, out.Header.Number)
	}
	txs, err := decodeTxs(cfg, out.Header, rawTxs)
	if err != nil {
		return nil, &PermanentError{Err: fmt.Errorf("block %d %v: %w", n, out.Hash, err)}
	}
	out.Txs = txs
	return out, nil
}

// decodeEthBlock splits an Ethereum-form block RLP ([header, txs, ommers,
// withdrawals?, ...]) into a gov5 header and the transactions' EIP-2718
// encodings (a legacy transaction is an embedded list, a typed one a string).
func decodeEthBlock(raw []byte) (*block.Header, [][]byte, error) {
	s := rlp.NewStream(bytes.NewReader(raw), uint64(len(raw)))
	if _, err := s.List(); err != nil {
		return nil, nil, err
	}
	hdrRaw, err := s.Raw()
	if err != nil {
		return nil, nil, fmt.Errorf("header: %w", err)
	}
	var h block.Header
	if err := rlp.DecodeBytes(hdrRaw, &h); err != nil {
		return nil, nil, fmt.Errorf("header: %w", err)
	}
	if _, err := s.List(); err != nil {
		return nil, nil, fmt.Errorf("transactions: %w", err)
	}
	var txs [][]byte
	for {
		kind, _, err := s.Kind()
		if errors.Is(err, rlp.EOL) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("transaction %d: %w", len(txs), err)
		}
		var enc []byte
		if kind == rlp.List {
			enc, err = s.Raw()
		} else {
			enc, err = s.Bytes()
		}
		if err != nil {
			return nil, nil, fmt.Errorf("transaction %d: %w", len(txs), err)
		}
		txs = append(txs, enc)
	}
	if err := s.ListEnd(); err != nil {
		return nil, nil, fmt.Errorf("transactions: %w", err)
	}
	return &h, txs, nil
}

// decodeTxs decodes EIP-2718 encodings and recovers and sets each sender
// with the block's signer, as gov5's pool does at admission.
func decodeTxs(cfg *params.ChainConfig, h *block.Header, raw [][]byte) (transaction.Transactions, error) {
	signer := transaction.MakeSignerWithTimestamp(cfg, h.Number.ToBig(), h.Time)
	txs := make(transaction.Transactions, len(raw))
	for i, enc := range raw {
		tx, err := transaction.DecodeEthereumTransaction(enc)
		if err != nil {
			return nil, fmt.Errorf("transaction %d: %w", i, err)
		}
		from, err := transaction.Sender(signer, tx)
		if err != nil {
			return nil, fmt.Errorf("transaction %d sender: %w", i, err)
		}
		tx.SetFrom(from)
		txs[i] = tx
	}
	return txs, nil
}

// headerFromJSON builds a gov5 header from eth_getBlockByNumber JSON, the
// last resort when the node serves no raw header. The caller checks its hash.
func headerFromJSON(b *rpcBlock) (*block.Header, error) {
	var firstErr error
	q := func(s string) *big.Int {
		v, err := parseQuantity(s)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		if v == nil {
			v = new(big.Int)
		}
		return v
	}
	u256 := func(s string) *uint256.Int {
		v, overflow := uint256.FromBig(q(s))
		if overflow && firstErr == nil {
			firstErr = fmt.Errorf("%q overflows 256 bits", s)
		}
		return v
	}
	u64 := func(s string) uint64 { return q(s).Uint64() }
	data := func(s string) []byte {
		v, err := hexutil.Decode(s)
		if err != nil && s != "0x" && firstErr == nil {
			firstErr = fmt.Errorf("%q: %w", s, err)
		}
		return v
	}
	hashPtr := func(s *string) *types.Hash {
		if s == nil {
			return nil
		}
		h := types.HexToHash(*s)
		return &h
	}
	u64Ptr := func(s *string) *uint64 {
		if s == nil {
			return nil
		}
		v := u64(*s)
		return &v
	}
	h := &block.Header{
		ParentHash:          types.HexToHash(b.ParentHash),
		UncleHash:           types.HexToHash(b.Sha3Uncles),
		Coinbase:            types.HexToAddress(b.Miner),
		Root:                types.HexToHash(b.StateRoot),
		TxHash:              types.HexToHash(b.TransactionsRoot),
		ReceiptHash:         types.HexToHash(b.ReceiptsRoot),
		Bloom:               block.BytesToBloom(data(b.LogsBloom)),
		Difficulty:          u256(b.Difficulty),
		Number:              u256(b.Number),
		GasLimit:            u64(b.GasLimit),
		GasUsed:             u64(b.GasUsed),
		Time:                u64(b.Timestamp),
		Extra:               data(b.ExtraData),
		MixDigest:           types.HexToHash(b.MixHash),
		WithdrawalsHash:     hashPtr(b.WithdrawalsRoot),
		BlobGasUsed:         u64Ptr(b.BlobGasUsed),
		ExcessBlobGas:       u64Ptr(b.ExcessBlobGas),
		ParentBeaconRoot:    hashPtr(b.ParentBeaconRoot),
		RequestsHash:        hashPtr(b.RequestsHash),
		BlockAccessListHash: hashPtr(b.BlockAccessListHash),
		SlotNumber:          u64Ptr(b.SlotNumber),
		MobileRegistryRoot:  hashPtr(b.MobileRegistryRoot),
	}
	if b.BaseFeePerGas != nil {
		h.BaseFee = u256(*b.BaseFeePerGas)
	}
	if n := data(b.Nonce); len(n) == 8 {
		copy(h.Nonce[:], n)
	} else if strings.TrimPrefix(b.Nonce, "0x") != "" {
		if firstErr == nil {
			firstErr = fmt.Errorf("nonce %q is not 8 bytes", b.Nonce)
		}
	}
	if len(h.Extra) == 0 {
		h.Extra = []byte{}
	}
	return h, firstErr
}
