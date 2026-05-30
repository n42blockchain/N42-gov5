package stateless

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// HeaderChain is the minimal client's trust backbone. A minimal node trusts ONE
// anchor (a checkpoint header it accepts out-of-band: hard-coded, social
// consensus, or a PoS-finalized header). From that anchor it follows the
// parentHash hash-chain forward: header[i].ParentHash == hash(header[i-1]) and
// number[i] == number[i-1]+1. Each verified header's Root (stateRoot) and
// ReceiptHash thereby become trusted verification targets — so the EVM-execution
// check (receipts) and the MPT-stateless check (state root) for any block in the
// verified range compare against a value whose authenticity traces to the anchor.
//
// The chain is cheap (~600 B/header, ~30 MB for a week of 12 s blocks) and its
// verification is just hashing, so a minimal node keeps a rolling week of
// headers and re-anchors as the trusted checkpoint advances.
//
// HeaderChain holds only the trusted (number -> stateRoot/receiptRoot/hash)
// projection needed downstream; it does not retain full headers.
type HeaderChain struct {
	anchorNum  uint64
	anchorHash types.Hash
	headNum    uint64
	headHash   types.Hash

	// trusted per-block targets, indexed by block number.
	stateRoot   map[uint64]types.Hash
	receiptRoot map[uint64]types.Hash
	hash        map[uint64]types.Hash
}

// NewHeaderChain starts a chain trusting the given anchor header. The anchor's
// own stateRoot/receiptRoot are recorded as trusted (the caller vouches for the
// anchor out-of-band).
func NewHeaderChain(anchor *block.Header) (*HeaderChain, error) {
	if anchor == nil || anchor.Number == nil {
		return nil, fmt.Errorf("headerchain: nil anchor or anchor.Number")
	}
	n := anchor.Number.Uint64()
	h := anchor.Hash()
	hc := &HeaderChain{
		anchorNum:   n,
		anchorHash:  h,
		headNum:     n,
		headHash:    h,
		stateRoot:   map[uint64]types.Hash{n: anchor.Root},
		receiptRoot: map[uint64]types.Hash{n: anchor.ReceiptHash},
		hash:        map[uint64]types.Hash{n: h},
	}
	return hc, nil
}

// Extend verifies and appends a single header that must be the direct child of
// the current head: number == head+1 and ParentHash == headHash. On success the
// new header's stateRoot/receiptRoot/hash become trusted and head advances.
func (hc *HeaderChain) Extend(h *block.Header) error {
	if h == nil || h.Number == nil {
		return fmt.Errorf("headerchain: nil header or Number")
	}
	n := h.Number.Uint64()
	if n != hc.headNum+1 {
		return fmt.Errorf("headerchain: non-contiguous number: got %d, want %d", n, hc.headNum+1)
	}
	if h.ParentHash != hc.headHash {
		return fmt.Errorf("headerchain: broken chain at %d: parentHash %x != head %x",
			n, h.ParentHash[:8], hc.headHash[:8])
	}
	hh := h.Hash()
	hc.headNum = n
	hc.headHash = hh
	hc.stateRoot[n] = h.Root
	hc.receiptRoot[n] = h.ReceiptHash
	hc.hash[n] = hh
	return nil
}

// ExtendBatch verifies a contiguous slice of headers (ascending by number)
// starting at head+1. It stops at the first invalid header, returning how many
// were accepted and the error. All-or-nothing is the caller's choice; this
// reports partial progress so a sync loop can retry from the failure point.
func (hc *HeaderChain) ExtendBatch(headers []*block.Header) (accepted int, err error) {
	for _, h := range headers {
		if e := hc.Extend(h); e != nil {
			return accepted, e
		}
		accepted++
	}
	return accepted, nil
}

// TrustedStateRoot returns the verified stateRoot for block n, or (zero,false)
// if n is outside the verified [anchor, head] range.
func (hc *HeaderChain) TrustedStateRoot(n uint64) (types.Hash, bool) {
	r, ok := hc.stateRoot[n]
	return r, ok
}

// TrustedReceiptRoot returns the verified receiptsRoot for block n.
func (hc *HeaderChain) TrustedReceiptRoot(n uint64) (types.Hash, bool) {
	r, ok := hc.receiptRoot[n]
	return r, ok
}

// TrustedHash returns the verified block hash for block n.
func (hc *HeaderChain) TrustedHash(n uint64) (types.Hash, bool) {
	h, ok := hc.hash[n]
	return h, ok
}

// Head returns the current verified head (number, hash).
func (hc *HeaderChain) Head() (uint64, types.Hash) { return hc.headNum, hc.headHash }

// Anchor returns the trusted anchor (number, hash).
func (hc *HeaderChain) Anchor() (uint64, types.Hash) { return hc.anchorNum, hc.anchorHash }

// Prune drops trusted targets for blocks below keepFrom, bounding memory to a
// rolling window (e.g. one week). The anchor projection is kept regardless so
// re-anchoring stays possible. keepFrom <= anchor is a no-op.
func (hc *HeaderChain) Prune(keepFrom uint64) {
	for n := range hc.stateRoot {
		if n < keepFrom && n != hc.anchorNum {
			delete(hc.stateRoot, n)
			delete(hc.receiptRoot, n)
			delete(hc.hash, n)
		}
	}
}
