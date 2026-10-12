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

package rawdb

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// Frame-referenced bodies (docs/QS_MULTI_MILLION_DESIGN.md P1b).
//
// The BlockBody row keeps its 12-byte BodyForStorage form with no
// transactions (TxAmount 2: the two system slots), so every reader of that
// table keeps working. The ordered frame-hash list sits beside it under the
// body key plus one tag byte ('F'), like the withdrawals sidecar, and the
// frames themselves are in the Frames table keyed by FrameHash.

const frameBodyTag = 0x46

func frameBodyKey(number uint64, hash types.Hash) []byte {
	key := make([]byte, modules.NumberLength+types.HashLength+1)
	copy(key, modules.BlockBodyKey(number, hash))
	key[len(key)-1] = frameBodyTag
	return key
}

// The frame store (N42_FRAME_DB, docs/QS_MULTI_MILLION_DESIGN.md P11).
//
// A 163k-transfer block names ~17 MB of frames, keyed by hash: random
// inserts into a table that only grows, more than half of every block's
// chaindata payload and of its commit. With a frame store registered,
// frames are written there, in their own durable transaction that commits
// before the block's chaindata transaction (the ordered writer starts it at
// the hand-off, so it overlaps the previous block's write), and the
// chaindata Frames table keeps only what was stored before. Readers look in
// the frame store first, then in the Frames table.

type frameStoreHolder struct{ db kv.RwDB }

var frameStore atomic.Pointer[frameStoreHolder]

// FrameStoreTables is the frame store's table configuration.
func FrameStoreTables(kv.TableCfg) kv.TableCfg {
	return kv.TableCfg{modules.Frames: {}}
}

// SetFrameStore registers db (nil unregisters) as the frame store and
// returns the restore.
func SetFrameStore(db kv.RwDB) func() {
	var h *frameStoreHolder
	if db != nil {
		h = &frameStoreHolder{db: db}
	}
	prev := frameStore.Swap(h)
	return func() { frameStore.Store(prev) }
}

// FrameStore returns the registered frame store, or nil.
func FrameStore() kv.RwDB {
	if h := frameStore.Load(); h != nil {
		return h.db
	}
	return nil
}

// WriteFramesToStore stores the frames with data in the frame store in one
// transaction, durable when it returns (nil: no frame store).
func WriteFramesToStore(ctx context.Context, hashes []types.Hash, raws [][]byte) error {
	fdb := FrameStore()
	if fdb == nil {
		return nil
	}
	return fdb.Update(ctx, func(tx kv.RwTx) error {
		for i, h := range hashes {
			if i >= len(raws) || len(raws[i]) == 0 {
				continue
			}
			if err := tx.Put(modules.Frames, h[:], raws[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// readFrameStore returns copies of the frames of hashes the frame store
// holds (nil entries for the others).
func readFrameStore(fdb kv.RwDB, hashes []types.Hash) ([][]byte, error) {
	out := make([][]byte, len(hashes))
	err := fdb.View(context.Background(), func(tx kv.Tx) error {
		for i, h := range hashes {
			v, err := tx.GetOne(modules.Frames, h[:])
			if err != nil {
				return err
			}
			if len(v) > 0 {
				out[i] = append([]byte(nil), v...)
			}
		}
		return nil
	})
	return out, err
}

// WriteFrame stores one encoded frame under its hash in the Frames table.
func WriteFrame(db kv.Putter, h types.Hash, raw []byte) error {
	return db.Put(modules.Frames, h[:], raw)
}

// ReadFrame returns the encoded frame stored under h -- from the frame
// store, else the Frames table -- or nil.
func ReadFrame(db kv.Getter, h types.Hash) ([]byte, error) {
	if fdb := FrameStore(); fdb != nil {
		raws, err := readFrameStore(fdb, []types.Hash{h})
		if err != nil {
			return nil, err
		}
		if raws[0] != nil {
			return raws[0], nil
		}
	}
	return db.GetOne(modules.Frames, h[:])
}

// HasFrame reports whether the frame store or the Frames table holds h.
func HasFrame(db kv.Getter, h types.Hash) (bool, error) {
	if fdb := FrameStore(); fdb != nil {
		raws, err := readFrameStore(fdb, []types.Hash{h})
		if err != nil {
			return false, err
		}
		if raws[0] != nil {
			return true, nil
		}
	}
	return db.Has(modules.Frames, h[:])
}

// ReadFrameBodyHashes returns the frame-hash list of a frame-referenced
// body, or nil for any other body.
func ReadFrameBodyHashes(db kv.Getter, hash types.Hash, number uint64) ([]types.Hash, error) {
	v, err := db.GetOne(modules.BlockBody, frameBodyKey(number, hash))
	if err != nil || len(v) == 0 {
		return nil, err
	}
	if len(v)%types.HashLength != 0 {
		return nil, fmt.Errorf("frame body: hash list is %d bytes", len(v))
	}
	out := make([]types.Hash, len(v)/types.HashLength)
	for i := range out {
		copy(out[i][:], v[i*types.HashLength:])
	}
	return out, nil
}

// ReadFrameTxs decodes the transactions of the named frames, in order,
// from the Frames table.
func ReadFrameTxs(db kv.Getter, hashes []types.Hash) ([]*transaction.Transaction, error) {
	raws := make([][]byte, len(hashes))
	if fdb := FrameStore(); fdb != nil {
		var err error
		if raws, err = readFrameStore(fdb, hashes); err != nil {
			return nil, err
		}
	}
	for i, h := range hashes {
		if raws[i] != nil {
			continue
		}
		raw, err := db.GetOne(modules.Frames, h[:])
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return nil, fmt.Errorf("frame %s missing from the Frames table", h.Hex())
		}
		// The value aliases the read transaction's memory; decoded
		// transactions must outlive it.
		raws[i] = append([]byte(nil), raw...)
	}
	txs, _, err := frame.DecodeFrames(raws, 0)
	return txs, err
}

// writeFrameBody persists a frame-referenced body: the 12-byte body row with
// no transactions, the hash list, every frame not yet in the Frames table,
// and the verifiers and rewards as for any body.
func writeFrameBody(db kv.RwTx, hash types.Hash, number uint64, body *block.Body) error {
	return writeFrameBodyOpts(db, hash, number, body, false)
}

// WriteBlockFramesStored is WriteBlock for a frame-referenced block whose
// frames with data the caller already stored in the frame store
// (WriteFramesToStore): it writes the header, the body row and the hash
// list, and checks that each frame without data is stored somewhere.
func WriteBlockFramesStored(db kv.RwTx, b *block.Block) error {
	body, ok := b.Body().(*block.Body)
	if !ok || !body.IsFrameBody() || FrameStore() == nil {
		return WriteBlock(db, b)
	}
	blockNumber, err := requireBlockNumber(b, "block number unavailable")
	if err != nil {
		return err
	}
	if err := writeFrameBodyOpts(db, b.Hash(), blockNumber.Uint64(), body, true); err != nil {
		return err
	}
	header, ok := b.Header().(*block.Header)
	if !ok {
		return fmt.Errorf("illegal: assert header")
	}
	WriteHeader(db, header)
	return nil
}

// writeFrameBodyOpts writes a frame body; with a frame store its frames go
// there (in their own transaction, before this one commits) unless stored
// says the caller did that already.
func writeFrameBodyOpts(db kv.RwTx, hash types.Hash, number uint64, body *block.Body, stored bool) error {
	baseTxId, err := db.IncrementSequence(modules.BlockTx, 2)
	if err != nil {
		return err
	}
	if err := WriteBodyForStorage(db, hash, number, &block.BodyForStorage{BaseTxId: baseTxId, TxAmount: 2}); err != nil {
		return fmt.Errorf("failed to write body: %w", err)
	}
	list := make([]byte, 0, len(body.FrameHashes)*types.HashLength)
	for _, h := range body.FrameHashes {
		list = append(list, h[:]...)
	}
	if err := db.Put(modules.BlockBody, frameBodyKey(number, hash), list); err != nil {
		return fmt.Errorf("failed to write frame body: %w", err)
	}
	toStore := FrameStore() != nil
	if toStore && !stored {
		if err := WriteFramesToStore(context.Background(), body.FrameHashes, body.FrameData); err != nil {
			return fmt.Errorf("failed to write frames to the frame store: %w", err)
		}
	}
	for i, h := range body.FrameHashes {
		var raw []byte
		if i < len(body.FrameData) {
			raw = body.FrameData[i]
		}
		if len(raw) > 0 && toStore {
			continue
		}
		if len(raw) == 0 {
			ok, err := HasFrame(db, h)
			if err != nil {
				return err
			}
			if !ok {
				return fmt.Errorf("frame body: frame %d (%s) has no data", i, h.Hex())
			}
			continue
		}
		if err := WriteFrame(db, h, raw); err != nil {
			return fmt.Errorf("failed to write frame: %w", err)
		}
	}
	if len(body.Verifiers) > 0 {
		if err := WriteVerifies(db, hash, number, body.Verifiers); err != nil {
			return err
		}
	}
	if len(body.Rewards) > 0 {
		if err := WriteRewards(db, hash, number, body.Rewards); err != nil {
			return err
		}
	}
	return nil
}
