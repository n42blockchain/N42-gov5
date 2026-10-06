package parallel

import (
	"encoding/binary"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

func encAcc(nonce uint64, bal uint64) []byte {
	a := account.StateAccount{Initialised: true, Nonce: nonce}
	a.Balance.SetUint64(bal)
	return a.MarshalV2()
}

func decAcc(t *testing.T, b []byte) *account.StateAccount {
	t.Helper()
	a, err := DecodeAccount(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return a
}

func TestMVS_DeltaComposition(t *testing.T) {
	m := NewMVS()
	k := LocationKey{Field: FieldBalance}
	k.Address[0] = 1
	// tx0 full (nonce 1, bal 100); tx1 delta +5; tx2 delta +7; tx3 full (nonce 2, bal 1000); tx4 delta +1
	m.Write(k, 0, 0, encAcc(1, 100))
	m.WriteDelta(k, 1, 0, uint256.NewInt(5))
	m.WriteDelta(k, 2, 0, uint256.NewInt(7))
	m.Write(k, 3, 0, encAcc(2, 1000))
	m.WriteDelta(k, 4, 0, uint256.NewInt(1))

	full, ftx, _, found, delta := m.ReadAccount(k, 3)
	if !found || ftx != 0 || delta == nil || delta.Uint64() != 12 {
		t.Fatalf("read at 3: found=%v tx=%d delta=%v", found, ftx, delta)
	}
	if a := decAcc(t, composeAccount(full, delta)); a.Nonce != 1 || a.Balance.Uint64() != 112 {
		t.Fatalf("composed at 3: %+v", a)
	}
	v, wtx, _, ok := m.Read(k, 5)
	if !ok || wtx != 3 {
		t.Fatalf("Read at 5: ok=%v wtx=%d", ok, wtx)
	}
	if a := decAcc(t, v); a.Nonce != 2 || a.Balance.Uint64() != 1001 {
		t.Fatalf("Read composed at 5: %+v", a)
	}
	// Only deltas before tx1's read: no full, delta nil at 1; delta 5 at 2.
	if _, _, _, found, d := m.ReadAccount(k, 1); !found || d != nil {
		t.Fatalf("read at 1: found=%v d=%v", found, d)
	}
	m.Delete(k, 0)
	if _, _, _, found, d := m.ReadAccount(k, 2); found || d == nil || d.Uint64() != 5 {
		t.Fatalf("read at 2 after delete: found=%v d=%v", found, d)
	}
	// ApplyAll: full at 3 plus delta 1.
	var got []byte
	var gotDelta *uint256.Int
	_ = m.ApplyAll(5, func(key LocationKey, value []byte, delta *uint256.Int) error {
		got, gotDelta = value, delta
		return nil
	})
	if gotDelta != nil {
		t.Fatalf("apply: delta %v with a full write present", gotDelta)
	}
	if a := decAcc(t, got); a.Nonce != 2 || a.Balance.Uint64() != 1001 {
		t.Fatalf("apply composed: %+v", a)
	}
	// Delta-only key.
	k2 := k
	k2.Address[0] = 2
	m.WriteDelta(k2, 1, 0, uint256.NewInt(3))
	m.WriteDelta(k2, 4, 0, uint256.NewInt(4))
	_ = m.ApplyAll(5, func(key LocationKey, value []byte, delta *uint256.Int) error {
		if key == k2 {
			if value != nil || delta == nil || delta.Uint64() != 7 {
				t.Fatalf("delta-only apply: value=%v delta=%v", value, delta)
			}
		}
		return nil
	})
}

func TestValidator_BalanceInsensitiveRead(t *testing.T) {
	m := NewMVS()
	k := LocationKey{Field: FieldBalance}
	k.Address[0] = 9
	base := encAcc(3, 50)
	// tx5 read the base with no deltas, ignoring balance; tx2 later credits.
	rw := NewReadWriteSet(5)
	rw.RecordAccountRead(k, -1, 0, true, base, base, false)
	rw.RecordDeltaWrite(k, uint256.NewInt(1))
	rw.MarkBalanceInsensitive(func(types.Address) bool { return false })
	if !rw.Reads[0].IgnoreBalance {
		t.Fatal("read should be balance-insensitive")
	}
	if !Validate(m, rw) {
		t.Fatal("valid before any write")
	}
	m.WriteDelta(k, 2, 0, uint256.NewInt(10))
	if !Validate(m, rw) {
		t.Fatal("a preceding delta must not invalidate a balance-insensitive read")
	}
	m.Write(k, 1, 0, encAcc(4, 50)) // nonce changed by a preceding full write
	if Validate(m, rw) {
		t.Fatal("a nonce change must invalidate")
	}
	// Balance-sensitive read of the same: the delta alone invalidates.
	m2 := NewMVS()
	rw2 := NewReadWriteSet(5)
	rw2.RecordAccountRead(k, -1, 0, true, base, base, false)
	rw2.MarkBalanceInsensitive(func(types.Address) bool { return true })
	m2.WriteDelta(k, 2, 0, uint256.NewInt(10))
	if Validate(m2, rw2) {
		t.Fatal("an observed balance must invalidate on a preceding delta")
	}
	// A full write by the transaction itself keeps the read sensitive.
	rw3 := NewReadWriteSet(5)
	rw3.RecordAccountRead(k, -1, 0, true, base, base, false)
	rw3.RecordWrite(k, encAcc(3, 60))
	rw3.MarkBalanceInsensitive(func(types.Address) bool { return false })
	if rw3.Reads[0].IgnoreBalance {
		t.Fatal("a full write of the account keeps its read balance-sensitive")
	}
}

// TestExecutor_HotRecipientBlockDeltas is the round-35 block with recipient
// credits as deltas: the senders' chains are the only dependencies left.
func TestExecutor_HotRecipientBlockDeltas(t *testing.T) {
	const S, R, numTxs, workers = 4000, 22857, 15204, 32
	sender := func(i int) int { return i % S }
	recipient := func(i int) int { return int(uint32(i)*2654435761>>7) % R }
	key := func(tag byte, n int) LocationKey {
		var k LocationKey
		k.Field = FieldBalance
		k.Address[0] = tag
		binary.BigEndian.PutUint32(k.Address[1:5], uint32(n))
		return k
	}
	baseOf := func(k LocationKey) []byte {
		if k.Address[0] == 1 {
			return encAcc(0, 1_000_000)
		}
		return encAcc(0, 0)
	}
	var ex *Executor
	ex = NewExecutor(numTxs, workers, func(i int, rw *ReadWriteSet) error {
		read := func(k LocationKey) *account.StateAccount {
			full, ftx, finc, found, delta := ex.MVS().ReadAccount(k, i)
			if found {
				v := composeAccount(full, delta)
				rw.RecordAccountRead(k, ftx, finc, false, v, nil, delta != nil)
				return decAcc(t, v)
			}
			b := baseOf(k)
			v := composeAccount(b, delta)
			rw.RecordAccountRead(k, -1, 0, true, v, b, delta != nil)
			return decAcc(t, v)
		}
		sk, rk := key(1, sender(i)), key(2, recipient(i))
		s := read(sk)
		_ = read(rk) // for its code hash; the balance is never observed
		out := account.StateAccount{Initialised: true, Nonce: s.Nonce + 1}
		out.Balance.Sub(&s.Balance, uint256.NewInt(10))
		rw.RecordWrite(sk, out.MarshalV2())
		rw.RecordDeltaWrite(rk, uint256.NewInt(10))
		observed := sk.Address
		rw.MarkBalanceInsensitive(func(a types.Address) bool { return a == observed })
		return nil
	})
	ex.SetAffinity(func(i int) uint64 { return uint64(sender(i)) })
	res := ex.Run()
	for i, r := range res {
		if r.Err != nil {
			t.Fatalf("tx %d: %v", i, r.Err)
		}
	}
	if ex.FellBack() {
		t.Fatal("sequential fallback")
	}
	e, a := ex.Stats()
	t.Logf("waves %d executions %d aborts %d", ex.Waves(), e, a)
	if ex.Waves() > 2 || e > numTxs+numTxs/20 {
		t.Fatalf("waves %d executions %d: the recipient credits still conflict", ex.Waves(), e)
	}
	total := uint64(0)
	_ = ex.MVS().ApplyAll(numTxs, func(k LocationKey, v []byte, d *uint256.Int) error {
		if k.Address[0] == 2 {
			if v != nil || d == nil {
				t.Fatalf("recipient %x: expected a delta-only entry", k.Address[:5])
			}
			total += d.Uint64()
		} else if a := decAcc(t, v); a.Balance.Uint64() != 1_000_000-10*a.Nonce {
			t.Fatalf("sender %x: %+v", k.Address[:5], a)
		}
		return nil
	})
	if total != 10*numTxs {
		t.Fatalf("credits: %d", total)
	}
}
