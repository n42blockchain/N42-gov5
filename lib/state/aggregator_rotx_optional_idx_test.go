package state

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAggregatorRoTx_BuildOptionalMissedIndices(t *testing.T) {
	ctx := context.Background()
	agg, _, _, _ := lsTSeedAggregator(t, 4, 16)

	ac := agg.BeginFilesRo()
	defer ac.Close()

	require.NoError(t, ac.BuildOptionalMissedIndices(ctx, 2))
}
