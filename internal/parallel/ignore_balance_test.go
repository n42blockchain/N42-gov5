package parallel

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// fulls tracks the full writes through inserts, overwrites in both
// directions and removals.
func TestMVSFullWriteCount(t *testing.T) {
	m := NewMVS()
	k := LocationKey{Field: FieldBalance}
	k.Address[0] = 7
	if !m.lookup(&k).noFullWrites() {
		t.Fatal("absent entry has full writes")
	}
	m.WriteDelta(k, 1, 0, uint256.NewInt(3))
	m.WriteDelta(k, 4, 0, uint256.NewInt(3))
	e := m.lookup(&k)
	if !e.noFullWrites() {
		t.Fatal("deltas counted as full writes")
	}
	m.Write(k, 2, 0, encAcc(1, 10))
	m.Write(k, 1, 1, encAcc(1, 10)) // a delta overwritten by a full write
	if n := e.fulls.Load(); n != 2 {
		t.Fatalf("fulls %d, want 2", n)
	}
	m.WriteDelta(k, 2, 1, uint256.NewInt(1)) // a full write overwritten by a delta
	m.Delete(k, 1)
	if !e.noFullWrites() {
		t.Fatalf("fulls %d after removing them", e.fulls.Load())
	}
	m.Write(k, 9, 0, nil) // a deletion is a full write
	m.DeleteAll(9)
	if !e.noFullWrites() || len(e.versions) != 2 {
		t.Fatalf("fulls %d, versions %d", e.fulls.Load(), len(e.versions))
	}
}

// A balance-insensitive read returns the base account without the deltas
// before it, is valid while only deltas follow, and fails once a full
// write with other fields precedes it. An account absent from the base
// that deltas created is composed, not reported absent.
func TestReadAccountIgnoringBalance(t *testing.T) {
	base := newCountingBase()
	present := types.Address{1}
	absent := types.Address{2}
	base.present[present] = &account.StateAccount{Initialised: true, Nonce: 5, Balance: *uint256.NewInt(100)}
	m := NewMVS()
	keyP := LocationKey{Address: present, Field: FieldBalance}
	keyA := LocationKey{Address: absent, Field: FieldBalance}
	m.WriteDelta(keyP, 0, 0, uint256.NewInt(7))
	m.WriteDelta(keyA, 0, 0, uint256.NewInt(9))

	rw := NewReadWriteSet(3)
	r := NewParallelStateReader(base, m, rw, 3)
	r.SetBaseCache(NewBaseCache(16))
	acc, err := r.ReadAccountIgnoringBalance(present)
	if err != nil || acc == nil || acc.Nonce != 5 || acc.Balance.Uint64() != 100 {
		t.Fatalf("present: %+v %v", acc, err)
	}
	acc, err = r.ReadAccountIgnoringBalance(absent)
	if err != nil || acc == nil || acc.Balance.Uint64() != 9 {
		t.Fatalf("absent but credited: %+v %v", acc, err)
	}
	for i := range rw.Reads {
		if !rw.Reads[i].IgnoreBalance {
			t.Fatalf("read %d not balance-insensitive", i)
		}
	}
	m.WriteDelta(keyP, 1, 0, uint256.NewInt(1))
	if !Validate(m, rw) {
		t.Fatal("a later delta invalidated a balance-insensitive read")
	}
	m.Write(keyP, 2, 0, encAcc(6, 50))
	if Validate(m, rw) {
		t.Fatal("a full write with another nonce did not invalidate the read")
	}
}
