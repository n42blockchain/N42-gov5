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
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/erigontech/mdbx-go/mdbx"
	stack2 "github.com/go-stack/stack"
	"golang.org/x/sync/semaphore"

	"github.com/n42blockchain/N42/lib/common/dbg"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const NonExistingDBI kv.DBI = 999_999_999

type MdbxKV struct {
	log          log.Logger
	env          *mdbx.Env
	buckets      kv.TableCfg
	roTxsLimiter *semaphore.Weighted // does limit amount of concurrent Ro transactions - in most casess runtime.NumCPU() is good value for this channel capacity - this channel can be shared with other components (like Decompressor)
	opts         MdbxOpts
	txSize       uint64
	closed       atomic.Bool
	path         string

	txsCount              uint
	txsCountMutex         *sync.Mutex
	txsAllDoneOnCloseCond *sync.Cond

	leakDetector *dbg.LeakDetector

	// MaxBatchSize is the maximum size of a batch. Default value is
	// copied from DefaultMaxBatchSize in Open.
	//
	// If <=0, disables batching.
	//
	// Do not change concurrently with calls to Batch.
	MaxBatchSize int

	// MaxBatchDelay is the maximum delay before a batch starts.
	// Default value is copied from DefaultMaxBatchDelay in Open.
	//
	// If <=0, effectively disables batching.
	//
	// Do not change concurrently with calls to Batch.
	MaxBatchDelay time.Duration

	batchMu sync.Mutex
	batch   *batch
}

type MdbxTx struct {
	tx               *mdbx.Txn
	id               uint64 // set only if TRACE_TX=true
	db               *MdbxKV
	statelessCursors map[string]kv.RwCursor
	readOnly         bool
	ctx              context.Context

	cursors  map[uint64]*mdbx.Cursor
	cursorID uint64

	streams  map[int]kv.Closer
	streamID int

	// Per-transaction I/O counters for metrics.
	readCount  atomic.Uint64
	writeCount atomic.Uint64
	readBytes  atomic.Uint64
	writeBytes atomic.Uint64

	// tableWrites is per-table write attribution, populated only while
	// N42_WRITE_PROBE is on. See write_probe.go.
	tableWrites *tableWrites
}

func (db *MdbxKV) Path() string     { return db.opts.path }
func (db *MdbxKV) PageSize() uint64 { return db.opts.pageSize }
func (db *MdbxKV) ReadOnly() bool   { return db.opts.HasFlag(mdbx.Readonly) }
func (db *MdbxKV) Accede() bool     { return db.opts.HasFlag(mdbx.Accede) }

func (db *MdbxKV) CHandle() unsafe.Pointer {
	return db.env.CHandle()
}

// discoverDBIs scans the root DBI to find all named databases and
// registers them in db.buckets so they can be opened by openDBIs.
// This enables reading foreign MDBX files (e.g., Reth) without
// pre-registering their table names.
func (db *MdbxKV) discoverDBIs() error {
	return db.env.View(func(tx *mdbx.Txn) error {
		root, err := tx.OpenRoot(0)
		if err != nil {
			return err
		}
		cursor, err := tx.OpenCursor(root)
		if err != nil {
			return err
		}
		defer cursor.Close()

		var lastErr error
		count := 0
		for k, _, err := cursor.Get(nil, nil, mdbx.First); k != nil; k, _, err = cursor.Get(nil, nil, mdbx.Next) {
			if err != nil {
				lastErr = err
				break
			}
			name := string(k)
			if _, exists := db.buckets[name]; !exists {
				db.buckets[name] = kv.TableCfgItem{}
				count++
			}
			if count > 500 { // sanity limit
				break
			}
		}
		return lastErr
	})
}

// openDBIs - first trying to open existing DBI's in RO transaction
// otherwise re-try by RW transaction
// it allow open DB from another process - even if main process holding long RW transaction
func (db *MdbxKV) openDBIs(buckets []string) error {
	createAll := func(migrator kv.BucketMigrator) error {
		for _, name := range buckets {
			if db.buckets[name].IsDeprecated {
				continue
			}
			if err := migrator.CreateBucket(name); err != nil {
				return err
			}
		}
		return nil
	}

	if db.ReadOnly() || db.Accede() {
		return db.View(context.Background(), func(tx kv.Tx) error {
			if err := createAll(tx.(kv.BucketMigrator)); err != nil {
				return err
			}
			return tx.Commit() // when open db as read-only, commit of this RO transaction is required
		})
	}

	return db.Update(context.Background(), func(tx kv.RwTx) error {
		return createAll(tx.(kv.BucketMigrator))
	})
}

// openDeprecatedBuckets opens deprecated buckets if they exist (without creating them).
func (db *MdbxKV) openDeprecatedBuckets(buckets []string) error {
	return db.env.View(func(tx *mdbx.Txn) error {
		for _, name := range buckets {
			if !db.buckets[name].IsDeprecated {
				continue
			}
			cnfCopy := db.buckets[name]
			dbi, err := tx.OpenDBI(name, mdbx.DBAccede, nil, nil)
			if err != nil {
				if mdbx.IsNotFound(err) {
					cnfCopy.DBI = NonExistingDBI
					db.buckets[name] = cnfCopy
					continue
				}
				return fmt.Errorf("bucket: %s, %w", name, err)
			}
			cnfCopy.DBI = kv.DBI(dbi)
			db.buckets[name] = cnfCopy
		}
		return nil
	})
}

func (db *MdbxKV) trackTxBegin() bool {
	db.txsCountMutex.Lock()
	defer db.txsCountMutex.Unlock()

	isOpen := !db.closed.Load()
	if isOpen {
		db.txsCount++
	}
	return isOpen
}

func (db *MdbxKV) hasTxsAllDoneAndClosed() bool {
	return (db.txsCount == 0) && db.closed.Load()
}

func (db *MdbxKV) trackTxEnd() {
	db.txsCountMutex.Lock()
	defer db.txsCountMutex.Unlock()

	if db.txsCount > 0 {
		db.txsCount--
	} else {
		panic("MdbxKV: unmatched trackTxEnd")
	}

	if db.hasTxsAllDoneAndClosed() {
		db.txsAllDoneOnCloseCond.Signal()
	}
}

func (db *MdbxKV) waitTxsAllDoneOnClose() {
	db.txsCountMutex.Lock()
	defer db.txsCountMutex.Unlock()

	for !db.hasTxsAllDoneAndClosed() {
		db.txsAllDoneOnCloseCond.Wait()
	}
}

// Close closes db.
// All transactions must be closed before closing the database.
func (db *MdbxKV) Close() {
	// Flush any pending batch before setting closed=true,
	// otherwise batch.run() will fail because trackTxBegin() checks the closed flag.
	db.flushPendingBatch()

	if ok := db.closed.CompareAndSwap(false, true); !ok {
		return
	}

	db.waitTxsAllDoneOnClose()

	db.env.Close()
	db.env = nil

	if db.opts.inMem {
		if err := os.RemoveAll(db.opts.path); err != nil {
			db.log.Warn("failed to remove in-mem db file", "err", err)
		}
	}
	removeFromPathDbMap(db.path)
}

func (db *MdbxKV) flushPendingBatch() {
	db.batchMu.Lock()
	b := db.batch
	db.batch = nil
	db.batchMu.Unlock()
	if b != nil {
		b.trigger()
	}
}

func (db *MdbxKV) BeginRo(ctx context.Context) (txn kv.Tx, err error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if !db.trackTxBegin() {
		return nil, fmt.Errorf("db closed")
	}

	if semErr := db.roTxsLimiter.Acquire(ctx, 1); semErr != nil {
		db.trackTxEnd()
		return nil, fmt.Errorf("mdbx.MdbxKV.BeginRo: roTxsLimiter error %w", semErr)
	}

	defer func() {
		if txn == nil {
			db.roTxsLimiter.Release(1)
			db.trackTxEnd()
		}
	}()

	tx, err := db.env.BeginTxn(nil, mdbx.Readonly)
	if err != nil {
		return nil, fmt.Errorf("%w, label: %s, trace: %s", err, db.opts.label.String(), stack2.Trace().String())
	}

	return &MdbxTx{
		ctx:      ctx,
		db:       db,
		tx:       tx,
		readOnly: true,
		id:       db.leakDetector.Add(),
	}, nil
}

func (db *MdbxKV) BeginRw(ctx context.Context) (kv.RwTx, error) {
	return db.beginRw(ctx, 0)
}
func (db *MdbxKV) BeginRwNosync(ctx context.Context) (kv.RwTx, error) {
	return db.beginRw(ctx, mdbx.TxNoSync)
}

func (db *MdbxKV) beginRw(ctx context.Context, flags uint) (txn kv.RwTx, err error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}

	if !db.trackTxBegin() {
		return nil, fmt.Errorf("db closed")
	}

	runtime.LockOSThread()
	tx, err := db.env.BeginTxn(nil, flags)
	if err != nil {
		runtime.UnlockOSThread() // unlock only in case of error. normal flow is "defer .Rollback()"
		db.trackTxEnd()
		return nil, fmt.Errorf("%w, lable: %s, trace: %s", err, db.opts.label.String(), stack2.Trace().String())
	}

	return &MdbxTx{
		db:  db,
		tx:  tx,
		ctx: ctx,
		id:  db.leakDetector.Add(),
	}, nil
}

func (db *MdbxKV) View(ctx context.Context, f func(tx kv.Tx) error) (err error) {
	// can't use db.env.View method - because it calls commit for read transactions - it conflicts with write transactions.
	tx, err := db.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	return f(tx)
}

func (db *MdbxKV) UpdateNosync(ctx context.Context, f func(tx kv.RwTx) error) error {
	return db.update(ctx, f, db.BeginRwNosync)
}

func (db *MdbxKV) Update(ctx context.Context, f func(tx kv.RwTx) error) error {
	return db.update(ctx, f, db.BeginRw)
}

func (db *MdbxKV) update(ctx context.Context, f func(tx kv.RwTx) error, begin func(ctx context.Context) (kv.RwTx, error)) error {
	tx, err := begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = f(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (tx *MdbxTx) CHandle() unsafe.Pointer {
	return tx.tx.CHandle()
}
