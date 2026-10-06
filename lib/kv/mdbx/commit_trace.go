package mdbx

import (
	"os"
	"strconv"

	mdbxlib "github.com/erigontech/mdbx-go/mdbx"
)

// Native commit timings already come back from Commit. Keep their optional
// logging separate from the per-table write probe, which instruments every Put.
var commitTraceEnabled = func() bool {
	enabled, _ := strconv.ParseBool(os.Getenv("N42_MDBX_COMMIT_TRACE"))
	return enabled
}()

// CommitTraceEnabled reports whether native commit-phase logging is enabled.
func CommitTraceEnabled() bool { return commitTraceEnabled }

func (tx *MdbxTx) logCommitTrace(txID uint64, latency mdbxlib.CommitLatency) {
	writeProbeLogger.RLock()
	logger := writeProbeLogger.log
	writeProbeLogger.RUnlock()
	// These durations are converted from MDBX's 1/65536-second counters;
	// nanoseconds describe the output unit, not the measurement resolution.
	// GC CPU time can be unavailable on a given native build/platform.
	logger("mdbx commit phases",
		"label", tx.db.opts.label,
		"txid", txID,
		"prepareNs", latency.Preparation.Nanoseconds(),
		"gcWallNs", latency.GCWallClock.Nanoseconds(),
		"gcCpuNs", latency.GCCpuTime.Nanoseconds(),
		"auditNs", latency.Audit.Nanoseconds(),
		"writeNs", latency.Write.Nanoseconds(),
		"syncNs", latency.Sync.Nanoseconds(),
		"endingNs", latency.Ending.Nanoseconds(),
		"wholeNs", latency.Whole.Nanoseconds())
}
