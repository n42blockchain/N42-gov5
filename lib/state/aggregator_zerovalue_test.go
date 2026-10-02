package state

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAggV3TypesCloseNilSafe(t *testing.T) {
	var c AggV3Collation
	c.Close() // zero-value maps/collations: must not panic

	var sf AggV3StaticFiles
	sf.Close()

	var ssf SelectedStaticFilesV3
	ssf.Close()

	var mf MergedFilesV3
	mf.Close()
	require.Empty(t, mf.FrozenList())
}

func TestAggregator_StartUnbufferedWrites(t *testing.T) {
	_, _, agg := lsTNewAggregator(t, 4)
	a2 := agg.StartUnbufferedWrites()
	require.Same(t, agg, a2)
	agg.FinishWrites()
}
