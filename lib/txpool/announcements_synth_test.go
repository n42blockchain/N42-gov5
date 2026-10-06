/*
   Copyright 2022-2026 The N42 Authors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/
//
// Synthetic tests for announcements.go helpers (GetRlp, Append*Announcements,
// IdHashKnown, FilterKnownIdHashes, getCachedBlobTxnLocked), built against a
// pool constructed with New() and populated directly via its internal maps
// (same package, no network/validation pipeline needed).

package txpool

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/kvcache"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/lib/types"
)

func newSynthTestPool(t *testing.T) (*TxPool, kv.RwDB) {
	t.Helper()
	ch := make(chan types.Announcements, 10)
	db, coreDB := memdb.NewTestPoolDB(t), memdb.NewTestDB(t)
	cfg := txpoolcfg.DefaultConfig
	cache := kvcache.New(kvcache.DefaultCoherentConfig)
	pool, err := New(ch, coreDB, cfg, cache, *uint256.NewInt(1), nil, nil, nil, nil, nil, nil, nil, nil, log.New())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return pool, db
}

func addSynthTx(pool *TxPool, hash [32]byte, addr common.Address, isLocal bool, typ byte, size uint32) {
	senderID := uint64(hash[0]) + 1
	pool.senders.senderIDs[addr] = senderID
	pool.senders.senderID2Addr[senderID] = addr
	slot := &types.TxSlot{
		IDHash:   hash,
		SenderID: senderID,
		Type:     typ,
		Size:     size,
		Rlp:      []byte{0xAA, 0xBB},
	}
	mt := newMetaTx(slot, isLocal, 0)
	pool.byHash[string(hash[:])] = mt
}

func TestGetRlpFromMemory(t *testing.T) {
	pool, db := newSynthTestPool(t)
	var hash [32]byte
	hash[0] = 1
	addr := common.Address{0x01}
	addSynthTx(pool, hash, addr, true, 0, 10)

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	defer tx.Rollback()

	rlp, err := pool.GetRlp(tx, hash[:])
	if err != nil {
		t.Fatalf("GetRlp: %v", err)
	}
	if len(rlp) != 2 || rlp[0] != 0xAA || rlp[1] != 0xBB {
		t.Fatalf("unexpected rlp: %v", rlp)
	}
}

func TestGetRlpUnknownHash(t *testing.T) {
	pool, db := newSynthTestPool(t)
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	defer tx.Rollback()

	var hash [32]byte
	hash[0] = 0xFF
	rlp, err := pool.GetRlp(tx, hash[:])
	if err != nil {
		t.Fatalf("GetRlp: %v", err)
	}
	if rlp != nil {
		t.Fatalf("expected nil rlp for unknown hash, got %v", rlp)
	}
}

func TestAppendAnnouncements(t *testing.T) {
	pool, _ := newSynthTestPool(t)
	var localHash, remoteHash [32]byte
	localHash[0], remoteHash[0] = 1, 2
	addSynthTx(pool, localHash, common.Address{0x01}, true, 0, 10)
	addSynthTx(pool, remoteHash, common.Address{0x02}, false, 1, 20)

	types_, sizes, hashes := pool.AppendLocalAnnouncements(nil, nil, nil)
	if len(types_) != 1 || len(sizes) != 1 || len(hashes) != 32 {
		t.Fatalf("expected exactly 1 local announcement, got types=%d sizes=%d hashes=%d",
			len(types_), len(sizes), len(hashes))
	}

	types2, sizes2, hashes2 := pool.AppendRemoteAnnouncements(nil, nil, nil)
	if len(types2) != 1 || len(sizes2) != 1 || len(hashes2) != 32 {
		t.Fatalf("expected exactly 1 remote announcement, got types=%d sizes=%d hashes=%d",
			len(types2), len(sizes2), len(hashes2))
	}

	typesAll, sizesAll, hashesAll := pool.AppendAllAnnouncements(nil, nil, nil)
	if len(typesAll) != 2 || len(sizesAll) != 2 || len(hashesAll) != 64 {
		t.Fatalf("expected 2 combined announcements, got types=%d sizes=%d hashes=%d",
			len(typesAll), len(sizesAll), len(hashesAll))
	}
}

func TestAppendRemoteAnnouncementsIncludesUnprocessed(t *testing.T) {
	pool, _ := newSynthTestPool(t)
	var hash [32]byte
	hash[0] = 3
	hashS := string(hash[:])
	pool.unprocessedRemoteByHash[hashS] = 0
	pool.unprocessedRemoteTxs.Txs = append(pool.unprocessedRemoteTxs.Txs, &types.TxSlot{
		IDHash: hash, Type: 2, Size: 50,
	})

	_, sizes, hashes := pool.AppendRemoteAnnouncements(nil, nil, nil)
	if len(sizes) != 1 || len(hashes) != 32 {
		t.Fatalf("expected 1 unprocessed remote announcement, got sizes=%d hashes=%d", len(sizes), len(hashes))
	}
}

func TestIdHashKnown(t *testing.T) {
	pool, db := newSynthTestPool(t)
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	defer tx.Rollback()

	var hash [32]byte
	hash[0] = 4
	known, err := pool.IdHashKnown(tx, hash[:])
	if err != nil {
		t.Fatalf("IdHashKnown: %v", err)
	}
	if known {
		t.Fatal("expected unknown hash to be unknown")
	}

	addSynthTx(pool, hash, common.Address{0x04}, false, 0, 10)
	known, err = pool.IdHashKnown(tx, hash[:])
	if err != nil {
		t.Fatalf("IdHashKnown: %v", err)
	}
	if !known {
		t.Fatal("expected hash in byHash to be known")
	}
}

func TestIdHashKnownViaDiscardReasons(t *testing.T) {
	pool, db := newSynthTestPool(t)
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	defer tx.Rollback()

	var hash [32]byte
	hash[0] = 5
	pool.discardReasonsLRU.Add(string(hash[:]), txpoolcfg.Success)

	known, err := pool.IdHashKnown(tx, hash[:])
	if err != nil {
		t.Fatalf("IdHashKnown: %v", err)
	}
	if !known {
		t.Fatal("expected hash recorded in discardReasonsLRU to be known")
	}
}

func TestFilterKnownIdHashes(t *testing.T) {
	pool, db := newSynthTestPool(t)
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	defer tx.Rollback()

	var known, unknown types.Hashes
	var kh, uh [32]byte
	kh[0], uh[0] = 6, 7
	addSynthTx(pool, kh, common.Address{0x06}, false, 0, 10)
	known = append(known, kh[:]...)
	unknown = append(unknown, uh[:]...)

	all := append(append(types.Hashes{}, known...), unknown...)
	result, err := pool.FilterKnownIdHashes(tx, all)
	if err != nil {
		t.Fatalf("FilterKnownIdHashes: %v", err)
	}
	if len(result) != 32 {
		t.Fatalf("expected exactly 1 unknown 32-byte hash, got %d bytes", len(result))
	}
	if string(result) != string(unknown) {
		t.Fatalf("expected unknown hash to be the uncached one")
	}
}

func TestGetCachedBlobTxnLocked(t *testing.T) {
	pool, db := newSynthTestPool(t)
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatalf("BeginRo: %v", err)
	}
	defer tx.Rollback()

	var hash [32]byte
	hash[0] = 8

	mt, err := pool.getCachedBlobTxnLocked(tx, hash[:])
	if err != nil {
		t.Fatalf("getCachedBlobTxnLocked: %v", err)
	}
	if mt != nil {
		t.Fatalf("expected nil for completely unknown hash, got %v", mt)
	}

	// Via minedBlobTxsByHash.
	addr := common.Address{0x08}
	slot := &types.TxSlot{IDHash: hash, SenderID: 1}
	pool.senders.senderIDs[addr] = 1
	pool.senders.senderID2Addr[1] = addr
	expected := newMetaTx(slot, false, 0)
	pool.minedBlobTxsByHash[string(hash[:])] = expected

	mt, err = pool.getCachedBlobTxnLocked(tx, hash[:])
	if err != nil {
		t.Fatalf("getCachedBlobTxnLocked: %v", err)
	}
	if mt != expected {
		t.Fatalf("expected the mined blob tx to be returned")
	}
}
