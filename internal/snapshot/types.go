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

package snapshot

import (
	"encoding/binary"
	"encoding/json"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// Meta holds the metadata for a single state snapshot.
// A snapshot is a logical marker at a block height: it records that the
// complete PlainState at that height is recoverable from the current
// PlainState minus the changesets between the snapshot and HEAD.
type Meta struct {
	BlockNumber  uint64 `json:"block_number"`
	CreatedAt    int64  `json:"created_at"`     // unix seconds
	AccountCount uint64 `json:"account_count"`
	StorageCount uint64 `json:"storage_count"`
	CodeCount    uint64 `json:"code_count"`
}

// encodeBlockNumber returns an 8-byte big-endian encoding of n.
func encodeBlockNumber(n uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, n)
	return b
}

// SaveSnapshot persists a snapshot entry in the SnapshotIndex table.
func SaveSnapshot(tx kv.RwTx, m *Meta) error {
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return tx.Put(modules.SnapshotIndex, encodeBlockNumber(m.BlockNumber), data)
}

// LoadSnapshot loads a single snapshot by block number.
func LoadSnapshot(tx kv.Tx, blockNum uint64) (*Meta, error) {
	data, err := tx.GetOne(modules.SnapshotIndex, encodeBlockNumber(blockNum))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// DeleteSnapshot removes a snapshot entry.
func DeleteSnapshot(tx kv.RwTx, blockNum uint64) error {
	return tx.Delete(modules.SnapshotIndex, encodeBlockNumber(blockNum))
}

// ListSnapshots returns all snapshots ordered by block number (ascending).
func ListSnapshots(tx kv.Tx) ([]*Meta, error) {
	c, err := tx.Cursor(modules.SnapshotIndex)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	var snapshots []*Meta
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		var m Meta
		if err := json.Unmarshal(v, &m); err != nil {
			return nil, err
		}
		snapshots = append(snapshots, &m)
	}
	return snapshots, nil
}

// LatestSnapshot returns the most recent snapshot, or nil if none exist.
func LatestSnapshot(tx kv.Tx) (*Meta, error) {
	c, err := tx.Cursor(modules.SnapshotIndex)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	k, v, err := c.Last()
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, nil
	}
	var m Meta
	if err := json.Unmarshal(v, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// OldestSnapshot returns the earliest retained snapshot, or nil if none exist.
func OldestSnapshot(tx kv.Tx) (*Meta, error) {
	c, err := tx.Cursor(modules.SnapshotIndex)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	k, v, err := c.First()
	if err != nil {
		return nil, err
	}
	if k == nil {
		return nil, nil
	}
	var m Meta
	if err := json.Unmarshal(v, &m); err != nil {
		return nil, err
	}
	return &m, nil
}
