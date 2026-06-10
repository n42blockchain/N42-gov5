// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MDBX-backed QMDB index (P3 memory tier 3). Implements qmdb.Index over an MDBX
// table (keyHash -> BE8(slot)), so the live-key map lives in the B+tree page
// cache instead of the Go heap and survives restarts without a rebuild. The live
// count is maintained in memory (cheap existence check on Put/Delete) and
// persisted to QMDBMeta["liveCount"] so a resumed process knows the key set size
// without scanning.

package commitment

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

var qmdbLiveCountKey = []byte("liveCount")

// qmdbMDBXIndex implements qmdb.Index over modules.QMDBIndex. The tx is repointed
// at the current batch's RwTx each batch (like the cold reader); reads/writes are
// transactional with the rest of the batch (ACID rollback on crash).
type qmdbMDBXIndex struct {
	tx    kv.RwTx
	count int
}

// newQMDBMDBXIndex builds the index over tx and restores the persisted live count.
func newQMDBMDBXIndex(tx kv.RwTx) *qmdbMDBXIndex {
	x := &qmdbMDBXIndex{tx: tx}
	if v, err := tx.GetOne(modules.QMDBMeta, qmdbLiveCountKey); err == nil && len(v) >= 8 {
		x.count = int(binary.BigEndian.Uint64(v))
	}
	return x
}

func (x *qmdbMDBXIndex) setTx(tx kv.RwTx) { x.tx = tx }

func (x *qmdbMDBXIndex) Get(k qmdb.Hash) (uint64, bool) {
	v, err := x.tx.GetOne(modules.QMDBIndex, k[:])
	if err != nil || len(v) < 8 {
		return 0, false
	}
	return binary.BigEndian.Uint64(v), true
}

func (x *qmdbMDBXIndex) Put(k qmdb.Hash, slot uint64) {
	if _, existed := x.Get(k); !existed {
		x.count++
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], slot)
	_ = x.tx.Put(modules.QMDBIndex, k[:], b[:])
}

func (x *qmdbMDBXIndex) Delete(k qmdb.Hash) {
	if _, existed := x.Get(k); existed {
		x.count--
		_ = x.tx.Delete(modules.QMDBIndex, k[:])
	}
}

func (x *qmdbMDBXIndex) Len() int { return x.count }

// persistCount writes the live count so a resumed process can size its key set
// (and so LoadFrom's rebuild-vs-fast-path decision is correct).
func (x *qmdbMDBXIndex) persistCount() {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(x.count))
	_ = x.tx.Put(modules.QMDBMeta, qmdbLiveCountKey, b[:])
}
