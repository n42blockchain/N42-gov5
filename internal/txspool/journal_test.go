package txspool

import (
	"context"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func newJournalTestDB(tb testing.TB) kv.RwDB {
	tb.Helper()
	modules.N42Init()

	db := mdbx.NewMDBX(log.New()).InMem(tb.TempDir()).WithTableCfg(func(_ kv.TableCfg) kv.TableCfg {
		cfg := make(kv.TableCfg, len(modules.N42TableCfg)+1)
		for name, item := range modules.N42TableCfg {
			cfg[name] = item
		}
		cfg[kv.PoolTransaction] = kv.TableCfgItem{}
		return cfg
	}).MustOpen()
	tb.Cleanup(db.Close)
	return db
}

func newPersistedTestTx(nonce uint64) *transaction.Transaction {
	to := types.Address{0x01}
	return transaction.NewTx(&transaction.LegacyTx{
		Nonce:    nonce,
		To:       &to,
		Value:    uint256.NewInt(1),
		Gas:      21000,
		GasPrice: uint256.NewInt(1),
		Data:     []byte{0x01, 0x02, 0x03},
	})
}

func TestLoadPersistedTransactionsClearsUnreadableJournalEntries(t *testing.T) {
	db := newJournalTestDB(t)
	ctx := context.Background()

	if err := db.Update(ctx, func(tx kv.RwTx) error {
		return tx.Put(modules.TxPoolJournal, []byte("bad-entry"), []byte{0x03, 0x00, 0x00, 0x00})
	}); err != nil {
		t.Fatalf("seed TxPoolJournal: %v", err)
	}

	txs, err := loadPersistedTransactions(ctx, db)
	if err != nil {
		t.Fatalf("loadPersistedTransactions() error = %v", err)
	}
	if len(txs) != 0 {
		t.Fatalf("loadPersistedTransactions() returned %d txs, want 0", len(txs))
	}

	if err := db.View(ctx, func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.TxPoolJournal, []byte("bad-entry"))
		if err != nil {
			return err
		}
		if v != nil {
			t.Fatalf("expected unreadable journal entry to be cleared, got %x", v)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify TxPoolJournal cleared: %v", err)
	}
}

func TestLoadPersistedTransactionsMigratesLegacyEntriesWithoutClearingForeignData(t *testing.T) {
	db := newJournalTestDB(t)
	ctx := context.Background()

	tx := newPersistedTestTx(1)
	encoded, err := tx.Marshal()
	if err != nil {
		t.Fatalf("marshal tx: %v", err)
	}

	foreignKey := []byte("foreign-entry")
	foreignValue := []byte{0x01, 0x02, 0x03}

	if err := db.Update(ctx, func(dbTx kv.RwTx) error {
		if err := dbTx.Put(kv.PoolTransaction, tx.Hash().Bytes(), encoded); err != nil {
			return err
		}
		return dbTx.Put(kv.PoolTransaction, foreignKey, foreignValue)
	}); err != nil {
		t.Fatalf("seed legacy PoolTransaction: %v", err)
	}

	txs, err := loadPersistedTransactions(ctx, db)
	if err != nil {
		t.Fatalf("loadPersistedTransactions() error = %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("loadPersistedTransactions() returned %d txs, want 1", len(txs))
	}
	if txs[0].Hash() != tx.Hash() {
		t.Fatalf("loaded tx hash = %s, want %s", txs[0].Hash(), tx.Hash())
	}

	if err := db.View(ctx, func(dbTx kv.Tx) error {
		migrated, err := dbTx.GetOne(kv.PoolTransaction, tx.Hash().Bytes())
		if err != nil {
			return err
		}
		if migrated != nil {
			t.Fatalf("expected migrated legacy entry to be removed, got %x", migrated)
		}

		foreign, err := dbTx.GetOne(kv.PoolTransaction, foreignKey)
		if err != nil {
			return err
		}
		if string(foreign) != string(foreignValue) {
			t.Fatalf("foreign PoolTransaction entry changed: got %x want %x", foreign, foreignValue)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify legacy migration: %v", err)
	}
}
