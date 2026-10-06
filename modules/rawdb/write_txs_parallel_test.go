package rawdb

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
)

// A block above the parallel-encode threshold must store byte-identical
// rows to the serial path and read back every transaction.
func TestWriteTransactionsParallelMatchesSerial(t *testing.T) {
	modules.N42Init()
	prev := kv.ChaindataTablesCfg
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = prev })
	txs := benchWriteTxs(parallelTxEncodeMin + 123)
	db := memdb.NewTestDB(t)
	const base = uint64(1000)
	if err := db.Update(context.Background(), func(tx kv.RwTx) error {
		return WriteTransactions(tx, txs, base)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		got, err := CanonicalTransactions(tx, base, uint32(len(txs)))
		if err != nil {
			return err
		}
		if len(got) != len(txs) {
			t.Fatalf("read back %d transactions, wrote %d", len(got), len(txs))
		}
		for i := range txs {
			if got[i].Hash() != txs[i].Hash() {
				t.Fatalf("transaction %d: read %x, wrote %x", i, got[i].Hash(), txs[i].Hash())
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// The serial encoding of each transaction is what the rows hold.
	for i := 0; i < 5; i++ {
		want, err := encodeTxForStorage(txs[i])
		if err != nil {
			t.Fatal(err)
		}
		encs, err := encodeTxsParallel(txs[:parallelTxEncodeMin])
		if err != nil {
			t.Fatal(err)
		}
		if string(encs[i]) != string(want) {
			t.Fatalf("transaction %d: parallel encoding differs from serial", i)
		}
	}
}
