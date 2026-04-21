// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// parallel_commit.go: block-level commit phase for the Block-STM
// parallel executor. After ExecuteBlockParallel returns with every tx
// in TxStatusCommitted, this module produces the deterministic
// (writes, gasUsed, logs) tuple that gets applied to the real state
// store and packaged into a block receipt.
//
// Why a separate commit phase:
//
//   - Gas pool: each tx consumes gas. In Block-STM speculative
//     execution, workers can't mutate a shared gas counter without
//     races (plus re-executions would need to undo). Instead, each
//     tx records its `GasUsed` in TxOutput, and we sum at commit.
//   - Logs: EVM logs get a sequential index; speculative execution
//     doesn't know the final index. Commit walks outputs in tx order
//     and assigns block-global indices.
//   - Coinbase: the block builder's gas tip accumulates across txs.
//     Speculative writes to `balance[coinbase]` would force every tx
//     to conflict on the coinbase slot. The paper's "lazy coinbase"
//     approach: each tx records its tip, and commit applies the sum
//     as ONE write to coinbase's balance.
//   - Log index + cumulativeGasUsed: needed for receipts. Commit is
//     the only place where tx order is linearized so these scalars
//     make sense.
//
// The commit phase is SINGLE-THREADED — it runs after all workers
// have drained. Determinism matters more than speed here.

package state

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// TxOutput is what a worker produces for each successfully-executed
// tx, over and above the MVHashMap writes. The Block-STM worker fills
// this after the executor returns; the commit phase consumes it.
//
// Invariants:
//   - TxIdx matches the scheduler's tx index.
//   - If Err is set, GasUsed is still valid (ran until OOG / revert),
//     Logs should be empty (reverted), CoinbaseTip is still charged.
//   - Logs are in emission order within this tx; commit assigns the
//     global block-wide log index.
type TxOutput struct {
	TxIdx       int
	GasUsed     uint64
	CoinbaseTip *uint256.Int // gas tip (gasUsed * effectiveGasPrice) paid to coinbase
	Logs        []Log
	// Receipt scalar bits. Status: 1 == success, 0 == reverted.
	Status uint8
	Err    error
}

// Log mirrors the subset of block.Log fields needed at commit time.
// Kept independent of the block package to avoid import cycles in
// state-package tests.
type Log struct {
	Address types.Address
	Topics  []types.Hash
	Data    []byte
	// TxIndex / Index / BlockNumber are filled by the commit phase.
	TxIndex int
	Index   uint
}

// BlockCommit is the deterministic output of the commit phase.
// Callers apply Writes to their state store, use GasUsed in the
// block header, and attach Logs to receipts in order.
type BlockCommit struct {
	// Writes are the final per-key state deltas from the MVHashMap's
	// highest-txIdx write per key, sorted by key for MDBX B-tree
	// locality. Values == nil mean delete.
	Writes []CommitEntry

	// CoinbaseDelta is the total tip credited to the coinbase across
	// all successful txs. Applied AFTER Writes so it composes with
	// any direct account update in a tx.
	CoinbaseAddress types.Address
	CoinbaseDelta   *uint256.Int

	// GasUsed is the block's total gas consumption (sum of per-tx
	// GasUsed). Matches header.gasUsed.
	GasUsed uint64

	// Logs are all tx logs concatenated in tx order, with block-global
	// Index assigned. Callers split by TxIndex to attach to receipts.
	Logs []Log

	// Receipts in tx order, with CumulativeGasUsed populated.
	Receipts []TxReceipt
}

// TxReceipt captures per-tx receipt scalars. Kept independent of
// block.Receipt to avoid import cycles; real code translates this
// into block.Receipt at the caller.
type TxReceipt struct {
	TxIdx              int
	Status             uint8
	CumulativeGasUsed  uint64
	FirstLogIndex      uint // inclusive
	LastLogIndex       uint // exclusive
}

// FinalizeBlock runs the commit phase. Inputs:
//   - mv:      the MVHashMap with every tx's final writes
//   - outputs: TxOutput[i] for every committed tx (slice indexed by
//              tx index; entries for reverted txs still contribute
//              GasUsed and CoinbaseTip)
//   - coinbase: block producer address for tip aggregation
//
// Returns the deterministic BlockCommit. Pure function — does NOT
// mutate mv or outputs.
func FinalizeBlock(
	mv *MVHashMap,
	outputs []TxOutput,
	coinbase types.Address,
) (*BlockCommit, error) {
	bc := &BlockCommit{
		CoinbaseAddress: coinbase,
		CoinbaseDelta:   new(uint256.Int),
		Receipts:        make([]TxReceipt, 0, len(outputs)),
	}

	// 1. Collect the final writes from MVHashMap.
	//    CollectHighestWrites gives us (key, value, txIdx) per key.
	//    Sort by key for MDBX B+tree locality (same as the sequential
	//    PlainStateBuffer.ApplyTo does).
	entries := mv.CollectHighestWrites()
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].Key, entries[j].Key) < 0
	})
	bc.Writes = entries

	// 2. Aggregate gas + coinbase tips + logs in tx order.
	var cumGas uint64
	var logIdx uint
	for i := range outputs {
		out := &outputs[i]
		if out.TxIdx != i {
			return nil, fmt.Errorf("outputs[%d].TxIdx=%d (misaligned)", i, out.TxIdx)
		}
		cumGas += out.GasUsed

		if out.CoinbaseTip != nil && !out.CoinbaseTip.IsZero() {
			bc.CoinbaseDelta.Add(bc.CoinbaseDelta, out.CoinbaseTip)
		}

		// Assign sequential log indices.
		firstLogIdx := logIdx
		for j := range out.Logs {
			out.Logs[j].TxIndex = i
			out.Logs[j].Index = logIdx
			logIdx++
		}
		bc.Logs = append(bc.Logs, out.Logs...)

		bc.Receipts = append(bc.Receipts, TxReceipt{
			TxIdx:             i,
			Status:            out.Status,
			CumulativeGasUsed: cumGas,
			FirstLogIndex:     firstLogIdx,
			LastLogIndex:      logIdx,
		})
	}
	bc.GasUsed = cumGas

	return bc, nil
}

// ApplyTarget is what a BlockCommit writes into. Production impl will
// wrap PlainStateWriter or a direct MDBX tx; tests use a simple map.
type ApplyTarget interface {
	// PutAccount writes the encoded account at addr, or deletes if
	// enc is nil/empty.
	PutAccount(addr types.Address, enc []byte) error
	// PutStorage writes value at (addr, slot), or deletes if nil.
	PutStorage(addr types.Address, slot types.Hash, value []byte) error
	// PutCode stores code keyed by codeHash. No-op if already present.
	PutCode(codeHash types.Hash, code []byte) error
	// AddBalance adds delta to addr's balance. Used for coinbase
	// aggregation; delta is always non-negative.
	AddBalance(addr types.Address, delta *uint256.Int) error
}

// Apply writes every BlockCommit write to target, then applies the
// coinbase tip aggregate. Writes are already sorted by key (FinalizeBlock
// guarantees deterministic order).
func (bc *BlockCommit) Apply(target ApplyTarget) error {
	for _, e := range bc.Writes {
		if err := applyEntry(target, e); err != nil {
			return fmt.Errorf("apply %x: %w", e.Key, err)
		}
	}
	if bc.CoinbaseDelta != nil && !bc.CoinbaseDelta.IsZero() {
		if err := target.AddBalance(bc.CoinbaseAddress, bc.CoinbaseDelta); err != nil {
			return fmt.Errorf("coinbase tip: %w", err)
		}
	}
	return nil
}

// applyEntry dispatches one CommitEntry by key tag.
func applyEntry(target ApplyTarget, e CommitEntry) error {
	if len(e.Key) == 0 {
		return fmt.Errorf("empty key")
	}
	switch e.Key[0] {
	case mvKeyTagAccount:
		if len(e.Key) != 21 {
			return fmt.Errorf("bad account key len=%d", len(e.Key))
		}
		var addr types.Address
		copy(addr[:], e.Key[1:])
		return target.PutAccount(addr, e.Value)
	case mvKeyTagStorage:
		if len(e.Key) != 53 {
			return fmt.Errorf("bad storage key len=%d", len(e.Key))
		}
		var addr types.Address
		var slot types.Hash
		copy(addr[:], e.Key[1:21])
		copy(slot[:], e.Key[21:])
		return target.PutStorage(addr, slot, e.Value)
	case mvKeyTagCode:
		if len(e.Key) != 33 {
			return fmt.Errorf("bad code key len=%d", len(e.Key))
		}
		var codeHash types.Hash
		copy(codeHash[:], e.Key[1:])
		return target.PutCode(codeHash, e.Value)
	}
	return fmt.Errorf("unknown key tag %d", e.Key[0])
}

// --- MapApplyTarget: test-only in-memory target ---

// MapApplyTarget is an ApplyTarget backed by a plain map. Used for
// unit tests and the parallel-executor integration test harness.
type MapApplyTarget struct {
	Accounts map[types.Address][]byte
	Storage  map[types.Address]map[types.Hash][]byte
	Code     map[types.Hash][]byte
	Balance  map[types.Address]*uint256.Int
}

func NewMapApplyTarget() *MapApplyTarget {
	return &MapApplyTarget{
		Accounts: make(map[types.Address][]byte),
		Storage:  make(map[types.Address]map[types.Hash][]byte),
		Code:     make(map[types.Hash][]byte),
		Balance:  make(map[types.Address]*uint256.Int),
	}
}

func (m *MapApplyTarget) PutAccount(addr types.Address, enc []byte) error {
	if len(enc) == 0 {
		delete(m.Accounts, addr)
		return nil
	}
	m.Accounts[addr] = append([]byte(nil), enc...)
	return nil
}

func (m *MapApplyTarget) PutStorage(addr types.Address, slot types.Hash, value []byte) error {
	slots, ok := m.Storage[addr]
	if !ok {
		if len(value) == 0 {
			return nil // no-op delete of absent slot
		}
		slots = make(map[types.Hash][]byte)
		m.Storage[addr] = slots
	}
	if len(value) == 0 {
		delete(slots, slot)
		return nil
	}
	slots[slot] = append([]byte(nil), value...)
	return nil
}

func (m *MapApplyTarget) PutCode(codeHash types.Hash, code []byte) error {
	if _, exists := m.Code[codeHash]; exists {
		return nil // idempotent
	}
	m.Code[codeHash] = append([]byte(nil), code...)
	return nil
}

func (m *MapApplyTarget) AddBalance(addr types.Address, delta *uint256.Int) error {
	cur, ok := m.Balance[addr]
	if !ok {
		cur = new(uint256.Int)
	} else {
		cur = new(uint256.Int).Set(cur)
	}
	cur.Add(cur, delta)
	m.Balance[addr] = cur
	return nil
}

// --- Helpers for tests ---

// DecodeAccountValue is a convenience for tests that want to unpack
// an encoded account from a BlockCommit.Write entry.
func DecodeAccountValue(enc []byte) (*account.StateAccount, error) {
	if len(enc) == 0 {
		return nil, nil
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(enc); err != nil {
		return nil, err
	}
	return &a, nil
}
