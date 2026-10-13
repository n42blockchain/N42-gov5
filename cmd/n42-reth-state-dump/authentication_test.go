package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

var errExportCursor = errors.New("injected export cursor failure")

type exportCursorFailure struct {
	kv.Cursor
	first bool
}

func (c exportCursorFailure) First() ([]byte, []byte, error) {
	if c.first {
		return nil, nil, errExportCursor
	}
	return c.Cursor.First()
}

func (c exportCursorFailure) Next() ([]byte, []byte, error) {
	k, v, err := c.Cursor.Next()
	if err == nil && k == nil {
		return nil, nil, errExportCursor
	}
	return k, v, err
}

type exportTxFailure struct {
	kv.Tx
	first bool
}

func (tx exportTxFailure) Cursor(table string) (kv.Cursor, error) {
	c, err := tx.Tx.Cursor(table)
	if err == nil && table == modules.Account {
		return exportCursorFailure{c, tx.first}, nil
	}
	return c, err
}

func TestExportAuthenticatesPlainRowsAndPropagatesCursorErrors(t *testing.T) {
	previous := kv.ChaindataTablesCfg
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	t.Cleanup(func() { kv.ChaindataTablesCfg = previous })
	for _, defect := range []string{"healthy", "retained-empty", "account", "storage", "code", "missing-account", "omitted-storage", "omitted-account", "cursor-first", "cursor-end"} {
		t.Run(defect, func(t *testing.T) {
			db := memdb.NewTestDB(t)
			tx, err := db.BeginRw(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			addr, slot := types.BytesToAddress([]byte{1}), types.BytesToHash([]byte{2})
			code := []byte{0x60, 0}
			a := account.NewAccount()
			a.Balance.SetUint64(42)
			a.CodeHash = crypto.Keccak256Hash(code)
			encoded := a.MarshalV2()
			var value types.Hash
			value[31] = 5
			tree := qmdb.New()
			accountKey := qmdb.Hash(commitment.AccountKeyHash(addr))
			tree.Set(accountKey, encoded)
			tree.Set(qmdb.Hash(commitment.StorageKeyHash(addr, slot)), value[:])
			if defect == "retained-empty" {
				emptyAddr := types.BytesToAddress([]byte{3})
				tree.Set(qmdb.Hash(commitment.AccountKeyHash(emptyAddr)), []byte{0})
			}
			if _, _, err := tree.FlushTo(tx, 0); err != nil {
				t.Fatal(err)
			}
			storageKey := append(append([]byte{}, addr[:]...), slot[:]...)
			if err := tx.Put(modules.Account, addr[:], encoded); err != nil {
				t.Fatal(err)
			}
			if err := tx.Put(modules.Storage, storageKey, []byte{5}); err != nil {
				t.Fatal(err)
			}
			if err := tx.Put(modules.Code, a.CodeHash[:], code); err != nil {
				t.Fatal(err)
			}
			var source kv.Tx = tx
			switch defect {
			case "account":
				a.Balance.SetUint64(43)
				err = tx.Put(modules.Account, addr[:], a.MarshalV2())
			case "storage":
				err = tx.Put(modules.Storage, storageKey, []byte{6})
			case "code":
				err = tx.Put(modules.Code, a.CodeHash[:], []byte{0x61, 0})
			case "missing-account":
				tree.Delete(accountKey)
			case "omitted-storage":
				err = tx.Delete(modules.Storage, storageKey)
			case "omitted-account":
				err = tx.Delete(modules.Account, addr[:])
				if err == nil {
					err = tx.Delete(modules.Storage, storageKey)
				}
			case "cursor-first", "cursor-end":
				source = exportTxFailure{tx, defect == "cursor-first"}
			}
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			accounts, slots, err := exportAccounts(source, tree, json.NewEncoder(&out), 0, log.New(), time.Now())
			if defect == "healthy" || defect == "retained-empty" {
				if err != nil || accounts != 1 || slots != 1 {
					t.Fatalf("healthy export: %d/%d %v", accounts, slots, err)
				}
			} else if err == nil {
				t.Fatalf("export accepted %s corruption", defect)
			} else if (defect == "cursor-first" || defect == "cursor-end") && !errors.Is(err, errExportCursor) {
				t.Fatalf("cursor error lost: %v", err)
			}
		})
	}
}
