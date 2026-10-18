package rawdb

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/ancientera"
)

type checkedReceiptReadErrorTx struct {
	kv.Tx
	err error
}

func (tx checkedReceiptReadErrorTx) GetOne(table string, key []byte) ([]byte, error) {
	if table == modules.Receipts {
		return nil, tx.err
	}
	return tx.Tx.GetOne(table, key)
}

func TestReadReceiptsWithErrorPreservesArchiveEmptyAndPropagatesExecError(t *testing.T) {
	hashes, _ := buildFallbackFixture(t)
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	const n = uint64(123)
	if err := tx.Put(modules.HeaderCanonical, modules.EncodeBlockNumber(n), hashes[n][:]); err != nil {
		t.Fatal(err)
	}
	b := ReadBlock(tx, hashes[n], n)
	if b == nil {
		t.Fatal("missing real archived block")
	}
	receipts, err := ReadReceiptsWithError(tx, b, nil)
	if err != nil || len(receipts) != 0 {
		t.Fatalf("valid zero-byte archived receipt list: %v %v", receipts, err)
	}
	AncientStore().Close()
	if _, err := ReadReceiptsWithError(tx, b, nil); !errors.Is(err, ancientera.ErrPruned) {
		t.Fatalf("archive execution error hidden: %v", err)
	}
	// A hot-store error must be returned directly, even though the archive is
	// also unavailable. It must never trigger the legacy miss fallback.
	sentinel := errors.New("hot receipt store read failed")
	if _, err := ReadReceiptsWithError(checkedReceiptReadErrorTx{Tx: tx, err: sentinel}, b, nil); !errors.Is(err, sentinel) {
		t.Fatalf("hot IO error replaced by archive fallback: %v", err)
	}
}

func TestReadReceiptsWithErrorRejectsArchivedMalformedEncoding(t *testing.T) {
	hashes, _ := buildFallbackFixture(t, []byte{0xff})
	db := memdb.NewTestDB(t)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	const n = uint64(123)
	if err := tx.Put(modules.HeaderCanonical, modules.EncodeBlockNumber(n), hashes[n][:]); err != nil {
		t.Fatal(err)
	}
	b := ReadBlock(tx, hashes[n], n)
	if b == nil {
		t.Fatal("missing archived block")
	}
	if _, err := ReadReceiptsWithError(tx, b, nil); err == nil {
		t.Fatal("malformed archived receipts accepted for empty block")
	}
}
