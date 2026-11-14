package mdbx

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/log/v3"
)

func rwHolderTestHold(t *testing.T, db kv.RwDB, started chan<- struct{}, hold time.Duration) {
	require.NoError(t, db.Update(context.Background(), func(tx kv.RwTx) error {
		close(started)
		time.Sleep(hold)
		return tx.Put("HolderT", []byte("a"), []byte("b"))
	}))
}

// A write transaction that waited for the writer names the transaction
// that held it, with how long it held it.
func TestRwBeginBehindNamesHolder(t *testing.T) {
	defer SetRwHolderDiagForTest(true)()
	db := NewMDBX(log.New()).InMem(t.TempDir()).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return kv.TableCfg{"HolderT": {}}
	}).MustOpen()
	t.Cleanup(db.Close)

	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		rwHolderTestHold(t, db, started, 60*time.Millisecond)
	}()
	<-started
	rw, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer rw.Rollback()
	wait, behind := rw.(*MdbxTx).RwBeginBehind()
	<-done
	require.Greater(t, wait, 20*time.Millisecond)
	require.True(t, strings.Contains(behind, "rwHolderTestHold:"), "behind %q", behind)

	// Off: an untagged holder is accounted as "other", a tagged one by its
	// tag, whatever the diagnostics switch says.
	rw.Rollback()
	defer SetRwHolderDiagForTest(false)()
	started2 := make(chan struct{})
	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		require.NoError(t, db.Update(kv.WithRwTag(context.Background(), "tagged"), func(tx kv.RwTx) error {
			close(started2)
			time.Sleep(60 * time.Millisecond)
			return tx.Put("HolderT", []byte("a"), []byte("c"))
		}))
	}()
	<-started2
	rw2, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	wait2, behind := rw2.(*MdbxTx).RwBeginBehind()
	holder := rw2.(*MdbxTx).RwWaitHolder()
	rw2.Rollback()
	<-done2
	require.Greater(t, wait2, 20*time.Millisecond)
	require.True(t, strings.HasPrefix(behind, "tagged:"), "behind %q", behind)
	require.True(t, strings.HasPrefix(holder, "tagged:"), "holder %q", holder)
	st := db.(*MdbxKV).RwHolderStatsForTest()
	require.Equal(t, int64(1), st["tagged"][0])
	require.GreaterOrEqual(t, time.Duration(st["tagged"][1]), 50*time.Millisecond)
	require.GreaterOrEqual(t, st["other"][0], int64(1))
	require.GreaterOrEqual(t, time.Duration(st["other"][2]), 20*time.Millisecond, "the untagged begin's wait")
}

// The per-holder window renders by held time with the held share.
func TestFormatRwHolders(t *testing.T) {
	win := map[string]*rwHolderStat{
		"blockWrite": {n: 2, held: 120 * time.Millisecond, wait: 30 * time.Millisecond},
		"canon":      {n: 3, held: 30 * time.Millisecond},
	}
	s, share := formatRwHolders(win, time.Second)
	require.Equal(t, "blockWrite=2/120.0/30.0 canon=3/30.0/0.0", s)
	require.InDelta(t, 0.15, share, 1e-9)
}
