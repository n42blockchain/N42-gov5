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
// Package bal is the Ethereum (Amsterdam) EIP-7928 block-level access list:
// the canonical data model, its RLP encoding and hash, the size constraint,
// and a Builder that collects the accesses of one block's execution.
//
// It is distinct from internal/ethel/bal, the N42-native access list gated on
// ChainConfig.BALTime, whose layout differs and must stay unchanged.
//
// Block access indices follow the EIP: 0 is the pre-execution system calls,
// 1..n the block's transactions, n+1 the post-execution withdrawals and
// system calls.
package bal

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// ItemCost is the gas-limit divisor of the EIP-7928 size constraint:
// an access list may hold at most gasLimit / ItemCost items, an item being an
// address or a storage key (written or read).
const ItemCost = 2000

// StorageChange is a slot's value after the block access index that wrote it.
type StorageChange struct {
	Index     uint64
	PostValue uint256.Int
}

// SlotChanges lists the writes to one storage slot, by ascending index.
type SlotChanges struct {
	Slot    uint256.Int
	Changes []StorageChange
}

// BalanceChange is an account's balance after the index that changed it.
type BalanceChange struct {
	Index       uint64
	PostBalance uint256.Int
}

// NonceChange is an account's nonce after the index that changed it.
type NonceChange struct {
	Index     uint64
	PostNonce uint64
}

// CodeChange is an account's code after the index that changed it.
type CodeChange struct {
	Index   uint64
	NewCode []byte
}

// AccountChanges is everything one block did to, or read from, one address.
// The field order is the RLP field order of the EIP.
type AccountChanges struct {
	Address        types.Address
	StorageChanges []SlotChanges
	StorageReads   []uint256.Int
	BalanceChanges []BalanceChange
	NonceChanges   []NonceChange
	CodeChanges    []CodeChange
}

// BlockAccessList is the canonical access list, accounts sorted by address.
type BlockAccessList []AccountChanges

// Encode returns the canonical RLP encoding of the list.
func (l BlockAccessList) Encode() ([]byte, error) {
	return rlp.EncodeToBytes([]AccountChanges(l))
}

// Decode parses an RLP-encoded access list, as an Amsterdam execution
// payload carries it. It checks the encoding only, not that the list is in
// canonical order; comparing its hash with the executed list's does that.
func Decode(data []byte) (BlockAccessList, error) {
	var l []AccountChanges
	if err := rlp.DecodeBytes(data, &l); err != nil {
		return nil, err
	}
	return BlockAccessList(l), nil
}

// Hash is the header's block_access_list_hash: keccak256 of the encoding.
func (l BlockAccessList) Hash() (types.Hash, error) {
	enc, err := l.Encode()
	if err != nil {
		return types.Hash{}, err
	}
	return crypto.Keccak256Hash(enc), nil
}

// ItemCount counts the items the size constraint limits: every address plus
// every storage key it carries, written or read.
func (l BlockAccessList) ItemCount() uint64 {
	count := uint64(len(l))
	for i := range l {
		count += uint64(len(l[i].StorageChanges)) + uint64(len(l[i].StorageReads))
	}
	return count
}

// ValidateSize enforces the EIP-7928 size constraint for a block gas limit.
func (l BlockAccessList) ValidateSize(gasLimit uint64) error {
	if items, limit := l.ItemCount(), gasLimit/ItemCost; items > limit {
		return fmt.Errorf("block access list has %d items, gas limit %d allows %d", items, gasLimit, limit)
	}
	return nil
}

// Builder collects the accesses of one block's execution. Changes must be
// reported in ascending index order; a slot written at any index is listed
// only under its writes, never as a read.
type Builder struct {
	accounts map[types.Address]*accountBuilder
}

type accountBuilder struct {
	writes   map[types.Hash][]StorageChange
	reads    map[types.Hash]struct{}
	balances []BalanceChange
	nonces   []NonceChange
	codes    []CodeChange
}

// NewBuilder returns an empty builder.
func NewBuilder() *Builder {
	return &Builder{accounts: make(map[types.Address]*accountBuilder)}
}

func (b *Builder) account(addr types.Address) *accountBuilder {
	a, ok := b.accounts[addr]
	if !ok {
		a = &accountBuilder{
			writes: make(map[types.Hash][]StorageChange),
			reads:  make(map[types.Hash]struct{}),
		}
		b.accounts[addr] = a
	}
	return a
}

// AccountAccess records that addr was accessed.
func (b *Builder) AccountAccess(addr types.Address) {
	b.account(addr)
}

// StorageRead records that a slot of addr was read.
func (b *Builder) StorageRead(addr types.Address, slot types.Hash) {
	a := b.account(addr)
	if _, written := a.writes[slot]; !written {
		a.reads[slot] = struct{}{}
	}
}

// StorageWrite records a slot's value after index.
func (b *Builder) StorageWrite(index uint64, addr types.Address, slot types.Hash, value *uint256.Int) {
	a := b.account(addr)
	a.writes[slot] = append(a.writes[slot], StorageChange{Index: index, PostValue: *value})
	delete(a.reads, slot)
}

// BalanceChange records addr's balance after index.
func (b *Builder) BalanceChange(index uint64, addr types.Address, balance *uint256.Int) {
	a := b.account(addr)
	a.balances = append(a.balances, BalanceChange{Index: index, PostBalance: *balance})
}

// NonceChange records addr's nonce after index.
func (b *Builder) NonceChange(index uint64, addr types.Address, nonce uint64) {
	a := b.account(addr)
	a.nonces = append(a.nonces, NonceChange{Index: index, PostNonce: nonce})
}

// CodeChange records addr's code after index.
func (b *Builder) CodeChange(index uint64, addr types.Address, code []byte) {
	a := b.account(addr)
	a.codes = append(a.codes, CodeChange{Index: index, NewCode: bytes.Clone(code)})
}

// Build returns the canonical list: accounts by address, slots and read keys
// by key, changes by index.
func (b *Builder) Build() BlockAccessList {
	out := make(BlockAccessList, 0, len(b.accounts))
	for addr, a := range b.accounts {
		ac := AccountChanges{
			Address:        addr,
			StorageChanges: make([]SlotChanges, 0, len(a.writes)),
			StorageReads:   make([]uint256.Int, 0, len(a.reads)),
			BalanceChanges: a.balances,
			NonceChanges:   a.nonces,
			CodeChanges:    a.codes,
		}
		for _, slot := range sortedKeys(a.writes) {
			var s uint256.Int
			s.SetBytes32(slot[:])
			ac.StorageChanges = append(ac.StorageChanges, SlotChanges{Slot: s, Changes: a.writes[slot]})
		}
		for _, slot := range sortedKeys(a.reads) {
			var s uint256.Int
			s.SetBytes32(slot[:])
			ac.StorageReads = append(ac.StorageReads, s)
		}
		out = append(out, ac)
	}
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i].Address[:], out[j].Address[:]) < 0 })
	return out
}

func sortedKeys[V any](m map[types.Hash]V) []types.Hash {
	keys := make([]types.Hash, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return bytes.Compare(keys[i][:], keys[j][:]) < 0 })
	return keys
}
