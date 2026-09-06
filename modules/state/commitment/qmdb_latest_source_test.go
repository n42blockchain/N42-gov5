package commitment

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

// n42TestDB opens an in-memory chain database with the native table set (the
// QMDB entry log is not in lib/kv's default configuration).
func n42TestDB(t *testing.T) kv.RwDB {
	t.Helper()
	prevCfg := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prevCfg })
	return memdb.NewTestDB(t)
}

func testAcct(nonce uint64, bal uint64) *account.StateAccount {
	a := account.NewAccount()
	a.Nonce = nonce
	a.Balance = *uint256.NewInt(bal)
	return &a
}

// applyAndPersist runs one block through the owner's sequence: apply the
// dirty set, flush the new entries through a write transaction, commit it,
// then adopt the flush and evict the window.
func applyAndPersist(t *testing.T, rc *QMDBRootComputer, db kv.RwDB, dirty map[types.Address]*account.StateAccount) {
	t.Helper()
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		rc.SetCold(tx)
		if _, err := rc.ComputeRoot(dirty, nil); err != nil {
			return err
		}
		_, err := rc.FlushTo(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rc.CommitFlushed()
	rc.EvictFlushed()
	rc.TakeUndo()
	rc.SetCold(nil)
}

func decodeNonce(t *testing.T, enc []byte) uint64 {
	t.Helper()
	var a account.StateAccount
	if err := a.DecodeForStorage(enc); err != nil {
		t.Fatal(err)
	}
	return a.Nonce
}

// TestQMDBLatestAccountSource_ResidentEvictedAndEmpty: a resident entry is
// read from RAM; once the block is persisted and evicted it is faulted through
// the caller's transaction; a transaction that predates the persist is
// retried through the database, and errors when there is none; an EIP-161
// empty account reads as absent, as the plain table would.
func TestQMDBLatestAccountSource_ResidentEvictedAndEmpty(t *testing.T) {
	db := n42TestDB(t)
	rc := NewQMDBRootComputer()
	ctx := context.Background()

	a1 := types.BytesToAddress([]byte{1})
	a2 := types.BytesToAddress([]byte{2})
	empty := types.BytesToAddress([]byte{3})
	unknown := types.BytesToAddress([]byte{9})

	// Block 1 applied but not yet persisted: resident reads.
	if _, err := rc.ComputeRoot(map[types.Address]*account.StateAccount{
		a1: testAcct(7, 100), a2: testAcct(1, 5), empty: testAcct(0, 0),
	}, nil); err != nil {
		t.Fatal(err)
	}
	src := NewQMDBLatestAccountSource(rc, nil)
	ro, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Rollback()
	enc, err := src.LatestAccount(ro, a1[:])
	if err != nil || decodeNonce(t, enc) != 7 {
		t.Fatalf("resident read: enc=%x err=%v", enc, err)
	}
	if enc, err := src.LatestAccount(ro, empty[:]); err != nil || enc != nil {
		t.Fatalf("EIP-161 empty account must read as absent: enc=%x err=%v", enc, err)
	}
	if enc, err := src.LatestAccount(ro, unknown[:]); err != nil || enc != nil {
		t.Fatalf("unknown account must read as absent: enc=%x err=%v", enc, err)
	}

	// Persist block 1 (ro, opened before, now lags the store).
	if err := db.Update(ctx, func(tx kv.RwTx) error {
		rc.SetCold(tx)
		_, err := rc.FlushTo(tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	rc.CommitFlushed()
	rc.EvictFlushed()
	rc.TakeUndo()
	rc.SetCold(nil)

	fresh, err := db.BeginRo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Rollback()
	enc, err = src.LatestAccount(fresh, a2[:])
	if err != nil || decodeNonce(t, enc) != 1 {
		t.Fatalf("evicted read through a fresh transaction: enc=%x err=%v", enc, err)
	}
	// The lagging transaction cannot see the row. Without a database the
	// source must say so; with one it recovers.
	if _, err := src.LatestAccount(ro, a2[:]); err == nil {
		t.Fatal("lagging transaction with no fallback database must error, not read as absent")
	}
	withDB := NewQMDBLatestAccountSource(rc, db)
	enc, err = withDB.LatestAccount(ro, a2[:])
	if err != nil || decodeNonce(t, enc) != 1 {
		t.Fatalf("lagging transaction must be retried through the database: enc=%x err=%v", enc, err)
	}
	// And without any transaction at all.
	enc, err = withDB.LatestAccount(nil, a1[:])
	if err != nil || decodeNonce(t, enc) != 7 {
		t.Fatalf("no transaction: enc=%x err=%v", enc, err)
	}
	if enc, err := withDB.LatestAccount(nil, unknown[:]); err != nil || enc != nil {
		t.Fatalf("no transaction, unknown account: enc=%x err=%v", enc, err)
	}
}

// TestQMDBLatestAccountSource_ConcurrentWithWriter: readers on other
// goroutines while the owner applies, flushes, commits and evicts. Meant for
// -race; the values are checked only for decodability.
func TestQMDBLatestAccountSource_ConcurrentWithWriter(t *testing.T) {
	db := n42TestDB(t)
	rc := NewQMDBRootComputer()
	src := NewQMDBLatestAccountSource(rc, db)
	addrs := make([]types.Address, 64)
	for i := range addrs {
		addrs[i] = types.BytesToAddress([]byte{byte(i + 1)})
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				ro, err := db.BeginRo(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				enc, err := src.LatestAccount(ro, addrs[(i+g)%len(addrs)][:])
				ro.Rollback()
				if err != nil {
					t.Error(err)
					return
				}
				if len(enc) > 0 {
					var a account.StateAccount
					if err := a.DecodeForStorage(enc); err != nil {
						t.Error(err)
						return
					}
				}
			}
		}(g)
	}
	for blk := 1; blk <= 40; blk++ {
		dirty := map[types.Address]*account.StateAccount{}
		for i := 0; i < 8; i++ {
			dirty[addrs[(blk*3+i)%len(addrs)]] = testAcct(uint64(blk), uint64(i))
		}
		applyAndPersist(t, rc, db, dirty)
	}
	close(stop)
	wg.Wait()
}

// TestLatestAccountSeam: installing a source routes ReadLatestAccount away
// from the table without touching the writer; the write skip is its own
// switch; removing both restores the table.
func TestLatestAccountSeam(t *testing.T) {
	db := n42TestDB(t)
	rc := NewQMDBRootComputer()
	addr := types.BytesToAddress([]byte{5})
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Put(modules.Account, addr[:], testAcct(3, 3).MarshalV2())
	}); err != nil {
		t.Fatal(err)
	}
	ro, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Rollback()

	if modules.LatestAccountSourceInstalled() || modules.PlainAccountWriteSkipped() {
		t.Fatal("clean state expected")
	}
	enc, err := modules.ReadLatestAccount(ro, addr[:])
	if err != nil || decodeNonce(t, enc) != 3 {
		t.Fatalf("plain fallback: %x %v", enc, err)
	}

	modules.SetLatestAccountSource(NewQMDBLatestAccountSource(rc, db))
	t.Cleanup(func() { modules.SetLatestAccountSource(nil); modules.SetPlainAccountWriteSkipped(false) })
	if !modules.LatestAccountSourceInstalled() {
		t.Fatal("source not installed")
	}
	if modules.PlainAccountWriteSkipped() {
		t.Fatal("installing a source must not switch the writer off by itself")
	}
	enc, err = modules.ReadLatestAccount(ro, addr[:])
	if err != nil || enc != nil {
		t.Fatalf("source must answer (tree is empty), not the table: %x %v", enc, err)
	}
	modules.SetPlainAccountWriteSkipped(true)
	if !modules.PlainAccountWriteSkipped() {
		t.Fatal("write skip not set")
	}
	modules.SetLatestAccountSource(nil)
	modules.SetPlainAccountWriteSkipped(false)
	if modules.LatestAccountSourceInstalled() || modules.PlainAccountWriteSkipped() {
		t.Fatal("both switches must clear")
	}
}

// TestLookupSource_ConcurrentReadersDuringApply: many goroutines read through
// LookupSource with their own transactions while the owner applies, flushes
// and evicts blocks -- the Block-STM worker shape. For -race.
func TestLookupSource_ConcurrentReadersDuringApply(t *testing.T) {
	db := n42TestDB(t)
	rc := NewQMDBRootComputer()
	addrs := make([]types.Address, 128)
	for i := range addrs {
		addrs[i] = types.BytesToAddress([]byte{byte(i + 1), 7})
	}
	applyAndPersist(t, rc, db, map[types.Address]*account.StateAccount{addrs[0]: testAcct(1, 1), addrs[1]: testAcct(2, 2)})
	var wg sync.WaitGroup
	stop := make(chan struct{})
	var reads atomic.Int64
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				tx, err := db.BeginRo(context.Background())
				if err != nil {
					t.Error(err)
					return
				}
				src := NewLookupSource(rc, tx)
				for j := 0; j < 16; j++ {
					kh := qmdb.Hash(AccountKeyHash(addrs[(i*16+j+g)%len(addrs)]))
					if v, ok := src.Get(kh); ok && len(v) == 0 {
						t.Error("found with empty value")
					}
					reads.Add(1)
				}
				tx.Rollback()
			}
		}(g)
	}
	for blk := 1; blk <= 30; blk++ {
		dirty := map[types.Address]*account.StateAccount{}
		for i := 0; i < 16; i++ {
			dirty[addrs[(blk*5+i)%len(addrs)]] = testAcct(uint64(blk), uint64(i+1))
		}
		applyAndPersist(t, rc, db, dirty)
	}
	close(stop)
	wg.Wait()
	if reads.Load() == 0 {
		t.Fatal("no reads happened")
	}
}
