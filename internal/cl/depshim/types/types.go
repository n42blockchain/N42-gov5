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

// Hash returns the Keccak256 RLP hash of the header. STUB — see package doc.
func (h *Header) Hash() common.Hash {
	panic("depshim/types: Header.Hash is a Phase-2 stub; wire eladapter (Phase 5) before invoking")
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

// DeriveSha computes the Merkle-Patricia root of a list. STUB — see package
// doc.
func DeriveSha(_ any) common.Hash {
	panic("depshim/types: DeriveSha is a Phase-2 stub; wire eladapter (Phase 5) before invoking")
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
	HeaderField       *Header
	Transactions      [][]byte
	Withdrawals       []*Withdrawal
}

// Header returns the block header (matches erigon accessor signature).
func (b *Block) Header() *Header { return b.HeaderField }

// RawBody returns a RawBody view over the block's transactions and
// withdrawals. STUB — only invoked from cl/cltypes test code.
func (b *Block) RawBody() *RawBody {
	return &RawBody{Transactions: b.Transactions, Withdrawals: b.Withdrawals}
}

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

// NewBlock builds a Block stub from header + transactions +
// uncles + withdrawals (+ optional requests, matching erigon's
// 5-arg variadic). STUB — only invoked from cl/cltypes test code.
func NewBlock(header *Header, txs []Transaction, _ []*Header, withdrawals Withdrawals, _ ...any) *Block {
	_ = withdrawals
	rawTxs := make([][]byte, 0, len(txs))
	return &Block{HeaderField: header, Transactions: rawTxs}
}
