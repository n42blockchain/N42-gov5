//go:build linux && amd64 && cgo

package mdbx

/*
#include <stdint.h>
#include <stddef.h>
#include <sys/uio.h>

// Public ABI from the pinned mdbx-go v0.44.0/libmdbx/mdbx.h. Link against
// that package's existing MDBX objects; never load or compile a second MDBX.
typedef struct MDBX_txn MDBX_txn;
typedef struct MDBX_cursor MDBX_cursor;
typedef uint32_t MDBX_dbi;
typedef struct iovec MDBX_val;
_Static_assert(sizeof(MDBX_dbi) == 4, "MDBX DBI width");
_Static_assert(sizeof(MDBX_val) == 16, "MDBX value width");
_Static_assert(offsetof(MDBX_val, iov_len) == 8, "MDBX value field order");
extern int mdbx_dbi_flags_ex(const MDBX_txn *, MDBX_dbi, unsigned *, unsigned *);
extern int mdbx_cursor_open(MDBX_txn *, MDBX_dbi, MDBX_cursor **);
extern int mdbx_cursor_put(MDBX_cursor *, const MDBX_val *, MDBX_val *, unsigned);
extern void mdbx_cursor_close(MDBX_cursor *);

typedef struct {
    int code;
    int stage;
    size_t attempted;
    size_t succeeded;
} n42_put_many_result;

static int n42_mdbx_ok(int code) { return code == 0 || code == -1; }

static n42_put_many_result n42_put_same_value_many(
    MDBX_txn *txn, MDBX_dbi dbi, const uint8_t *keys, size_t count,
    const uint8_t *value, size_t value_len) {
    n42_put_many_result result = {0, 0, 0, 0};
    unsigned flags = 0, state = 0;
    result.code = mdbx_dbi_flags_ex(txn, dbi, &flags, &state);
    if (!n42_mdbx_ok(result.code)) return result;
    if (flags != 0) {
        result.code = -30784; // MDBX_INCOMPATIBLE in the pinned ABI.
        return result;
    }
    result.stage = 1;
    MDBX_cursor *cursor = NULL;
    result.code = mdbx_cursor_open(txn, dbi, &cursor);
    if (!n42_mdbx_ok(result.code)) return result;
    result.stage = 2;
    for (size_t i = 0; i < count; ++i) {
        // MDBX values are in/out arguments: never reuse a mutated descriptor.
        MDBX_val key = {(void *)(keys + 32 * i), 32};
        MDBX_val data = {(void *)value, value_len};
        ++result.attempted;
        result.code = mdbx_cursor_put(cursor, &key, &data, 0);
        if (!n42_mdbx_ok(result.code)) break;
        ++result.succeeded;
    }
    mdbx_cursor_close(cursor);
    return result;
}
*/
import "C"

import (
	"fmt"
	"math"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/erigontech/mdbx-go/mdbx"

	"github.com/n42blockchain/N42/lib/kv"
)

// TryPutSameValueMany writes ordinary 32-byte keys in the existing transaction.
// Passing the exact incoming Putter prevents promoted methods on wrappers from
// bypassing their routing, fault injection or accounting. A declined call has
// no side effects; any selected backend failure must never be replayed.
func (tx *MdbxTx) TryPutSameValueMany(owner kv.Putter, table string, keys []byte, keySize int, value []byte) (bool, error) {
	actual, direct := owner.(*MdbxTx)
	if !direct || actual != tx {
		return false, nil
	}
	if tx == nil || tx.tx == nil || tx.db == nil || tx.tx.CHandle() == nil {
		return true, &mdbx.OpError{Op: "mdbx_cursor_put", Errno: mdbx.BadTxn}
	}
	if tx.readOnly {
		return true, &mdbx.OpError{Op: "mdbx_cursor_put", Errno: syscall.EACCES}
	}
	bucket, ok := tx.db.buckets[table]
	if !ok {
		return true, fmt.Errorf("unknown batch table: %s", table)
	}
	if bucket.Flags != 0 || bucket.AutoDupSortKeysConversion {
		return false, nil
	}
	if keySize != 32 || len(keys)%32 != 0 || (len(keys) != 0 && len(value) > math.MaxInt/(len(keys)/32)-32) {
		return true, fmt.Errorf("invalid same-value batch geometry")
	}
	if len(keys) == 0 {
		return true, nil
	}
	result := C.n42_put_same_value_many(
		(*C.MDBX_txn)(tx.tx.CHandle()), C.MDBX_dbi(bucket.DBI),
		(*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(keys))), C.size_t(len(keys)/32),
		(*C.uint8_t)(unsafe.Pointer(unsafe.SliceData(value))), C.size_t(len(value)),
	)
	runtime.KeepAlive(tx)
	runtime.KeepAlive(keys)
	runtime.KeepAlive(value)
	attempted, succeeded := uint64(result.attempted), uint64(result.succeeded)
	payload := uint64(32 + len(value))
	tx.writeCount.Add(succeeded)
	tx.writeBytes.Add(succeeded * payload)
	if writeProbeEnabled && attempted != 0 {
		if tx.tableWrites == nil {
			tx.tableWrites = &tableWrites{rows: make(map[string]*tableWriteStat, 24)}
		}
		tx.tableWrites.mu.Lock()
		s := tx.tableWrites.rows[table]
		if s == nil {
			s = &tableWriteStat{}
			tx.tableWrites.rows[table] = s
		}
		s.puts += int64(attempted)
		s.bytes += int64(attempted * payload)
		tx.tableWrites.mu.Unlock()
	}
	sameValueBatches.Add(1)
	sameValueAttempts.Add(attempted)
	sameValueWritten.Add(succeeded)
	op := []string{"mdbx_dbi_flags_ex", "mdbx_cursor_open", "mdbx_cursor_put"}[int(result.stage)]
	return true, sameValueNativeError(op, int(result.code))
}

func sameValueNativeError(op string, code int) error {
	switch code {
	case 0, -1:
		return nil
	case int(mdbx.NotFound):
		return mdbx.ErrNotFound
	case int(syscall.ENODATA):
		return mdbx.ErrNoData
	}
	if int(mdbx.KeyExist) <= code && code <= int(mdbx.LaggardReader) {
		return &mdbx.OpError{Op: op, Errno: mdbx.Errno(code)}
	}
	return &mdbx.OpError{Op: op, Errno: syscall.Errno(code)}
}
