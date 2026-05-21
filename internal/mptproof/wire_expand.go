package mptproof

import (
	"bytes"
	"fmt"
	"sort"
)

// expandSubtreeProofPath emits the standard MPT proof nodes that
// descend from a sub-trie root toward a target leaf. Used when the
// walk's deepest hop stops at a slot whose stored hash is a SUB-TRIE
// root rather than the leaf hash directly — i.e. the leaf sits at
// deeper depth than the persisted branches go.
//
// Inputs:
//
//	leaves    = all leaves under the sub-trie (effective keys relative
//	            to the sub-trie root, each terminated with 0x10)
//	keyTail   = our target key's nibbles relative to the sub-trie root
//	            (no terminator; we add internally for the leaf node)
//
// Returns: proof nodes ordered root → leaf, each an RLP-encoded
// standard MPT node. Concatenated with the persisted-branch portion
// of the proof, these form a complete eth_getProof byte array that
// VerifyStandardProof accepts.
//
// Algorithm:
//
//	if 1 leaf: emit [leaf RLP], done
//	else:
//	  find common prefix among leaves
//	  if non-empty common prefix: emit extension RLP, recurse with
//	      stripped leaves and stripped keyTail
//	  else (branch at root): emit branch RLP with all 16 sibling
//	      slots encoded correctly, recurse into the slot matching
//	      our keyTail[0]
func expandSubtreeProofPath(leaves []subLeaf, keyTail []byte) (ProofBytes, error) {
	if len(leaves) == 0 {
		return nil, fmt.Errorf("expandSubtree: empty leaves")
	}
	// Make a defensive copy + sort by effective key.
	sorted := make([]subLeaf, len(leaves))
	copy(sorted, leaves)
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].effectiveKey, sorted[j].effectiveKey) < 0
	})

	if len(sorted) == 1 {
		// Single leaf — emit. Its effectiveKey already has the 0x10
		// terminator at the end.
		return ProofBytes{encodeLeafRLP(sorted[0].effectiveKey, sorted[0].value)}, nil
	}

	// Find longest common prefix among effectiveKeys (excluding the
	// 0x10 terminator at the end of each).
	common := longestCommonNibblePrefix(sorted)
	if len(common) > 0 {
		// Emit extension node: RLP([HP(common, leaf=false), child])
		// child = sub-trie under leaves with common prefix stripped.
		stripped := make([]subLeaf, len(sorted))
		for i, l := range sorted {
			stripped[i] = subLeaf{
				effectiveKey: l.effectiveKey[len(common):],
				value:        l.value,
			}
		}
		// Check our keyTail also follows the extension.
		if len(keyTail) < len(common) || !bytesEq(keyTail[:len(common)], common) {
			return nil, fmt.Errorf("expandSubtree: keyTail %x diverges from common prefix %x",
				keyTail, common)
		}
		// Recurse on the sub-trie below the extension.
		sub, err := expandSubtreeProofPath(stripped, keyTail[len(common):])
		if err != nil {
			return nil, err
		}
		// Child slot of the extension = the first sub-node's
		// encoding (inline if < 32, else 0xa0||hash).
		childEnc := sub[0]
		var childSlot []byte
		if len(childEnc) < 32 {
			childSlot = childEnc
			// inline: don't emit the child separately — it's
			// contained in the extension. Drop sub[0] and shift up.
			sub = sub[1:]
		} else {
			childSlot = wrapHashSlot(keccakOf(childEnc))
		}
		extPayload := append(encodeBytes(hexToCompact(common)), childSlot...)
		extRLP := encodeList(extPayload)
		return append(ProofBytes{extRLP}, sub...), nil
	}

	// No common prefix — branch node at the sub-trie root.
	if len(keyTail) == 0 {
		return nil, fmt.Errorf("expandSubtree: empty keyTail at branch")
	}
	ourNibble := keyTail[0]

	// Group leaves by their first nibble.
	groups := make(map[byte][]subLeaf, 16)
	for _, l := range sorted {
		if len(l.effectiveKey) == 0 {
			continue
		}
		n := l.effectiveKey[0]
		groups[n] = append(groups[n], subLeaf{
			effectiveKey: l.effectiveKey[1:],
			value:        l.value,
		})
	}

	// Recurse into our path's nibble first to get both:
	//   - childEnc (for our slot's encoding)
	//   - childTail (proof nodes below the branch for our path)
	var childTail ProofBytes
	var childEnc []byte
	if g, ok := groups[ourNibble]; ok {
		sub, err := expandSubtreeProofPath(g, keyTail[1:])
		if err != nil {
			return nil, fmt.Errorf("recurse nibble 0x%x: %w", ourNibble, err)
		}
		childEnc = sub[0]
		if len(childEnc) < 32 {
			// First sub-node is inlined into our slot, not emitted
			// separately; descendants (if any) still emit below.
			childTail = sub[1:]
		} else {
			childTail = sub
		}
	}

	// Build 17-slot branch.
	slots := [17][]byte{}
	emptyEnc := []byte{0x80}
	for i := byte(0); i < 16; i++ {
		g, ok := groups[i]
		if !ok {
			slots[i] = emptyEnc
			continue
		}
		if i == ourNibble {
			if len(childEnc) < 32 {
				slots[i] = childEnc
			} else {
				slots[i] = wrapHashSlot(keccakOf(childEnc))
			}
			continue
		}
		// Sibling slot.
		rootEnc, err := encodeSubtreeRootBytes(g)
		if err != nil {
			return nil, fmt.Errorf("sibling nibble 0x%x: %w", i, err)
		}
		if len(rootEnc) < 32 {
			slots[i] = rootEnc
		} else {
			slots[i] = wrapHashSlot(keccakOf(rootEnc))
		}
	}
	slots[16] = emptyEnc

	var payload bytes.Buffer
	for _, s := range slots {
		payload.Write(s)
	}
	branchRLP := encodeList(payload.Bytes())
	return append(ProofBytes{branchRLP}, childTail...), nil
}

// encodeSubtreeRootBytes returns the standard MPT root-node RLP for a
// set of leaves (relative effective keys). Mirrors expandSubtreeProofPath
// but does NOT recurse for proof emission — it only builds the bytes
// the parent slot needs.
func encodeSubtreeRootBytes(leaves []subLeaf) ([]byte, error) {
	if len(leaves) == 0 {
		return []byte{0x80}, nil
	}
	sorted := make([]subLeaf, len(leaves))
	copy(sorted, leaves)
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].effectiveKey, sorted[j].effectiveKey) < 0
	})

	if len(sorted) == 1 {
		return encodeLeafRLP(sorted[0].effectiveKey, sorted[0].value), nil
	}

	common := longestCommonNibblePrefix(sorted)
	if len(common) > 0 {
		stripped := make([]subLeaf, len(sorted))
		for i, l := range sorted {
			stripped[i] = subLeaf{
				effectiveKey: l.effectiveKey[len(common):],
				value:        l.value,
			}
		}
		childEnc, err := encodeSubtreeRootBytes(stripped)
		if err != nil {
			return nil, err
		}
		var childSlot []byte
		if len(childEnc) < 32 {
			childSlot = childEnc
		} else {
			childSlot = wrapHashSlot(keccakOf(childEnc))
		}
		extPayload := append(encodeBytes(hexToCompact(common)), childSlot...)
		return encodeList(extPayload), nil
	}

	// Branch at root.
	groups := make(map[byte][]subLeaf, 16)
	for _, l := range sorted {
		if len(l.effectiveKey) == 0 {
			continue
		}
		n := l.effectiveKey[0]
		groups[n] = append(groups[n], subLeaf{
			effectiveKey: l.effectiveKey[1:],
			value:        l.value,
		})
	}
	slots := [17][]byte{}
	emptyEnc := []byte{0x80}
	for i := byte(0); i < 16; i++ {
		g, ok := groups[i]
		if !ok {
			slots[i] = emptyEnc
			continue
		}
		rootEnc, err := encodeSubtreeRootBytes(g)
		if err != nil {
			return nil, err
		}
		if len(rootEnc) < 32 {
			slots[i] = rootEnc
		} else {
			slots[i] = wrapHashSlot(keccakOf(rootEnc))
		}
	}
	slots[16] = emptyEnc

	var payload bytes.Buffer
	for _, s := range slots {
		payload.Write(s)
	}
	return encodeList(payload.Bytes()), nil
}

// longestCommonNibblePrefix returns the longest nibble prefix shared
// by all effectiveKeys. The 0x10 terminator at the end of each is
// excluded (we don't want it to count as a "shared" nibble).
func longestCommonNibblePrefix(leaves []subLeaf) []byte {
	if len(leaves) == 0 {
		return nil
	}
	// Strip terminator from each for the comparison.
	stripTerm := func(b []byte) []byte {
		if len(b) > 0 && b[len(b)-1] == 0x10 {
			return b[:len(b)-1]
		}
		return b
	}
	first := stripTerm(leaves[0].effectiveKey)
	prefixLen := len(first)
	for _, l := range leaves[1:] {
		k := stripTerm(l.effectiveKey)
		if len(k) < prefixLen {
			prefixLen = len(k)
		}
		for i := 0; i < prefixLen; i++ {
			if first[i] != k[i] {
				prefixLen = i
				break
			}
		}
		if prefixLen == 0 {
			return nil
		}
	}
	return append([]byte{}, first[:prefixLen]...)
}
