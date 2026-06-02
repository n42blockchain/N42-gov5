// Package modestate wires a node's data-availability Mode (rpccaps) to the
// modules/state.StateReader the RPC/EVM layer reads through. The seam already
// exists — internal/api builds its EVM state via state.New(<a StateReader>) — so
// per-mode state access is just selecting which StateReader to hand it:
//
//	full / archive : state.NewPlainState(tx, blockNr)        — the on-disk PlainState
//	M1 snapshot+hot: snapshotreader.NewStateReader(seg, code) — cold snapshot at H0
//	                 (the hot overlay sits above it in eth-el's engine_state_adapter)
//	M0 witness-dir : not StateReader-backed (recent-changes via the changeset window)
//
// Keeping this in its own package avoids coupling the pure-spec rpccaps package to
// the heavier state/snapshotreader dependencies.
package modestate

import (
	"fmt"

	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
	"github.com/n42blockchain/N42/internal/ethel/snapshotreader"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

// ErrNotStateBacked is returned for modes whose state is not exposed as a
// modules/state.StateReader (M0 answers recent-changes from its changeset window,
// not a full-state reader).
var ErrNotStateBacked = fmt.Errorf("modestate: mode is not StateReader-backed")

// StateReaderFor returns the StateReader for a node mode at blockNr.
//   - Full/Archive need a non-nil tx (PlainState over the chaindata).
//   - M1 needs a non-nil snapshot Segment; code may be nil (then contract code
//     reads return empty). The caller stacks the hot overlay above this for
//     blocks after the snapshot height.
//   - M0 returns ErrNotStateBacked.
func StateReaderFor(mode rpccaps.Mode, tx kv.Tx, seg *snapshotreader.Segment, code state.CodeSource, blockNr uint64) (state.StateReader, error) {
	switch mode {
	case rpccaps.Full, rpccaps.Archive:
		if tx == nil {
			return nil, fmt.Errorf("modestate: %s needs a chaindata tx", mode)
		}
		return state.NewPlainState(tx, blockNr), nil
	case rpccaps.M1:
		if seg == nil {
			return nil, fmt.Errorf("modestate: M1 needs a snapshot segment")
		}
		return snapshotreader.NewStateReader(seg, code), nil
	case rpccaps.M0:
		return nil, ErrNotStateBacked
	default:
		return nil, fmt.Errorf("modestate: unknown mode %v", mode)
	}
}
