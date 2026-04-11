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
// High-level block assembly helpers on top of the rawdb primitives.
// ReadBlock stitches a stored header together with a canonical body
// via ReadCanonicalBodyWithTransactions to return a *block.Block, and
// ReadBlockWithSenders additionally loads per-tx sender addresses.
// HasBlock short-circuits on StorageBodyRAW without decoding txs.

package rawdb

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// ReadBlock retrieves an entire block corresponding to the hash, assembling it
// back from the stored header and body. If either the header or body could not
// be retrieved nil is returned.
//
// Note, due to concurrent download of header and block body the header and thus
// canonical hash can be stored in the database but the body data not (yet).
func ReadBlock(tx kv.Getter, hash types.Hash, number uint64) *block.Block {
	header := ReadHeader(tx, hash, number)
	if header == nil {
		return nil
	}
	body := ReadCanonicalBodyWithTransactions(tx, hash, number)
	if body == nil {
		return nil
	}
	return block.NewBlockFromStorage(hash, header, body)
}

// HasBlock is more efficient than ReadBlock because it doesn't read transactions.
// It is not equivalent to HasHeader because headers and bodies are written by different stages.
func HasBlock(db kv.Getter, hash types.Hash, number uint64) bool {
	return len(ReadStorageBodyRAW(db, hash, number)) > 0
}

func ReadBlockWithSenders(db kv.Getter, hash types.Hash, number uint64) (*block.Block, []types.Address, error) {
	block := ReadBlock(db, hash, number)
	if block == nil {
		return nil, nil, nil
	}
	senders, err := ReadSenders(db, hash, number)
	if err != nil {
		return nil, nil, err
	}
	if len(senders) != len(block.Transactions()) {
		return block, senders, nil
	}
	block.SendersToTxs(senders)
	return block, senders, nil
}

// WriteBlock serializes a block into the database, header and body separately.
func WriteBlock(db kv.RwTx, b *block.Block) error {
	blockNumber, err := requireBlockNumber(b, "block number unavailable")
	if err != nil {
		return err
	}
	if err := WriteBody(db, b.Hash(), blockNumber.Uint64(), b.Body().(*block.Body)); err != nil {
		return err
	}
	header, ok := b.Header().(*block.Header)
	if !ok {
		return fmt.Errorf("illegal: assert header")
	}
	WriteHeader(db, header)
	return nil
}

// WriteBlockPooled is like WriteBlock but uses pooled byte buffers for the
// header serialization to reduce GC pressure during high-throughput sync.
func WriteBlockPooled(db kv.RwTx, b *block.Block) error {
	blockNumber, err := requireBlockNumber(b, "block number unavailable")
	if err != nil {
		return err
	}
	if err := WriteBody(db, b.Hash(), blockNumber.Uint64(), b.Body().(*block.Body)); err != nil {
		return err
	}
	header, ok := b.Header().(*block.Header)
	if !ok {
		return fmt.Errorf("illegal: assert header")
	}
	WriteHeaderPooled(db, header)
	return nil
}

// WriteHeaderPooled is like WriteHeader but uses a pooled byte buffer for the
// protobuf serialization of the header data.
func WriteHeaderPooled(db kv.Putter, header *block.Header) {
	number, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		log.Crit("Failed to store header", "err", err)
	}
	var (
		hash      = header.Hash()
		numberU64 = number.Uint64()
	)

	if err := WriteHeaderNumber(db, hash, numberU64); err != nil {
		log.Crit("Failed to store hash to number mapping", "err", err)
	}

	data, err := header.Marshal()
	if err != nil {
		log.Crit("failed to Marshal header", "err", err)
	}
	if err := db.Put(modules.Headers, modules.HeaderKey(numberU64, hash), data); err != nil {
		log.Crit("Failed to store header", "err", err)
	}
}

func ReadBlockByNumber(db kv.Getter, number uint64) (*block.Block, error) {
	hash, err := ReadCanonicalHash(db, number)
	if err != nil {
		return nil, fmt.Errorf("failed ReadCanonicalHash: %w", err)
	}
	if hash == (types.Hash{}) {
		return nil, nil
	}
	return ReadBlock(db, hash, number), nil
}

func CanonicalBlockByNumberWithSenders(db kv.Tx, number uint64) (*block.Block, []types.Address, error) {
	hash, err := ReadCanonicalHash(db, number)
	if err != nil {
		return nil, nil, fmt.Errorf("failed ReadCanonicalHash: %w", err)
	}
	if hash == (types.Hash{}) {
		return nil, nil, nil
	}
	return ReadBlockWithSenders(db, hash, number)
}

func ReadBlockByHash(db kv.Tx, hash types.Hash) (*block.Block, error) {
	number := ReadHeaderNumber(db, hash)
	if number == nil {
		return nil, nil
	}
	return ReadBlock(db, hash, *number), nil
}

func ReadHeaderByNumber(db kv.Getter, number uint64) *block.Header {
	hash, err := ReadCanonicalHash(db, number)
	if err != nil {
		log.Error("ReadCanonicalHash failed", "err", err)
		return nil
	}
	if hash == (types.Hash{}) {
		return nil
	}
	return ReadHeader(db, hash, number)
}

func ReadHeaderByHash(db kv.Getter, hash types.Hash) (*block.Header, error) {
	number := ReadHeaderNumber(db, hash)
	if number == nil {
		return nil, nil
	}
	return ReadHeader(db, hash, *number), nil
}
