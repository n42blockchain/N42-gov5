package internal

import (
	"testing"

	"github.com/n42blockchain/N42/conf"
)

func TestSetQMDBUndoWindowBounds(t *testing.T) {
	t.Cleanup(func() { SetQMDBUndoWindow(0) })
	for _, c := range []struct{ in, want uint64 }{
		{0, DefaultQMDBUndoWindow},
		{10, DefaultQMDBUndoWindow},
		{1000, 1000},
		{conf.MaxQMDBUndoWindow + 1, conf.MaxQMDBUndoWindow},
	} {
		if got := SetQMDBUndoWindow(c.in); got != c.want || QMDBUndoWindow() != c.want {
			t.Fatalf("SetQMDBUndoWindow(%d) = %d / %d, want %d", c.in, got, QMDBUndoWindow(), c.want)
		}
	}
	if qmdbRealignMaxDepth != conf.QMDBRealignMaxDepth {
		t.Fatal("realign depth out of sync with the prune retention floor")
	}
}
