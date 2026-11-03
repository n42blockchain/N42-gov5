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

	// Off: nothing is named.
	rw.Rollback()
	defer SetRwHolderDiagForTest(false)()
	rw2, err := db.BeginRw(context.Background())
	require.NoError(t, err)
	defer rw2.Rollback()
	_, behind = rw2.(*MdbxTx).RwBeginBehind()
	require.Empty(t, behind)
}
