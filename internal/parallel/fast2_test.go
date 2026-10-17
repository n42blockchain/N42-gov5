package parallel

import (
	"math/rand"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// fast2Trace is everything a run of fast2Script observed: each read's
// account, error and recorded descriptor (without the entry pointer), and
// each validation verdict.
type fast2Trace struct {
	accs  []*account.StateAccount
	reads []ReadDescriptor
	valid []bool
}

// fast2Script drives one reader per simulated worker through a random mix
// of reads (plain and balance-insensitive), full writes, deltas and
// re-executions over 3,000 accounts, enough to grow the store's and the
// base cache's tables, and records what it saw.
func fast2Script(seed int64) fast2Trace {
	rng := rand.New(rand.NewSource(seed))
	base := newCountingBase()
	addrs := make([]types.Address, 3000)
	for i := range addrs {
		rng.Read(addrs[i][:])
		if i%5 != 0 { // a fifth are absent from the base
			base.present[addrs[i]] = &account.StateAccount{Initialised: true, Nonce: uint64(i % 7), Balance: *uint256.NewInt(uint64(1000 + i))}
		}
	}
	m := NewMVS()
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
		// A Zipf-ish pick: a hot head and a long tail.
		a := addrs[int(rng.ExpFloat64()*300)%len(addrs)]
		key := LocationKey{Address: a, Field: FieldBalance}
		tx := rng.Intn(5000)
		switch r := rng.Intn(10); {
		case r < 4:
			rd := readers[rng.Intn(len(readers))]
			rw := NewReadWriteSet(tx)
			rd.Rebind(rw, tx)
			var acc *account.StateAccount
			var err error
			if r < 2 {
				acc, err = rd.ReadAccountData(a)
			} else {
				acc, err = rd.ReadAccountIgnoringBalance(a)
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
				d.entry = nil
				tr.reads = append(tr.reads, d)
			}
			sets = append(sets, rw)
		case r < 8:
			m.WriteDelta(key, tx, uint32(rng.Intn(2)), uint256.NewInt(uint64(1+rng.Intn(50))))
		case r < 9:
			m.Write(key, tx, 0, encAcc(uint64(rng.Intn(9)), uint64(rng.Intn(5000))))
		default:
			m.Delete(key, tx)
		}
	}
	for _, rw := range sets {
		tr.valid = append(tr.valid, Validate(m, rw))
	}
	return tr
}

// TestFast2ReaderEquivalence: the entry cache, the store-to-base links and
// the presized delta lists change no read, recorded descriptor or
// validation verdict.
func TestFast2ReaderEquivalence(t *testing.T) {
	for seed := int64(1); seed <= 3; seed++ {
		restore := SetFast2ForTest(false)
		old := fast2Script(seed)
		restore()
		restore = SetFast2ForTest(true)
		got := fast2Script(seed)
		restore()
		if len(old.reads) == 0 {
			t.Fatal("no reads recorded")
		}
		if !reflect.DeepEqual(old.accs, got.accs) {
			t.Fatalf("seed %d: accounts differ", seed)
		}
		if !reflect.DeepEqual(old.reads, got.reads) {
			t.Fatalf("seed %d: read descriptors differ", seed)
		}
		if !reflect.DeepEqual(old.valid, got.valid) {
			t.Fatalf("seed %d: validation verdicts differ", seed)
		}
	}
}

// TestMVTableProbeHash: lookups through long probe chains find every key
// and report absent keys absent while the shard tables grow.
func TestMVTableProbeHash(t *testing.T) {
	m := NewMVS()
	rng := rand.New(rand.NewSource(5))
	keys := make([]LocationKey, 20000)
	for i := range keys {
		rng.Read(keys[i].Address[:])
		keys[i].Field = LocationField(i % 3)
		e := m.getOrCreateEntry(keys[i])
		if e.key != keys[i] {
			t.Fatalf("key %d: created entry has another key", i)
		}
	}
	for i := range keys {
		if e := m.lookup(&keys[i]); e == nil || e.key != keys[i] {
			t.Fatalf("key %d not found", i)
		}
		absent := keys[i]
		absent.Slot[0] = 1
		if m.lookup(&absent) != nil {
			t.Fatalf("absent key %d found", i)
		}
	}
	c := NewBaseCache(16)
	for i := range keys {
		c.insert(keys[i].Address, &account.StateAccount{Nonce: uint64(i)})
	}
	for i := range keys {
		e := c.lookup(keys[i].Address)
		if e == nil || e.addr != keys[i].Address || e.acc != &e.accVal {
			t.Fatalf("base key %d", i)
		}
	}
}
