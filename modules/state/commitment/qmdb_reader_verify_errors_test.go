package commitment

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/qmdb"
)

type failingVerifyPlain struct {
	*fakePlain
	err error
}

func (p *failingVerifyPlain) ReadAccountData(types.Address) (*account.StateAccount, error) {
	return nil, p.err
}
func (p *failingVerifyPlain) ReadAccountStorage(types.Address, *types.Hash) ([]byte, error) {
	return nil, p.err
}

func TestQMDBVerifyCountsDecodeFailureAndPreservesPlainResult(t *testing.T) {
	addr := types.Address{0xab}
	want := mkAccount(1, 7)
	src := mapSource{qmdb.Hash(AccountKeyHash(addr)): {1}} // missing nonce varint
	plain := &fakePlain{acc: map[types.Address]*account.StateAccount{addr: want}}
	before, _, count := QMDBVerifyTotals()
	r := NewQMDBStateReader(src, plain, QMDBReadVerify)
	got, err := r.ReadAccountData(addr)
	if err != nil || got != want {
		t.Fatal("verify must preserve the plain result despite a QMDB decode failure")
	}
	am, sm, attempts := r.Mismatches()
	after, _, afterCount := QMDBVerifyTotals()
	if am != 1 || sm != 0 || attempts != 1 || after != before+1 || afterCount != count+1 {
		t.Fatal("decode failure incorrectly certified as a zero-mismatch comparison")
	}
	// Answering from QMDB must still propagate that error.
	if _, err := NewQMDBStateReader(src, plain, QMDBReadOn).ReadAccountData(addr); err == nil {
		t.Fatal("QMDB read mode swallowed decode failure")
	}
}

func TestQMDBVerifyCountsPlainReadFailures(t *testing.T) {
	failure := errors.New("injected plain read failure")
	plain := &failingVerifyPlain{fakePlain: &fakePlain{}, err: failure}
	r := NewQMDBStateReader(mapSource{}, plain, QMDBReadVerify)
	addr, slot := types.Address{0xab}, types.Hash{0xcd}
	beforeA, beforeS, beforeCount := QMDBVerifyTotals()
	if got, err := r.ReadAccountData(addr); got != nil || !errors.Is(err, failure) {
		t.Fatal("plain account error was not preserved")
	}
	if got, err := r.ReadAccountStorage(addr, &slot); got != nil || !errors.Is(err, failure) {
		t.Fatal("plain storage error was not preserved")
	}
	a, s, n := r.Mismatches()
	afterA, afterS, afterCount := QMDBVerifyTotals()
	if a != 1 || s != 1 || n != 2 || afterA != beforeA+1 || afterS != beforeS+1 || afterCount != beforeCount+2 {
		t.Fatal("failed reads incorrectly certified as matching absent values")
	}
}
