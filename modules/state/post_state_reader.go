// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// PostState is an immutable snapshot of what a finished block changed: the
// accounts it wrote or removed, the storage slots it wrote or wiped, and the
// code it deployed. It is taken from the block's IntraBlockState after the
// state root was computed, with the SAME account and storage rules the root
// walk applies (computeRootViaComputer), so a reader layered on it sees the
// block's effects exactly as the commitment tree does.
//
// It exists for the miner's chained speculative build: a build that extends
// a block this node sealed but has not written yet reads its base state from
// the store, which does not hold that block. Round 35zr: the chained build of
// an empty block credited the coinbase on the balance from before the
// parent's reward and every follower rejected it on the state root.
type PostState struct {
	accounts map[types.Address]*account.StateAccount // nil: removed by the block
	storage  map[types.Address]map[types.Hash][]byte // nil value: slot is zero
	wiped    map[types.Address]struct{}              // slots not listed read zero
	code     map[types.Hash][]byte
}

// CapturePostState snapshots the dirty set of sdb. It reads only the
// in-memory objects (never the underlying reader), so it is safe on a state
// whose transaction has been rolled back. Call it after IntermediateRoot and
// before the state is handed to the block write, which mutates object flags.
func CapturePostState(sdb *IntraBlockState) *PostState {
	ps := &PostState{
		accounts: make(map[types.Address]*account.StateAccount, len(sdb.stateObjectsDirty)),
		storage:  make(map[types.Address]map[types.Hash][]byte),
		wiped:    make(map[types.Address]struct{}),
		code:     make(map[types.Hash][]byte),
	}
	for addr := range sdb.balanceInc {
		if bi := sdb.balanceInc[addr]; bi != nil && !bi.transferred {
			if _, loaded := sdb.stateObjects[addr]; loaded {
				sdb.stateObjectsDirty[addr] = struct{}{}
			}
		}
	}
	for addr := range sdb.journal.dirties {
		sdb.stateObjectsDirty[addr] = struct{}{}
	}
	for addr := range sdb.stateObjectsDirty {
		obj := sdb.stateObjects[addr]
		// An empty account is absent: the store readers answer nil for one
		// (emptyByPlainPolicy) and the root walk deletes its key. A block's
		// value-zero system call creates the system address as an empty
		// object every block precisely because the store never shows it as
		// existing; a snapshot that showed it would make the next block's
		// SubBalance(0) find it, skip the create, and drop a leaf (35zt).
		if obj == nil || obj.deleted || obj.selfdestructed || obj.empty() {
			ps.accounts[addr] = nil
			ps.wiped[addr] = struct{}{}
			continue
		}
		acct := new(account.StateAccount)
		acct.Copy(&obj.data)
		ps.accounts[addr] = acct
		if obj.code != nil && obj.dirtyCode {
			ps.code[obj.data.CodeHash] = obj.code
		}
		if _, w := sdb.storageWipes[addr]; w {
			ps.wiped[addr] = struct{}{}
		}
		wiped := sdb.activeWipedSlots(addr)
		if len(wiped) == 0 && len(obj.dirtyKeys) == 0 {
			continue
		}
		slots := make(map[types.Hash][]byte, len(wiped)+len(obj.dirtyKeys))
		for key := range wiped {
			slots[key] = nil
		}
		for _, key := range obj.dirtyKeys {
			v := obj.dirtyValue(key)
			slots[key] = storageBytes(&v)
		}
		ps.storage[addr] = slots
	}
	return ps
}

// storageBytes encodes a slot the way the plain Storage table stores it: the
// minimal big-endian bytes, nil for zero.
func storageBytes(v *uint256.Int) []byte {
	bl := v.ByteLen()
	if bl == 0 {
		return nil
	}
	b := make([]byte, bl)
	v.WriteToSlice(b)
	return b
}

// Accounts reports how many accounts the snapshot holds (diagnostics).
func (ps *PostState) Accounts() int {
	if ps == nil {
		return 0
	}
	return len(ps.accounts)
}

// PostStateReader answers reads from a PostState first and falls through to
// base for everything the block did not touch. Layer one per unwritten
// ancestor, oldest innermost.
type PostStateReader struct {
	post *PostState
	base StateReader
}

// NewPostStateReader layers post over base.
func NewPostStateReader(post *PostState, base StateReader) *PostStateReader {
	return &PostStateReader{post: post, base: base}
}

func (r *PostStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if a, ok := r.post.accounts[address]; ok {
		if a == nil {
			return nil, nil
		}
		c := new(account.StateAccount)
		c.Copy(a)
		return c, nil
	}
	return r.base.ReadAccountData(address)
}

func (r *PostStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	if slots, ok := r.post.storage[address]; ok {
		if v, hit := slots[*key]; hit {
			return v, nil
		}
	}
	if _, wiped := r.post.wiped[address]; wiped {
		return nil, nil
	}
	return r.base.ReadAccountStorage(address, key)
}

func (r *PostStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	if c, ok := r.post.code[codeHash]; ok {
		return c, nil
	}
	return r.base.ReadAccountCode(address, codeHash)
}

func (r *PostStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	if c, ok := r.post.code[codeHash]; ok {
		return len(c), nil
	}
	return r.base.ReadAccountCodeSize(address, codeHash)
}

// ForEachStorage merges the snapshot over the base enumeration: overlay slots
// win, a wiped account contributes only its overlay slots.
func (r *PostStateReader) ForEachStorage(addr types.Address, f func(slot types.Hash, value []byte) bool) error {
	slots := r.post.storage[addr]
	seen := make(map[types.Hash]struct{}, len(slots))
	for key, v := range slots {
		seen[key] = struct{}{}
		if len(v) == 0 {
			continue
		}
		if !f(key, v) {
			return nil
		}
	}
	if _, wiped := r.post.wiped[addr]; wiped {
		return nil
	}
	enum, ok := r.base.(StorageEnumerator)
	if !ok {
		return ErrNoStorageEnumeration
	}
	return enum.ForEachStorage(addr, func(slot types.Hash, value []byte) bool {
		if _, shadowed := seen[slot]; shadowed {
			return true
		}
		return f(slot, value)
	})
}

// PostStateLayers returns the PostState snapshots layered on reader,
// outermost (newest) first, and the reader beneath them. A component that
// opens its own store transactions to read base state -- the parallel
// builder's per-worker readers -- must layer the same snapshots over each
// of them, or a chained build reads the store without its unwritten parent
// (round 35zu: the faucet lost the parent's reward in the parallel fill).
func PostStateLayers(reader StateReader) (layers []*PostState, base StateReader) {
	for {
		psr, ok := reader.(*PostStateReader)
		if !ok {
			return layers, reader
		}
		layers = append(layers, psr.post)
		reader = psr.base
	}
}

// LayerPostStates wraps base with layers as returned by PostStateLayers
// (outermost first), reproducing the same read order.
func LayerPostStates(layers []*PostState, base StateReader) StateReader {
	for i := len(layers) - 1; i >= 0; i-- {
		base = NewPostStateReader(layers[i], base)
	}
	return base
}

// SetPostStateLayers records the snapshots layered on this state's reader
// so a component that opens its own store transactions can reproduce them
// without walking the reader chain -- which other wrappers (the mobile
// read-log recorder, the JMT tracing reader) hide from a type walk (round
// 35zx: every worker of the parallel fill read the store without the
// unwritten parent while the block's own reads were right).
func (sdb *IntraBlockState) SetPostStateLayers(layers []*PostState) {
	sdb.postLayers = layers
}

// PostStateLayers returns what SetPostStateLayers recorded, outermost first.
func (sdb *IntraBlockState) PostStateLayers() []*PostState {
	return sdb.postLayers
}
