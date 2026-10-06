package mdbx

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
)

// TestRatioHelper covers the ratio() formatting helper directly, including
// its payload<=0 guard.
func TestRatioHelper(t *testing.T) {
	require.Equal(t, "n/a", ratio(100, 0))
	require.Equal(t, "n/a", ratio(100, -1))
	require.Equal(t, "2.0", ratio(200, 100))
}

// TestWriteProbeEnabledDefault just reads the current compiled-in flag; it is
// false unless N42_WRITE_PROBE=1 was set at process start.
func TestWriteProbeEnabledDefault(t *testing.T) {
	_ = WriteProbeEnabled()
}

// TestSetWriteProbeLogger covers both the normal and nil-logger branches.
func TestSetWriteProbeLogger(t *testing.T) {
	var mu sync.Mutex
	var calls int
	SetWriteProbeLogger(func(msg string, kv ...interface{}) {
		mu.Lock()
		calls++
		mu.Unlock()
	})
	t.Cleanup(func() { SetWriteProbeLogger(nil) })

	writeProbeLogger.RLock()
	logger := writeProbeLogger.log
	writeProbeLogger.RUnlock()
	logger("hello")
	mu.Lock()
	require.Equal(t, 1, calls)
	mu.Unlock()

	// nil logger should be replaced with a no-op, not stored as nil.
	SetWriteProbeLogger(nil)
	writeProbeLogger.RLock()
	logger2 := writeProbeLogger.log
	writeProbeLogger.RUnlock()
	require.NotPanics(t, func() { logger2("noop") })
}

// TestWriteProbeFullPath forces writeProbeEnabled on (this is a package-level
// var, directly assignable from white-box tests) to exercise noteWrite,
// writeProbeDirty, and logWriteProbe end to end against a real MDBX
// transaction, then restores the original value.
func TestWriteProbeFullPath(t *testing.T) {
	orig := writeProbeEnabled
	writeProbeEnabled = true
	t.Cleanup(func() { writeProbeEnabled = orig })

	var loggedMsg string
	var loggedArgs []interface{}
	var mu sync.Mutex
	SetWriteProbeLogger(func(msg string, args ...interface{}) {
		mu.Lock()
		loggedMsg = msg
		loggedArgs = args
		mu.Unlock()
	})
	t.Cleanup(func() { SetWriteProbeLogger(nil) })

	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)

	require.NoError(t, tx.Put("Table", []byte("wk1"), []byte("wv1")))
	require.NoError(t, tx.Put("Table", []byte("wk2"), []byte("wv2")))
	require.NoError(t, tx.Delete("Table", []byte("wk1")))

	require.NoError(t, tx.Commit())

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, "write probe", loggedMsg)
	require.NotEmpty(t, loggedArgs)
}

// TestWriteProbeNoteWriteReadOnlyNoop ensures noteWrite is a no-op on a
// read-only transaction even when the probe is enabled.
func TestWriteProbeNoteWriteReadOnlyNoop(t *testing.T) {
	orig := writeProbeEnabled
	writeProbeEnabled = true
	t.Cleanup(func() { writeProbeEnabled = orig })

	db := BaseCaseDB(t)
	roTx, err := db.BeginRo(context.Background())
	require.NoError(t, err)
	defer roTx.Rollback()

	mtx := roTx.(*MdbxTx)
	mtx.noteWrite("Table", 10, false)
	require.Nil(t, mtx.tableWrites)
}

// TestWriteProbeDirtyDisabled checks writeProbeDirty's early-return branch
// when the probe is off (the default/production state).
func TestWriteProbeDirtyDisabled(t *testing.T) {
	require.False(t, writeProbeEnabled)
	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	mtx := tx.(*MdbxTx)
	dirty, limit, ok := mtx.writeProbeDirty()
	require.False(t, ok)
	require.Zero(t, dirty)
	require.Zero(t, limit)
}

// TestWriteProbeLogWriteProbeNoRows checks the empty-totalRows early return.
func TestWriteProbeLogWriteProbeNoRows(t *testing.T) {
	orig := writeProbeEnabled
	writeProbeEnabled = true
	t.Cleanup(func() { writeProbeEnabled = orig })

	db := BaseCaseDB(t)
	tx, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer tx.Rollback()

	mtx := tx.(*MdbxTx)
	mtx.tableWrites = &tableWrites{rows: map[string]*tableWriteStat{}}
	// Should simply return without panicking since totalRows == 0.
	mtx.logWriteProbe(0, 0, 0)
	_ = kv.ChainDB
}
