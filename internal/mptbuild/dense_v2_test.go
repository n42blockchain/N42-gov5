package mptbuild

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/lib/trie"
)

// TestMarshalTrieNodeDenseV2_LeafMarker verifies that for synthetic
// branches with mixed tree/leaf children, V2 encoding emits 1-byte
// LeafMarker slots for HasTree=0 + hashed entries and full 33B for
// HasTree=1 + hashed entries.
func TestMarshalTrieNodeDenseV2_LeafMarker(t *testing.T) {
	entries := makeAccountEntries(100)

	var captured []capturedDense
	tgt := &MapTarget{}
	_, err := Build(context.Background(), Opts{
		Source:    &MapSource{Entries: entries},
		Target:    tgt,
		Extractor: NewAccountExtractor(),
		TmpDir:    filepath.Join(t.TempDir(), "etl"),
		BufMB:     1,
		DenseBranchSink: func(keyHex []byte, stateMask, treeMask uint16, slotData []byte) error {
			captured = append(captured, capturedDense{
				keyHex:    append([]byte(nil), keyHex...),
				stateMask: stateMask,
				treeMask:  treeMask,
				slotData:  append([]byte(nil), slotData...),
			})
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("no branches captured")
	}

	var (
		v1Total int
		v2Total int
		leafMarkerCount int
		hashBranchSlots int
	)
	const stride = 33
	for _, c := range captured {
		v1 := trie.MarshalTrieNodeDense(c.stateMask, c.treeMask, c.slotData, nil)
		v2 := trie.MarshalTrieNodeDenseV2(c.stateMask, c.treeMask, c.slotData, nil)
		v1Total += len(v1)
		v2Total += len(v2)

		// Decode V2 and check slot shape per HasTree.
		stateOut, treeOut, slots, derr := trie.UnmarshalTrieNodeDenseV2(v2)
		if derr != nil {
			t.Fatalf("UnmarshalTrieNodeDenseV2 (key %x): %v", c.keyHex, derr)
		}
		if stateOut != c.stateMask || treeOut != c.treeMask {
			t.Errorf("key %x masks differ: got state=%x tree=%x want state=%x tree=%x",
				c.keyHex, stateOut, treeOut, c.stateMask, c.treeMask)
		}
		j := 0
		for digit := 0; digit < 16; digit++ {
			if c.stateMask&(1<<digit) == 0 {
				if slots[digit] != nil {
					t.Errorf("key %x digit %d: unexpected slot for non-state bit", c.keyHex, digit)
				}
				continue
			}
			origPrefix := c.slotData[j*stride]
			origLen := slotLen(origPrefix)
			origBytes := c.slotData[j*stride : j*stride+origLen]

			isLeafHash := origPrefix == 0xa0 && (c.treeMask&(1<<digit)) == 0
			isBranchHash := origPrefix == 0xa0 && (c.treeMask&(1<<digit)) != 0

			if isLeafHash {
				leafMarkerCount++
				if !trie.IsLeafMarker(slots[digit]) {
					t.Errorf("key %x digit %d: expected LeafMarker, got %x", c.keyHex, digit, slots[digit])
				}
			} else if isBranchHash {
				hashBranchSlots++
				if !bytes.Equal(slots[digit], origBytes) {
					t.Errorf("key %x digit %d: branch slot bytes differ\n  got  %x\n  want %x",
						c.keyHex, digit, slots[digit], origBytes)
				}
			} else {
				// Inline RLP — unchanged.
				if !bytes.Equal(slots[digit], origBytes) {
					t.Errorf("key %x digit %d: inline slot bytes differ\n  got  %x\n  want %x",
						c.keyHex, digit, slots[digit], origBytes)
				}
			}
			j++
		}
	}

	t.Logf("V1 total %d B, V2 total %d B (savings %.1f%%), leaf markers %d, branch hashes %d",
		v1Total, v2Total, 100*(1-float64(v2Total)/float64(v1Total)),
		leafMarkerCount, hashBranchSlots)

	// Sanity: we should see at least SOME leaf-marker substitutions
	// (account leaves at deepest hops have HasTree=0).
	if leafMarkerCount == 0 {
		t.Error("expected at least one HasTree=0 hashed leaf slot to be substituted with LeafMarker")
	}
	if v2Total >= v1Total {
		t.Errorf("V2 not smaller than V1: v1=%d v2=%d", v1Total, v2Total)
	}
}
