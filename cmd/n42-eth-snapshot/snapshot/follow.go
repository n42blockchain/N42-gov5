package snapshot

import (
	"context"
	"fmt"
	"io"
	"time"
)

// FollowConfig configures the autopilot loop.
type FollowConfig struct {
	Datadir      string
	Source       string        // file:// or http(s):// per-network root
	Mode         string        // minimal|full|archive
	PollInterval time.Duration // sleep between cycles; default 30s if zero
	MaxCycles    int           // 0 = unlimited (stop only on ctx cancel)
	MaxIter      int           // per-cycle catch-up max iterations; 0 = unlimited

	// OnCycle is invoked at the end of every cycle, before the
	// next sleep. Optional — useful for tests + observability.
	OnCycle func(cycle int, rep *CatchUpReport, err error)
}

// FollowReport summarises an entire Follow run.
type FollowReport struct {
	Cycles         int
	AppliedDeltas  int
	FinalHeight    uint64
	LastError      string
	CancelledClean bool // true if ctx cancellation ended the loop
}

// Follow is the autopilot: every PollInterval, run a catch-up cycle
// and apply any new deltas. Stops when ctx is cancelled, MaxCycles
// is hit, or an unrecoverable error occurs.
//
// Per cycle:
//   1. Status to learn current vs remote
//   2. If behind, run CatchUp to apply any available deltas
//   3. Sleep PollInterval (interruptible via ctx)
//
// Errors during one cycle are recorded in LastError but do not
// terminate the loop — operators want autopilot resilience across
// transient mirror outages.
func Follow(ctx context.Context, cfg FollowConfig) (*FollowReport, error) {
	if cfg.Source == "" || cfg.Datadir == "" {
		return nil, fmt.Errorf("Follow: --source and --datadir required")
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 30 * time.Second
	}
	rep := &FollowReport{}

	for {
		if ctx.Err() != nil {
			rep.CancelledClean = true
			return rep, nil
		}
		if cfg.MaxCycles > 0 && rep.Cycles >= cfg.MaxCycles {
			return rep, nil
		}
		rep.Cycles++

		cur, cerr := CatchUp(cfg.Datadir, cfg.Source, cfg.Mode, cfg.MaxIter)
		if cur != nil {
			rep.AppliedDeltas += cur.Iterations
			if cur.FinalHeight > rep.FinalHeight {
				rep.FinalHeight = cur.FinalHeight
			}
		}
		if cerr != nil {
			rep.LastError = cerr.Error()
		} else {
			rep.LastError = ""
		}
		if cfg.OnCycle != nil {
			cfg.OnCycle(rep.Cycles, cur, cerr)
		}

		// Interruptible sleep.
		select {
		case <-ctx.Done():
			rep.CancelledClean = true
			return rep, nil
		case <-time.After(cfg.PollInterval):
		}
	}
}

// Print writes a human-readable report.
func (r *FollowReport) Print(w io.Writer) {
	fmt.Fprintf(w, "follow:\n")
	fmt.Fprintf(w, "  cycles run     : %d\n", r.Cycles)
	fmt.Fprintf(w, "  deltas applied : %d\n", r.AppliedDeltas)
	fmt.Fprintf(w, "  final height   : %d\n", r.FinalHeight)
	if r.LastError != "" {
		fmt.Fprintf(w, "  last error     : %s\n", r.LastError)
	}
	if r.CancelledClean {
		fmt.Fprintf(w, "  stopped via    : ctx cancel (clean)\n")
	}
}
