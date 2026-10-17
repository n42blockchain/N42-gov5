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

package parallel

import (
	"encoding/binary"
	"os"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// ParallelStateReader wraps a base StateReader and layers MVS reads on top.
// For each state read, it first checks the MVS for writes from preceding
// transactions. If found, it uses the MVS value; otherwise, it falls back
// to the base reader. All reads are recorded in the ReadWriteSet.
type ParallelStateReader struct {
	base    state.StateReader
	mvs     *MVS
	rw      *ReadWriteSet
	txIndex int
	// baseCache memoises base reads for the whole block (see BaseCache); nil
	// disables it, which is what the tests and the serial path use.
	baseCache *BaseCache
	// reuseAccount makes ReadAccountData return accBuf, overwritten by the
	// next call (see ReuseAccountBuffer).
	reuseAccount bool
	accBuf       account.StateAccount
	// slab, when set, holds the encodings the reader records (SetSlab).
	slab *ValueSlab
	// ents, when set, remembers the store entries ReadAccountData found
	// (EnableEntryCache).
	ents *entryCache
}

// entryCacheSlots is the size of a reader's entry cache (a power of two).
const entryCacheSlots = 2048

// entryCache is a reader's direct-mapped cache of account store entries,
// by address. A store entry, once created, is its location's for the whole
// block (entries are never removed, a table's growth keeps them), so a
// cached entry is the one a lookup would return.
type entryCache [entryCacheSlots]struct {
	addr types.Address
	e    *mvEntry
}

// EnableEntryCache makes ReadAccountData find an account's store entry in
// a per-reader cache before the store's index (fast2 only). A worker runs a
// sender's transactions in order, so from the chain's second transaction on
// the sender's entry is the one its previous transaction read and wrote;
// the index lookup was a cache miss per transfer (r35zzzco: MVS.lookup
// 8.5 s of executeSingle's 76 s, half of it the senders').
func (r *ParallelStateReader) EnableEntryCache() {
	if fast2 {
		r.ents = new(entryCache)
	}
}

// accountEntry is r.mvs.lookup(key) for an account key, through the entry
// cache when there is one.
func (r *ParallelStateReader) accountEntry(key *LocationKey) *mvEntry {
	c := r.ents
	if c == nil {
		return r.mvs.lookup(key)
	}
	a := &key.Address
	sl := &c[(binary.LittleEndian.Uint32(a[0:4])^binary.LittleEndian.Uint32(a[16:20]))&(entryCacheSlots-1)]
	if sl.e != nil && sl.addr == *a {
		return sl.e
	}
	e := r.mvs.lookup(key)
	if e != nil {
		sl.addr, sl.e = *a, e
	}
	return e
}

// SetSlab makes the reader record its composed account encodings from s,
// the worker's slab, instead of allocating each.
func (r *ParallelStateReader) SetSlab(s *ValueSlab) { r.slab = s }

// ReuseAccountBuffer makes ReadAccountData return the reader's own buffer,
// valid until its next call, instead of a new account per read. Only for a
// reader whose sole consumer copies the account before reading again: an
// IntraBlockState does (newObject copies it; Exist only tests it for nil).
// The executor's workers read two accounts per transfer, and the
// allocation was a quarter of the wave's.
func (r *ParallelStateReader) ReuseAccountBuffer() { r.reuseAccount = true }

// newAccount returns the account a read decodes into.
func (r *ParallelStateReader) newAccount() *account.StateAccount {
	if r.reuseAccount {
		r.accBuf = account.StateAccount{}
		return &r.accBuf
	}
	return new(account.StateAccount)
}

// SetBaseCache shares one block's base-read cache with this reader. Every
// worker's reader gets the same cache: the base state is the parent's
// post-state and does not change while the block executes.
func (r *ParallelStateReader) SetBaseCache(c *BaseCache) { r.baseCache = c }

// baseAccount reads an account from the base state, through the block's cache
// when there is one. It returns the account (nil when absent) and its V2
// encoding; both are shared with the cache and must not be mutated.
func (r *ParallelStateReader) baseAccount(address types.Address) (*account.StateAccount, []byte, error) {
	return r.baseAccountVia(nil, address)
}

// baseAccountVia is baseAccount for an account whose store entry is mv (nil
// when it has none): with fast2 the entry's link to the base-cache entry
// serves the read, and a read that looks the account up sets the link.
func (r *ParallelStateReader) baseAccountVia(mv *mvEntry, address types.Address) (*account.StateAccount, []byte, error) {
	link := fast2 && mv != nil && r.baseCache != nil
	if link {
		if e := mv.base.Load(); e != nil {
			return e.acc, e.enc, nil
		}
	}
	if e := r.baseCache.lookup(address); e != nil {
		if link {
			mv.base.Store(e)
		}
		return e.acc, e.enc, nil
	}
	acc, err := r.base.ReadAccountData(address)
	if err != nil {
		return nil, nil, err
	}
	if r.baseCache == nil {
		if acc == nil {
			return nil, nil, nil
		}
		enc, _ := encodeAccount(acc)
		return acc, enc, nil
	}
	e := r.baseCache.insert(address, acc)
	if link {
		mv.base.Store(e)
	}
	return e.acc, e.enc, nil
}

// NewParallelStateReader creates a state reader that reads from MVS first,
// then falls back to the base reader.
func NewParallelStateReader(base state.StateReader, mvs *MVS, rw *ReadWriteSet, txIndex int) *ParallelStateReader {
	return &ParallelStateReader{
		base:    base,
		mvs:     mvs,
		rw:      rw,
		txIndex: txIndex,
	}
}

// ReadAccountData reads account data, checking MVS first.
// Rebind points the reader at another transaction's read set and index, so
// one reader per worker serves the whole block.
func (r *ParallelStateReader) Rebind(rw *ReadWriteSet, txIndex int) {
	r.rw = rw
	r.txIndex = txIndex
}

func (r *ParallelStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	key := LocationKey{Address: address, Field: FieldBalance}

	var deltaBuf uint256.Int
	e := r.accountEntry(&key)
	full, fullTx, fullInc, found, hasDelta := e.readAccount(r.txIndex, &deltaBuf)
	var delta *uint256.Int
	if hasDelta {
		delta = &deltaBuf
	}
	if found {
		if full == nil && delta == nil {
			// Account was deleted by a preceding tx.
			r.recordAccountRead(e, key, fullTx, fullInc, false, nil, nil, false)
			return nil, nil
		}
		acc := r.newAccount()
		if delta == nil {
			r.recordAccountRead(e, key, fullTx, fullInc, false, full, nil, false)
			if err := acc.DecodeForStorageV2(full); err != nil {
				return nil, err
			}
			return acc, nil
		}
		// composeAccount and a decode of its output, in one decode: the
		// composed account is the decoded full write plus the deltas.
		if full != nil {
			if err := acc.DecodeForStorageV2(full); err != nil {
				val := composeAccount(full, delta)
				r.recordAccountRead(e, key, fullTx, fullInc, false, val, nil, true)
				return DecodeAccount(val)
			}
		} else {
			acc.Reset()
		}
		acc.Balance.Add(&acc.Balance, delta)
		r.recordAccountRead(e, key, fullTx, fullInc, false, r.slab.encodeAccount(acc), nil, true)
		return acc, nil
	}

	// No full write in the store — read from base, record the base bytes in
	// the writer's encoding so a later write of the same account by a
	// preceding transaction validates by value, and add the deltas. The base
	// account and its encoding come from the block's cache (shared, so the
	// caller gets a copy).
	base, enc, err := r.baseAccountVia(e, address)
	if err != nil {
		r.rw.RecordRead(key, -1, 0, true)
		return nil, err
	}
	if delta == nil {
		r.recordAccountRead(e, key, -1, 0, true, enc, enc, false)
		if base == nil {
			return nil, nil
		}
		acc := r.newAccount()
		*acc = *base
		return acc, nil
	}
	// The base account in its encoded form (what composeAccount decodes)
	// plus the deltas.
	acc := r.newAccount()
	if enc != nil {
		if err := acc.DecodeForStorageV2(enc); err != nil {
			return nil, err
		}
	} else {
		acc.Reset()
	}
	acc.Balance.Add(&acc.Balance, delta)
	r.recordAccountRead(e, key, -1, 0, true, r.slab.encodeAccount(acc), enc, true)
	return acc, nil
}

// ReadAccountIgnoringBalance is ReadAccountData for a caller whose outputs
// cannot depend on the account's balance (it never observes the balance
// and writes the account at most as a delta). While no transaction has
// stored a full value at the account, it returns the base account without
// the preceding transactions' deltas -- the balance is stale, every other
// field exact -- and records the read as a balance-insensitive base read,
// skipping the version scan, the delta sum and the composed encoding.
// Otherwise it is ReadAccountData. The read is recorded with
// IgnoreBalance set; the caller must honour it.
func (r *ParallelStateReader) ReadAccountIgnoringBalance(address types.Address) (*account.StateAccount, error) {
	key := LocationKey{Address: address, Field: FieldBalance}
	if e := r.mvs.lookup(&key); e.noFullWrites() {
		base, enc, err := r.baseAccountVia(e, address)
		if err != nil {
			r.rw.RecordRead(key, -1, 0, true)
			return nil, err
		}
		// An account absent from the base exists once a delta credits it:
		// that takes the composing read below.
		if base != nil {
			r.recordAccountRead(e, key, -1, 0, true, enc, enc, false)
			r.rw.Reads[len(r.rw.Reads)-1].IgnoreBalance = true
			acc := r.newAccount()
			*acc = *base
			return acc, nil
		}
	}
	acc, err := r.ReadAccountData(address)
	if err == nil {
		r.rw.Reads[len(r.rw.Reads)-1].IgnoreBalance = true
	}
	return acc, err
}

// recordAccountRead records an account read with the store entry it was
// served from (nil when the location had none yet): validation and the
// transaction's own write to the account then skip the lookup.
func (r *ParallelStateReader) recordAccountRead(e *mvEntry, key LocationKey, writerTx int, writerIncarnation uint32, fromBase bool, value, base []byte, hadDelta bool) {
	r.rw.RecordAccountRead(key, writerTx, writerIncarnation, fromBase, value, base, hadDelta)
	r.rw.Reads[len(r.rw.Reads)-1].entry = e
}

func (r *ParallelStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	locKey := LocationKey{Address: address, Field: FieldStorage, Slot: *key}

	val, writerTx, writerInc, found := r.mvs.Read(locKey, r.txIndex)
	if found {
		r.rw.RecordRead(locKey, writerTx, writerInc, false)
	} else {
		r.rw.RecordRead(locKey, -1, 0, true)
	}

	wipeKey := LocationKey{Address: address, Field: FieldStorageWipe}
	if wipeVal, wipeTx, wipeInc, wipeFound := r.mvs.Read(wipeKey, r.txIndex); wipeFound && len(wipeVal) > 0 {
		r.rw.RecordRead(wipeKey, wipeTx, wipeInc, false)
		// Wipe overrides when the slot came from base (no MVS writer → pre-wipe
		// data) or the wipe is strictly newer than the slot's writer. An equal
		// txIdx means the same tx wiped then re-wrote the slot, so the explicit
		// slot write wins (do not shadow).
		if !found || wipeTx > writerTx {
			return nil, nil
		}
	}

	if found {
		return val, nil
	}
	return r.base.ReadAccountStorage(address, key)
}

// ReadAccountCode reads contract code, checking MVS first.
func (r *ParallelStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	key := LocationKey{Address: address, Field: FieldCode}

	val, writerTx, writerInc, found := r.mvs.Read(key, r.txIndex)
	if found {
		r.rw.RecordReadValue(key, writerTx, writerInc, false, val)
		return val, nil
	}

	// Not in MVS — read from base.
	code, err := r.base.ReadAccountCode(address, codeHash)
	if err != nil {
		r.rw.RecordRead(key, -1, 0, true)
		return nil, err
	}
	r.rw.RecordReadValue(key, -1, 0, true, code)
	return code, nil
}

// ReadAccountCodeSize reads code size. Derives from ReadAccountCode.
func (r *ParallelStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

// DecodeAccount decodes a V2-encoded StateAccount.
func DecodeAccount(data []byte) (*account.StateAccount, error) {
	acc := new(account.StateAccount)
	if err := acc.DecodeForStorageV2(data); err != nil {
		return nil, err
	}
	return acc, nil
}

// encodeAccount encodes a StateAccount using V2 variable-length format.
func encodeAccount(acc *account.StateAccount) ([]byte, error) {
	return acc.MarshalV2(), nil
}

// fast2 is N42_TRANSFER_FAST2 (default on; "0" disables) on the reader's
// side: an account's store entry remembers the base-cache entry its first
// base read found, so later reads of the account through the entry skip
// the cache's lookup.
var fast2 = os.Getenv("N42_TRANSFER_FAST2") != "0"

// SetFast2ForTest sets the reader's N42_TRANSFER_FAST2 switch and returns
// the restore.
func SetFast2ForTest(on bool) func() {
	prev := fast2
	fast2 = on
	return func() { fast2 = prev }
}
