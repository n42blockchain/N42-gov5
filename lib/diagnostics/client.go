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
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/c2h5oh/datasize"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
)

type DiagnosticClient struct {
	ctx         context.Context
	db          kv.RwDB
	metricsMux  *http.ServeMux
	dataDirPath string
	speedTest   bool

	syncStages          []SyncStage
	syncStats           SyncStatistics
	BlockExecution      BlockEexcStatsData
	snapshotFileList    SnapshoFilesList
	mu                  sync.Mutex
	headerMutex         sync.Mutex
	hardwareInfo        HardwareInfo
	peersStats          *PeerStats
	headers             Headers
	bodies              BodiesInfo
	bodiesMutex         sync.Mutex
	resourcesUsage      ResourcesUsage
	resourcesUsageMutex sync.Mutex
	networkSpeed        NetworkSpeedTestResult
	networkSpeedMutex   sync.Mutex
	webseedsList        []string
}

func NewDiagnosticClient(ctx context.Context, metricsMux *http.ServeMux, dataDirPath string, speedTest bool, webseedsList []string) (*DiagnosticClient, error) {
	dirPath := filepath.Join(dataDirPath, "diagnostics")
	db, err := createDb(ctx, dirPath)
	if err != nil {
		return nil, err
	}

	hInfo, ss, snpdwl, snpidx, snpfd := ReadSavedData(db)

	return &DiagnosticClient{
		ctx:         ctx,
		db:          db,
		metricsMux:  metricsMux,
		dataDirPath: dataDirPath,
		speedTest:   speedTest,
		syncStages:  ss,
		syncStats: SyncStatistics{
			SnapshotDownload: snpdwl,
			SnapshotIndexing: snpidx,
			SnapshotFillDB:   snpfd,
		},
		hardwareInfo:     hInfo,
		snapshotFileList: SnapshoFilesList{},
		bodies:           BodiesInfo{},
		resourcesUsage: ResourcesUsage{
			MemoryUsage: []MemoryStats{},
		},
		peersStats:   NewPeerStats(1000), // 1000 is the limit of peers; TODO: make it configurable through a flag
		webseedsList: webseedsList,
	}, nil
}

func createDb(ctx context.Context, dbDir string) (kv.RwDB, error) {
	return mdbx.NewMDBX(log.New()).
		Label(kv.DiagnosticsDB).
		WithTableCfg(func(defaultBuckets kv.TableCfg) kv.TableCfg { return kv.DiagnosticsTablesCfg }).
		GrowthStep(4 * datasize.MB).
		MapSize(16 * datasize.GB).
		PageSize(uint64(4 * datasize.KB)).
		RoTxsLimiter(semaphore.NewWeighted(9_000)).
		Path(dbDir).
		Open(ctx)
}

func (d *DiagnosticClient) Setup() {
	rootCtx, _ := common.RootContext()

	d.setupSnapshotDiagnostics(rootCtx)
	d.setupStagesDiagnostics(rootCtx)
	d.setupSysInfoDiagnostics()
	d.runCollectPeersStatistics(rootCtx)
	d.runBlockExecutionListener(rootCtx)
	d.setupHeadersDiagnostics(rootCtx)
	d.setupBodiesDiagnostics(rootCtx)
	d.runMemoryStatsListener(rootCtx)
	d.setupSpeedtestDiagnostics(rootCtx)
	d.runSaveProcess(rootCtx)
}

// Save diagnostic data by time interval to reduce save events
func (d *DiagnosticClient) runSaveProcess(rootCtx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				d.SaveData()
			case <-rootCtx.Done():
				d.SaveData()
				return
			}
		}
	}()
}

func (d *DiagnosticClient) SaveData() {
	d.mu.Lock()
	defer d.mu.Unlock()

	updaters := []func(tx kv.RwTx) error{
		SnapshotDownloadUpdater(d.syncStats.SnapshotDownload),
		StagesListUpdater(d.syncStages),
		SnapshotIndexingUpdater(d.syncStats.SnapshotIndexing),
	}

	err := d.db.Update(d.ctx, func(tx kv.RwTx) error {
		for _, updater := range updaters {
			if err := updater(tx); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		log.Warn("Failed to save diagnostics data", "err", err)
	}
}

func ReadSavedData(db kv.RoDB) (hinfo HardwareInfo, ssinfo []SyncStage, snpdwl SnapshotDownloadStatistics, snpidx SnapshotIndexingStatistics, snpfd SnapshotFillDBStatistics) {
	type readFunc func(kv.Tx) ([]byte, error)

	readers := []readFunc{
		ReadRAMInfoFromTx,
		ReadCPUInfoFromTx,
		ReadDiskInfoFromTx,
		SyncStagesFromTX,
		SnapshotDownloadInfoFromTx,
		SnapshotIndexingInfoFromTx,
		SnapshotFillDBInfoFromTx,
	}

	results := make([][]byte, len(readers))

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		for i, read := range readers {
			data, err := read(tx)
			if err != nil {
				return err
			}
			results[i] = data
		}
		return nil
	}); err != nil {
		return HardwareInfo{}, nil, SnapshotDownloadStatistics{}, SnapshotIndexingStatistics{}, SnapshotFillDBStatistics{}
	}

	var ramInfo RAMInfo
	var cpuInfo []CPUInfo
	var diskInfo DiskInfo
	ParseData(results[0], &ramInfo)
	ParseData(results[1], &cpuInfo)
	ParseData(results[2], &diskInfo)

	hinfo = HardwareInfo{
		RAM:  ramInfo,
		CPU:  cpuInfo,
		Disk: diskInfo,
	}

	ParseData(results[3], &ssinfo)
	ParseData(results[4], &snpdwl)
	ParseData(results[5], &snpidx)
	ParseData(results[6], &snpfd)

	return hinfo, ssinfo, snpdwl, snpidx, snpfd
}
