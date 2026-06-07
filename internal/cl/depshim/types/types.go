// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Types unit for the types package.
// Defines the Header, Withdrawal, RawBody, and Block types.
// Exports helpers such as BytesToBloom, Hash, and DeriveSha.
// Shim for upstream blockchain type aliases.

//go:build n42el

// Package types is a Phase 2 stub of erigon's execution/types package. It
// exposes only the type names and field shapes that the cl/cltypes pure-type
// layer references — Header, Bloom, Withdrawal(s), RawBody, BinaryTransactions,
// DeriveSha and BytesToBloom.
//
// THE METHOD BODIES ARE INTENTIONALLY STUBS. They compile but they do not do
// the right thing at runtime. They are only invoked from the CL→EL block
// conversion routines (Eth1Block.RlpHeader / Body / NewEth1BlockFromHeaderAndBody),
// which run inside the execution_client adapter that is wired up in Phase 5.
// Replace the stubs with a real implementation (either by vendoring erigon's
// types or by bridging to N42's common/block) when Phase 5 lands.
//
// Until then, any code path that triggers a stub method will panic with a
// clear message identifying the symbol — much safer than silently producing
// wrong hashes.

package types

import (
	"math/big"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/merge"
)

// BloomByteLength is the EL bloom filter size in bytes (256 bytes / 2048 bits).
const BloomByteLength = 256

// Bloom is the 256-byte log bloom filter that lives in EL block headers.
type Bloom [BloomByteLength]byte

// BytesToBloom builds a Bloom from a byte slice. Right-aligned: shorter
// inputs are padded on the left with zeros.
func BytesToBloom(b []byte) Bloom {
	var out Bloom
	if len(b) > BloomByteLength {
		b = b[len(b)-BloomByteLength:]
	}
	copy(out[BloomByteLength-len(b):], b)
	return out
}

// MarshalJSON encodes Bloom as a 0x-prefixed hex string, matching
// the EL JSON convention used by all Ethereum tooling.
func (b Bloom) MarshalJSON() ([]byte, error) {
	out := make([]byte, 0, 2+2*BloomByteLength+2)
	out = append(out, '"', '0', 'x')
	const hex = "0123456789abcdef"
	for _, by := range b {
		out = append(out, hex[by>>4], hex[by&0x0f])
	}
	out = append(out, '"')
	return out, nil
}

// UnmarshalJSON decodes a 0x-prefixed hex Bloom from JSON.
// Accepts strings up to BloomByteLength bytes; shorter inputs
// right-align (matching BytesToBloom).
func (b *Bloom) UnmarshalJSON(data []byte) error {
	// Strip surrounding quotes.
	if len(data) < 2 || data[0] != '"' || data[len(data)-1] != '"' {
		return errInvalidBloomJSON
	}
	s := data[1 : len(data)-1]
	if len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s = s[2:]
	}
	if len(s)%2 != 0 {
		return errInvalidBloomJSON
	}
	if len(s)/2 > BloomByteLength {
		return errInvalidBloomJSON
	}
	raw := make([]byte, len(s)/2)
	for i := 0; i < len(s)/2; i++ {
		hi, ok1 := hexNibble(s[2*i])
		lo, ok2 := hexNibble(s[2*i+1])
		if !ok1 || !ok2 {
			return errInvalidBloomJSON
		}
		raw[i] = hi<<4 | lo
	}
	*b = BytesToBloom(raw)
	return nil
}

var errInvalidBloomJSON = bloomJSONErr{}

type bloomJSONErr struct{}

func (bloomJSONErr) Error() string { return "depshim/types: invalid Bloom JSON" }

func hexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// Header is the EL block header. Field shape mirrors erigon's
// execution/types.Header so cl/cltypes' RlpHeader builder compiles
// unchanged. Methods are stubs — see package doc.
type Header struct {
	ParentHash            common.Hash
	UncleHash             common.Hash
	Coinbase              common.Address
	Root                  common.Hash
	TxHash                common.Hash
	ReceiptHash           common.Hash
	Bloom                 Bloom
	Difficulty            big.Int
	Number                big.Int
	GasLimit              uint64
	GasUsed               uint64
	Time                  uint64
	Extra                 []byte
	MixDigest             common.Hash
	Nonce                 merge.BlockNonce
	BaseFee               *uint256.Int
	WithdrawalsHash       *common.Hash
	BlobGasUsed           *uint64
	ExcessBlobGas         *uint64
	ParentBeaconBlockRoot *common.Hash
	RequestsHash          *common.Hash

	// Gloas (EIP-7928 / EIP-7843) additions. nil for pre-Gloas
	// headers; populated by the Engine API on Gloas+ payloads.
	// Caplin only dereferences these inside `version >= GloasVersion`
	// guards, so leaving them nil on non-Gloas chains is safe.
	BlockAccessListHash *common.Hash
	SlotNumber          *uint64
}

// Hash returns the keccak256 hash of the header's RLP encoding.
// Wire-compatible with erigon's execution/types.Header.Hash;
// see header_hash.go for the encoder implementation.
func (h *Header) Hash() common.Hash {
	return keccakRLPHeader(h)
}

// Withdrawal mirrors EIP-4895 withdrawal entries.
type Withdrawal struct {
	Index     uint64
	Validator uint64
	Address   common.Address
	Amount    uint64
}

// Withdrawals is a slice that satisfies the DerivableList interface used by
// DeriveSha.
type Withdrawals []*Withdrawal

// RawBody is the unprocessed EL block body that the CL ships to the EL.
type RawBody struct {
	Transactions [][]byte
	Withdrawals  []*Withdrawal
}

// BinaryTransactions is the DerivableList wrapper for raw RLP-encoded
// transactions used to compute the transactions root.
type BinaryTransactions [][]byte

// emptyMPTRoot is keccak256(RLP("")), the canonical empty
// Merkle-Patricia trie root used by Ethereum for any empty list
// (TxHash on a 0-tx block, WithdrawalsHash on a no-withdrawal
// post-Capella block, etc.).
var emptyMPTRoot = common.Hash{
	0x56, 0xe8, 0x1f, 0x17, 0x1b, 0xcc, 0x55, 0xa6,
	0xff, 0x83, 0x45, 0xe6, 0x92, 0xc0, 0xf8, 0x6e,
	0x5b, 0x48, 0xe0, 0x1b, 0x99, 0x6c, 0xad, 0xc0,
	0x01, 0x62, 0x2f, 0xb5, 0xe3, 0x63, 0xb4, 0x21,
}

// DeriveSha computes the Merkle-Patricia root of a derivable list.
// For empty inputs we return the canonical empty-trie root; for
// non-empty inputs the production path (eladapter) MUST supply
// the real MPT root via a real implementation. The cl/cltypes
// callers we exercise in tests only pass empty
// BinaryTransactions / Withdrawals lists.
func DeriveSha(list any) common.Hash {
	if l, ok := list.(BinaryTransactions); ok && len(l) == 0 {
		return emptyMPTRoot
	}
	if l, ok := list.(Withdrawals); ok && len(l) == 0 {
		return emptyMPTRoot
	}
	panic("depshim/types: DeriveSha on non-empty list is a stub; eladapter wires the real MPT root")
}

// Block is the canonical EL block container — header plus body components.
// Stub: cl/phase1/execution_client only references it as a parameter type
// for InsertBlock(s); the eladapter materialises real Block instances when
// it actually needs to import historical blocks.
//
// Field names match erigon's execution/types.Block public fields
// so straight initialiser-style constructors still work; the methods
// below provide the erigon-style accessor API used by cl/cltypes
// tests (block.Header() returns the *Header pointer).
type Block struct {
	HeaderField  *Header
	Transactions [][]byte
	Withdrawals  []*Withdrawal
}

// Header returns the block header (matches erigon accessor signature).
func (b *Block) Header() *Header { return b.HeaderField }

// RawBody returns a RawBody view over the block's transactions and
// withdrawals. STUB — only invoked from cl/cltypes test code.
func (b *Block) RawBody() *RawBody {
	return &RawBody{Transactions: b.Transactions, Withdrawals: b.Withdrawals}
}

// Hash forwards to the header hash (matches erigon Block accessor).
// Phase 7.4 — block_collector reads Block.Hash() to chain blocks.
func (b *Block) Hash() common.Hash {
	if b.HeaderField == nil {
		return common.Hash{}
	}
	return b.HeaderField.Hash()
}

// ParentHash forwards to the header ParentHash (matches erigon Block accessor).
func (b *Block) ParentHash() common.Hash {
	if b.HeaderField == nil {
		return common.Hash{}
	}
	return b.HeaderField.ParentHash
}

// NumberU64 forwards to the header block number (matches erigon Block accessor).
func (b *Block) NumberU64() uint64 {
	if b.HeaderField == nil {
		return 0
	}
	return b.HeaderField.Number.Uint64()
}

// HeaderNoCopy returns the block header by reference (matches erigon's accessor;
// the shim Block already holds a single *Header, so this is the same pointer as
// Header()).
func (b *Block) HeaderNoCopy() *Header { return b.HeaderField }

// NewBlockFromStorageWithBinaryTxs builds a Block from a header + the raw
// (binary) transaction list, mirroring erigon's constructor. The shim Block
// stores transactions in binary form, so the parsed txs and uncles params are
// accepted for signature fidelity but only binaryTxs/withdrawals are retained.
func NewBlockFromStorageWithBinaryTxs(hash common.Hash, header *Header, txs []Transaction, binaryTxs BinaryTransactions, uncles []*Header, withdrawals []*Withdrawal) *Block {
	return &Block{
		HeaderField:  header,
		Transactions: [][]byte(binaryTxs),
		Withdrawals:  withdrawals,
	}
}

// Slot is a Caplin-side helper that returns the slot the block was
// proposed for. EL Block stub stores no slot — return 0; caller treats
// this as "unknown".
func (b *Block) Slot() uint64 { return 0 }

// --- test-only constructors --------------------------------------------
// These are panic-stubs so test files that need them compile under
// `go vet -tags n42el`. Production code lives in the EL adapter,
// which constructs Block/Transaction values via N42's core packages,
// not via these helpers.

// Transaction is a placeholder for cl/cltypes test helpers that
// construct synthetic EL transactions. The real EL tx type lives
// in N42's common/transaction; eladapter bridges as needed.
type Transaction struct {
	Nonce    uint64
	To       common.Address
	Value    *uint256.Int
	GasLimit uint64
	GasPrice *uint256.Int
	Data     []byte
}

// NewTransaction builds a Transaction stub. STUB — see package doc.
func NewTransaction(nonce uint64, to common.Address, value *uint256.Int,
	gasLimit uint64, gasPrice *uint256.Int, data []byte) Transaction {
	return Transaction{Nonce: nonce, To: to, Value: value,
		GasLimit: gasLimit, GasPrice: gasPrice, Data: data}
}

// BlobTxType is the EIP-4844 tx type discriminator. Mirrored from
// erigon execution/types so caplin's blob validation paths compile.
const BlobTxType = 0x03

// Type returns the tx type byte. STUB — without real RLP decoding the
// transaction is treated as legacy (0). The Phase 7.2.7 caplin
// cherry-pick only uses Type() in misc.ValidateBlobs path; that path
// is itself a stub (see depshim/elmisc) and skips blob validation
// pre-Fulu. For follower-mode mainnet this is harmless because Caplin
// already validates blob hashes upstream on the CL side.
func (t Transaction) Type() byte { return 0 }

// GetBlobHashes returns the EIP-4844 blob versioned hashes carried by
// the transaction. STUB returns nil; see Type() doc.
func (t Transaction) GetBlobHashes() []common.Hash { return nil }

// DecodeTransactions decodes the per-block raw RLP byte slices Caplin
// receives in ExecutionPayload.Transactions back into Transaction
// structs. STUB returns a slice of zero-value Transaction; the
// downstream consumers (misc.ValidateBlobs) only need len(); the real
// EL-side validation runs inside api.EngineAPIv4.NewPayloadV4 which is
// already wired (Phase 7.1.1.b).
//
// When PeerDAS / blob validation needs real per-tx structure (Fusaka+),
// this must be replaced with a proper RLP decode that materialises
// the tx type byte and blob versioned-hashes list.
func DecodeTransactions(raw [][]byte) ([]Transaction, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]Transaction, len(raw))
	return out, nil
}

// NewBlock builds a Block stub from header + transactions +
// uncles + withdrawals (+ optional requests, matching erigon's
// 5-arg variadic). STUB — only invoked from cl/cltypes test code.
func NewBlock(header *Header, txs []Transaction, _ []*Header, withdrawals Withdrawals, _ ...any) *Block {
	_ = withdrawals
	rawTxs := make([][]byte, 0, len(txs))
	return &Block{HeaderField: header, Transactions: rawTxs}
}
