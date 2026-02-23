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

package mdbx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/erigontech/mdbx-go/mdbx"
	stack2 "github.com/go-stack/stack"
	"golang.org/x/exp/maps"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/lib/common/dbg"
	"github.com/n42blockchain/N42/lib/common/dir"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/mmap"
)

type TableCfgFunc func(defaultBuckets kv.TableCfg) kv.TableCfg

func WithChaindataTables(defaultBuckets kv.TableCfg) kv.TableCfg {
	return defaultBuckets
}

type MdbxOpts struct {
	// must be in the range from 12.5% (almost empty) to 50% (half empty)
	// which corresponds to the range from 8192 and to 32768 in units respectively
	log             log.Logger
	roTxsLimiter    *semaphore.Weighted
	bucketsCfg      TableCfgFunc
	path            string
	syncPeriod      time.Duration
	mapSize         datasize.ByteSize
	growthStep      datasize.ByteSize
	shrinkThreshold int
	flags           uint
	pageSize        uint64
	dirtySpace      uint64 // if exeed this space, modified pages will `spill` to disk
	mergeThreshold  uint64
	verbosity       kv.DBVerbosityLvl
	label           kv.Label // marker to distinct db instances - one process may open many databases. for example to collect metrics of only 1 database
	inMem           bool
}

const DefaultMapSize = 2 * datasize.TB
const DefaultGrowthStep = 2 * datasize.GB

func NewMDBX(log log.Logger) MdbxOpts {
	opts := MdbxOpts{
		bucketsCfg: WithChaindataTables,
		flags:      mdbx.NoReadahead | mdbx.Durable,
		log:        log,
		pageSize:   kv.DefaultPageSize(),

		mapSize:         DefaultMapSize,
		growthStep:      DefaultGrowthStep,
		mergeThreshold:  3 * 8192,
		shrinkThreshold: -1, // default
		label:           kv.InMem,
	}
	return opts
}

func (opts MdbxOpts) GetLabel() kv.Label  { return opts.label }
func (opts MdbxOpts) GetInMem() bool      { return opts.inMem }
func (opts MdbxOpts) GetPageSize() uint64 { return opts.pageSize }

func (opts MdbxOpts) Label(label kv.Label) MdbxOpts {
	opts.label = label
	return opts
}

func (opts MdbxOpts) DirtySpace(s uint64) MdbxOpts {
	opts.dirtySpace = s
	return opts
}

func (opts MdbxOpts) RoTxsLimiter(l *semaphore.Weighted) MdbxOpts {
	opts.roTxsLimiter = l
	return opts
}

func (opts MdbxOpts) PageSize(v uint64) MdbxOpts {
	opts.pageSize = v
	return opts
}

func (opts MdbxOpts) GrowthStep(v datasize.ByteSize) MdbxOpts {
	opts.growthStep = v
	return opts
}

func (opts MdbxOpts) Path(path string) MdbxOpts {
	opts.path = path
	return opts
}

func (opts MdbxOpts) Set(opt MdbxOpts) MdbxOpts {
	return opt
}

func (opts MdbxOpts) InMem(tmpDir string) MdbxOpts {
	if tmpDir != "" {
		if err := os.MkdirAll(tmpDir, 0755); err != nil {
			panic(err)
		}
	}
	path, err := os.MkdirTemp(tmpDir, "erigon-memdb-")
	if err != nil {
		panic(err)
	}
	opts.path = path
	opts.inMem = true
	opts.flags = mdbx.UtterlyNoSync | mdbx.NoMetaSync | mdbx.NoMemInit
	opts.growthStep = 2 * datasize.MB
	opts.mapSize = 512 * datasize.MB
	opts.dirtySpace = uint64(128 * datasize.MB)
	opts.shrinkThreshold = 0 // disable
	opts.label = kv.InMem
	return opts
}

func (opts MdbxOpts) Exclusive() MdbxOpts {
	opts.flags = opts.flags | mdbx.Exclusive
	return opts
}

func (opts MdbxOpts) Flags(f func(uint) uint) MdbxOpts {
	opts.flags = f(opts.flags)
	return opts
}

func (opts MdbxOpts) HasFlag(flag uint) bool { return opts.flags&flag != 0 }
func (opts MdbxOpts) Readonly() MdbxOpts {
	opts.flags = opts.flags | mdbx.Readonly
	return opts
}
func (opts MdbxOpts) Accede() MdbxOpts {
	opts.flags = opts.flags | mdbx.Accede
	return opts
}

func (opts MdbxOpts) SyncPeriod(period time.Duration) MdbxOpts {
	opts.syncPeriod = period
	return opts
}

func (opts MdbxOpts) DBVerbosity(v kv.DBVerbosityLvl) MdbxOpts {
	opts.verbosity = v
	return opts
}

func (opts MdbxOpts) MapSize(sz datasize.ByteSize) MdbxOpts {
	opts.mapSize = sz
	return opts
}

func (opts MdbxOpts) WriteMap() MdbxOpts {
	opts.flags |= mdbx.WriteMap
	return opts
}
func (opts MdbxOpts) LifoReclaim() MdbxOpts {
	opts.flags |= mdbx.LifoReclaim
	return opts
}

func (opts MdbxOpts) WriteMergeThreshold(v uint64) MdbxOpts {
	opts.mergeThreshold = v
	return opts
}

func (opts MdbxOpts) WithTableCfg(f TableCfgFunc) MdbxOpts {
	opts.bucketsCfg = f
	return opts
}

var pathDbMap = map[string]kv.RoDB{}
var pathDbMapLock sync.Mutex

func addToPathDbMap(path string, db kv.RoDB) {
	pathDbMapLock.Lock()
	defer pathDbMapLock.Unlock()
	pathDbMap[path] = db
}

func removeFromPathDbMap(path string) {
	pathDbMapLock.Lock()
	defer pathDbMapLock.Unlock()
	delete(pathDbMap, path)
}

func PathDbMap() map[string]kv.RoDB {
	pathDbMapLock.Lock()
	defer pathDbMapLock.Unlock()
	return maps.Clone(pathDbMap)
}

var ErrDBDoesNotExists = fmt.Errorf("can't create database - because opening in `Accede` mode. probably another (main) process can create it")

func (opts MdbxOpts) Open(ctx context.Context) (kv.RwDB, error) {
	if dbg.WriteMap() {
		opts = opts.WriteMap() //nolint
	}
	if dbg.DirtySpace() > 0 {
		opts = opts.DirtySpace(dbg.DirtySpace()) //nolint
	}
	if dbg.NoSync() {
		opts = opts.Flags(func(u uint) uint { return u | mdbx.SafeNoSync }) //nolint
	}
	if dbg.MergeTr() > 0 {
		opts = opts.WriteMergeThreshold(uint64(dbg.MergeTr() * 8192)) //nolint
	}
	if dbg.MdbxReadAhead() {
		opts = opts.Flags(func(u uint) uint { return u &^ mdbx.NoReadahead }) //nolint
	}
	if opts.HasFlag(mdbx.Accede) || opts.HasFlag(mdbx.Readonly) {
		for retry := 0; ; retry++ {
			exists := dir.FileExist(filepath.Join(opts.path, "mdbx.dat"))
			if exists {
				break
			}
			if retry >= 5 {
				return nil, fmt.Errorf("%w, label: %s, path: %s", ErrDBDoesNotExists, opts.label.String(), opts.path)
			}
			select {
			case <-time.After(500 * time.Millisecond):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	env, err := mdbx.NewEnv(mdbx.Label(opts.label.String()))
	if err != nil {
		return nil, err
	}
	if opts.label == kv.ChainDB && opts.verbosity != -1 {
		err = env.SetDebug(mdbx.LogLvl(opts.verbosity), mdbx.DbgDoNotChange, mdbx.LoggerDoNotChange) // temporary disable error, because it works if call it 1 time, but returns error if call it twice in same process (what often happening in tests)
		if err != nil {
			return nil, fmt.Errorf("db verbosity set: %w", err)
		}
	}
	if err = env.SetOption(mdbx.OptMaxDB, 200); err != nil {
		return nil, err
	}
	if err = env.SetOption(mdbx.OptMaxReaders, kv.ReadersLimit); err != nil {
		return nil, err
	}

	if !opts.HasFlag(mdbx.Accede) {
		if err = env.SetGeometry(-1, -1, int(opts.mapSize), int(opts.growthStep), opts.shrinkThreshold, int(opts.pageSize)); err != nil {
			return nil, err
		}
		if err = os.MkdirAll(opts.path, 0744); err != nil {
			return nil, fmt.Errorf("could not create dir: %s, %w", opts.path, err)
		}
	}

	// Increase "page measured" options for large transactions.
	// Must be called before env.Open() because after that they require rwtx-lock.
	if !opts.HasFlag(mdbx.Readonly) {
		txnDpInitial, err := env.GetOption(mdbx.OptTxnDpInitial)
		if err != nil {
			return nil, err
		}
		if opts.label == kv.ChainDB {
			if err = env.SetOption(mdbx.OptTxnDpInitial, txnDpInitial*2); err != nil {
				return nil, err
			}
			dpReserveLimit, err := env.GetOption(mdbx.OptDpReverseLimit)
			if err != nil {
				return nil, err
			}
			if err = env.SetOption(mdbx.OptDpReverseLimit, dpReserveLimit*2); err != nil {
				return nil, err
			}
		}

		// before env.Open() we don't know real pageSize. but will be implemented soon: https://gitflic.ru/project/erthink/libmdbx/issue/15
		// but we want call all `SetOption` before env.Open(), because:
		//   - after they will require rwtx-lock, which is not acceptable in ACCEDEE mode.
		pageSize := opts.pageSize
		if pageSize == 0 {
			pageSize = kv.DefaultPageSize()
		}

		var dirtySpace uint64
		if opts.dirtySpace > 0 {
			dirtySpace = opts.dirtySpace
		} else {
			dirtySpace = mmap.TotalMemory() / 42 // it's default of mdbx, but our package also supports cgroups and GOMEMLIMIT
			// clamp to max size
			const dirtySpaceMaxChainDB = uint64(1 * datasize.GB)
			const dirtySpaceMaxDefault = uint64(128 * datasize.MB)

			if opts.label == kv.ChainDB && dirtySpace > dirtySpaceMaxChainDB {
				dirtySpace = dirtySpaceMaxChainDB
			} else if opts.label != kv.ChainDB && dirtySpace > dirtySpaceMaxDefault {
				dirtySpace = dirtySpaceMaxDefault
			}
		}
		//can't use real pagesize here - it will be known only after env.Open()
		if err = env.SetOption(mdbx.OptTxnDpLimit, dirtySpace/pageSize); err != nil {
			return nil, err
		}

		// must be in the range from 12.5% (almost empty) to 50% (half empty)
		// which corresponds to the range from 8192 and to 32768 in units respectively
		if err = env.SetOption(mdbx.OptMergeThreshold16dot16Percent, opts.mergeThreshold); err != nil {
			return nil, err
		}
	}

	err = env.Open(opts.path, opts.flags, 0664)
	if err != nil {
		return nil, fmt.Errorf("%w, label: %s, trace: %s", err, opts.label.String(), stack2.Trace().String())
	}

	// mdbx will not change pageSize if db already exists. means need read real value after env.open()
	in, err := env.Info(nil)
	if err != nil {
		return nil, fmt.Errorf("%w, label: %s, trace: %s", err, opts.label.String(), stack2.Trace().String())
	}

	opts.pageSize = uint64(in.PageSize)
	opts.mapSize = datasize.ByteSize(in.MapSize)
	if opts.label == kv.ChainDB {
		opts.log.Info("[db] open", "label", opts.label, "sizeLimit", opts.mapSize, "pageSize", opts.pageSize)
	} else {
		opts.log.Debug("[db] open", "label", opts.label, "sizeLimit", opts.mapSize, "pageSize", opts.pageSize)
	}

	dirtyPagesLimit, err := env.GetOption(mdbx.OptTxnDpLimit)
	if err != nil {
		return nil, err
	}

	if opts.syncPeriod != 0 {
		if err = env.SetSyncPeriod(opts.syncPeriod); err != nil {
			env.Close()
			return nil, err
		}
	}

	if opts.roTxsLimiter == nil {
		targetSemCount := int64(runtime.GOMAXPROCS(-1) * 16)
		opts.roTxsLimiter = semaphore.NewWeighted(targetSemCount) // 1 less than max to allow unlocking to happen
	}

	txsCountMutex := &sync.Mutex{}

	db := &MdbxKV{
		opts:         opts,
		env:          env,
		log:          opts.log,
		buckets:      kv.TableCfg{},
		txSize:       dirtyPagesLimit * opts.pageSize,
		roTxsLimiter: opts.roTxsLimiter,

		txsCountMutex:         txsCountMutex,
		txsAllDoneOnCloseCond: sync.NewCond(txsCountMutex),

		leakDetector: dbg.NewLeakDetector("db."+opts.label.String(), dbg.SlowTx()),

		MaxBatchSize:  DefaultMaxBatchSize,
		MaxBatchDelay: DefaultMaxBatchDelay,
	}

	customBuckets := opts.bucketsCfg(kv.ChaindataTablesCfg)
	for name, cfg := range customBuckets { // copy map to avoid changing global variable
		db.buckets[name] = cfg
	}

	buckets := bucketSlice(db.buckets)
	if err := db.openDBIs(buckets); err != nil {
		return nil, err
	}

	// Configure buckets and open deprecated buckets
	if err := env.View(func(tx *mdbx.Txn) error {
		for _, name := range buckets {
			// Open deprecated buckets if they exist, don't create
			if !db.buckets[name].IsDeprecated {
				continue
			}
			cnfCopy := db.buckets[name]
			dbi, createErr := tx.OpenDBI(name, mdbx.DBAccede, nil, nil)
			if createErr != nil {
				if mdbx.IsNotFound(createErr) {
					cnfCopy.DBI = NonExistingDBI
					db.buckets[name] = cnfCopy
					continue // if deprecated bucket couldn't be open - then it's deleted and it's fine
				} else {
					return fmt.Errorf("bucket: %s, %w", name, createErr)
				}
			}
			cnfCopy.DBI = kv.DBI(dbi)
			db.buckets[name] = cnfCopy
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if !opts.inMem {
		if staleReaders, err := db.env.ReaderCheck(); err != nil {
			db.log.Error("failed ReaderCheck", "err", err)
		} else if staleReaders > 0 {
			db.log.Info("cleared reader slots from dead processes", "amount", staleReaders)
		}
	}
	db.path = opts.path
	addToPathDbMap(opts.path, db)
	return db, nil
}

func (opts MdbxOpts) MustOpen() kv.RwDB {
	db, err := opts.Open(context.Background())
	if err != nil {
		panic(fmt.Errorf("fail to open mdbx: %w", err))
	}
	return db
}
