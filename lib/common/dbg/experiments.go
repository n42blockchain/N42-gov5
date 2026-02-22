/*
   Copyright 2021 Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package dbg

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"sync"
	"time"

	libcommon "github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/mmap"
)

var (
	// DownloaderOnlyBlocks forces skipping of any non-Erigon2 .torrent files.
	DownloaderOnlyBlocks = EnvBool("DOWNLOADER_ONLY_BLOCKS", false)
	saveHeapProfile      = EnvBool("SAVE_HEAP_PROFILE", false)
	heapProfileFilePath  = EnvString("HEAP_PROFILE_FILE_PATH", "")
	// CaplinSyncedDataMangerDeadlockDetection enables deadlock detection in Caplin synced data manager.
	CaplinSyncedDataMangerDeadlockDetection = EnvBool("CAPLIN_DEADLOCK_DETECTION", false)
)

var StagesOnlyBlocks = EnvBool("STAGES_ONLY_BLOCKS", false)

var doMemstat = func() bool {
	_, ok := os.LookupEnv("NO_MEMSTAT")
	return !ok
}()

func DoMemStat() bool { return doMemstat }

func ReadMemStats(m *runtime.MemStats) {
	if doMemstat {
		runtime.ReadMemStats(m)
	}
}

// Lazy-loaded experiment flags using sync.OnceValue to eliminate boilerplate.
// Each function reads its environment variable exactly once on first call.
var (
	WriteMap             = lazyEnvBool("WRITE_MAP")
	NoSync               = lazyEnvBool("NO_SYNC")
	MdbxReadAhead        = lazyEnvBool("MDBX_READAHEAD")
	DiscardHistory       = lazyEnvBool("DISCARD_HISTORY")
	StopAfterReconst     = lazyEnvBool("STOP_AFTER_RECONSTITUTE")
	LogHashMismatchReason = lazyEnvBool("LOG_HASH_MISMATCH_REASON")
	SkipBlobGasValidation = lazyEnvBool("SKIP_BLOB_GAS_VALIDATION")

	StopBeforeStage = lazyEnvString("STOP_BEFORE_STAGE")
	// TODO(allada) Consider removing STOP_BEFORE_STAGE, as STOP_AFTER_STAGE can
	// perform the same functionality. Kept for backward compatibility.
	StopAfterStage = lazyEnvString("STOP_AFTER_STAGE")

	// BigRoTxKb logs info about large read-only transactions.
	BigRoTxKb = lazyEnvUint("DEBUG_BIG_RO_TX_KB")
	// BigRwTxKb logs info about large read-write transactions.
	BigRwTxKb = lazyEnvUint("DEBUG_BIG_RW_TX_KB")

	SlowCommit = lazyEnvDuration("SLOW_COMMIT")
	SlowTx     = lazyEnvDuration("SLOW_TX")

	MergeTr = lazyEnvIntBounded("MERGE_THRESHOLD")

	SnapshotVersion = lazyEnvUint8("SNAPSHOT_VERSION")

	// DebugBlockExecution returns the block number to debug (0 means disabled).
	// Set DEBUG_BLOCK_EXECUTION=<block_number> to enable detailed execution logging.
	DebugBlockExecution = lazyEnvUint64("DEBUG_BLOCK_EXECUTION")
)

// DirtySpace reads MDBX_DIRTY_SPACE_MB and converts MB to bytes.
var DirtySpace = sync.OnceValue(func() uint64 {
	v, _ := os.LookupEnv("MDBX_DIRTY_SPACE_MB")
	if v != "" {
		i, err := strconv.Atoi(v)
		if err != nil {
			panic(err)
		}
		bytes := uint64(i * 1024 * 1024)
		log.Info("[Experiment]", "MDBX_DIRTY_SPACE_MB", bytes)
		return bytes
	}
	return 0
})

type saveHeapOptions struct {
	memStats *runtime.MemStats
	logger   *log.Logger
}

type SaveHeapOption func(options *saveHeapOptions)

func SaveHeapWithMemStats(memStats *runtime.MemStats) SaveHeapOption {
	return func(options *saveHeapOptions) {
		options.memStats = memStats
	}
}

func SaveHeapWithLogger(logger *log.Logger) SaveHeapOption {
	return func(options *saveHeapOptions) {
		options.logger = logger
	}
}

func SaveHeapProfileNearOOM(opts ...SaveHeapOption) {
	if !saveHeapProfile {
		return
	}

	var options saveHeapOptions
	for _, opt := range opts {
		opt(&options)
	}

	var logger log.Logger
	if options.logger != nil {
		logger = *options.logger
	}

	var memStats runtime.MemStats
	if options.memStats != nil {
		memStats = *options.memStats
	} else {
		ReadMemStats(&memStats)
	}

	totalMemory := mmap.TotalMemory()
	if logger != nil {
		logger.Info(
			"[Experiment] heap profile threshold check",
			"alloc", libcommon.ByteCount(memStats.Alloc),
			"total", libcommon.ByteCount(totalMemory),
		)
	}
	if memStats.Alloc < (totalMemory/100)*45 {
		return
	}

	filePath := heapProfileFilePath
	if filePath == "" {
		filePath = filepath.Join(os.TempDir(), "erigon-mem.prof")
	}
	if logger != nil {
		logger.Info("[Experiment] saving heap profile as near OOM", "filePath", filePath)
	}

	f, err := os.Create(filePath)
	if err != nil && logger != nil {
		logger.Warn("[Experiment] could not create heap profile file", "err", err)
	}

	defer func() {
		if closeErr := f.Close(); closeErr != nil && logger != nil {
			logger.Warn("[Experiment] could not close heap profile file", "err", closeErr)
		}
	}()

	runtime.GC()
	if writeErr := pprof.WriteHeapProfile(f); writeErr != nil && logger != nil {
		logger.Warn("[Experiment] could not write heap profile file", "err", writeErr)
	}
}

func SaveHeapProfileNearOOMPeriodically(ctx context.Context, opts ...SaveHeapOption) {
	if !saveHeapProfile {
		return
	}

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			SaveHeapProfileNearOOM(opts...)
		}
	}
}
