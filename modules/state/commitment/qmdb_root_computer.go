// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// QMDBRootComputer adapts the append-only twig-forest prototype (lib/qmdb) to
// the state.RootComputer interface, so replay-v2 can drive it like JMT/BMT/MPT.
// P1: in-memory only — validates that the QMDB layout produces a correct,
// deterministic world root from the per-block dirty set. P2 will back the twig
// log with the freezer and the index with MPHF.
//
// NOTE: the QMDB world root commits to live entries at their append slots, so it
// is a function of update history, not a canonical function of the key set. A
// from-live-key-set rebuild yields a different root by design; snapshot-sync must
// ship slot positions (qmdb.SnapshotLog / FromSnapshotLog).

package commitment

import (
	"bytes"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules/state"
)

// QMDBRootComputer maintains a qmdb.Tree across blocks.
type QMDBRootComputer struct {
	t              *qmdb.Tree
	flushedThrough uint64 // entry-log slots already persisted (for incremental flush)
}

// NewQMDBRootComputer creates an empty in-memory QMDB root computer.
func NewQMDBRootComputer() *QMDBRootComputer {
	return &QMDBRootComputer{t: qmdb.New()}
}

// Tree exposes the underlying tree (for snapshot/proof tests).
func (r *QMDBRootComputer) Tree() *qmdb.Tree { return r.t }

// Root returns the current world root without applying a dirty set.
func (r *QMDBRootComputer) Root() types.Hash { return types.Hash(r.t.Root()) }

// LoadFrom rebuilds the forest from a previously-flushed positional layout (the
// cross-process resume path — the QMDB root is history-dependent, so resume must
// replay positions, not rebuild from the key set). flushedThrough advances to the
// reloaded cursor so the next flush is incremental.
func (r *QMDBRootComputer) LoadFrom(g qmdb.Getter) error {
	if err := r.t.LoadFrom(g); err != nil {
		return err
	}
	r.flushedThrough = r.t.NextSlot()
	return nil
}

// FlushTo persists entries appended since the last flush plus twig metadata and
// recovery meta (positional, sequential). Returns bytes written.
func (r *QMDBRootComputer) FlushTo(p qmdb.Putter) (int, error) {
	next, n, err := r.t.FlushTo(p, r.flushedThrough)
	if err != nil {
		return n, err
	}
	r.flushedThrough = next
	return n, nil
}

// SetCold attaches the cold entry source (the persisted positional log) so the
// tree can evict flushed entries from RAM and fault them back on demand. The
// engine re-points this at the current batch's tx each batch.
func (r *QMDBRootComputer) SetCold(g qmdb.Getter) {
	r.t.SetCold(qmdb.ColdReaderFromGetter(g))
}

// EvictFlushed drops entry records up to the flushed cursor from RAM (they are
// recoverable from cold), bounding the resident entry footprint to the unflushed
// window. Must be called after FlushTo and after SetCold.
func (r *QMDBRootComputer) EvictFlushed() { r.t.EvictThrough(r.flushedThrough) }

// RootScheme reports an unspecified scheme (the prototype does not yet have a
// dedicated state.RootScheme constant).
func (*QMDBRootComputer) RootScheme() state.RootScheme { return state.RootSchemeUnknown }

// ComputeRoot applies the dirty accounts and storage to the twig forest and
// returns the new world root. Empty accounts and zero storage slots delete.
//
// QMDB is append-only and its root is a function of the APPLICATION ORDER (each
// Set consumes a new slot). Go map iteration is randomized, so applying the dirty
// maps directly would make the root non-reproducible across nodes/runs. We
// therefore collect every operation, sort by keyHash, and apply in that
// deterministic order — so any node replaying the same blocks produces the same
// append sequence and the same root (the cross-node-agreement requirement for a
// physical-layout-dependent commitment).
func (r *QMDBRootComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	type op struct {
		kh    qmdb.Hash
		value []byte // nil => delete
	}
	ops := make([]op, 0, len(accounts)+len(storage))
	for addr, acct := range accounts {
		kh := qmdb.Hash(AccountKeyHash(addr))
		if acct == nil || isAccountEmpty(acct) {
			ops = append(ops, op{kh: kh, value: nil})
		} else {
			ops = append(ops, op{kh: kh, value: EncodeAccountValue(acct)})
		}
	}
	for addr, slots := range storage {
		for slot, val := range slots {
			kh := qmdb.Hash(StorageKeyHash(addr, slot))
			if val == nil || val.IsZero() {
				ops = append(ops, op{kh: kh, value: nil})
			} else {
				var buf [32]byte
				val.WriteToSlice(buf[:])
				v := make([]byte, 32)
				copy(v, buf[:])
				ops = append(ops, op{kh: kh, value: v})
			}
		}
	}
	sort.Slice(ops, func(i, j int) bool {
		return bytes.Compare(ops[i].kh[:], ops[j].kh[:]) < 0
	})
	for _, o := range ops {
		if o.value == nil {
			r.t.Delete(o.kh)
		} else {
			r.t.Set(o.kh, o.value)
		}
	}
	return types.Hash(r.t.Root()), nil
}
