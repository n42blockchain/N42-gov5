package parallel

import (
	"sync/atomic"
	"testing"
	"time"
)

func withWaveModes(t *testing.T, lpt, steal bool) {
	ol, os := waveLPT, waveSteal
	waveLPT, waveSteal = lpt, steal
	t.Cleanup(func() { waveLPT, waveSteal = ol, os })
}

// TestWaveStealExecutesOnce runs a skewed, conflict-free block with stealing
// (and a prepared partition) and checks every transaction ran exactly once.
func TestWaveStealExecutesOnce(t *testing.T) {
	withWaveModes(t, true, true)
	const numTxs, senders, workers = 6000, 150, 8
	sender := func(i int) uint64 { return uint64(i*i%senders) + 1 }
	for _, prepare := range []bool{false, true} {
		var runs [numTxs]atomic.Int32
		ex := NewExecutor(numTxs, workers, func(i int, rw *ReadWriteSet) error {
			runs[i].Add(1)
			if i%97 == 0 {
				time.Sleep(200 * time.Microsecond)
			}
			rw.RecordWrite(LocationKey{Address: benchAddr(i), Field: FieldBalance}, benchVal(uint64(i)))
			return nil
		})
		ex.SetAffinity(sender)
		if prepare {
			ex.PrepareAffinity()
		}
		ex.Run()
		if ex.Waves() != 1 {
			t.Fatalf("prepare=%v: waves %d", prepare, ex.Waves())
		}
		for i := range runs {
			if n := runs[i].Load(); n != 1 {
				t.Fatalf("prepare=%v: tx %d ran %d times", prepare, i, n)
			}
		}
		sum := 0
		for _, ws := range ex.workerStats {
			sum += int(ws.execs)
		}
		if sum != numTxs {
			t.Fatalf("workerStats execs %d want %d", sum, numTxs)
		}
	}
}

// TestWaveStealMatchesNoSteal compares final state of a conflicting workload
// with stealing on and off.
func TestWaveStealMatchesNoSteal(t *testing.T) {
	const numTxs, S, workers = 4000, 40, 8
	sender := func(i int) int { return (i * i) % S }
	run := func(steal bool) []uint64 {
		withWaveModes(t, true, steal)
		var ex *Executor
		ex = NewExecutor(numTxs, workers, func(i int, rw *ReadWriteSet) error {
			k := LocationKey{Address: benchAddr(sender(i)), Field: FieldBalance}
			var cur uint64
			if v, wtx, winc, found := ex.MVS().Read(k, i); found {
				rw.RecordReadValue(k, wtx, winc, false, v)
				cur = uint64(v[31]) | uint64(v[30])<<8 | uint64(v[29])<<16
			} else {
				rw.RecordReadValue(k, -1, 0, true, nil)
			}
			rw.RecordWrite(k, benchVal(cur+1))
			return nil
		})
		ex.SetAffinity(func(i int) uint64 { return uint64(sender(i)) })
		ex.PrepareAffinity()
		ex.Run()
		out := make([]uint64, S)
		for s := 0; s < S; s++ {
			v, _, _, _ := ex.MVS().Read(LocationKey{Address: benchAddr(s), Field: FieldBalance}, numTxs)
			if v == nil {
				continue
			}
			out[s] = uint64(v[31]) | uint64(v[30])<<8 | uint64(v[29])<<16
		}
		return out
	}
	a, b := run(false), run(true)
	for s := range a {
		if a[s] != b[s] {
			t.Fatalf("sender %d: %d vs %d", s, a[s], b[s])
		}
	}
}

// BenchmarkWaveSteal runs a block whose transactions of a few senders are
// slow, so a fixed LPT partition by count leaves some workers late.
func BenchmarkWaveSteal(b *testing.B) {
	const numTxs, senders, workers = 20000, 2000, 16
	sender := func(i int) uint64 { return uint64(i%senders) + 1 }
	for _, steal := range []bool{false, true} {
		name := "steal=off"
		if steal {
			name = "steal=on"
		}
		b.Run(name, func(b *testing.B) {
			ol, os := waveLPT, waveSteal
			waveLPT, waveSteal = true, steal
			defer func() { waveLPT, waveSteal = ol, os }()
			for n := 0; n < b.N; n++ {
				ex := NewExecutor(numTxs, workers, func(i int, rw *ReadWriteSet) error {
					// Slow senders cost far more per transaction at equal
					// counts (sleep, so the skew shows on a small box).
					if (sender(i)-1)%workers == 0 && sender(i)%5 == 1 {
						time.Sleep(100 * time.Microsecond)
					}
					rw.RecordWrite(LocationKey{Address: benchAddr(i), Field: FieldBalance}, benchVal(uint64(i)))
					return nil
				})
				ex.SetAffinity(sender)
				ex.PrepareAffinity()
				ex.Run()
			}
		})
	}
}
