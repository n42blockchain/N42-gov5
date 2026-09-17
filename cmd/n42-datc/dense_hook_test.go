package main

import (
	"bytes"
	"testing"
)

// TestDenseHookKeepsOnlyRecordedLevels checks that the dense-node hook keeps
// the levels a node-record flush can take and drops deeper branches, which
// would otherwise stay in the maps forever.
func TestDenseHookKeepsOnlyRecordedLevels(t *testing.T) {
	b := &builder{accDepth: 4, stoDepth: 2}
	child := append([]byte{0xa0}, bytes.Repeat([]byte{7}, 32)...)
	report := func(domain, path []byte) { b.onDenseNode(domain, path, 1, 0, child) }
	domain := bytes.Repeat([]byte{0xc3}, 32)
	key := func(path []byte) string { return string(append(append([]byte{}, domain...), path...)) }

	report(nil, []byte{})
	report(nil, []byte{1, 2, 3})
	report(nil, []byte{1, 2, 3, 4})
	report(domain, []byte{})
	report(domain, []byte{5})
	report(domain, []byte{5, 6})

	for _, tc := range []struct {
		storage bool
		key     string
		kept    bool
	}{
		{false, "", true},
		{false, string([]byte{1, 2, 3}), true},
		{false, string([]byte{1, 2, 3, 4}), false},
		{true, key(nil), true},
		{true, key([]byte{5}), true},
		{true, key([]byte{5, 6}), false},
	} {
		dn, _ := b.takeDense(tc.storage, tc.key)
		if got := dn != nil; got != tc.kept {
			t.Errorf("storage=%v path len %d: kept=%v, want %v", tc.storage, len(tc.key), got, tc.kept)
		}
	}
	for i := range b.hooks {
		if n := len(b.hooks[i].denseAcc) + len(b.hooks[i].denseSto); n != 0 {
			t.Fatalf("shard %d still holds %d entries after every kept path was taken", i, n)
		}
	}
}
