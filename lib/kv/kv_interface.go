/*
   Copyright 2022 Erigon contributors

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

package kv

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	"github.com/n42blockchain/N42/lib/kv/iter"
	"github.com/n42blockchain/N42/lib/kv/order"
	"github.com/n42blockchain/N42/lib/metrics"
)

// Variable naming conventions:
//   tx       - Database Transaction
//   txn      - Ethereum Transaction (TxNum is also number of Ethereum Transaction)
//   blockNum - Ethereum block number - same across all nodes. blockID - auto-increment ID - can differ across nodes
//   txNum/txID - same
//   RoTx     - Read-Only Database Transaction. RwTx - read-write
//   k, v     - key, value
//   ts       - TimeStamp. Usually Ethereum's TransactionNumber (auto-increment ID) or BlockNumber.
//   Cursor   - low-level mdbx API to navigate over Table
//   Iter     - high-level iterator API over Table/InvertedIndex/History/Domain. See package `iter`.
//
// Method naming conventions:
//   Prune:  delete old data
//   Unwind: delete recent data
//   Get:    exact match of criteria
//   Range:  [from, to). from=nil means StartOfTable, to=nil means EndOfTable, limit=-1 means Unlimited
//           Analog of SQL: SELECT * FROM Table WHERE k>=from AND k<to ORDER BY k ASC/DESC LIMIT n
//   Prefix: Range(Table, prefix, kv.NextSubtree(prefix))
//
// Abstraction layers:
//   LowLevel:
//     1. DB/Tx - low-level key-value database
//     2. Snapshots/FrozenData - immutable files with historical data
//   MediumLevel:
//     1. TemporalDB - abstracting DB+Snapshots with time-travel API
//   HighLevel:
//     1. Application - relies on TemporalDB (e.g. ExecutionLayer) or just DB (e.g. TxPool, Sentry)

const ReadersLimit = 32000 // MDBX_READERS_LIMIT=32767

const Unlim int = -1

var (
	ErrAttemptToDeleteNonDeprecatedBucket = errors.New("only buckets from dbutils.ChaindataDeprecatedTables can be deleted")

	DbSize    = metrics.GetOrCreateGauge(`db_size`)    //nolint
	TxLimit   = metrics.GetOrCreateGauge(`tx_limit`)   //nolint
	TxSpill   = metrics.GetOrCreateGauge(`tx_spill`)   //nolint
	TxUnspill = metrics.GetOrCreateGauge(`tx_unspill`) //nolint
	TxDirty   = metrics.GetOrCreateGauge(`tx_dirty`)   //nolint

	DbCommitPreparation = metrics.GetOrCreateSummary(`db_commit_seconds{phase="preparation"}`) //nolint
	DbCommitWrite       = metrics.GetOrCreateSummary(`db_commit_seconds{phase="write"}`)       //nolint
	DbCommitSync        = metrics.GetOrCreateSummary(`db_commit_seconds{phase="sync"}`)        //nolint
	DbCommitEnding      = metrics.GetOrCreateSummary(`db_commit_seconds{phase="ending"}`)      //nolint
	DbCommitTotal       = metrics.GetOrCreateSummary(`db_commit_seconds{phase="total"}`)       //nolint

	DbPgopsNewly   = metrics.GetOrCreateGauge(`db_pgops{phase="newly"}`)   //nolint
	DbPgopsCow     = metrics.GetOrCreateGauge(`db_pgops{phase="cow"}`)     //nolint
	DbPgopsClone   = metrics.GetOrCreateGauge(`db_pgops{phase="clone"}`)   //nolint
	DbPgopsSplit   = metrics.GetOrCreateGauge(`db_pgops{phase="split"}`)   //nolint
	DbPgopsMerge   = metrics.GetOrCreateGauge(`db_pgops{phase="merge"}`)   //nolint
	DbPgopsSpill   = metrics.GetOrCreateGauge(`db_pgops{phase="spill"}`)   //nolint
	DbPgopsUnspill = metrics.GetOrCreateGauge(`db_pgops{phase="unspill"}`) //nolint
	DbPgopsWops    = metrics.GetOrCreateGauge(`db_pgops{phase="wops"}`)    //nolint

	GcLeafMetric     = metrics.GetOrCreateGauge(`db_gc_leaf`)     //nolint
	GcOverflowMetric = metrics.GetOrCreateGauge(`db_gc_overflow`) //nolint
	GcPagesMetric    = metrics.GetOrCreateGauge(`db_gc_pages`)    //nolint

	// DB read/write counters — incremented per transaction in CollectMetrics().
	DbReadCount  = metrics.GetOrCreateGauge(`db_read_total`)       //nolint
	DbWriteCount = metrics.GetOrCreateGauge(`db_write_total`)      //nolint
	DbReadBytes  = metrics.GetOrCreateGauge(`db_read_bytes_total`) //nolint
	DbWriteBytes = metrics.GetOrCreateGauge(`db_write_bytes_total`)//nolint
)

type DBVerbosityLvl int8
type Label uint8

const (
	ChainDB       Label = 0
	TxPoolDB      Label = 1
	SentryDB      Label = 2
	ConsensusDB   Label = 3
	DownloaderDB  Label = 4
	InMem         Label = 5
	DiagnosticsDB Label = 6
	// CaplinDB is the dedicated MDBX environment Caplin (the embedded
	// consensus layer) owns. Added in Phase 7.4 so block_collector and
	// other caplin-internal stores can label their environments
	// distinctly from the main chaindata.
	CaplinDB Label = 7
)

func (l Label) String() string {
	switch l {
	case ChainDB:
		return "chaindata"
	case TxPoolDB:
		return "txpool"
	case SentryDB:
		return "sentry"
	case ConsensusDB:
		return "consensus"
	case DownloaderDB:
		return "downloader"
	case InMem:
		return "inMem"
	case DiagnosticsDB:
		return "diagnostics"
	case CaplinDB:
		return "caplin"
	default:
		return "unknown"
	}
}

func UnmarshalLabel(s string) Label {
	switch s {
	case "chaindata":
		return ChainDB
	case "txpool":
		return TxPoolDB
	case "sentry":
		return SentryDB
	case "consensus":
		return ConsensusDB
	case "downloader":
		return DownloaderDB
	case "inMem":
		return InMem
	case "diagnostics":
		return DiagnosticsDB
	case "caplin":
		return CaplinDB
	default:
		panic(fmt.Sprintf("unexpected label: %s", s))
	}
}

type Has interface {
	// Has indicates whether a key exists in the database.
	Has(table string, key []byte) (bool, error)
}
type GetPut interface {
	Getter
	Putter
}
type Getter interface {
	Has

	// GetOne references a readonly section of memory that must not be accessed after txn has terminated
	GetOne(table string, key []byte) (val []byte, err error)

	// ForEach iterates over entries with keys greater or equal to fromPrefix.
	// walker is called for each eligible entry.
	// If walker returns an error:
	//   - implementations of local db - stop
	//   - implementations of remote db - do not handle this error and may finish (send all entries to client) before error happen.
	ForEach(table string, fromPrefix []byte, walker func(k, v []byte) error) error
	ForPrefix(table string, prefix []byte, walker func(k, v []byte) error) error
	ForAmount(table string, prefix []byte, amount uint32, walker func(k, v []byte) error) error
}

// Putter wraps the database write operations.
type Putter interface {
	// Put inserts or updates a single entry.
	Put(table string, k, v []byte) error
}

// Deleter wraps the database delete operations.
type Deleter interface {
	// Delete removes a single entry.
	Delete(table string, k []byte) error
}

type Closer interface {
	Close()
}

// RoDB - Read-only version of KV.
type RoDB interface {
	Closer
	ReadOnly() bool
	View(ctx context.Context, f func(tx Tx) error) error

	// BeginRo - creates transaction
	// 	tx may be discarded by .Rollback() method
	//
	// A transaction and its cursors must only be used by a single
	// 	thread (not goroutine), and a thread may only have a single transaction at a time.
	//  It happen automatically by - because this method calls runtime.LockOSThread() inside (Rollback/Commit releases it)
	//  By this reason application code can't call runtime.UnlockOSThread() - it leads to undefined behavior.
	//
	// If this `parent` is non-NULL, the new transaction
	//	will be a nested transaction, with the transaction indicated by parent
	//	as its parent. Transactions may be nested to any level. A parent
	//	transaction and its cursors may not issue any other operations than
	//	Commit and Rollback while it has active child transactions.
	BeginRo(ctx context.Context) (Tx, error)
	AllTables() TableCfg
	PageSize() uint64

	// Pointer to the underlying C environment handle, if applicable (e.g. *C.MDBX_env)
	CHandle() unsafe.Pointer
}

// RwDB low-level database interface - main target is - to provide common abstraction over top of MDBX and RemoteKV.
//
// Common pattern for short-living transactions:
//
//	 if err := db.View(ctx, func(tx ethdb.Tx) error {
//	    ... code which uses database in transaction
//	 }); err != nil {
//			return err
//	}
//
// Common pattern for long-living transactions:
//
//	tx, err := db.Begin()
//	if err != nil {
//		return err
//	}
//	defer tx.Rollback()
//
//	... code which uses database in transaction
//
//	err := tx.Commit()
//	if err != nil {
//		return err
//	}
type RwDB interface {
	RoDB

	Update(ctx context.Context, f func(tx RwTx) error) error
	UpdateNosync(ctx context.Context, f func(tx RwTx) error) error

	BeginRw(ctx context.Context) (RwTx, error)
	BeginRwNosync(ctx context.Context) (RwTx, error)
}

type StatelessReadTx interface {
	Getter

	Commit() error // Commit all the operations of a transaction into the database.
	Rollback()     // Rollback - abandon all the operations of the transaction instead of saving them.

	// ReadSequence - allows to create a linear sequence of unique positive integers for each table.
	// Can be called for a read transaction to retrieve the current sequence value, and the increment must be zero.
	// Sequence changes become visible outside the current write transaction after it is committed, and discarded on abort.
	// Starts from 0.
	ReadSequence(table string) (uint64, error)
}

type StatelessWriteTx interface {
	Putter
	Deleter

	/*
		// if need N id's:
		baseId, err := tx.IncrementSequence(bucket, N)
		if err != nil {
		   return err
		}
		for i := 0; i < N; i++ {    // if N == 0, it will work as expected
		    id := baseId + i
		    // use id
		}


		// or if need only 1 id:
		id, err := tx.IncrementSequence(bucket, 1)
		if err != nil {
		    return err
		}
		// use id
	*/
	IncrementSequence(table string, amount uint64) (uint64, error)
	Append(table string, k, v []byte) error
	AppendDup(table string, k, v []byte) error
}

type StatelessRwTx interface {
	StatelessReadTx
	StatelessWriteTx
}

// PendingMutations in-memory storage of changes
// Later they can either be flushed to the database or abandon
type PendingMutations interface {
	StatelessRwTx
	// Flush all in-memory data into `tx`
	Flush(ctx context.Context, tx RwTx) error
	Close()
	BatchSize() int
}

// Tx
// WARNING:
//   - Tx is not threadsafe and may only be used in the goroutine that created it
//   - ReadOnly transactions do not lock goroutine to thread, RwTx does
type Tx interface {
	StatelessReadTx
	BucketMigratorRO

	// ID returns the identifier associated with this transaction. For a
	// read-only transaction, this corresponds to the snapshot being read;
	// concurrent readers will frequently have the same transaction ID.
	ViewID() uint64

	// Cursor - creates cursor object on top of given bucket. Type of cursor - depends on bucket configuration.
	// If bucket was created with mdbx.DupSort flag, then cursor with interface CursorDupSort created
	// Otherwise - object of interface Cursor created
	//
	// Cursor, also provides a grain of magic - it can use a declarative configuration - and automatically break
	// long keys into DupSort key/values. See docs for `bucket.go:TableCfgItem`
	Cursor(table string) (Cursor, error)
	CursorDupSort(table string) (CursorDupSort, error) // CursorDupSort - can be used if bucket has mdbx.DupSort flag

	DBSize() (uint64, error)

	// --- High-Level methods: 1request -> stream of server-side pushes ---

	// Range [from, to)
	// Range(from, nil) means [from, EndOfTable)
	// Range(nil, to)   means [StartOfTable, to)
	Range(table string, fromPrefix, toPrefix []byte) (iter.KV, error)
	// Stream is like Range, but for requesting huge data (Example: full table scan). Client can't stop it.
	//Stream(table string, fromPrefix, toPrefix []byte) (iter.KV, error)
	// RangeAscend - like Range [from, to) but also allow pass Limit parameters
	// Limit -1 means Unlimited
	RangeAscend(table string, fromPrefix, toPrefix []byte, limit int) (iter.KV, error)
	//StreamAscend(table string, fromPrefix, toPrefix []byte, limit int) (iter.KV, error)
	// RangeDescend - is like Range [from, to), but expecing `from`<`to`
	// example: RangeDescend("Table", "B", "A", -1)
	RangeDescend(table string, fromPrefix, toPrefix []byte, limit int) (iter.KV, error)
	//StreamDescend(table string, fromPrefix, toPrefix []byte, limit int) (iter.KV, error)
	// Prefix - is exactly Range(Table, prefix, kv.NextSubtree(prefix))
	Prefix(table string, prefix []byte) (iter.KV, error)

	// RangeDupSort - like Range but for fixed single key and iterating over range of values
	RangeDupSort(table string, key []byte, fromPrefix, toPrefix []byte, asc order.By, limit int) (iter.KV, error)

	// --- High-Level methods: 1request -> 1page of values in response -> send next page request ---
	// Paginate(table string, fromPrefix, toPrefix []byte) (PairsStream, error)

	// --- High-Level deprecated methods ---

	ForEach(table string, fromPrefix []byte, walker func(k, v []byte) error) error
	ForPrefix(table string, prefix []byte, walker func(k, v []byte) error) error
	ForAmount(table string, prefix []byte, amount uint32, walker func(k, v []byte) error) error

	// Pointer to the underlying C transaction handle (e.g. *C.MDBX_txn)
	CHandle() unsafe.Pointer
	BucketSize(table string) (uint64, error)
}

// RwTx
//
// WARNING:
//   - RwTx is not threadsafe and may only be used in the goroutine that created it.
//   - ReadOnly transactions do not lock goroutine to thread, RwTx does
//   - User Can't call runtime.LockOSThread/runtime.UnlockOSThread in same goroutine until RwTx Commit/Rollback
type RwTx interface {
	Tx
	StatelessWriteTx
	BucketMigrator

	RwCursor(table string) (RwCursor, error)
	RwCursorDupSort(table string) (RwCursorDupSort, error)

	// CollectMetrics - does collect all DB-related and Tx-related metrics
	// this method exists only in RwTx to avoid concurrency
	CollectMetrics()
}

type BucketMigratorRO interface {
	ListBuckets() ([]string, error)
}

// BucketMigrator used for buckets migration, don't use it in usual app code
type BucketMigrator interface {
	BucketMigratorRO
	DropBucket(string) error
	CreateBucket(string) error
	ExistsBucket(string) (bool, error)
	ClearBucket(string) error
}

// Cursor - class for navigating through a database
// CursorDupSort are inherit this class
//
// If methods (like First/Next/Seek) return error, then returned key SHOULD not be nil (can be []byte{} for example).
// Then looping code will look as:
// c := kv.Cursor(bucketName)
//
//	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
//	   if err != nil {
//	       return err
//	   }
//	   ... logic
//	}
type Cursor interface {
	First() ([]byte, []byte, error)               // First - position at first key/data item
	Seek(seek []byte) ([]byte, []byte, error)     // Seek - position at first key greater than or equal to specified key
	SeekExact(key []byte) ([]byte, []byte, error) // SeekExact - position at exact matching key if exists
	Next() ([]byte, []byte, error)                // Next - position at next key/value (can iterate over DupSort key/values automatically)
	Prev() ([]byte, []byte, error)                // Prev - position at previous key
	Last() ([]byte, []byte, error)                // Last - position at last key and last possible value
	Current() ([]byte, []byte, error)             // Current - return key/data at current cursor position

	Count() (uint64, error) // Count - fast way to calculate amount of keys in bucket. It counts all keys even if Prefix was set.

	Close()
}

type RwCursor interface {
	Cursor

	Put(k, v []byte) error           // Put - based on order
	Append(k []byte, v []byte) error // Append - append the given key/data pair to the end of the database. This option allows fast bulk loading when keys are already known to be in the correct order.
	Delete(k []byte) error           // Delete - short version of SeekExact+DeleteCurrent or SeekBothExact+DeleteCurrent

	// DeleteCurrent This function deletes the key/data pair to which the cursor refers.
	// This does not invalidate the cursor, so operations such as MDB_NEXT
	// can still be used on it.
	// Both MDB_NEXT and MDB_GET_CURRENT will return the same record after
	// this operation.
	DeleteCurrent() error
}

// CursorDupSort
//
// Example:
//
//	for k, v, err = cursor.First(); k != nil; k, v, err = cursor.NextNoDup() {
//		if err != nil {
//			return err
//		}
//		for ; v != nil; _, v, err = cursor.NextDup() {
//			if err != nil {
//				return err
//			}
//
//		}
//	}
type CursorDupSort interface {
	Cursor

	// SeekBothExact -
	// second parameter can be nil only if searched key has no duplicates, or return error
	SeekBothExact(key, value []byte) ([]byte, []byte, error)
	SeekBothRange(key, value []byte) ([]byte, error) // SeekBothRange - exact match of the key, but range match of the value
	FirstDup() ([]byte, error)                       // FirstDup - position at first data item of current key
	NextDup() ([]byte, []byte, error)                // NextDup - position at next data item of current key
	NextNoDup() ([]byte, []byte, error)              // NextNoDup - position at first data item of next key
	PrevDup() ([]byte, []byte, error)
	PrevNoDup() ([]byte, []byte, error)
	LastDup() ([]byte, error) // LastDup - position at last data item of current key

	CountDuplicates() (uint64, error) // CountDuplicates - number of duplicates for the current key
}

type RwCursorDupSort interface {
	CursorDupSort
	RwCursor

	PutNoDupData(key, value []byte) error // PutNoDupData - inserts key without dupsort
	DeleteCurrentDuplicates() error       // DeleteCurrentDuplicates - deletes all of the data items for the current key
	DeleteExact(k1, k2 []byte) error      // DeleteExact - delete 1 value from given key
	AppendDup(key, value []byte) error    // AppendDup - same as Append, but for sorted dup data
}

// ---- Temporal part

type (
	Domain      string
	History     string
	InvertedIdx string
)

type TemporalTx interface {
	Tx
	DomainGet(name Domain, k, k2 []byte) (v []byte, ok bool, err error)
	DomainGetAsOf(name Domain, k, k2 []byte, ts uint64) (v []byte, ok bool, err error)
	HistoryGet(name History, k []byte, ts uint64) (v []byte, ok bool, err error)

	// IndexRange - return iterator over range of inverted index for given key `k`
	// Asc semantic:  [from, to) AND from > to
	// Desc semantic: [from, to) AND from < to
	// Limit -1 means Unlimited
	// from -1, to -1 means unbounded (StartOfTable, EndOfTable)
	// Example: IndexRange("IndexName", 10, 5, order.Desc, -1)
	// Example: IndexRange("IndexName", -1, -1, order.Asc, 10)
	IndexRange(name InvertedIdx, k []byte, fromTs, toTs int, asc order.By, limit int) (timestamps iter.U64, err error)
	HistoryRange(name History, fromTs, toTs int, asc order.By, limit int) (it iter.KV, err error)
	DomainRange(name Domain, fromKey, toKey []byte, ts uint64, asc order.By, limit int) (it iter.KV, err error)
}

// Step represents a state version step (temporal domain).
type Step = uint64
