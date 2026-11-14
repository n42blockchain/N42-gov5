package parallel

import (
	"runtime"

	"github.com/n42blockchain/N42/common/envswitch"
)

// waveCPUDiag (N42_WAVE_CPU_DIAG=1, default off) measures each wave worker's
// thread CPU time and placement, for the "parallel block" log line.
//
// The measurement perturbs what it measures, slightly: a clock needs its
// worker pinned to one OS thread, so each worker runs under LockOSThread, and
// a locked goroutine that blocks hands its P to another thread and needs a
// full thread switch to resume. Compare wave timings against a flag-off leg
// before reading them as the undisturbed cost.
var waveCPUDiag = envswitch.Is1("N42_WAVE_CPU_DIAG", envswitch.Diag, "Measure each wave worker's thread CPU time.")

// Topology of the bench box: 256 threads, SMT siblings n and n+128, 16 CCXs of
// 8 cores each. On any other host the core and CCX counts are not meaningful.
const (
	diagThreadsPerSMT = 128
	diagCoresPerCCX   = 8
)

func diagCore(cpu int32) int32 { return cpu % diagThreadsPerSMT }
func diagCCX(cpu int32) int32  { return diagCore(cpu) / diagCoresPerCCX }

// cpuDiagStart pins the worker to its thread and samples the thread's clock
// and placement. Pair with cpuDiagStop on the same goroutine.
func (ws *workerStat) cpuDiagStart() {
	runtime.LockOSThread()
	ws.cpu0 = currentCPU()
	ws.cpuNs = threadCPUNs()
}

func (ws *workerStat) cpuDiagStop() {
	ws.cpuNs = threadCPUNs() - ws.cpuNs
	ws.cpu1 = currentCPU()
	runtime.UnlockOSThread()
}

// summarizeCPU fills s's N42_WAVE_CPU_DIAG fields from the last wave's worker
// stats. s.SlowestWorker must be set. The slowest and fastest workers are
// chosen by wall time, the same workers wkWallMaxMs and wkWallMinMs report.
//
// A move is only seen when a worker ends on another CPU than it started on,
// so one that migrates and returns does not count. Core sharing uses each
// worker's start CPU: workers start within microseconds of each other but end
// apart, so end CPUs could match on a core reused after one finished.
func (e *Executor) summarizeCPU(s *WorkerStatsSummary) {
	s.CPUDiag = true
	var sumCPUNs int64
	fastest := 0
	cores := make(map[int32]int, len(e.workerStats))
	for i := range e.workerStats {
		ws := &e.workerStats[i]
		sumCPUNs += ws.cpuNs
		if ws.wallNs < e.workerStats[fastest].wallNs {
			fastest = i
		}
		if ws.cpu0 < 0 || ws.cpu1 < 0 {
			continue // placement unavailable on this platform
		}
		if ws.cpu0 != ws.cpu1 {
			s.CPUMoved++
		}
		if diagCCX(ws.cpu0) != diagCCX(ws.cpu1) {
			s.CCXMoved++
		}
		cores[diagCore(ws.cpu0)]++
	}
	for _, n := range cores {
		if n > 1 {
			s.CoreShared += n
		}
	}
	s.CPUSlowMs = float64(e.workerStats[s.SlowestWorker].cpuNs) / 1e6
	s.CPUFastMs = float64(e.workerStats[fastest].cpuNs) / 1e6
	s.CPUMeanMs = float64(sumCPUNs) / float64(len(e.workerStats)) / 1e6
}

// CPUFields returns the log key/value pairs of the N42_WAVE_CPU_DIAG fields,
// nil when the flag is off. They pair with wkWallMaxMs, wkWallMeanMs and
// wkWallMinMs on the same line, which already carry the wall side.
func (s WorkerStatsSummary) CPUFields() []any {
	if !s.CPUDiag {
		return nil
	}
	return []any{
		"wkCpuSlowMs", s.CPUSlowMs, "wkCpuMeanMs", s.CPUMeanMs, "wkCpuFastMs", s.CPUFastMs,
		"wkCpuMoved", s.CPUMoved, "wkCcxMoved", s.CCXMoved, "wkCoreShared", s.CoreShared,
	}
}
