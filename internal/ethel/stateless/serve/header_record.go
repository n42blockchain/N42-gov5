package serve

import (
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// HeaderRecord is the compact header wire used when the producer's header source
// is the columnar headerc — which is LOSSY: it drops ParentHash and Bloom and
// keeps only the canonical hash (block.Header.SetHash). A client therefore cannot
// re-derive keccak256(rlp(header)) from served fields; instead it chains on the
// producer's authoritative stored hashes. This is a "trusted header source"
// posture (the checkpoint + header hashes are vouched for by the IDC / social
// consensus); layers ② (witness replay → receiptRoot) and ③ (MPT anchor →
// stateRoot) remain the independent execution/state checks.
//
// Wire (136 B): hash(32) || parentHash(32) || number(8 LE) || stateRoot(32) ||
// receiptRoot(32). These are exactly the fields HeaderChain + the ②③ targets use.
// When a Bloom-preserving header source is available, use HeaderToRLP instead for
// a fully self-verifying (recomputable-hash) wire.
const headerRecordLen = 32 + 32 + 8 + 32 + 32

// EncodeHeaderRecord serializes h with an explicit parentHash (the producer
// reconstructs it as the stored hash of block n-1, since headerc dropped it).
func EncodeHeaderRecord(h *block.Header, parentHash types.Hash) []byte {
	out := make([]byte, headerRecordLen)
	hh := h.Hash()
	copy(out[0:32], hh[:])
	copy(out[32:64], parentHash[:])
	binary.LittleEndian.PutUint64(out[64:72], h.Number.Uint64())
	copy(out[72:104], h.Root[:])
	copy(out[104:136], h.ReceiptHash[:])
	return out
}

// DecodeHeaderRecord rebuilds a minimal *block.Header (Number, ParentHash, Root,
// ReceiptHash) and pins Hash() to the carried authoritative hash via SetHash.
func DecodeHeaderRecord(b []byte) (*block.Header, error) {
	if len(b) != headerRecordLen {
		return nil, fmt.Errorf("serve: header record %d bytes, want %d", len(b), headerRecordLen)
	}
	var hh, parent, root, rcpt types.Hash
	copy(hh[:], b[0:32])
	copy(parent[:], b[32:64])
	num := binary.LittleEndian.Uint64(b[64:72])
	copy(root[:], b[72:104])
	copy(rcpt[:], b[104:136])
	h := &block.Header{
		ParentHash:  parent,
		Number:      uint256.NewInt(num),
		Root:        root,
		ReceiptHash: rcpt,
	}
	h.SetHash(hh)
	return h, nil
}
