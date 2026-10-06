package state

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDomainRanges_StringAndAny(t *testing.T) {
	var empty DomainRanges
	require.False(t, empty.any())
	require.Equal(t, "", empty.String())

	full := DomainRanges{
		values: true, valuesStartTxNum: 1, valuesEndTxNum: 2,
		history: true, historyStartTxNum: 3, historyEndTxNum: 4,
		index: true, indexStartTxNum: 5, indexEndTxNum: 6,
	}
	require.True(t, full.any())
	s := full.String()
	require.Contains(t, s, "Values: [1, 2)")
	require.Contains(t, s, "History: [3, 4)")
	require.Contains(t, s, "Index: [5, 6)")
}

func TestHistoryRanges_StringAndAny(t *testing.T) {
	var empty HistoryRanges
	require.False(t, empty.any())
	require.Equal(t, "", empty.String(4))

	full := HistoryRanges{
		history: true, historyStartTxNum: 4, historyEndTxNum: 8,
		index: true, indexStartTxNum: 8, indexEndTxNum: 12,
	}
	require.True(t, full.any())
	s := full.String(4)
	require.Contains(t, s, "hist: 1-2")
	require.Contains(t, s, "idx: 2-3")
}
