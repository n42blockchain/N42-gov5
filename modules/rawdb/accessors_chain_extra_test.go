// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

func testHeader(number uint64) *block.Header {
	return &block.Header{
		Number:     uint256.NewInt(number),
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(0),
		Extra:      make([]byte, 32),
	}
}

// Headers written with WriteHeader round trip through ReadHeader/ReadHeaderRAW,
// and deleteHeader removes both the header row and the hash->number mapping.
func TestWriteReadDeleteHeader(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	h := testHeader(5)
	WriteHeader(tx, h)
	hash := h.Hash()

	if got := ReadHeaderNumber(tx, hash); got == nil || *got != 5 {
		t.Fatalf("ReadHeaderNumber = %v, want 5", got)
	}
	raw := ReadHeaderRAW(tx, hash, 5)
	if len(raw) == 0 {
		t.Fatal("ReadHeaderRAW returned empty data")
	}
	got := ReadHeader(tx, hash, 5)
	if got == nil || got.Number.Uint64() != 5 {
		t.Fatalf("ReadHeader = %v", got)
	}
	if !HasHeader(tx, hash, 5) {
		t.Fatal("HasHeader = false, want true")
	}

	deleteHeader(tx, hash, 5)
	if HasHeader(tx, hash, 5) {
		t.Fatal("HasHeader = true after delete")
	}
	if ReadHeaderNumber(tx, hash) != nil {
		t.Fatal("ReadHeaderNumber non-nil after delete")
	}
	DeleteHeaderNumber(tx, hash) // no-op, must not panic
}

// ReadHeadersByNumber returns every header stored at a given height.
func TestReadHeadersByNumber(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	h1 := testHeader(9)
	h1.Extra = append([]byte{1}, make([]byte, 31)...)
	h2 := testHeader(9)
	h2.Extra = append([]byte{2}, make([]byte, 31)...)
	WriteHeader(tx, h1)
	WriteHeader(tx, h2)

	hdrs, err := ReadHeadersByNumber(tx, 9)
	if err != nil {
		t.Fatalf("ReadHeadersByNumber: %v", err)
	}
	if len(hdrs) != 2 {
		t.Fatalf("got %d headers, want 2", len(hdrs))
	}

	none, err := ReadHeadersByNumber(tx, 123456)
	if err != nil || len(none) != 0 {
		t.Fatalf("ReadHeadersByNumber at empty height = %v, %v", none, err)
	}
}

// WriteRawBody / WriteRawBodyIfNotExists / ReadBodyForStorageByKey round trip
// the base-tx-id/tx-count envelope, and the "if not exists" variant is a
// true no-op the second time around.
func TestWriteRawBodyRoundTrip(t *testing.T) {
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })

	db := memdb.NewTestDB(t)
	hash := types.Hash{0x01}
	rawTxs := [][]byte{[]byte("tx-a"), []byte("tx-b")}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		ok, lastTxnNum, err := WriteRawBodyIfNotExists(tx, hash, 3, &block.RawBody{Transactions: rawTxs})
		if err != nil {
			return err
		}
		if !ok {
			t.Fatal("WriteRawBodyIfNotExists: ok=false on first write")
		}
		if lastTxnNum == 0 {
			t.Fatal("WriteRawBodyIfNotExists: lastTxnNum=0")
		}

		// Second call must be a no-op since the body already exists.
		ok2, _, err := WriteRawBodyIfNotExists(tx, hash, 3, &block.RawBody{Transactions: rawTxs})
		if err != nil {
			return err
		}
		if ok2 {
			t.Fatal("WriteRawBodyIfNotExists: ok=true on second write")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		bfs, err := ReadStorageBody(tx, hash, 3)
		if err != nil {
			return err
		}
		if bfs.TxAmount != uint32(len(rawTxs))+2 {
			t.Fatalf("TxAmount = %d, want %d", bfs.TxAmount, len(rawTxs)+2)
		}

		k := modules.BlockBodyKey(3, hash)
		bfsByKey, err := ReadBodyForStorageByKey(tx, k)
		if err != nil {
			return err
		}
		if bfsByKey == nil || bfsByKey.BaseTxId != bfs.BaseTxId {
			t.Fatalf("ReadBodyForStorageByKey = %+v", bfsByKey)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// ReadBodyForStorageByKey returns (nil, nil) for a missing key and an error
// for a malformed (too short) stored value.
func TestReadBodyForStorageByKeyEdgeCases(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	k := modules.BlockBodyKey(1, types.Hash{0xaa})
	got, err := ReadBodyForStorageByKey(tx, k)
	if err != nil || got != nil {
		t.Fatalf("missing key: got %v, err %v", got, err)
	}

	if err := tx.Put(modules.BlockBody, k, []byte("short")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadBodyForStorageByKey(tx, k); err == nil {
		t.Fatal("expected error for malformed body raw")
	}

	// ReadStorageBody also rejects malformed data.
	if _, err := ReadStorageBody(tx, types.Hash{0xaa}, 1); err == nil {
		t.Fatal("ReadStorageBody: expected error for malformed data")
	}
}

// WriteEngineWithdrawalsRLP / ReadEngineWithdrawalsRLP / DeleteEngineWithdrawalsRLP
// round trip, and a nil write deletes the sidecar.
func TestEngineWithdrawalsRLPRoundTrip(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	hash, number := types.Hash{0x02}, uint64(7)

	got, err := ReadEngineWithdrawalsRLP(tx, hash, number)
	if err != nil || got != nil {
		t.Fatalf("missing sidecar: got %v, err %v", got, err)
	}

	payload := []byte{0xde, 0xad, 0xbe, 0xef}
	if err := WriteEngineWithdrawalsRLP(tx, hash, number, payload); err != nil {
		t.Fatal(err)
	}
	got, err = ReadEngineWithdrawalsRLP(tx, hash, number)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("ReadEngineWithdrawalsRLP = %v, %v", got, err)
	}

	if err := WriteEngineWithdrawalsRLP(tx, hash, number, nil); err != nil {
		t.Fatal(err)
	}
	got, err = ReadEngineWithdrawalsRLP(tx, hash, number)
	if err != nil || got != nil {
		t.Fatalf("after nil write: got %v, err %v", got, err)
	}

	// DeleteEngineWithdrawalsRLP on an already-absent key must not error.
	if err := WriteEngineWithdrawalsRLP(tx, hash, number, payload); err != nil {
		t.Fatal(err)
	}
	if err := DeleteEngineWithdrawalsRLP(tx, hash, number); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadEngineWithdrawalsRLP(tx, hash, number); err != nil || got != nil {
		t.Fatalf("after delete: got %v, err %v", got, err)
	}
}

// ReadSenders decodes the packed address list written at a body key, and
// returns an empty slice for a missing one.
func TestReadSenders(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	hash, number := types.Hash{0x03}, uint64(4)

	empty, err := ReadSenders(tx, hash, number)
	if err != nil || len(empty) != 0 {
		t.Fatalf("ReadSenders on missing key = %v, %v", empty, err)
	}

	a1 := types.HexToAddress("0x1000000000000000000000000000000000000001")
	a2 := types.HexToAddress("0x2000000000000000000000000000000000000002")
	data := append(append([]byte{}, a1[:]...), a2[:]...)
	if err := tx.Put(modules.Senders, modules.BlockBodyKey(number, hash), data); err != nil {
		t.Fatal(err)
	}
	got, err := ReadSenders(tx, hash, number)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != a1 || got[1] != a2 {
		t.Fatalf("ReadSenders = %v", got)
	}
}

// WriteBody writes transactions, verifiers and rewards together; the
// canonical read path reassembles all three.
func TestWriteBodyAndReadCanonicalBodyWithTransactions(t *testing.T) {
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })

	db := memdb.NewTestDB(t)
	hash, number := types.Hash{0x04}, uint64(11)
	txs := benchWriteTxs(3)

	verifies := []*block.Verify{{}}
	rewards := []*block.Reward{{Address: types.Address{0x09}, Amount: uint256.NewInt(5)}}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		body := &block.Body{Txs: txs, Verifiers: verifies, Rewards: rewards}
		if err := WriteCanonicalHash(tx, hash, number); err != nil {
			return err
		}
		return WriteBody(tx, hash, number, body)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		body := ReadCanonicalBodyWithTransactions(tx, hash, number)
		if body == nil {
			t.Fatal("ReadCanonicalBodyWithTransactions = nil")
		}
		if len(body.Txs) != len(txs) {
			t.Fatalf("got %d txs, want %d", len(body.Txs), len(txs))
		}
		if len(body.Rewards) != 1 || body.Rewards[0].Amount.Uint64() != 5 {
			t.Fatalf("Rewards = %v", body.Rewards)
		}

		got, err := ReadBodyWithTransactions(tx, hash, number)
		if err != nil || got == nil || len(got.Txs) != len(txs) {
			t.Fatalf("ReadBodyWithTransactions = %v, %v", got, err)
		}

		if _, err := ReadBodyWithTransactions(tx, types.Hash{0xff}, number); err == nil {
			t.Fatal("ReadBodyWithTransactions: expected mismatch error for wrong hash")
		}

		gotBody, baseTxId, txAmount, err := ReadBodyByNumber(tx, number)
		if err != nil || gotBody == nil {
			t.Fatalf("ReadBodyByNumber = %v, %v, %v, %v", gotBody, baseTxId, txAmount, err)
		}

		missingBody, _, _, err := ReadBodyByNumber(tx, number+100)
		if err != nil || missingBody != nil {
			t.Fatalf("ReadBodyByNumber at missing number = %v, %v", missingBody, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// RawTransactionsRange collects the raw transaction payloads over a block
// range, skipping blocks with no canonical body.
func TestRawTransactionsRange(t *testing.T) {
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })

	db := memdb.NewTestDB(t)
	hash, number := types.Hash{0x05}, uint64(20)
	txs := benchWriteTxs(2)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		if err := WriteCanonicalHash(tx, hash, number); err != nil {
			return err
		}
		return WriteBody(tx, hash, number, &block.Body{Txs: txs})
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		raws, err := RawTransactionsRange(tx, number-1, number+1)
		if err != nil {
			return err
		}
		if len(raws) == 0 {
			t.Fatal("RawTransactionsRange returned nothing")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// CanonicalTxnByID reads one transaction by its global tx id.
func TestCanonicalTxnByID(t *testing.T) {
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })

	db := memdb.NewTestDB(t)
	txs := benchWriteTxs(2)
	const base = uint64(500)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return WriteTransactions(tx, txs, base)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		got, err := CanonicalTxnByID(tx, base)
		if err != nil {
			return err
		}
		if got.Hash() != txs[0].Hash() {
			t.Fatalf("CanonicalTxnByID hash mismatch")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// ReadTdByHash resolves a hash through the header-number index before
// reading the TD row, and returns nil for an unknown hash.
func TestReadTdByHashResolvesViaHeaderNumber(t *testing.T) {
	_, tx := memdb.NewTestTx(t)
	h := testHeader(42)
	WriteHeader(tx, h)
	hash := h.Hash()

	if err := WriteTd(tx, hash, 42, uint256.NewInt(99)); err != nil {
		t.Fatal(err)
	}
	td, err := ReadTdByHash(tx, hash)
	if err != nil || td == nil || td.Uint64() != 99 {
		t.Fatalf("ReadTdByHash = %v, %v", td, err)
	}

	td, err = ReadTdByHash(tx, types.Hash{0xee})
	if err != nil || td != nil {
		t.Fatalf("ReadTdByHash unknown = %v, %v", td, err)
	}
}

// LastKey/FirstKey/SecondKey walk a cursor's extremes.
func TestFirstLastSecondKey(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	if _, err := FirstKey(tx, modules.HeaderCanonical); err != nil {
		t.Fatalf("FirstKey on empty table: %v", err)
	}
	if _, err := LastKey(tx, modules.HeaderCanonical); err != nil {
		t.Fatalf("LastKey on empty table: %v", err)
	}

	if err := WriteCanonicalHash(tx, types.Hash{0x01}, 1); err != nil {
		t.Fatal(err)
	}
	if err := WriteCanonicalHash(tx, types.Hash{0x02}, 2); err != nil {
		t.Fatal(err)
	}
	if err := WriteCanonicalHash(tx, types.Hash{0x03}, 3); err != nil {
		t.Fatal(err)
	}

	first, err := FirstKey(tx, modules.HeaderCanonical)
	if err != nil || first == nil {
		t.Fatalf("FirstKey = %v, %v", first, err)
	}
	last, err := LastKey(tx, modules.HeaderCanonical)
	if err != nil || last == nil {
		t.Fatalf("LastKey = %v, %v", last, err)
	}
	second, err := SecondKey(tx, modules.HeaderCanonical)
	if err != nil || second == nil {
		t.Fatalf("SecondKey = %v, %v", second, err)
	}
	secondNum, err := modules.DecodeBlockNumber(second)
	if err != nil || secondNum != 2 {
		t.Fatalf("SecondKey decoded = %d, %v, want 2", secondNum, err)
	}
}

// TruncateCanonicalHash removes mapping rows at/after a pivot, and with
// deleteHeaders set also removes the matching header.
func TestTruncateCanonicalHash(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	for n := uint64(1); n <= 3; n++ {
		h := testHeader(n)
		WriteHeader(tx, h)
		if err := WriteCanonicalHash(tx, h.Hash(), n); err != nil {
			t.Fatal(err)
		}
	}

	if err := TruncateCanonicalHash(tx, 2, true); err != nil {
		t.Fatal(err)
	}

	h1, err := ReadCanonicalHash(tx, 1)
	if err != nil || h1 == (types.Hash{}) {
		t.Fatalf("block 1 canonical hash removed unexpectedly: %v, %v", h1, err)
	}
	h2, err := ReadCanonicalHash(tx, 2)
	if err != nil || h2 != (types.Hash{}) {
		t.Fatalf("block 2 canonical hash still present: %v, %v", h2, err)
	}
}

// TruncateBlocks removes headers/bodies/tx rows from blockFrom onward, and
// clamps blockFrom to 1 to protect genesis.
func TestTruncateBlocks(t *testing.T) {
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })

	db := memdb.NewTestDB(t)
	txs := benchWriteTxs(2)

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		for n := uint64(1); n <= 3; n++ {
			h := testHeader(n)
			WriteHeader(tx, h)
			if err := WriteCanonicalHash(tx, h.Hash(), n); err != nil {
				return err
			}
			if err := WriteBody(tx, h.Hash(), n, &block.Body{Txs: txs}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return TruncateBlocks(context.Background(), tx, 2)
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		hash2, _ := ReadCanonicalHash(tx, 2)
		if HasHeader(tx, hash2, 2) {
			t.Fatal("header 2 still present after TruncateBlocks")
		}
		hash1, _ := ReadCanonicalHash(tx, 1)
		if !HasHeader(tx, hash1, 1) {
			t.Fatal("header 1 removed, genesis-adjacent block must survive")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// blockFrom < 1 is clamped to 1 (protects genesis); must not error on an
	// already-truncated table.
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return TruncateBlocks(context.Background(), tx, 0)
	}); err != nil {
		t.Fatal(err)
	}
}

// PruneTable deletes rows keyed by an 8-byte big-endian block number below a
// pruneTo cutoff, bounded by a row limit per call.
func TestPruneTable(t *testing.T) {
	_, tx := memdb.NewTestTx(t)

	for n := uint64(1); n <= 5; n++ {
		if err := WriteCanonicalHash(tx, types.Hash{byte(n)}, n); err != nil {
			t.Fatal(err)
		}
	}

	if err := PruneTable(tx, modules.HeaderCanonical, 3, context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	if h, _ := ReadCanonicalHash(tx, 1); h != (types.Hash{}) {
		t.Fatal("block 1 not pruned")
	}
	if h, _ := ReadCanonicalHash(tx, 3); h == (types.Hash{}) {
		t.Fatal("block 3 pruned, should survive (>= pruneTo)")
	}

	// A canceled context must stop the walk with the sentinel error.
	for n := uint64(10); n <= 12; n++ {
		if err := WriteCanonicalHash(tx, types.Hash{byte(n)}, n); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := PruneTable(tx, modules.HeaderCanonical, 20, ctx, 100); err == nil {
		t.Fatal("expected error from canceled context")
	}
}

// PruneTableDupSort removes duplicate-value groups below pruneTo, respecting
// the per-call row limit and reporting whether the retention boundary was
// reached.
func TestPruneTableDupSort(t *testing.T) {
	modules.N42Init()
	_, tx := memdb.NewTestTx(t)

	for n := uint64(1); n <= 5; n++ {
		key := modules.EncodeBlockNumber(n)
		if err := tx.Put(modules.AccountChangeSet, key, []byte{byte(n)}); err != nil {
			t.Fatal(err)
		}
		if err := tx.Put(modules.AccountChangeSet, key, []byte{byte(n), 0x01}); err != nil {
			t.Fatal(err)
		}
	}

	logEvery := time.NewTicker(time.Hour)
	defer logEvery.Stop()

	done, err := PruneTableDupSort(tx, modules.AccountChangeSet, "test", 3, 100, logEvery, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("PruneTableDupSort: expected done=true, retention boundary reached")
	}

	c, err := tx.CursorDupSort(modules.AccountChangeSet)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	k, _, err := c.Seek(modules.EncodeBlockNumber(1))
	if err != nil {
		t.Fatal(err)
	}
	if k != nil {
		kn, err := modules.DecodeBlockNumber(k)
		if err != nil {
			t.Fatal(err)
		}
		if kn < 3 {
			t.Fatalf("row below pruneTo survived: block %d", kn)
		}
	}

	// limit=0 stops immediately and reports done=false (more remains).
	done2, err := PruneTableDupSort(tx, modules.AccountChangeSet, "test", 10, 0, logEvery, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if done2 {
		t.Fatal("PruneTableDupSort with limit=0: expected done=false")
	}
}
