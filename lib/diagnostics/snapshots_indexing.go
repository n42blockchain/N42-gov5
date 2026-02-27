// Copyright 2024 The Erigon Authors
// This file is part of Erigon.
//
// Erigon is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Erigon is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with Erigon. If not, see <http://www.gnu.org/licenses/>.

package diagnostics

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/lib/log/v3"
)

func (d *DiagnosticClient) runSegmentIndexingListener(rootCtx context.Context) {
	go func() {
		ctx, ch, closeChannel := Context[SnapshotIndexingStatistics](rootCtx, 1)
		defer closeChannel()

		StartProviders(ctx, TypeOf(SnapshotIndexingStatistics{}), log.Root())
		for {
			select {
			case <-rootCtx.Done():
				return
			case info := <-ch:
				d.AddOrUpdateSegmentIndexingState(info)
				indexingFinished := d.UpdateIndexingStatus()
				if indexingFinished {
					d.SaveData()
					return
				}
			}
		}
	}()
}

func (d *DiagnosticClient) AddOrUpdateSegmentIndexingState(upd SnapshotIndexingStatistics) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.syncStats.SnapshotIndexing.Segments == nil {
		d.syncStats.SnapshotIndexing.Segments = []SnapshotSegmentIndexingStatistics{}
	}

	existing := d.syncStats.SnapshotIndexing.Segments
	for _, updSeg := range upd.Segments {
		found := false
		for j := range existing {
			if existing[j].SegmentName == updSeg.SegmentName {
				existing[j] = updSeg
				found = true
				break
			}
		}
		if !found {
			existing = append(existing, updSeg)
		}
	}
	d.syncStats.SnapshotIndexing.Segments = existing

	// TimeElapsed == -1 means indexing took less than main loop interval; skip update
	if upd.TimeElapsed != -1 {
		d.syncStats.SnapshotIndexing.TimeElapsed = upd.TimeElapsed
	}
}

func (d *DiagnosticClient) UpdateIndexingStatus() (indexingFinished bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	totalProgressPercent := 0
	for _, seg := range d.syncStats.SnapshotIndexing.Segments {
		totalProgressPercent += seg.Percent
	}

	totalProgress := totalProgressPercent / len(d.syncStats.SnapshotIndexing.Segments)

	d.updateSnapshotStageStats(SyncStageStats{
		TimeElapsed: SecondsToHHMMString(uint64(d.syncStats.SnapshotIndexing.TimeElapsed)),
		TimeLeft:    "unknown",
		Progress:    fmt.Sprintf("%d%%", totalProgress),
	}, "Indexing snapshots")

	if totalProgress >= 100 {
		d.syncStats.SnapshotIndexing.IndexingFinished = true
	}

	return d.syncStats.SnapshotIndexing.IndexingFinished
}
