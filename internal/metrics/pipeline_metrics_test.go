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

package metrics

import (
	"testing"
	"time"
)

func TestPipelineTiming_Record(t *testing.T) {
	pt := NewPipelineTiming()

	time.Sleep(5 * time.Millisecond)
	pt.MarkBuildComplete()

	time.Sleep(5 * time.Millisecond)
	pt.MarkImportComplete()

	time.Sleep(5 * time.Millisecond)
	pt.MarkCommitted()

	pt.TxCount = 100
	pt.GasUsed = 2_100_000

	// Record must not panic.
	pt.Record()

	buildMs := pt.BuildComplete.Sub(pt.Created).Milliseconds()
	importMs := pt.ImportComplete.Sub(pt.BuildComplete).Milliseconds()
	commitMs := pt.Committed.Sub(pt.ImportComplete).Milliseconds()
	totalMs := pt.Committed.Sub(pt.Created).Milliseconds()

	if buildMs <= 0 {
		t.Errorf("expected buildMs > 0, got %d", buildMs)
	}
	if importMs <= 0 {
		t.Errorf("expected importMs > 0, got %d", importMs)
	}
	if commitMs <= 0 {
		t.Errorf("expected commitMs > 0, got %d", commitMs)
	}
	if totalMs <= 0 {
		t.Errorf("expected totalMs > 0, got %d", totalMs)
	}
	if totalMs < buildMs+importMs {
		t.Errorf("totalMs (%d) should be >= buildMs+importMs (%d)", totalMs, buildMs+importMs)
	}
}

func TestPipelineTiming_ZeroTxCount(t *testing.T) {
	pt := NewPipelineTiming()

	time.Sleep(2 * time.Millisecond)
	pt.MarkBuildComplete()

	time.Sleep(2 * time.Millisecond)
	pt.MarkImportComplete()

	time.Sleep(2 * time.Millisecond)
	pt.MarkCommitted()

	pt.TxCount = 0
	pt.GasUsed = 0

	// Must not panic with zero tx count (division by zero guard).
	pt.Record()
	pt.LogSummary()
}
