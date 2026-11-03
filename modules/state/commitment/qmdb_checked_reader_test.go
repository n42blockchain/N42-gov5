package commitment

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
)

type checkedFailureSource struct{ err error }

func (s checkedFailureSource) Get(qmdb.Hash) ([]byte, bool) { return nil, false }
func (s checkedFailureSource) GetChecked(qmdb.Hash) ([]byte, bool, error) {
	return nil, false, s.err
}

func TestQMDBReaderPropagatesCheckedSourceErrors(t *testing.T) {
	failure := errors.New("cold read failure")
	src := checkedFailureSource{failure}
	addr, slot := types.Address{1}, types.Hash{2}
	for _, mode := range []QMDBReadMode{QMDBReadOff, QMDBReadVerify, QMDBReadOn} {
		r := NewQMDBStateReader(src, &fakePlain{}, mode)
		_, aerr := r.ReadAccountData(addr)
		_, serr := r.ReadAccountStorage(addr, &slot)
		if mode == QMDBReadOn {
			if !errors.Is(aerr, failure) || !errors.Is(serr, failure) {
				t.Fatal("QMDB execution swallowed a cold/index error")
			}
		} else if aerr != nil || serr != nil {
			t.Fatal("plain execution result changed")
		}
		a, s, n := r.Mismatches()
		if mode == QMDBReadVerify && (a != 1 || s != 1 || n != 2) {
			t.Fatal("failed checked reads certified as equal nil values")
		}
		if mode != QMDBReadVerify && (a != 0 || s != 0 || n != 0) {
			t.Fatal("non-verify mode counted comparisons")
		}
	}
}

type checkedIndexTx struct {
	kv.RwTx
	value   []byte
	present bool
	err     error
}

func (tx checkedIndexTx) GetOne(string, []byte) ([]byte, error) { return tx.value, tx.err }
func (tx checkedIndexTx) Has(string, []byte) (bool, error)      { return tx.present, tx.err }

func TestQMDBMDBXCheckedIndexRejectsMalformedRecords(t *testing.T) {
	for _, size := range []int{0, 1, 7, 9} {
		idx := &qmdbMDBXIndex{tx: checkedIndexTx{value: make([]byte, size), present: true}}
		if _, ok, err := idx.GetChecked(qmdb.Hash{}); ok || err == nil {
			t.Fatalf("accepted present index record of length %d", size)
		}
	}
	idx := &qmdbMDBXIndex{tx: checkedIndexTx{}}
	if _, ok, err := idx.GetChecked(qmdb.Hash{}); ok || err != nil {
		t.Fatal("missing index key is not an error")
	}
	idx.tx = checkedIndexTx{value: make([]byte, 8), present: true}
	if slot, ok, err := idx.GetChecked(qmdb.Hash{}); !ok || slot != 0 || err != nil {
		t.Fatal("slot zero must remain a valid index target")
	}
	failure := errors.New("index read failure")
	idx.tx = checkedIndexTx{err: failure}
	if _, _, err := idx.GetChecked(qmdb.Hash{}); !errors.Is(err, failure) {
		t.Fatal("index read error was swallowed")
	}
}
