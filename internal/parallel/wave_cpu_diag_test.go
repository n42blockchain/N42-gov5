package parallel

import (
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"testing"
)

func withWaveCPUDiag(t *testing.T, on bool) {
	old := waveCPUDiag
	waveCPUDiag = on
	t.Cleanup(func() { waveCPUDiag = old })
}

// runDiagBlock runs a conflicting block with a few failing transactions and
// returns the results and the senders' final counters.
func runDiagBlock(diagOn bool, t *testing.T) (*Executor, []TxResult, []uint64) {
	withWaveCPUDiag(t, diagOn)
	const numTxs, senders, workers = 3000, 40, 8
	sender := func(i int) int { return (i * i) % senders }
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
		if i%701 == 0 {
			return errors.New("tx " + fmt.Sprint(i))
		}
		return nil
	})
	ex.SetAffinity(func(i int) uint64 { return uint64(sender(i)) })
	ex.PrepareAffinity()
	results := ex.Run()
	out := make([]uint64, senders)
	for s := range out {
		v, _, _, _ := ex.MVS().Read(LocationKey{Address: benchAddr(s), Field: FieldBalance}, numTxs)
		if v != nil {
			out[s] = uint64(v[31]) | uint64(v[30])<<8 | uint64(v[29])<<16
		}
	}
	return ex, results, out
}

// TestWaveCPUDiag runs one block with the flag on and off, in channel and in
// partitioned-with-stealing mode: the stats fill in and the results match.
func TestWaveCPUDiag(t *testing.T) {
	for _, mode := range []struct {
		name       string
		lpt, steal bool
	}{{"channel", false, false}, {"steal", true, true}} {
		t.Run(mode.name, func(t *testing.T) {
			withWaveModes(t, mode.lpt, mode.steal)

			exOff, resOff, stateOff := runDiagBlock(false, t)
			if f := exOff.WorkerStats().CPUFields(); f != nil {
				t.Fatalf("flag off logged %v", f)
			}
			for i, ws := range exOff.workerStats {
				if ws.cpuNs != 0 || ws.cpu0 != 0 || ws.cpu1 != 0 {
					t.Fatalf("flag off: worker %d measured %+v", i, ws)
				}
			}

			exOn, resOn, stateOn := runDiagBlock(true, t)
			if !reflect.DeepEqual(resOff, resOn) {
				t.Fatal("results differ with the flag on")
			}
			if !reflect.DeepEqual(stateOff, stateOn) {
				t.Fatalf("state differs with the flag on: %v vs %v", stateOff, stateOn)
			}

			s := exOn.WorkerStats()
			if !s.CPUDiag || len(s.CPUFields()) != 12 {
				t.Fatalf("fields %v", s.CPUFields())
			}
			if runtime.GOOS != "linux" {
				return
			}
			if len(exOn.workerStats) != 8 {
				t.Fatalf("workers %d", len(exOn.workerStats))
			}
			for i, ws := range exOn.workerStats {
				if ws.cpuNs <= 0 || ws.cpu0 < 0 || ws.cpu1 < 0 {
					t.Fatalf("worker %d: cpuNs %d cpu0 %d cpu1 %d", i, ws.cpuNs, ws.cpu0, ws.cpu1)
				}
			}
			if s.CPUMeanMs <= 0 || s.CPUSlowMs <= 0 || s.CPUFastMs <= 0 {
				t.Fatalf("cpu summary %+v", s)
			}
			if s.CCXMoved > s.CPUMoved || s.CPUMoved > 8 || s.CoreShared > 8 {
				t.Fatalf("placement summary %+v", s)
			}
		})
	}
}

// TestSummarizeCPU checks the summary's arithmetic and the box topology
// (SMT siblings n and n+128, 8 cores per CCX).
func TestSummarizeCPU(t *testing.T) {
	withWaveCPUDiag(t, true)
	e := &Executor{workerStats: []workerStat{
		{wallNs: 5e6, cpuNs: 1e6, cpu0: 0, cpu1: 0},         // shares core 0, fastest
		{wallNs: 9e6, cpuNs: 2e6, cpu0: 128, cpu1: 129},     // shares core 0, moved within CCX 0
		{wallNs: 7e6, cpuNs: 3e6, cpu0: 7, cpu1: 8},         // moved CCX 0 to 1
		{wallNs: 6e6, cpuNs: 6e6, cpu0: 255, cpu1: 127},     // sibling move, same core and CCX
		{wallNs: 8e6, cpuNs: 4e6, cpu0: 16, cpu1: 16},       // alone
		{wallNs: 8e6, cpuNs: 2e6, cpu0: 16 + 128, cpu1: 40}, // shares core 16, moved CCX 2 to 5
	}}
	s := e.WorkerStats()
	if s.SlowestWorker != 1 || s.CPUSlowMs != 2 || s.CPUFastMs != 1 {
		t.Fatalf("slow/fast %+v", s)
	}
	if s.CPUMeanMs != 3 {
		t.Fatalf("mean %v", s.CPUMeanMs)
	}
	if s.CPUMoved != 4 || s.CCXMoved != 2 || s.CoreShared != 4 {
		t.Fatalf("moved %d ccx %d shared %d", s.CPUMoved, s.CCXMoved, s.CoreShared)
	}
	// Workers without placement (off Linux) are left out of the counts.
	e.workerStats = []workerStat{{cpu0: -1, cpu1: -1}, {cpu0: -1, cpu1: -1}}
	if s := e.WorkerStats(); s.CPUMoved != 0 || s.CCXMoved != 0 || s.CoreShared != 0 {
		t.Fatalf("no placement %+v", s)
	}
}
