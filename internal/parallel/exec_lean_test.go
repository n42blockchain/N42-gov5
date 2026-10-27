package parallel

import (
	"fmt"
	"math/rand"
	"testing"
	"unsafe"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// The hint lives in the descriptor's padding: execLean must not grow the
// read set.
func TestReadDescriptorSize(t *testing.T) {
	if s := unsafe.Sizeof(ReadDescriptor{}); s != 128 {
		t.Fatalf("ReadDescriptor is %d bytes, want 128", s)
	}
}

// beforeHint gives before's answer for every reader and every hint, in
// range or not, on lists with gaps.
func TestBeforeHintMatchesBefore(t *testing.T) {
	defer SetExecLeanForTest(true)()
	rng := rand.New(rand.NewSource(1))
	for _, n := range []int{0, 1, 2, 15, 16, 17, 100, 1000} {
		e := &mvEntry{}
		tx := 0
		for i := 0; i < n; i++ {
			tx += 1 + rng.Intn(3)
			e.versions = append(e.versions, versionedValue{txIndex: tx})
		}
		for txIndex := 0; txIndex <= tx+2; txIndex++ {
			want := e.before(txIndex)
			hints := []int{-1, 0, want - 1, want, want + 1, n, n + 1, rng.Intn(n + 2)}
			for _, h := range hints {
				if got := e.beforeHint(txIndex, h); got != want {
					t.Fatalf("n=%d txIndex=%d hint=%d: beforeHint %d, before %d", n, txIndex, h, got, want)
				}
			}
		}
	}
}

// leanWorkload is a synthetic block over a real ParallelStateReader and
// writer: a few senders with deep nonce chains, three hot recipients and a
// coinbase credited by every transaction (deep delta lists), credits to
// other senders (full writes and deltas on one entry), and every
// 211th transaction a contract call bumping one shared storage slot
// (conflicts, aborts and re-executions).
type leanWorkload struct {
	numTxs   int
	senders  []types.Address
	hot      []types.Address
	coinbase types.Address
	contract types.Address
	base     *countingBase
}

func newLeanWorkload(numTxs, nSenders int) *leanWorkload {
	w := &leanWorkload{numTxs: numTxs, base: newCountingBase()}
	for i := 0; i < nSenders; i++ {
		a := types.Address{0x51, byte(i), byte(i >> 8)}
		w.senders = append(w.senders, a)
		w.base.present[a] = &account.StateAccount{Initialised: true, Balance: *uint256.NewInt(1 << 40)}
	}
	for i := 0; i < 3; i++ {
		a := types.Address{0x52, byte(i)}
		w.hot = append(w.hot, a)
		w.base.present[a] = &account.StateAccount{Initialised: true, Balance: *uint256.NewInt(1)}
	}
	w.coinbase = types.Address{0xC0}
	w.base.present[w.coinbase] = &account.StateAccount{Initialised: true, Balance: *uint256.NewInt(1)}
	w.contract = types.Address{0xCC}
	return w
}

// sender is tx i's sender: the first two senders send every 997th
// transaction and receive every 13th's credit, the rest send the others in
// runs of three.
func (w *leanWorkload) sender(i int) int {
	if i%997 == 0 {
		return (i / 997) % 2
	}
	return 2 + (i/3)%(len(w.senders)-2)
}

// keys is the locations the workload's observations cover: the two
// credited senders, ten more, the hot recipients, the coinbase and the
// contract slot.
func (w *leanWorkload) keys() []LocationKey {
	var ks []LocationKey
	for _, a := range append(append(append([]types.Address{}, w.senders[:min(12, len(w.senders))]...), w.hot...), w.coinbase) {
		ks = append(ks, LocationKey{Address: a, Field: FieldBalance})
	}
	return append(ks, LocationKey{Address: w.contract, Field: FieldStorage})
}

// run executes the block (affinity: the production sender pinning; off,
// a sender's chain spreads over the workers and its versions arrive out of
// order) and returns the executor, unreleased.
func (w *leanWorkload) run(t testing.TB, workers int, affinity bool) *Executor {
	cache := NewBaseCache(64)
	var ex *Executor
	ex = NewExecutor(w.numTxs, workers, func(i int, rw *ReadWriteSet) error {
		rd := NewParallelStateReader(w.base, ex.MVS(), rw, i)
		rd.SetBaseCache(cache)
		wr := NewParallelStateWriter(rw)
		from := w.senders[w.sender(i)]
		var to types.Address
		switch {
		case i%13 == 12:
			to = w.senders[i%2]
		default:
			to = w.hot[i%len(w.hot)]
		}
		sAcc, err := rd.ReadAccountData(from)
		if err != nil || sAcc == nil {
			return fmt.Errorf("tx %d: sender read: %v", i, err)
		}
		sOrig := *sAcc
		sNew := sOrig
		sNew.Nonce++
		sNew.Balance.Sub(&sNew.Balance, uint256.NewInt(10))
		tAcc, err := rd.ReadAccountIgnoringBalance(to)
		if err != nil || tAcc == nil {
			return fmt.Errorf("tx %d: recipient read: %v", i, err)
		}
		tOrig := *tAcc
		tNew := tOrig
		tNew.Balance.Add(&tNew.Balance, uint256.NewInt(7))
		wr.UpdateAccountDataEligible(from, &sOrig, &sNew, false)
		wr.UpdateAccountDataEligible(to, &tOrig, &tNew, true)
		if i%211 == 5 {
			// The contract call: read, bump, write the shared slot.
			var slot types.Hash
			v, err := rd.ReadAccountStorage(w.contract, &slot)
			if err != nil {
				return err
			}
			var n uint256.Int
			n.SetBytes(v)
			n.AddUint64(&n, 1)
			if err := wr.WriteAccountStorage(w.contract, slot, uint256.Int{}, n); err != nil {
				return err
			}
		}
		cAcc, err := rd.ReadAccountIgnoringBalance(w.coinbase)
		if err != nil || cAcc == nil {
			return fmt.Errorf("tx %d: coinbase read: %v", i, err)
		}
		cOrig := *cAcc
		cNew := cOrig
		cNew.Balance.Add(&cNew.Balance, uint256.NewInt(1))
		wr.UpdateAccountDataEligible(w.coinbase, &cOrig, &cNew, true)
		rw.MarkBalanceInsensitive(func(a types.Address) bool { return a == from })
		return nil
	})
	if affinity {
		ex.SetAffinity(func(i int) uint64 { return uint64(w.sender(i)) })
	}
	for i, r := range ex.Run() {
		if r.Err != nil {
			t.Fatalf("tx %d: %v", i, r.Err)
		}
	}
	return ex
}

// applied is the store's final state, location by location.
func applied(t testing.TB, ex *Executor, numTxs int) map[LocationKey]string {
	out := map[LocationKey]string{}
	if err := ex.MVS().ApplyAll(numTxs, func(k LocationKey, v []byte, d *uint256.Int) error {
		ds := "-"
		if d != nil {
			ds = d.Hex()
		}
		out[k] = fmt.Sprintf("%x/%s", v, ds)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// observe is everything a reader and the validator see in the store: every
// transaction's validation verdict, and at every (txIndex, key) the store's
// account or slot read and a fresh reader's recorded descriptor (but its
// hint, which only execLean sets).
func observe(w *leanWorkload, ex *Executor) []string {
	var out []string
	mvs := ex.MVS()
	for i := 0; i < w.numTxs; i++ {
		out = append(out, fmt.Sprintf("valid %d %v", i, Validate(mvs, ex.rwSets[i])))
	}
	cache := NewBaseCache(64)
	for i := 0; i <= w.numTxs; i++ {
		for _, k := range w.keys() {
			if k.Field == FieldStorage {
				v, wtx, winc, ok := mvs.Read(k, i)
				out = append(out, fmt.Sprintf("slot %d %x %d %d %v", i, v, wtx, winc, ok))
				continue
			}
			full, ftx, finc, found, d := mvs.ReadAccount(k, i)
			out = append(out, fmt.Sprintf("acct %d %x %x %d %d %v %v", i, k.Address[:3], full, ftx, finc, found, d))
			rw := NewReadWriteSet(i)
			rd := NewParallelStateReader(w.base, mvs, rw, i)
			rd.SetBaseCache(cache)
			acc, err := rd.ReadAccountData(k.Address)
			r := rw.Reads[len(rw.Reads)-1]
			out = append(out, fmt.Sprintf("rd %d %x %+v %v %d %d %v %v %v %x %x", i, k.Address[:3], acc, err,
				r.WriterTx, r.WriterIncarnation, r.FromBase, r.HasValue, r.HadDelta, r.Value, r.Base))
		}
	}
	return out
}

func diffObs(t *testing.T, what string, off, on []string) {
	t.Helper()
	if len(off) != len(on) {
		t.Fatalf("%s: %d observations off, %d on", what, len(off), len(on))
	}
	for i := range off {
		if off[i] != on[i] {
			t.Fatalf("%s: observation %d differs:\n off %s\n on  %s", what, i, off[i], on[i])
		}
	}
}

// perturb rewrites the store as re-executions of lower transactions would:
// new incarnations with other values, inserts below readers' places, and
// removals -- the hints the reads recorded go stale.
func perturb(w *leanWorkload, ex *Executor, seed int64) {
	rng := rand.New(rand.NewSource(seed))
	mvs := ex.MVS()
	for n := 0; n < 200; n++ {
		i := rng.Intn(w.numTxs)
		k := w.keys()[rng.Intn(len(w.keys()))]
		switch rng.Intn(4) {
		case 0:
			if k.Field == FieldBalance {
				mvs.Write(k, i, 9, encAcc(uint64(rng.Intn(1000)), uint64(rng.Intn(1<<20))))
			}
		case 1:
			if k.Field == FieldBalance {
				mvs.WriteDelta(k, i, 9, uint256.NewInt(uint64(1+rng.Intn(5))))
			}
		case 2:
			mvs.Delete(k, i)
		default:
			// A re-execution that wrote the same value under a new
			// incarnation.
			if v, wtx, winc, ok := mvs.Read(k, i+1); ok && wtx == i && k.Field != FieldBalance {
				mvs.Write(k, i, winc+1, v)
			}
		}
	}
}

// TestExecLeanDifferential: N42_EXEC_LEAN changes no outcome. Full runs on
// and off reach the same state; on one store, every read and every
// validation verdict is the same with the switch on and off, before and
// after the store is perturbed under the recorded hints.
func TestExecLeanDifferential(t *testing.T) {
	const numTxs, workers = 6000, 32
	// With the sender pinning, ten senders: nonce chains of ~750 (deep
	// version lists). Without it a chain spreads over the workers and
	// re-executes a wave per link, so 602 senders keep the waves under the
	// limit; the coinbase and the hot recipients stay deep, their versions
	// arriving out of order.
	for _, tc := range []struct {
		affinity bool
		senders  int
	}{{true, 10}, {false, 602}} {
		affinity := tc.affinity
		t.Run(fmt.Sprintf("affinity=%v", affinity), func(t *testing.T) {
			w := newLeanWorkload(numTxs, tc.senders)

			restore := SetExecLeanForTest(false)
			exOff := w.run(t, workers, affinity)
			restore()
			restore = SetExecLeanForTest(true)
			exOn := w.run(t, workers, affinity)
			restore()
			defer exOff.Release()
			defer exOn.Release()
			e, a := exOn.Stats()
			t.Logf("lean run: waves %d executions %d aborts %d", exOn.Waves(), e, a)
			if a == 0 {
				t.Fatal("the workload must abort and re-execute transactions")
			}
			if exOff.FellBack() || exOn.FellBack() {
				t.Fatal("sequential fallback: the waves must run")
			}
			offState, onState := applied(t, exOff, numTxs), applied(t, exOn, numTxs)
			if len(offState) != len(onState) {
				t.Fatalf("applied %d locations off, %d on", len(offState), len(onState))
			}
			for k, v := range offState {
				if onState[k] != v {
					t.Fatalf("location %x/%d: off %s, on %s", k.Address[:3], k.Field, v, onState[k])
				}
			}
			hinted := 0
			for i := 0; i < numTxs; i++ {
				for _, rd := range exOn.rwSets[i].Reads {
					if rd.hintPos() >= 0 {
						hinted++
					}
				}
			}
			if hinted == 0 {
				t.Fatal("the lean run recorded no hints")
			}

			// One store, both switches: the lean run's (its reads carry hints).
			obs := func() (off, on []string) {
				r := SetExecLeanForTest(false)
				off = observe(w, exOn)
				r()
				r = SetExecLeanForTest(true)
				on = observe(w, exOn)
				r()
				return off, on
			}
			off, on := obs()
			diffObs(t, "executed store", off, on)
			for _, s := range off[:numTxs] {
				if s[len(s)-4:] != "true" {
					t.Fatalf("executed store: %s", s)
				}
			}
			perturb(w, exOn, 7)
			off, on = obs()
			diffObs(t, "perturbed store", off, on)
			invalid := 0
			for _, s := range off[:numTxs] {
				if s[len(s)-5:] == "false" {
					invalid++
				}
			}
			if invalid == 0 {
				t.Fatal("the perturbation invalidated nothing")
			}
			t.Logf("perturbed store: %d of %d transactions invalid, both switches", invalid, numTxs)
		})
	}
}

// BenchmarkMVLookup is an account read at the version list's tail (an
// executing transaction over its sender's earlier writes), a validation
// read inside the list with the place its execution recorded, and a
// delta append, at list depths 1 to a full block's 163k sequential
// appends; lean=0 is the old path.
func BenchmarkMVLookup(b *testing.B) {
	for _, depth := range []int{1, 100, 10_000, 163_000} {
		e := &mvEntry{}
		m := &MVS{}
		val := encAcc(1, 100)
		for i := 0; i < depth; i++ {
			m.writeOwnedHint(e, i, 0, val, 0)
		}
		rng := rand.New(rand.NewSource(1))
		mids := make([]int, 4096)
		for i := range mids {
			mids[i] = rng.Intn(depth)
		}
		for _, lean := range []bool{false, true} {
			name := fmt.Sprintf("depth=%d/lean=%d", depth, map[bool]int{false: 0, true: 1}[lean])
			b.Run(name+"/tail", func(b *testing.B) {
				defer SetExecLeanForTest(lean)()
				var d uint256.Int
				for i := 0; i < b.N; i++ {
					e.readAccountAt(depth, &d, -1)
				}
			})
			b.Run(name+"/validate", func(b *testing.B) {
				defer SetExecLeanForTest(lean)()
				var d uint256.Int
				for i := 0; i < b.N; i++ {
					tx := mids[i&4095]
					e.readAccountAt(tx, &d, int(readHint(tx))-1)
				}
			})
			b.Run(name+"/append-delta", func(b *testing.B) {
				defer SetExecLeanForTest(lean)()
				one := uint256.NewInt(1)
				m := &MVS{va: new(versionArena)}
				for i := 0; i < b.N; i++ {
					if i&4095 == 0 {
						m.va.reset()
					}
					d := &mvEntry{}
					for j := 0; j < 8; j++ {
						m.writeDelta(d, j, 0, one)
					}
				}
			})
		}
	}
}
