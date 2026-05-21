package mpttrie

import (
	"bytes"
	"sort"
	"testing"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/trie"
)

// mockSingleLeafLookup is a synthetic SingleLeafLookup backed by an
// in-memory sorted list of (keccakKey, value) pairs — mirrors the
// interface reth's HashedAccounts (32-byte key) would expose.
type mockSingleLeafLookup struct {
	// entries sorted ascending by keccakKey
	keys   [][32]byte
	values [][]byte
}

func (m *mockSingleLeafLookup) AccountLeafByPrefix(prefix []byte) (hashedAddr [32]byte, value []byte, ok bool, err error) {
	for i, k := range m.keys {
		if hasNibblePrefix(k[:], prefix) {
			return k, m.values[i], true, nil
		}
	}
	return [32]byte{}, nil, false, nil
}
func (m *mockSingleLeafLookup) StorageLeafByPrefix(prefix []byte) (composite [64]byte, value []byte, ok bool, err error) {
	return composite, nil, false, nil
}

func hasNibblePrefix(bytes32 []byte, nibblePrefix []byte) bool {
	for i, n := range nibblePrefix {
		bIdx := i / 2
		if bIdx >= len(bytes32) {
			return false
		}
		var got byte
		if i%2 == 0 {
			got = bytes32[bIdx] >> 4
		} else {
			got = bytes32[bIdx] & 0xf
		}
		if got != n {
			return false
		}
	}
	return true
}

// TestReconstructLeafHash_ExactMatch builds a 1-leaf "branch context"
// — branchPath + child nibble selects exactly one keccak(addr) — and
// verifies reconstructLeafHash produces the same leaf-RLP hash that
// HashBuilder would have produced.
func TestReconstructLeafHash_ExactMatch(t *testing.T) {
	// One synthetic account, fixed keccak, fixed value.
	var addr [20]byte
	addr[0] = 0xab
	keccakAddr := keccak256Bytes(addr[:])
	value := []byte{0xc4, 0x80, 0x80, 0x80, 0x80} // tiny synthetic value

	mock := &mockSingleLeafLookup{
		keys:   [][32]byte{keccakAddr},
		values: [][]byte{value},
	}

	// Suppose this leaf lives at branchPath = first 3 nibbles of keccak.
	branchPath := []byte{
		keccakAddr[0] >> 4,
		keccakAddr[0] & 0xf,
		keccakAddr[1] >> 4,
	}

	// Direct compute reference: leafRLP from full suffix
	suffix := make([]byte, 0, 64-len(branchPath)+1)
	for i, b := range keccakAddr {
		hi, lo := b>>4, b&0xf
		if 2*i >= len(branchPath) {
			suffix = append(suffix, hi)
		}
		if 2*i+1 >= len(branchPath) {
			suffix = append(suffix, lo)
		}
	}
	suffix = append(suffix, 0x10)

	wantRLP := encodeLeafRLP(suffix, value)
	want := keccak256Bytes(wantRLP)

	got, err := reconstructLeafHash(branchPath, false /*isStorage*/, mock)
	if err != nil {
		t.Fatalf("reconstructLeafHash: %v", err)
	}
	if got != want {
		t.Errorf("hash mismatch\n  got  %x\n  want %x", got, want)
	}
	t.Logf("✓ reconstructed leaf hash matches direct compute: %x", got)
}

// TestV2Marshal_MultiBranchRoundTrip ensures MarshalTrieNodeDenseV2 +
// UnmarshalTrieNodeDenseV2 round-trip preserves slot bytes for an
// 8-children branch with mixed tree/leaf bits.
func TestV2Marshal_MultiBranchRoundTrip(t *testing.T) {
	// State mask 0x00FF = children at nibbles 0..7
	// Tree mask 0x0055 = nibbles 0,2,4,6 are deeper branches
	// Leaf bits = nibbles 1,3,5,7 (hashed leaves)
	const stride = 33
	state := uint16(0x00FF)
	tree := uint16(0x0055)

	// Synthesize 8 slot blocks: alternating branch-hash + leaf-hash
	// (both prefixed with 0xa0, distinguishable only by tree mask).
	slotData := make([]byte, 8*stride)
	for i := 0; i < 8; i++ {
		slotData[i*stride] = 0xa0
		for j := 0; j < 32; j++ {
			slotData[i*stride+1+j] = byte(i*100 + j)
		}
	}

	encV2 := trie.MarshalTrieNodeDenseV2(state, tree, slotData, nil)
	stateOut, treeOut, slots, err := trie.UnmarshalTrieNodeDenseV2(encV2)
	if err != nil {
		t.Fatal(err)
	}
	if stateOut != state || treeOut != tree {
		t.Errorf("masks: got state=%x tree=%x", stateOut, treeOut)
	}
	leafMarkers := 0
	branchHashes := 0
	for digit := 0; digit < 16; digit++ {
		if state&(1<<digit) == 0 {
			if slots[digit] != nil {
				t.Errorf("digit %d: unexpected slot", digit)
			}
			continue
		}
		if tree&(1<<digit) != 0 {
			// Branch — should still be 33 bytes.
			if len(slots[digit]) != 33 || slots[digit][0] != 0xa0 {
				t.Errorf("digit %d branch: got len=%d prefix=%x", digit, len(slots[digit]), slots[digit][0])
			}
			branchHashes++
		} else {
			// Leaf — should be 1-byte marker.
			if !trie.IsLeafMarker(slots[digit]) {
				t.Errorf("digit %d leaf: not a marker, got %x", digit, slots[digit])
			}
			leafMarkers++
		}
	}
	if branchHashes != 4 || leafMarkers != 4 {
		t.Errorf("expected 4 branch + 4 leaf, got %d branch / %d leaf", branchHashes, leafMarkers)
	}

	// V2 size = 4 (header) + 4 * 33 (branches) + 4 * 1 (markers) = 140
	expectedSize := 4 + 4*33 + 4
	if len(encV2) != expectedSize {
		t.Errorf("V2 size: got %d want %d", len(encV2), expectedSize)
	}
	t.Logf("✓ V2 round-trip: %d bytes (4 branch hashes + 4 leaf markers)", len(encV2))
}

// keccak256Bytes is a test helper.
func keccak256Bytes(data []byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}

// Unused imports guard
var _ = bytes.Equal
var _ = sort.Sort
