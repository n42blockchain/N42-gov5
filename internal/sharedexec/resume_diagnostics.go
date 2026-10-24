package sharedexec

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Resume diagnostics are opt-in, process-local aggregates. Every elapsed value
// uses the same monotonic origin; UnixNano is only a cross-process anchor.
type resumeDiagnostics struct {
	mu                                                 sync.Mutex
	file                                               *os.File
	start                                              time.Time
	startMonoNS                                        int64
	stages                                             [12]resumeStage
	reserve                                            [5]uint64
	results                                            [5]uint64
	activeFrames, peakFrames, activeVerify, peakVerify int
	frameAreaNS, verifyAreaNS                          uint64
	activeChanged                                      time.Time
	outputError                                        string
}

type resumeStage struct {
	Count   uint64     `json:"count"`
	SumNS   uint64     `json:"sum_ns"`
	MaxNS   uint64     `json:"max_ns"`
	Buckets [24]uint64 `json:"buckets_us_pow2"`
}

// One goroutine owns a granted frame's trace until its final reply. Merge the
// bounded stage array once, outside queue and chain locks.
type resumeFrameTrace struct {
	started time.Time
	stages  [12]time.Duration
	seen    [12]bool
	outcome int
}

func (t *resumeFrameTrace) add(stage int, elapsed time.Duration) {
	if t == nil {
		return
	}
	t.stages[stage] += elapsed
	t.seen[stage] = true
}

func (d *resumeDiagnostics) merge(t *resumeFrameTrace) {
	if d == nil || t == nil {
		return
	}
	d.mu.Lock()
	for i, seen := range t.seen {
		if seen {
			d.addLocked(i, t.stages[i])
		}
	}
	d.results[t.outcome]++
	d.mu.Unlock()
}

var resumeStageNames = [12]string{"reserve_lock_wait_and_hold", "grant_write_attempt", "payload_read_attempt", "frame_validate", "verification_walk", "account_lock", "account_join", "account_read", "publish_lock_wait", "publish_lock_hold", "ack_write_attempt", "first_receipt_grant_to_ack"}
var resumeReserveNames = [5]string{"granted", "tx_credit", "byte_credit", "snapshot_retry", "rejected"}
var resumeResultNames = [5]string{"admitted", "duplicate", "snapshot_retry", "rejected", "ack_write_failed_after_publish"}

func newResumeDiagnostics(path string) (*resumeDiagnostics, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("resume diagnostics path must be absolute")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &ts); err != nil {
		f.Close()
		return nil, err
	}
	now := time.Now()
	return &resumeDiagnostics{file: f, start: now, startMonoNS: ts.Nano(), activeChanged: now}, nil
}

func (d *resumeDiagnostics) add(stage int, elapsed time.Duration) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.addLocked(stage, elapsed)
	d.mu.Unlock()
}

func (d *resumeDiagnostics) addLocked(stage int, elapsed time.Duration) {
	ns := uint64(max(elapsed.Nanoseconds(), 0))
	s := &d.stages[stage]
	s.Count++
	s.SumNS += ns
	if ns > s.MaxNS {
		s.MaxNS = ns
	}
	us := ns / 1000
	i := 0
	for us > 1 && i < len(s.Buckets)-1 {
		us >>= 1
		i++
	}
	s.Buckets[i]++
}

func (d *resumeDiagnostics) reservation(outcome int) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.reserve[outcome]++
	d.mu.Unlock()
}

func (d *resumeDiagnostics) frame(start bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.advanceActive(time.Now())
	if start {
		d.activeFrames++
		d.peakFrames = max(d.peakFrames, d.activeFrames)
	} else {
		d.activeFrames--
	}
	d.mu.Unlock()
}

func (d *resumeDiagnostics) verify(start bool) {
	if d == nil {
		return
	}
	d.mu.Lock()
	d.advanceActive(time.Now())
	if start {
		d.activeVerify++
		d.peakVerify = max(d.peakVerify, d.activeVerify)
	} else {
		d.activeVerify--
	}
	d.mu.Unlock()
}

func (d *resumeDiagnostics) advanceActive(now time.Time) {
	delta := uint64(max(now.Sub(d.activeChanged).Nanoseconds(), 0))
	d.frameAreaNS += delta * uint64(d.activeFrames)
	d.verifyAreaNS += delta * uint64(d.activeVerify)
	d.activeChanged = now
}

func (d *resumeDiagnostics) writeSnapshot(q *strictIngressQueue, terminal bool) error {
	if d == nil {
		return nil
	}
	var status StrictIngressStatus
	var before, after unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &before); err != nil {
		return err
	}
	unixSampleNS := time.Now().UnixNano()
	q.mu.Lock()
	status = q.status(Identity{})
	q.mu.Unlock()
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &after); err != nil {
		return err
	}
	d.mu.Lock()
	d.advanceActive(time.Now())
	stages := make(map[string]resumeStage, len(d.stages))
	for i, name := range resumeStageNames {
		stages[name] = d.stages[i]
	}
	reserve := make(map[string]uint64, len(d.reserve))
	for i, name := range resumeReserveNames {
		reserve[name] = d.reserve[i]
	}
	results := make(map[string]uint64, len(d.results))
	for i, name := range resumeResultNames {
		results[name] = d.results[i]
	}
	record := struct {
		SchemaVersion       int                    `json:"schema_version"`
		UnixAnchorNS        int64                  `json:"unix_anchor_ns"`
		MonotonicAnchorNS   int64                  `json:"monotonic_anchor_ns"`
		MonotonicNS         int64                  `json:"monotonic_ns"`
		MonotonicEndNS      int64                  `json:"monotonic_end_ns"`
		CaptureEndNS        int64                  `json:"capture_end_ns"`
		UnixSampleNS        int64                  `json:"unix_sample_ns"`
		ElapsedNS           int64                  `json:"elapsed_ns"`
		PID                 int                    `json:"pid"`
		Epoch               [16]byte               `json:"epoch"`
		CanonicalHash       [32]byte               `json:"canonical_hash"`
		CanonicalNumber     uint64                 `json:"canonical_number"`
		CanonicalGeneration uint64                 `json:"canonical_generation"`
		Reconciling         bool                   `json:"reconciling"`
		Pending             int                    `json:"pending"`
		RetainedBytes       int                    `json:"retained_bytes"`
		Reserved            int                    `json:"reserved"`
		ReservedBytes       int                    `json:"reserved_bytes"`
		Verified            uint64                 `json:"verified"`
		Admitted            uint64                 `json:"admitted"`
		ActiveFrames        int                    `json:"active_frames"`
		PeakFrames          int                    `json:"peak_frames"`
		FrameAreaNS         uint64                 `json:"frame_area_ns"`
		ActiveVerify        int                    `json:"active_verify"`
		PeakVerify          int                    `json:"peak_verify"`
		VerifyAreaNS        uint64                 `json:"verify_area_ns"`
		CPUUserNS           int64                  `json:"cpu_user_ns"`
		CPUSystemNS         int64                  `json:"cpu_system_ns"`
		TerminalComplete    bool                   `json:"terminal_complete"`
		Reserve             map[string]uint64      `json:"reserve"`
		Results             map[string]uint64      `json:"results"`
		Stages              map[string]resumeStage `json:"stages"`
	}{SchemaVersion: 1, UnixAnchorNS: d.start.UnixNano(), MonotonicAnchorNS: d.startMonoNS, MonotonicNS: before.Nano(), MonotonicEndNS: after.Nano(), UnixSampleNS: unixSampleNS, ElapsedNS: time.Since(d.start).Nanoseconds(), PID: os.Getpid(), Epoch: status.Epoch, CanonicalHash: status.CanonicalHash, CanonicalNumber: status.CanonicalNumber, CanonicalGeneration: status.CanonicalGeneration, Reconciling: status.Reconciling, Pending: status.PendingTransactions, RetainedBytes: status.RetainedEncodedBytes, Reserved: status.ReservedTransactions, ReservedBytes: status.ReservedEncodedBytes, Verified: status.CryptoVerified, Admitted: status.NewlyAdmitted, ActiveFrames: d.activeFrames, PeakFrames: d.peakFrames, FrameAreaNS: d.frameAreaNS, ActiveVerify: d.activeVerify, PeakVerify: d.peakVerify, VerifyAreaNS: d.verifyAreaNS, TerminalComplete: terminal, Reserve: reserve, Results: results, Stages: stages}
	d.mu.Unlock()
	var usage syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &usage); err != nil {
		return err
	}
	record.CPUUserNS = usage.Utime.Nano()
	record.CPUSystemNS = usage.Stime.Nano()
	var captureEnd unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC, &captureEnd); err != nil {
		return err
	}
	record.CaptureEndNS = captureEnd.Nano()
	return json.NewEncoder(d.file).Encode(record)
}

func (d *resumeDiagnostics) run(s *Server, q *strictIngressQueue) {
	defer s.resumeObserverWG.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := d.writeSnapshot(q, false); err != nil {
				d.outputError = err.Error()
				return
			}
		case <-s.ctx.Done():
			return
		}
	}
}

func (d *resumeDiagnostics) finish(q *strictIngressQueue) {
	if d.outputError == "" {
		if err := d.writeSnapshot(q, true); err != nil {
			d.outputError = err.Error()
		}
	}
	if err := d.file.Sync(); err != nil && d.outputError == "" {
		d.outputError = err.Error()
	}
	if err := d.file.Close(); err != nil && d.outputError == "" {
		d.outputError = err.Error()
	}
	status := struct {
		Complete bool   `json:"complete"`
		Error    string `json:"error,omitempty"`
	}{Complete: d.outputError == "", Error: d.outputError}
	data, err := json.Marshal(status)
	if err == nil {
		err = os.WriteFile(d.file.Name()+".status.json", append(data, '\n'), 0600)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "resume diagnostics status:", err)
	}
}
