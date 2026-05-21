package mptbuild

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/trie"
)

type capturedDense struct {
	keyHex    []byte
	stateMask uint16
	treeMask  uint16
	slotData  []byte
}

// TestDenseBranchSink_Synthetic verifies that the dense slot data we
// capture from HashBuilder, when fed through MarshalTrieNodeDense +
// UnmarshalTrieNodeDense, can reconstruct each branch's RLP encoding
// such that keccak(RLP) matches the StateRoot HashBuilder computed.
//
// This is the round-trip correctness test for Phase G1's dense form.
func TestDenseBranchSink_Synthetic(t *testing.T) {
	entries := makeAccountEntries(100)

	var captured []capturedDense
	tgt := &MapTarget{}
	res, err := Build(context.Background(), Opts{
		Source:    &MapSource{Entries: entries},
		Target:    tgt,
		Extractor: NewAccountExtractor(),
		TmpDir:    filepath.Join(t.TempDir(), "etl"),
		BufMB:     1,
		DenseBranchSink: func(keyHex []byte, stateMask, treeMask uint16, slotData []byte) error {
			c := capturedDense{
				keyHex:    append([]byte(nil), keyHex...),
				stateMask: stateMask,
				treeMask:  treeMask,
				slotData:  append([]byte(nil), slotData...),
			}
			captured = append(captured, c)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(captured) == 0 {
		t.Fatal("DenseBranchSink never invoked")
	}
	if int64(len(captured)) != res.Branches {
		t.Errorf("captured %d, Result.Branches %d (should be 1:1)", len(captured), res.Branches)
	}

	// Each captured branch: marshal dense → unmarshal → reconstruct
	// branch RLP from slot bytes → keccak → compare with what the
	// PARENT of this branch stored as the child reference (we can't
	// directly compare without parent context; instead we verify the
	// inverse — marshal-then-unmarshal is a faithful round-trip).
	var (
		hashedBranches int
		inlineBranches int
	)
	for i, c := range captured {
		encoded := trie.MarshalTrieNodeDense(c.stateMask, c.treeMask, c.slotData, nil)
		stateOut, treeOut, slots, err := trie.UnmarshalTrieNodeDense(encoded)
		if err != nil {
			t.Errorf("branch %d (key=%x) unmarshal: %v", i, c.keyHex, err)
			continue
		}
		if stateOut != c.stateMask {
			t.Errorf("branch %d: stateMask round-trip got %x want %x", i, stateOut, c.stateMask)
		}
		if treeOut != c.treeMask {
			t.Errorf("branch %d: treeMask round-trip got %x want %x", i, treeOut, c.treeMask)
		}

		// Each slot's bytes must match the original hashStack frame's
		// first-N bytes where N = slotLen(prefix).
		const stride = 33
		j := 0
		for digit := 0; digit < 16; digit++ {
			if c.stateMask&(1<<digit) == 0 {
				if slots[digit] != nil {
					t.Errorf("branch %d digit %d: unexpected non-nil slot", i, digit)
				}
				continue
			}
			origPrefix := c.slotData[j*stride]
			n := slotLen(origPrefix)
			orig := c.slotData[j*stride : j*stride+n]
			if !bytes.Equal(slots[digit], orig) {
				t.Errorf("branch %d digit %d: slot bytes differ\n  got  %x\n  want %x",
					i, digit, slots[digit], orig)
			}
			if origPrefix == 0xa0 {
				hashedBranches++
			} else {
				inlineBranches++
			}
			j++
		}
	}
	t.Logf("captured %d branches, %d hashed-child slots, %d inline-child slots",
		len(captured), hashedBranches, inlineBranches)

	// Reconstruct the ROOT branch's RLP from slots and verify
	// keccak == res.StateRoot. Find the root branch (key length 0).
	var root *capturedDense
	for i := range captured {
		if len(captured[i].keyHex) == 0 {
			root = &captured[i]
		}
	}
	if root == nil {
		t.Fatal("no root-level branch captured (empty keyHex)")
	}
	_, _, rootSlots, err := trie.UnmarshalTrieNodeDense(
		trie.MarshalTrieNodeDense(root.stateMask, root.treeMask, root.slotData, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	rlpBranch := encodeBranchRLP(rootSlots, root.stateMask)
	rootHash := keccak256(rlpBranch)
	if rootHash != res.StateRoot {
		t.Errorf("dense-reconstructed root mismatch\n  got  %x\n  want %x",
			rootHash, res.StateRoot)
	} else {
		t.Logf("✓ dense → RLP → keccak == StateRoot (%x)", rootHash[:8])
	}
}

// slotLen mirrors lib/trie's internal helper.
func slotLen(b0 byte) int {
	if b0 == 0xa0 {
		return 33
	}
	if b0 >= 0xc0 && b0 <= 0xfe {
		return int(b0-0xc0) + 1
	}
	if b0 == 0x80 {
		return 1
	}
	if b0 >= 0x81 && b0 <= 0xb7 {
		return int(b0-0x80) + 1
	}
	return 1
}

// encodeBranchRLP reproduces the standard MPT branch RLP from 16
// pre-encoded slot bytes (each already in its parent-facing form:
// either 33-byte 0xa0||hash or inline RLP) plus the empty value slot.
func encodeBranchRLP(slots [16][]byte, stateMask uint16) []byte {
	// Compute payload length.
	payloadLen := 0
	for digit := 0; digit < 16; digit++ {
		if stateMask&(1<<digit) == 0 {
			payloadLen++ // 0x80 = empty
			continue
		}
		payloadLen += len(slots[digit])
	}
	payloadLen++ // trailing 0x80 for the (always-empty) value slot

	var out []byte
	if payloadLen < 56 {
		out = make([]byte, 1, 1+payloadLen)
		out[0] = 0xc0 + byte(payloadLen)
	} else {
		// Long list: 0xf7 + sizeOfLength prefix + length bytes + payload
		lenBytes := encodeLen(payloadLen)
		out = make([]byte, 1+len(lenBytes), 1+len(lenBytes)+payloadLen)
		out[0] = 0xf7 + byte(len(lenBytes))
		copy(out[1:], lenBytes)
	}
	for digit := 0; digit < 16; digit++ {
		if stateMask&(1<<digit) == 0 {
			out = append(out, 0x80)
			continue
		}
		out = append(out, slots[digit]...)
	}
	out = append(out, 0x80) // value slot (always empty for our branches)
	return out
}

func encodeLen(n int) []byte {
	switch {
	case n <= 0xff:
		return []byte{byte(n)}
	case n <= 0xffff:
		return []byte{byte(n >> 8), byte(n)}
	case n <= 0xffffff:
		return []byte{byte(n >> 16), byte(n >> 8), byte(n)}
	default:
		return []byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}
	}
}

func keccak256(data []byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
