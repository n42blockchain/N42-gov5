package state

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKvList_SortInterface(t *testing.T) {
	var l KvList
	l.Push("b", []byte("2"))
	l.Push("a", []byte("1"))
	l.Push("c", []byte("3"))

	require.Equal(t, 3, l.Len())
	require.True(t, l.Less(1, 0)) // "a" < "b"

	sort.Sort(&l)
	require.Equal(t, []string{"a", "b", "c"}, l.Keys)
	require.Equal(t, [][]byte{[]byte("1"), []byte("2"), []byte("3")}, l.Vals)
}

func TestCommitmentMode_StringAndParse(t *testing.T) {
	require.Equal(t, "disabled", CommitmentModeDisabled.String())
	require.Equal(t, "direct", CommitmentModeDirect.String())
	require.Equal(t, "update", CommitmentModeUpdate.String())
	require.Equal(t, "unknown", CommitmentMode(99).String())

	require.Equal(t, CommitmentModeDisabled, ParseCommitmentMode("off"))
	require.Equal(t, CommitmentModeUpdate, ParseCommitmentMode("update"))
	require.Equal(t, CommitmentModeDirect, ParseCommitmentMode("anything-else"))
}
