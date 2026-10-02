package state

import (
	"container/heap"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReconHeap_HeapInterface(t *testing.T) {
	h := &ReconHeap{}
	heap.Init(h)

	heap.Push(h, &ReconItem{key: []byte("b"), txNum: 1})
	heap.Push(h, &ReconItem{key: []byte("a"), txNum: 2})
	heap.Push(h, &ReconItem{key: []byte("a"), txNum: 1})

	require.Equal(t, 3, h.Len())

	first := heap.Pop(h).(*ReconItem)
	require.Equal(t, "a", string(first.key))
	require.EqualValues(t, 1, first.txNum)

	second := heap.Pop(h).(*ReconItem)
	require.Equal(t, "a", string(second.key))
	require.EqualValues(t, 2, second.txNum)

	third := heap.Pop(h).(*ReconItem)
	require.Equal(t, "b", string(third.key))
}

func TestReconHeap_SwapDirect(t *testing.T) {
	rh := ReconHeap{
		{key: []byte("x")},
		{key: []byte("y")},
	}
	rh.Swap(0, 1)
	require.Equal(t, "y", string(rh[0].key))
	require.Equal(t, "x", string(rh[1].key))
}

func TestReconHeapOlderFirst_Less(t *testing.T) {
	rh := ReconHeap{
		{key: []byte("a"), txNum: 1},
		{key: []byte("a"), txNum: 2},
	}
	older := ReconHeapOlderFirst{ReconHeap: rh}
	// Same key: higher txNum sorts first (>=).
	require.True(t, older.Less(1, 0))
	require.False(t, older.Less(0, 1))
}
