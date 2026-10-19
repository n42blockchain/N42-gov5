package parallel

import (
	"bytes"
	"math/rand"
	"reflect"
	"sort"
	"testing"
	"unsafe"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// oneLookupScript is fast2Script with the direct transfer's reads (the
// ForWrite variants, which create the entry) and its writes through the
// executor's write path (writeEntry and its slot guess), so the version
// lists grow long, out of order and with re-executions, and every
// recorded position goes stale in some reads before validation.
func oneLookupScript(seed int64) fast2Trace {
	rng := rand.New(rand.NewSource(seed))
	base := newCountingBase()
	addrs := make([]types.Address, 2000)
	for i := range addrs {
		rng.Read(addrs[i][:])
		if i%5 != 0 {
			base.present[addrs[i]] = &account.StateAccount{Initialised: true, Nonce: uint64(i % 7), Balance: *uint256.NewInt(uint64(1000 + i))}
		}
	}
	m := NewMVS()
	ex := &Executor{mvs: m}
	bc := NewBaseCache(64)
	readers := make([]*ParallelStateReader, 4)
	for i := range readers {
		readers[i] = NewParallelStateReader(base, m, nil, 0)
		readers[i].SetBaseCache(bc)
		readers[i].ReuseAccountBuffer()
		readers[i].SetSlab(NewValueSlab())
		readers[i].EnableEntryCache()
	}
	var tr fast2Trace
	var sets []*ReadWriteSet
	for op := 0; op < 40000; op++ {
		a := addrs[int(rng.ExpFloat64()*200)%len(addrs)]
		key := LocationKey{Address: a, Field: FieldBalance}
		tx := rng.Intn(5000)
		switch r := rng.Intn(12); {
		case r < 6:
			rd := readers[rng.Intn(len(readers))]
			rw := NewReadWriteSet(tx)
			rd.Rebind(rw, tx)
			var acc *account.StateAccount
			var err error
			switch r {
			case 0:
				acc, err = rd.ReadAccountData(a)
			case 1:
				acc, err = rd.ReadAccountIgnoringBalance(a)
			case 2, 3:
				acc, err = rd.ReadAccountDataForWrite(a)
			default:
				acc, err = rd.ReadAccountIgnoringBalanceForWrite(a)
			}
			if err != nil {
				panic(err)
			}
			if acc != nil {
				cp := *acc
				acc = &cp
			}
			tr.accs = append(tr.accs, acc)
			for _, d := range rw.Reads {
				d.entry, d.at = nil, 0
				tr.reads = append(tr.reads, d)
			}
			// The transaction writes the account it read, as executeSingle
			// stores a transfer's writes (half deltas, half full writes),
			// sometimes after other transactions wrote in between.
			if rng.Intn(3) != 0 {
				if rng.Intn(2) == 0 {
					ex.mvs.WriteDelta(LocationKey{Address: addrs[rng.Intn(len(addrs))], Field: FieldBalance}, rng.Intn(5000), 0, uint256.NewInt(3))
				}
				ent, at := ex.writeEntry(rw, &key)
				if r%2 == 0 {
					m.writeDelta(ent, tx, 1, uint256.NewInt(uint64(1+rng.Intn(9))), at)
				} else {
					m.writeOwnedHint(ent, tx, 1, encAcc(uint64(rng.Intn(9)), uint64(rng.Intn(5000))), 0, at)
				}
			}
			sets = append(sets, rw)
		case r < 9:
			m.WriteDelta(key, tx, uint32(rng.Intn(2)), uint256.NewInt(uint64(1+rng.Intn(50))))
		case r < 11:
			m.Write(key, tx, 0, encAcc(uint64(rng.Intn(9)), uint64(rng.Intn(5000))))
		default:
			m.Delete(key, tx)
		}
	}
	for _, rw := range sets {
		tr.valid = append(tr.valid, Validate(m, rw))
	}
	// The final store, as ApplyAll replays it, in key order: the visiting
	// order follows the entries' creation order, which concurrent workers
	// make arbitrary anyway.
	var final []ReadDescriptor
	_ = m.ApplyAll(5000, func(key LocationKey, value []byte, delta *uint256.Int) error {
		final = append(final, ReadDescriptor{Key: key, Value: value, Base: deltaBytes(delta)})
		return nil
	})
	sort.Slice(final, func(i, j int) bool {
		return bytes.Compare(final[i].Key.Address[:], final[j].Key.Address[:]) < 0
	})
	tr.reads = append(tr.reads, final...)
	return tr
}

func deltaBytes(d *uint256.Int) []byte {
	if d == nil {
		return nil
	}
	b := d.Bytes32()
	return b[:]
}

// TestOneLookupReaderEquivalence: N42_TRANSFER_ONE_LOOKUP changes no read,
// recorded descriptor (but its entry and position), validation verdict or
// final store.
func TestOneLookupReaderEquivalence(t *testing.T) {
	for seed := int64(1); seed <= 4; seed++ {
		restore := SetOneLookupForTest(false)
		old := oneLookupScript(seed)
		restore()
		restore = SetOneLookupForTest(true)
		got := oneLookupScript(seed)
		restore()
		if len(old.reads) == 0 {
			t.Fatal("no reads recorded")
		}
		if !reflect.DeepEqual(old.accs, got.accs) {
			t.Fatalf("seed %d: accounts differ", seed)
		}
		if !reflect.DeepEqual(old.reads, got.reads) {
			t.Fatalf("seed %d: read descriptors or final store differ", seed)
		}
		if !reflect.DeepEqual(old.valid, got.valid) {
			t.Fatalf("seed %d: validation verdicts differ", seed)
		}
		stale := 0
		for _, v := range got.valid {
			if !v {
				stale++
			}
		}
		if stale == 0 || stale == len(got.valid) {
			t.Fatalf("seed %d: %d of %d reads stale: the script does not exercise validation", seed, stale, len(got.valid))
		}
	}
}

// TestBeforeAtMatchesSearch: any guess, right, stale or out of range,
// gives before's and slot's answer.
func TestBeforeAtMatchesSearch(t *testing.T) {
	defer SetOneLookupForTest(true)()
	rng := rand.New(rand.NewSource(9))
	for round := 0; round < 2000; round++ {
		e := &mvEntry{}
		n := rng.Intn(40)
		tx := 0
		for i := 0; i < n; i++ {
			tx += 1 + rng.Intn(4)
			e.versions = append(e.versions, versionedValue{txIndex: tx})
		}
		for q := 0; q < 20; q++ {
			txIndex := rng.Intn(tx + 3)
			want := e.search(txIndex)
			if got := e.before(txIndex); got != want {
				t.Fatalf("before(%d) = %d, want %d", txIndex, got, want)
			}
			for _, at := range []int{-1, 0, want, want - 1, want + 1, rng.Intn(n + 3), n, n + 1} {
				if got := e.beforeAt(txIndex, at); got != want {
					t.Fatalf("n %d beforeAt(%d, %d) = %d, want %d", n, txIndex, at, got, want)
				}
				if got := e.slotAt(txIndex, at); got != e.slot(txIndex) {
					t.Fatalf("n %d slotAt(%d, %d) = %d, want %d", n, txIndex, at, got, e.slot(txIndex))
				}
			}
		}
	}
}

// TestDescriptorSizes pins the read descriptor at 128 bytes (two cache
// lines) and a version at 72.
func TestDescriptorSizes(t *testing.T) {
	if s := unsafe.Sizeof(ReadDescriptor{}); s != 128 {
		t.Fatalf("ReadDescriptor is %d bytes, want 128", s)
	}
	if s := unsafe.Sizeof(versionedValue{}); s != 72 {
		t.Fatalf("versionedValue is %d bytes, want 72", s)
	}
}
