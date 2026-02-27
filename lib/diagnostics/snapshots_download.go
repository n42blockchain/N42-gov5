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

	"github.com/n42blockchain/N42/lib/log/v3"
)

func (d *DiagnosticClient) runSnapshotListener(rootCtx context.Context) {
	go func() {
		ctx, ch, closeChannel := Context[SnapshotDownloadStatistics](rootCtx, 1)
		defer closeChannel()

		StartProviders(ctx, TypeOf(SnapshotDownloadStatistics{}), log.Root())
		for {
			select {
			case <-rootCtx.Done():
				return
			case info := <-ch:
				d.SetSnapshotDownloadInfo(info)
				d.UpdateSnapshotStageStats(CalculateSyncStageStats(info), "Downloading snapshots")

				if info.DownloadFinished {
					d.SaveData()
					return
				}
			}
		}
	}()
}

func (d *DiagnosticClient) SetSnapshotDownloadInfo(info SnapshotDownloadStatistics) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// Preserve the existing SegmentsDownloading map; update all other fields.
	info.SegmentsDownloading = d.syncStats.SnapshotDownload.SegmentsDownloading
	d.syncStats.SnapshotDownload = info
}

func (d *DiagnosticClient) runSegmentDownloadingListener(rootCtx context.Context) {
	go func() {
		ctx, ch, closeChannel := Context[SegmentDownloadStatistics](rootCtx, 1)
		defer closeChannel()

		StartProviders(ctx, TypeOf(SegmentDownloadStatistics{}), log.Root())
		for {
			select {
			case <-rootCtx.Done():
				return
			case info := <-ch:
				d.SetDownloadSegments(info)
			}
		}
	}()
}

func (d *DiagnosticClient) SetDownloadSegments(info SegmentDownloadStatistics) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.setDownloadSegments(info)
}

func (d *DiagnosticClient) setDownloadSegments(info SegmentDownloadStatistics) {
	if d.syncStats.SnapshotDownload.SegmentsDownloading == nil {
		d.syncStats.SnapshotDownload.SegmentsDownloading = map[string]SegmentDownloadStatistics{}
	}

	existing := d.syncStats.SnapshotDownload.SegmentsDownloading[info.Name]
	info.DownloadedStats = existing.DownloadedStats
	d.syncStats.SnapshotDownload.SegmentsDownloading[info.Name] = info
}

func (d *DiagnosticClient) runSnapshotFilesListListener(rootCtx context.Context) {
	go func() {
		ctx, ch, closeChannel := Context[SnapshoFilesList](rootCtx, 1)
		defer closeChannel()

		StartProviders(ctx, TypeOf(SnapshoFilesList{}), log.Root())
		for {
			select {
			case <-rootCtx.Done():
				return
			case info := <-ch:
				d.SetSnapshotFilesList(info)

				if len(info.Files) > 0 {
					return
				}
			}
		}
	}()
}

func (d *DiagnosticClient) SetSnapshotFilesList(info SnapshoFilesList) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.snapshotFileList = info
}

func (d *DiagnosticClient) runFileDownloadedListener(rootCtx context.Context) {
	go func() {
		ctx, ch, closeChannel := Context[FileDownloadedStatisticsUpdate](rootCtx, 1)
		defer closeChannel()

		StartProviders(ctx, TypeOf(FileDownloadedStatisticsUpdate{}), log.Root())
		for {
			select {
			case <-rootCtx.Done():
				return
			case info := <-ch:
				d.UpdateFileDownloadedStatistics(&info, nil)
			}
		}
	}()
}

func (d *DiagnosticClient) UpdateFileDownloadedStatistics(downloadedInfo *FileDownloadedStatisticsUpdate, downloadingInfo *SegmentDownloadStatistics) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.syncStats.SnapshotDownload.SegmentsDownloading == nil {
		d.syncStats.SnapshotDownload.SegmentsDownloading = map[string]SegmentDownloadStatistics{}
	}

	if downloadedInfo != nil {
		val := d.syncStats.SnapshotDownload.SegmentsDownloading[downloadedInfo.FileName]
		val.Name = downloadedInfo.FileName
		val.DownloadedStats = FileDownloadedStatistics{
			TimeTook:    downloadedInfo.TimeTook,
			AverageRate: downloadedInfo.AverageRate,
		}
		d.syncStats.SnapshotDownload.SegmentsDownloading[downloadedInfo.FileName] = val
	} else {
		d.setDownloadSegments(*downloadingInfo)
	}
}

func (d *DiagnosticClient) UpdateSnapshotStageStats(stats SyncStageStats, subStageInfo string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.updateSnapshotStageStats(stats, subStageInfo)
}

func (d *DiagnosticClient) updateSnapshotStageStats(stats SyncStageStats, subStageInfo string) {
	idxs := d.getCurrentSyncIdxs()
	if idxs.Stage == -1 || idxs.SubStage == -1 {
		log.Debug("[Diagnostics] Can't find running stage or substage", "subStageInfo", subStageInfo)
		return
	}
	d.syncStages[idxs.Stage].SubStages[idxs.SubStage].Stats = stats
}
