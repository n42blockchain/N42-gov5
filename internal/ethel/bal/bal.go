// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Block-Level Access List (EIP-7928) — Phase 1: out-of-consensus data model,
// deterministic builder, RLP encoding and hash. This carries the account/slot
// touched by a block plus their post-execution values and the tx index that
// produced them, enabling parallel state prefetch and parallel validation.
//
// Phase 1 is deliberately decoupled from block production and validation: it
// neither reads a header field nor gates any block. A downstream phase harvests
// TxAccess records from the block-STM mvView read/write sets (which already hold
// per-tx post-values and tx indexes) and calls BuildBAL; a later phase binds the
// resulting hash into the header and drives prefetch/validation from it.
package bal

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/rlp"
)

// Reserved tx-index conventions from EIP-7928: index 0 is the pre-block system
// calls, 1..n are the block's transactions, n+1 is the post-block system calls.
const (
	TxIndexPreBlock uint16 = 0
)

// StorageWrite is a single post-execution slot value written by one tx.
type StorageWrite struct {
	TxIndex  uint16
	NewValue types.Hash
}

// SlotChanges is the ordered set of writes to one storage slot within the block.
type SlotChanges struct {
	Slot   types.Hash
	Writes []StorageWrite // sorted by TxIndex ascending
}

// BalanceChange is an account's post-execution balance after a given tx.
type BalanceChange struct {
	TxIndex     uint16
	PostBalance uint256.Int
}

// NonceChange is an account's new nonce after a given tx.
type NonceChange struct {
	TxIndex  uint16
	NewNonce uint64
}

// CodeChange is code newly set on an account by a given tx.
type CodeChange struct {
	TxIndex uint16
	NewCode []byte
}

// AccountChanges bundles everything a block touched for one address.
type AccountChanges struct {
	Address        types.Address
	StorageChanges []SlotChanges  // sorted by slot
	StorageReads   []types.Hash   // read-only slots (never written this block), sorted
	BalanceChanges []BalanceChange
	NonceChanges   []NonceChange
	CodeChanges    []CodeChange
}

// BlockAccessList is the canonical EIP-7928 access list for one block.
type BlockAccessList struct {
	Accounts []AccountChanges // sorted by address
}

// --- Builder input contract -------------------------------------------------
//
// TxAccess is the per-transaction access-change record a harvester produces from
// execution output. Keeping the builder input explicit (rather than reaching into
// executor internals) is what keeps Phase 1 consensus-neutral and unit-testable.

type SlotWrite struct {
	Address  types.Address
	Slot     types.Hash
	NewValue types.Hash
}

type SlotRead struct {
	Address types.Address
	Slot    types.Hash
}

type AccountBalance struct {
	Address     types.Address
	PostBalance uint256.Int
}

type AccountNonce struct {
	Address  types.Address
	NewNonce uint64
}

type AccountCode struct {
	Address types.Address
	NewCode []byte
}

// TxAccess is one transaction's (or system call's) access-change set.
type TxAccess struct {
	TxIndex        uint16
	StorageWrites  []SlotWrite
	StorageReads   []SlotRead
	BalanceChanges []AccountBalance
	NonceChanges   []AccountNonce
	CodeChanges    []AccountCode
}

// accountAgg accumulates one address's changes before canonicalization.
type accountAgg struct {
	addr     types.Address
	slots    map[types.Hash][]StorageWrite // slot -> writes
	reads    map[types.Hash]struct{}       // read-only candidate slots
	balances []BalanceChange
	nonces   []NonceChange
	codes    []CodeChange
	touched  bool
}

// BuildBAL folds a block's per-tx access records into a canonical, deterministic
// BlockAccessList. Ordering follows EIP-7928: accounts by address, storage slots
// by slot, per-slot writes by tx index, and balance/nonce/code changes by tx
// index. A slot that is written anywhere in the block is a write, never a read,
// so it is excluded from StorageReads.
func BuildBAL(txs []TxAccess) *BlockAccessList {
	aggs := make(map[types.Address]*accountAgg)
	get := func(addr types.Address) *accountAgg {
		a, ok := aggs[addr]
		if !ok {
			a = &accountAgg{
				addr:  addr,
				slots: make(map[types.Hash][]StorageWrite),
				reads: make(map[types.Hash]struct{}),
			}
			aggs[addr] = a
		}
		a.touched = true
		return a
	}

	for _, tx := range txs {
		for _, w := range tx.StorageWrites {
			a := get(w.Address)
			a.slots[w.Slot] = append(a.slots[w.Slot], StorageWrite{TxIndex: tx.TxIndex, NewValue: w.NewValue})
		}
		for _, r := range tx.StorageReads {
			a := get(r.Address)
			a.reads[r.Slot] = struct{}{}
		}
		for _, b := range tx.BalanceChanges {
			a := get(b.Address)
			a.balances = append(a.balances, BalanceChange{TxIndex: tx.TxIndex, PostBalance: b.PostBalance})
		}
		for _, n := range tx.NonceChanges {
			a := get(n.Address)
			a.nonces = append(a.nonces, NonceChange{TxIndex: tx.TxIndex, NewNonce: n.NewNonce})
		}
		for _, c := range tx.CodeChanges {
			a := get(c.Address)
			a.codes = append(a.codes, CodeChange{TxIndex: tx.TxIndex, NewCode: append([]byte(nil), c.NewCode...)})
		}
	}

	bal := &BlockAccessList{Accounts: make([]AccountChanges, 0, len(aggs))}
	for _, a := range aggs {
		if !a.touched {
			continue
		}
		ac := AccountChanges{Address: a.addr}

		// Storage writes: sort slots, then per-slot writes by tx index.
		slots := make([]types.Hash, 0, len(a.slots))
		for s := range a.slots {
			slots = append(slots, s)
		}
		sort.Slice(slots, func(i, j int) bool { return bytes.Compare(slots[i][:], slots[j][:]) < 0 })
		for _, s := range slots {
			ws := a.slots[s]
			sort.Slice(ws, func(i, j int) bool { return ws[i].TxIndex < ws[j].TxIndex })
			ac.StorageChanges = append(ac.StorageChanges, SlotChanges{Slot: s, Writes: ws})
		}

		// Read-only slots: those read but never written this block.
		reads := make([]types.Hash, 0, len(a.reads))
		for s := range a.reads {
			if _, written := a.slots[s]; written {
				continue
			}
			reads = append(reads, s)
		}
		sort.Slice(reads, func(i, j int) bool { return bytes.Compare(reads[i][:], reads[j][:]) < 0 })
		ac.StorageReads = reads

		// Balance/nonce/code changes: stable order by tx index, then drop entries
		// equal to the previous kept value so only txs that actually changed the
		// field are listed (EIP-7928). The very first entry per account is always
		// kept: without the pre-block base value it cannot be proven redundant.
		sort.SliceStable(a.balances, func(i, j int) bool { return a.balances[i].TxIndex < a.balances[j].TxIndex })
		ac.BalanceChanges = dedupBalances(a.balances)
		sort.SliceStable(a.nonces, func(i, j int) bool { return a.nonces[i].TxIndex < a.nonces[j].TxIndex })
		ac.NonceChanges = dedupNonces(a.nonces)
		sort.SliceStable(a.codes, func(i, j int) bool { return a.codes[i].TxIndex < a.codes[j].TxIndex })
		ac.CodeChanges = dedupCodes(a.codes)

		bal.Accounts = append(bal.Accounts, ac)
	}

	sort.Slice(bal.Accounts, func(i, j int) bool {
		return bytes.Compare(bal.Accounts[i].Address[:], bal.Accounts[j].Address[:]) < 0
	})
	return bal
}

// dedupBalances drops changes whose post balance equals the previous kept one.
func dedupBalances(in []BalanceChange) []BalanceChange {
	if len(in) == 0 {
		return nil
	}
	out := in[:1]
	for _, c := range in[1:] {
		if c.PostBalance.Cmp(&out[len(out)-1].PostBalance) != 0 {
			out = append(out, c)
		}
	}
	return out
}

// dedupNonces drops changes whose nonce equals the previous kept one.
func dedupNonces(in []NonceChange) []NonceChange {
	if len(in) == 0 {
		return nil
	}
	out := in[:1]
	for _, c := range in[1:] {
		if c.NewNonce != out[len(out)-1].NewNonce {
			out = append(out, c)
		}
	}
	return out
}

// dedupCodes drops changes whose code bytes equal the previous kept one.
func dedupCodes(in []CodeChange) []CodeChange {
	if len(in) == 0 {
		return nil
	}
	out := in[:1]
	for _, c := range in[1:] {
		if !bytes.Equal(c.NewCode, out[len(out)-1].NewCode) {
			out = append(out, c)
		}
	}
	return out
}

// --- RLP wire form ----------------------------------------------------------
//
// Encoded with explicit primitive-typed structs so the byte layout is fully
// determined (no reflection ambiguity over named array types), integers use
// canonical minimal big-endian RLP.

type wireStorageWrite struct {
	TxIndex  uint64
	NewValue []byte
}
type wireSlot struct {
	Slot   []byte
	Writes []wireStorageWrite
}
type wireBalance struct {
	TxIndex     uint64
	PostBalance []byte
}
type wireNonce struct {
	TxIndex  uint64
	NewNonce uint64
}
type wireCode struct {
	TxIndex uint64
	NewCode []byte
}
type wireAccount struct {
	Address        []byte
	StorageChanges []wireSlot
	StorageReads   [][]byte
	BalanceChanges []wireBalance
	NonceChanges   []wireNonce
	CodeChanges    []wireCode
}
type wireBAL struct {
	Accounts []wireAccount
}

func (b *BlockAccessList) toWire() wireBAL {
	w := wireBAL{Accounts: make([]wireAccount, 0, len(b.Accounts))}
	for _, a := range b.Accounts {
		wa := wireAccount{Address: a.Address[:]}
		for _, sc := range a.StorageChanges {
			ws := wireSlot{Slot: sc.Slot[:]}
			for _, sw := range sc.Writes {
				nv := sw.NewValue
				ws.Writes = append(ws.Writes, wireStorageWrite{TxIndex: uint64(sw.TxIndex), NewValue: nv[:]})
			}
			wa.StorageChanges = append(wa.StorageChanges, ws)
		}
		for _, r := range a.StorageReads {
			rr := r
			wa.StorageReads = append(wa.StorageReads, rr[:])
		}
		for _, bc := range a.BalanceChanges {
			bal := bc.PostBalance
			wa.BalanceChanges = append(wa.BalanceChanges, wireBalance{TxIndex: uint64(bc.TxIndex), PostBalance: bal.Bytes()})
		}
		for _, nc := range a.NonceChanges {
			wa.NonceChanges = append(wa.NonceChanges, wireNonce{TxIndex: uint64(nc.TxIndex), NewNonce: nc.NewNonce})
		}
		for _, cc := range a.CodeChanges {
			wa.CodeChanges = append(wa.CodeChanges, wireCode{TxIndex: uint64(cc.TxIndex), NewCode: cc.NewCode})
		}
		w.Accounts = append(w.Accounts, wa)
	}
	return w
}

// EncodeRLP returns the canonical RLP encoding of the block access list.
func (b *BlockAccessList) EncodeRLP() ([]byte, error) {
	return rlp.EncodeToBytes(b.toWire())
}

// Hash is the block_access_list_hash: keccak256 of the canonical RLP encoding.
// A later phase binds this into the block header.
func (b *BlockAccessList) Hash() (types.Hash, error) {
	enc, err := b.EncodeRLP()
	if err != nil {
		return types.Hash{}, err
	}
	return crypto.Keccak256Hash(enc), nil
}

// DecodeBAL parses the canonical RLP encoding produced by EncodeRLP back into a
// BlockAccessList. Used by the out-of-band BAL service so a consumer that
// received a full BAL (rather than only the header hash) can drive prefetch and
// verify it against the header hash without re-executing. Fixed 32-byte slots and
// values are length-checked.
func DecodeBAL(raw []byte) (*BlockAccessList, error) {
	var w wireBAL
	if err := rlp.DecodeBytes(raw, &w); err != nil {
		return nil, err
	}
	toHash := func(b []byte) (types.Hash, error) {
		if len(b) != len(types.Hash{}) {
			return types.Hash{}, fmt.Errorf("bal: expected %d-byte value, got %d", len(types.Hash{}), len(b))
		}
		var h types.Hash
		copy(h[:], b)
		return h, nil
	}
	bal := &BlockAccessList{Accounts: make([]AccountChanges, 0, len(w.Accounts))}
	for _, wa := range w.Accounts {
		if len(wa.Address) != len(types.Address{}) {
			return nil, fmt.Errorf("bal: expected %d-byte address, got %d", len(types.Address{}), len(wa.Address))
		}
		var ac AccountChanges
		copy(ac.Address[:], wa.Address)
		for _, ws := range wa.StorageChanges {
			slot, err := toHash(ws.Slot)
			if err != nil {
				return nil, err
			}
			sc := SlotChanges{Slot: slot}
			for _, w := range ws.Writes {
				nv, err := toHash(w.NewValue)
				if err != nil {
					return nil, err
				}
				sc.Writes = append(sc.Writes, StorageWrite{TxIndex: uint16(w.TxIndex), NewValue: nv})
			}
			ac.StorageChanges = append(ac.StorageChanges, sc)
		}
		for _, r := range wa.StorageReads {
			slot, err := toHash(r)
			if err != nil {
				return nil, err
			}
			ac.StorageReads = append(ac.StorageReads, slot)
		}
		for _, wb := range wa.BalanceChanges {
			ac.BalanceChanges = append(ac.BalanceChanges, BalanceChange{
				TxIndex:     uint16(wb.TxIndex),
				PostBalance: *new(uint256.Int).SetBytes(wb.PostBalance),
			})
		}
		for _, wn := range wa.NonceChanges {
			ac.NonceChanges = append(ac.NonceChanges, NonceChange{TxIndex: uint16(wn.TxIndex), NewNonce: wn.NewNonce})
		}
		for _, wc := range wa.CodeChanges {
			ac.CodeChanges = append(ac.CodeChanges, CodeChange{TxIndex: uint16(wc.TxIndex), NewCode: wc.NewCode})
		}
		bal.Accounts = append(bal.Accounts, ac)
	}
	return bal, nil
}
