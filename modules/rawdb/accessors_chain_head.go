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
// Chain head pointer accessors for current header/block lookups.
// ReadCurrentBlockNumber/ReadCurrentHeader/ReadCurrentBlock resolve
// the head via ReadHeadHeaderHash and ReadHeaderNumber, while
// ReadHeadHeaderHash/ReadHeadBlockHash fetch the named pointers
// modules.HeadHeaderKey and modules.HeadBlockKey from DatabaseInfo.

package rawdb

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

func ReadCurrentBlockNumber(db kv.Getter) *uint64 {
	headHash := ReadHeadHeaderHash(db)
	return ReadHeaderNumber(db, headHash)
}

// ReadCurrentFullBlockNumber resolves the full-block head through
// HeadBlockHash. Leader-driven consensus can advance the committed block head
// independently of a header-sync head, so state/proof consumers must use this
// accessor instead of ReadCurrentBlockNumber.
func ReadCurrentFullBlockNumber(db kv.Getter) *uint64 {
	headHash := ReadHeadBlockHash(db)
	return ReadHeaderNumber(db, headHash)
}

func ReadCurrentHeader(db kv.Getter) *block.Header {
	headHash := ReadHeadHeaderHash(db)
	headNumber := ReadHeaderNumber(db, headHash)
	if headNumber == nil {
		return nil
	}
	return ReadHeader(db, headHash, *headNumber)
}

func ReadCurrentBlock(db kv.Tx) *block.Block {
	headHash := ReadHeadBlockHash(db)
	headNumber := ReadHeaderNumber(db, headHash)
	if headNumber == nil {
		return nil
	}
	return ReadBlock(db, headHash, *headNumber)
}

// ReadHeadHeaderHash retrieves the hash of the current canonical head header.
func ReadHeadHeaderHash(db kv.Getter) types.Hash {
	return readHeadHash(db, modules.HeadHeaderKey)
}

// ReadHeadBlockHash retrieves the hash of the current canonical head block.
func ReadHeadBlockHash(db kv.Getter) types.Hash {
	return readHeadHash(db, modules.HeadBlockKey)
}

func readHeadHash(db kv.Getter, table string) types.Hash {
	data, err := db.GetOne(table, []byte(table))
	if err != nil {
		log.Error("read head hash failed", "table", table, "err", err)
	}
	if len(data) == 0 {
		return types.Hash{}
	}
	return types.BytesToHash(data)
}

// WriteHeadBlockHash stores the head block's hash.
func WriteHeadBlockHash(db kv.Putter, hash types.Hash) {
	if err := db.Put(modules.HeadBlockKey, []byte(modules.HeadBlockKey), hash.Bytes()); err != nil {
		log.Crit("Failed to store last block's hash", "err", err)
	}
}

// hotStuffCommittedHeadKey stores the hash of the highest block canonicalized
// by a QC-backed commit (BlockChain.CommitToCanonical). Unlike HeadBlockKey —
// which historical import-time writers also used to touch — this marker is
// written ONLY on the commit path, so it is trustworthy evidence of HotStuff
// finality. Kept in the HeadBlockKey single-row table under its own key.
const hotStuffCommittedHeadKey = "HotStuffCommittedHead"

const (
	forkchoiceSafeHeadKey      = "ForkchoiceSafeHead"
	forkchoiceFinalizedHeadKey = "ForkchoiceFinalizedHead"
)

// WriteHotStuffCommittedHead records the QC-committed head hash.
func WriteHotStuffCommittedHead(db kv.Putter, hash types.Hash) error {
	return db.Put(modules.HeadBlockKey, []byte(hotStuffCommittedHeadKey), hash.Bytes())
}

// ReadHotStuffCommittedHead returns the QC-committed head hash, or the zero
// hash when no commit has ever been recorded (pre-marker databases).
func ReadHotStuffCommittedHead(db kv.Getter) types.Hash {
	data, err := db.GetOne(modules.HeadBlockKey, []byte(hotStuffCommittedHeadKey))
	if err != nil || len(data) == 0 {
		return types.Hash{}
	}
	return types.BytesToHash(data)
}

// WriteForkchoiceSafeHash records the execution-layer safe block selected by
// the latest valid Engine API forkchoice update.
func WriteForkchoiceSafeHash(db kv.Putter, hash types.Hash) error {
	return db.Put(modules.HeadBlockKey, []byte(forkchoiceSafeHeadKey), hash.Bytes())
}

// ReadForkchoiceSafeHash returns the latest persisted Engine API safe block.
func ReadForkchoiceSafeHash(db kv.Getter) types.Hash {
	return readNamedHeadHash(db, forkchoiceSafeHeadKey)
}

// WriteForkchoiceFinalizedHash records the execution-layer finalized block
// selected by the latest valid Engine API forkchoice update.
func WriteForkchoiceFinalizedHash(db kv.Putter, hash types.Hash) error {
	return db.Put(modules.HeadBlockKey, []byte(forkchoiceFinalizedHeadKey), hash.Bytes())
}

// ReadForkchoiceFinalizedHash returns the latest persisted Engine API finalized block.
func ReadForkchoiceFinalizedHash(db kv.Getter) types.Hash {
	return readNamedHeadHash(db, forkchoiceFinalizedHeadKey)
}

func readNamedHeadHash(db kv.Getter, key string) types.Hash {
	data, err := db.GetOne(modules.HeadBlockKey, []byte(key))
	if err != nil || len(data) == 0 {
		return types.Hash{}
	}
	return types.BytesToHash(data)
}

// WriteHeadHeaderHash stores the hash of the current canonical head header.
func WriteHeadHeaderHash(db kv.Putter, hash types.Hash) error {
	if err := db.Put(modules.HeadHeaderKey, []byte(modules.HeadHeaderKey), hash.Bytes()); err != nil {
		return fmt.Errorf("failed to store last header's hash: %w", err)
	}
	return nil
}

func GetPoaSnapshot(db kv.Getter, hash types.Hash) ([]byte, error) {
	return db.GetOne(modules.PoaSnapshot, hash.Bytes())
}

func StorePoaSnapshot(db kv.Putter, hash types.Hash, data []byte) error {
	return db.Put(modules.PoaSnapshot, hash.Bytes(), data)
}
