// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MDBX-backed qmdb.HistoryStore: persists the QMDB full-history layer (death
// stamps, per-block cursor, per-key version positions, top-band journal)
// through the block batch's own RwTx, so history commits atomically with the
// chain data. Re-point the tx per batch with SetTx (the prior batch's tx is
// gone after commit/rollback), mirroring the cold-reader/index pattern.

package commitment

import (
	"encoding/binary"
	"errors"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

// MDBXQMDBHistoryStore implements qmdb.HistoryStore over a kv.Tx. Reads work
// on a read-only tx (offline ProofAtHeight checkers); writes require the tx to
// be a kv.RwTx and error otherwise.
type MDBXQMDBHistoryStore struct {
	tx kv.Tx
}

// NewMDBXQMDBHistoryStore wraps tx. Call SetTx on every new batch.
func NewMDBXQMDBHistoryStore(tx kv.Tx) *MDBXQMDBHistoryStore {
	return &MDBXQMDBHistoryStore{tx: tx}
}

// SetTx re-points the store at the current batch's transaction.
func (s *MDBXQMDBHistoryStore) SetTx(tx kv.Tx) { s.tx = tx }

func (s *MDBXQMDBHistoryStore) rw() (kv.RwTx, error) {
	if rw, ok := s.tx.(kv.RwTx); ok {
		return rw, nil
	}
	return nil, errHistoryReadOnly
}

var errHistoryReadOnly = errors.New("qmdb history store: write on a read-only tx")

func be8h(v uint64) []byte {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	return b[:]
}

func (s *MDBXQMDBHistoryStore) PutDeathStamps(twigID uint64, blob []byte) error {
	rw, err := s.rw()
	if err != nil {
		return err
	}
	return rw.Put(modules.QMDBDeathStamps, be8h(twigID), blob)
}

func (s *MDBXQMDBHistoryStore) GetDeathStamps(twigID uint64) ([]byte, error) {
	return s.tx.GetOne(modules.QMDBDeathStamps, be8h(twigID))
}

func (s *MDBXQMDBHistoryStore) PutBlockPos(block, nextSlot uint64) error {
	rw, err := s.rw()
	if err != nil {
		return err
	}
	return rw.Put(modules.QMDBBlockPos, be8h(block), be8h(nextSlot))
}

func (s *MDBXQMDBHistoryStore) FloorBlockPos(block uint64) (uint64, bool, error) {
	return s.floor8(modules.QMDBBlockPos, block, func(v []byte) uint64 {
		if len(v) < 8 {
			return 0
		}
		return binary.BigEndian.Uint64(v)
	})
}

// floor8 finds the newest record with key ≤ block in a BE8-keyed table.
func (s *MDBXQMDBHistoryStore) floor8(table string, block uint64, dec func([]byte) uint64) (uint64, bool, error) {
	c, err := s.tx.Cursor(table)
	if err != nil {
		return 0, false, err
	}
	defer c.Close()
	k, v, err := c.Seek(be8h(block + 1))
	if err != nil {
		return 0, false, err
	}
	if k == nil {
		k, v, err = c.Last()
	} else {
		k, v, err = c.Prev()
	}
	if err != nil || k == nil {
		return 0, false, err
	}
	return dec(v), true, nil
}

func (s *MDBXQMDBHistoryStore) AppendKeyVersion(keyHash qmdb.Hash, pos uint64) error {
	// DupSort: dups sort as raw bytes, and BE8 positions sort numerically —
	// KeyVersions walks them back in ascending order.
	rw, err := s.rw()
	if err != nil {
		return err
	}
	return rw.Put(modules.QMDBKeyVers, keyHash[:], be8h(pos))
}

func (s *MDBXQMDBHistoryStore) KeyVersions(keyHash qmdb.Hash) ([]uint64, error) {
	c, err := s.tx.CursorDupSort(modules.QMDBKeyVers)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	var out []uint64
	for k, v, err := c.SeekExact(keyHash[:]); k != nil; k, v, err = c.NextDup() {
		if err != nil {
			return nil, err
		}
		if len(v) >= 8 {
			out = append(out, binary.BigEndian.Uint64(v))
		}
	}
	return out, nil
}

func (s *MDBXQMDBHistoryStore) PutTopBand(block uint64, packed []byte) error {
	rw, err := s.rw()
	if err != nil {
		return err
	}
	return rw.Put(modules.QMDBTopBand, be8h(block), packed)
}

func (s *MDBXQMDBHistoryStore) FloorTopBand(block uint64) ([]byte, bool, error) {
	c, err := s.tx.Cursor(modules.QMDBTopBand)
	if err != nil {
		return nil, false, err
	}
	defer c.Close()
	k, v, err := c.Seek(be8h(block + 1))
	if err != nil {
		return nil, false, err
	}
	if k == nil {
		k, v, err = c.Last()
	} else {
		k, v, err = c.Prev()
	}
	if err != nil || k == nil {
		return nil, false, err
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, true, nil
}
