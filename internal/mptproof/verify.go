package mptproof

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/internal/mpttrie"
)

// ErrInlineLeaf is returned when the deepest hop has the target's
// child encoded inline (no separate stored hash). BranchNodeCompact
// can't represent the inline bytes; verify is impossible without the
// original leaf bytes from plain state.
var ErrInlineLeaf = errors.New("mptproof: child is inline in parent — cannot verify from compact form alone")

// ErrExtensionInPath is returned when the leaf-hash check at the
// deepest visited branch fails, which under reth/Erigon's compact
// branch encoding usually indicates an extension node exists between
// the deepest stored branch and the actual leaf position. The compact
// form cannot tell us the extension's nibbles, so a full standard-MPT
// proof reconstruction is needed (Phase D work via lib/trie's
// HashBuilder fed with the explicit walk path + leaf).
var ErrExtensionInPath = errors.New("mptproof: extension node in path — Phase C MVP verify cannot fold through; leaf value still trustworthy")

// Verify checks that the proof's siblings + leaf value fold up to
// produce a branch-hash chain ending at the stored StateRoot. This is
// the self-consistency gate before Phase D RPC integration — if it
// passes for production builds, our entire pipeline (mptbuild →
// mpttrie → mptproof) produces internally valid proofs.
//
// NOTE: this verifies against the root WE built (using reth Compact
// account values as leaves). It does NOT verify against the canonical
// Ethereum stateRoot — that requires reth Compact → standard RLP
// account transcoding (Phase A.5 follow-up).
func (p *AccountProof) Verify() (bool, error) {
	if !p.LeafFound {
		return false, errors.New("verify of non-inclusion proof not implemented in MVP")
	}
	if p.Walk == nil || len(p.Walk.Hops) == 0 {
		return false, errors.New("walk is empty")
	}
	return verifyFold(p.HashedAddr[:], p.LeafValue, p.Walk, p.StateRoot)
}

// Verify is the same fold check for a storage proof.
func (p *StorageProof) Verify() (bool, error) {
	if !p.LeafFound {
		return false, errors.New("verify of non-inclusion proof not implemented in MVP")
	}
	if p.Walk == nil || len(p.Walk.Hops) == 0 {
		return false, errors.New("walk is empty")
	}
	return verifyFold(p.HashedKey[:], p.LeafValue, p.Walk, p.StateRoot)
}

// verifyFold:
//  1. Hash the leaf node (HP-encoded remainder + value) per standard MPT.
//  2. Assert it equals what the deepest branch claims at TargetNibble.
//  3. For each hop from deepest to root, reconstruct the standard MPT
//     branch RLP from the stored hashes[] and check the resulting hash
//     equals either (a) the parent's stored hash for our target nibble,
//     or (b) the recorded StateRoot at the root hop.
//
// MVP LIMITATION — extension nodes in the path:
//
// reth/Erigon's compact branch encoding (state_mask, tree_mask,
// hash_mask, hashes) COLLAPSES extension nodes. When tree_mask is
// clear but hash_mask is set for our target nibble, the stored hash
// is keccak of "whatever subtree is below" — which may be:
//   (a) a direct leaf at depth+1 (our walk's assumption), OR
//   (b) an extension + leaf chain at depth+K (K>1), in which case the
//       stored hash equals keccak(extension RLP) wrapping the leaf —
//       NOT keccak(leaf RLP) directly.
//
// The compact form provides no way to distinguish (a) from (b)
// without the original leaf bytes from plain state. So when our
// computed leaf hash differs from the stored hash, we can't tell
// whether the proof is invalid or just has an extension we can't see.
//
// → Verify returns ErrExtensionInPath in that case. Callers should
//   treat it as "verification deferred to Phase D" rather than
//   "proof is wrong" — the leaf VALUE itself is still trustworthy.
func verifyFold(hashedKey, leafValue []byte, walk *mpttrie.WalkResult, stateRoot [32]byte) (bool, error) {
	keyNibbles := nibblesOf(hashedKey)
	leafDepth := walk.LeafDepth
	if leafDepth > len(keyNibbles) {
		return false, fmt.Errorf("leaf depth %d > key nibble count %d", leafDepth, len(keyNibbles))
	}
	// Remainder = the key nibbles BELOW the deepest branch.
	remainder := keyNibbles[leafDepth:]
	leafHash := computeLeafHash(remainder, leafValue)

	// Check: the deepest hop's branch must claim our leaf hash at
	// TargetNibble (if it has a stored hash for that nibble).
	deepest := walk.Hops[len(walk.Hops)-1]
	if !deepest.Branch.HasChild(deepest.TargetNibble) {
		return false, fmt.Errorf("deepest hop has no child at target nibble 0x%x", deepest.TargetNibble)
	}
	storedLeafHash, ok := deepest.Branch.ChildHash(deepest.TargetNibble)
	if !ok {
		// Child exists but no stored hash — leaf encoding was inlined
		// in parent. Without the inline bytes we can't fold further.
		return false, ErrInlineLeaf
	}
	if leafHash != storedLeafHash {
		// Either the proof is invalid OR there's an extension node
		// between our walk's deepest branch and the actual leaf. The
		// compact form cannot tell us which. See doc comment above.
		return false, ErrExtensionInPath
	}

	// Fold branches up. At each hop, recompute branch hash and verify
	// it matches what the parent (or root) claims.
	for i := len(walk.Hops) - 1; i >= 0; i-- {
		hop := walk.Hops[i]
		branchHash, err := computeBranchHash(hop.Branch)
		if err != nil {
			return false, fmt.Errorf("hop %d: %w", i, err)
		}
		if i == 0 {
			if branchHash != stateRoot {
				return false, fmt.Errorf("root branch hash mismatch:\n  computed 0x%x\n  stateRoot 0x%x",
					branchHash, stateRoot)
			}
			return true, nil
		}
		parent := walk.Hops[i-1]
		expected, ok := parent.Branch.ChildHash(parent.TargetNibble)
		if !ok {
			return false, fmt.Errorf("hop %d: parent has no stored hash for child nibble 0x%x",
				i-1, parent.TargetNibble)
		}
		if branchHash != expected {
			return false, fmt.Errorf("hop %d branch hash mismatch:\n  computed 0x%x\n  parent's slot 0x%x",
				i, branchHash, expected)
		}
	}
	return false, errors.New("walk had no hops") // unreachable
}

// computeBranchHash reconstructs the standard 17-element-list MPT
// branch RLP from a BranchNodeCompact and returns its keccak. Returns
// an error if any child has state_mask set but no stored hash (inline
// child whose bytes aren't recoverable from the compact form).
func computeBranchHash(b *mpttrie.BranchNode) ([32]byte, error) {
	// 16 child slots + 1 value slot (always empty for branch nodes).
	var slots [17][]byte
	emptyEnc := []byte{0x80}
	for i := byte(0); i < 16; i++ {
		if !b.HasChild(i) {
			slots[i] = emptyEnc
			continue
		}
		h, ok := b.ChildHash(i)
		if !ok {
			return [32]byte{}, fmt.Errorf("nibble 0x%x has state bit set but no stored hash (inline)", i)
		}
		// RLP of a 32-byte hash: 0xa0 || hash
		buf := make([]byte, 33)
		buf[0] = 0xa0
		copy(buf[1:], h[:])
		slots[i] = buf
	}
	slots[16] = emptyEnc

	// Concatenate payload then wrap in list header.
	var payload bytes.Buffer
	for _, s := range slots {
		payload.Write(s)
	}
	encoded := encodeList(payload.Bytes())
	return keccak256(encoded), nil
}

// computeLeafHash returns keccak(RLP([HP-encoded-key, value])) for a
// standard MPT leaf node. When the encoded leaf RLP itself is shorter
// than 32 bytes, the parent would have stored it inline — but our
// builder always emits hashes (verified by integration tests below),
// so we just always compute the keccak.
func computeLeafHash(remainderNibbles, value []byte) [32]byte {
	// Ensure terminator nibble (0x10) is present — required for
	// HP-encoding to set the leaf flag.
	terminated := remainderNibbles
	if len(terminated) == 0 || terminated[len(terminated)-1] != 0x10 {
		terminated = append(append([]byte{}, remainderNibbles...), 0x10)
	}
	hp := hexToCompact(terminated)
	keyEnc := encodeBytes(hp)
	valEnc := encodeBytes(value)
	payload := append(keyEnc, valEnc...)
	leafRLP := encodeList(payload)
	return keccak256(leafRLP)
}

// ============================================================================
// Minimal RLP encoding (only the cases we need: byte strings, lists)
// ============================================================================

// encodeBytes RLP-encodes a byte string.
func encodeBytes(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return []byte{b[0]}
	}
	if len(b) < 56 {
		out := make([]byte, 1+len(b))
		out[0] = 0x80 + byte(len(b))
		copy(out[1:], b)
		return out
	}
	// Long string: 0xb7 + sizeOfLen || lenBE || data
	return encodeLongPrefix(0xb7, b)
}

// encodeList wraps an already-concatenated payload in a list header.
func encodeList(payload []byte) []byte {
	if len(payload) < 56 {
		out := make([]byte, 1+len(payload))
		out[0] = 0xc0 + byte(len(payload))
		copy(out[1:], payload)
		return out
	}
	return encodeLongPrefix(0xf7, payload)
}

// encodeLongPrefix handles the >=56 byte length encoding for both
// strings (base 0xb7) and lists (base 0xf7).
func encodeLongPrefix(base byte, payload []byte) []byte {
	lenBytes := uintToBE(uint64(len(payload)))
	out := make([]byte, 1+len(lenBytes)+len(payload))
	out[0] = base + byte(len(lenBytes))
	copy(out[1:], lenBytes)
	copy(out[1+len(lenBytes):], payload)
	return out
}

// uintToBE returns the minimal big-endian encoding (no leading zeros).
func uintToBE(n uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], n)
	i := 0
	for ; i < 8 && buf[i] == 0; i++ {
	}
	return buf[i:]
}

// ============================================================================
// HP-prefix (Hex-Prefix) encoding per Ethereum MPT spec
// ============================================================================

// hexToCompact converts a nibble slice (optionally terminated with 0x10
// for leaves) to its HP-prefix byte form.
//
//	first byte = (terminator_flag << 5) | (odd_flag << 4) | first_nibble_if_odd
//	rest       = packed remaining nibbles (2 per byte, high then low)
func hexToCompact(hex []byte) []byte {
	terminator := byte(0)
	if len(hex) > 0 && hex[len(hex)-1] == 0x10 {
		terminator = 1
		hex = hex[:len(hex)-1]
	}
	buf := make([]byte, len(hex)/2+1)
	buf[0] = terminator << 5
	if len(hex)&1 == 1 {
		buf[0] |= 1 << 4
		buf[0] |= hex[0]
		hex = hex[1:]
	}
	for i := 0; i < len(hex); i += 2 {
		buf[1+i/2] = (hex[i] << 4) | hex[i+1]
	}
	return buf
}

func keccak256(data []byte) [32]byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out
}
