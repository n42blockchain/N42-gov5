// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package internal

import (
	"os"
	"strings"
	"time"

	"github.com/n42blockchain/N42/lib/kv"
)

// historyFoldYieldOn is N42_HISTORY_FOLD_YIELD (default off, r90a).
//
// After CANON_FOLD and HS_STATE_JOURNAL the history fold's write
// transactions are what the ordered block write still waits behind: in
// E1N26 run 70 B3, 83% of node0's block-write begin wait (36 waits of 47 ms,
// p99 55 ms) was a HistoryBackfiller.stepFold transaction, held 57.8 ms on
// average -- up to N42_HISTORY_FOLD_TXN_MS (30) of puts, then a commit whose
// data sync on the shared device took the rest. N42_LAG_WRITE_DEFER_BG
// already starts each fold transaction in the gap after a block write
// commits, but under load the next block write begins a few milliseconds
// later and finds the writer taken.
//
// With the switch on each fold write transaction
//
//   - begins only while no ordered block write is open or waiting
//     (lagBGGate, as N42_LAG_WRITE_DEFER_BG; the gate is kept under this
//     switch alone too);
//   - stops putting at the next key boundary once a block write is waiting
//     for the writer (at least one key's rows go in, so the fold always
//     progresses), or after N42_HISTORY_FOLD_YIELD_TXN_MS (default 5) of
//     puts;
//   - commits without a data sync (MDBX_TXN_NOSYNC, the store's
//     UpdateNosync): the fold's rows and marker become durable with the next
//     synced commit, the block write's, and until then a crash rolls the
//     store back to the last synced commit with the fold transaction whole
//     or absent. A lost fold transaction is redone on restart from the
//     marker, which is written with the last rows only, and the rows are
//     puts of unions, so the redone fold writes the same rows
//     (PreparedHistory.ApplyFrom). A store without UpdateNosync falls back
//     to a synced commit.
//
// What is written is unchanged: the same rows (only cut into more, shorter
// transactions) and the same marker, after the same last rows.
var historyFoldYieldOn = parseOptIn(os.Getenv("N42_HISTORY_FOLD_YIELD"))

// historyBackfillYieldWriterOn is N42_HISTORY_BACKFILL_YIELD_WRITER (default
// off, r95a): N42_HISTORY_FOLD_YIELD without its no-sync commit.
//
// Run 81 (node0): the history fold held the MDBX writer 36-51 ms on 2-4% of
// block writes (HistoryBackfiller.stepFold on 66 of the 76 begin waits of
// 1 ms or more); N42_HISTORY_FOLD_YIELD, which cuts that, was off because
// its fold transactions commit without a data sync, which moves what is
// durable when. With this switch each fold write transaction begins only
// while no ordered block write is open or waiting (lagBGGate) and stops
// putting at the next key boundary once a block write waits for the writer
// or after N42_HISTORY_FOLD_YIELD_TXN_MS of puts, exactly as under
// N42_HISTORY_FOLD_YIELD, but commits with its data sync as today. What is
// written and when it is durable are unchanged: the same rows, cut into
// more, shorter transactions, each synced; the marker with the last rows.
// With both switches on, N42_HISTORY_FOLD_YIELD's no-sync commit applies.
var historyBackfillYieldWriterOn = parseOptIn(os.Getenv("N42_HISTORY_BACKFILL_YIELD_WRITER"))

// SetHistoryBackfillYieldWriterForTest switches
// N42_HISTORY_BACKFILL_YIELD_WRITER and returns the restore.
func SetHistoryBackfillYieldWriterForTest(on bool) func() {
	prev := historyBackfillYieldWriterOn
	historyBackfillYieldWriterOn = on
	return func() { historyBackfillYieldWriterOn = prev }
}

// historyFoldYields reports whether fold transactions yield to block writes
// (either switch), and whether they commit without a data sync.
func historyFoldYields() (yield, nosync bool) {
	return historyFoldYieldOn || historyBackfillYieldWriterOn, historyFoldYieldOn
}

// historyFoldYieldTxn bounds the puts of one yielding fold transaction.
var historyFoldYieldTxn = envMillis("N42_HISTORY_FOLD_YIELD_TXN_MS", 5*time.Millisecond)

// SetHistoryFoldYieldForTest switches N42_HISTORY_FOLD_YIELD and returns
// the restore.
func SetHistoryFoldYieldForTest(on bool) func() {
	prev := historyFoldYieldOn
	historyFoldYieldOn = on
	return func() { historyFoldYieldOn = prev }
}

// historyFoldWriterWanted reports whether an ordered block write is waiting
// for (or holds) the MDBX writer.
// A variable for the tests.
var historyFoldWriterWanted = func() bool { return lagBGGate.open.Load() > 0 }

// historyFoldYieldStats is one fold's write-side record (logged per fold).
type historyFoldYieldStats struct {
	rows, txns, yields int
	heldMax, heldSum   time.Duration
	gateWait           time.Duration
	nosync             bool
}

// foldUpdateYield runs one yielding fold write transaction: without a data
// sync where the store supports it and nosync is set, else synced.
func (b *HistoryBackfiller) foldUpdateYield(body func(tx kv.RwTx) error, ys *historyFoldYieldStats, nosync bool) error {
	ctx := kv.WithRwTag(b.ctx, "historyFoldYield")
	t0 := time.Now()
	var err error
	if !nosync || b.nosyncUnsupported {
		err = b.db.Update(ctx, body)
	} else {
		ran := false
		err = b.db.UpdateNosync(ctx, func(tx kv.RwTx) error {
			ran = true
			return body(tx)
		})
		if err != nil && !ran && isNosyncUnsupported(err) {
			b.nosyncUnsupported = true
			t0 = time.Now()
			err = b.db.Update(ctx, body)
		} else {
			ys.nosync = true
		}
	}
	held := time.Since(t0)
	ys.txns++
	ys.heldSum += held
	if held > ys.heldMax {
		ys.heldMax = held
	}
	return err
}

func isNosyncUnsupported(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UpdateNosync not implemented")
}
