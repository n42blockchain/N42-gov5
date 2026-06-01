package stateless

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// Source is the transport-agnostic data a minimal client pulls (a producer RPC,
// a P2P peer, or a local cache — see docs/ethel/idc-stateless-node-architecture.md).
// This interface covers the header-chain (①) + MPT-anchor (③) orchestration; the
// per-block witness/body for layer ② is verified via the caller's VerifyBlockFull
// wiring (kept out of this interface so the rolling/retention logic is testable
// without an EVM).
type Source interface {
	Head() (uint64, error)
	Header(n uint64) (*block.Header, error)
	Anchor(n uint64) (*BlockProof, error) // valid only at anchor heights
}

// AnchorLister is an OPTIONAL Source capability: report the producer's ACTUAL
// anchor block heights in [from,to] (ascending). Under a variable cadence (coarse
// historical K + fine recent K) the client cannot derive anchor heights from a
// single K, and skipped/failed anchors are simply absent — so it learns the real
// set from the producer (the anchorc.blocks sidecar). A Source that does NOT
// implement this falls back to the fixed anchorEvery passed to NewMinimalClient.
type AnchorLister interface {
	AnchorHeights(from, to uint64) ([]uint64, error)
}

// MinimalClient follows the tip with a rolling retention window. It extends the
// trusted HeaderChain (① parentHash), verifies an MPT anchor every K blocks (③),
// prunes data older than RetentionBlocks (default a week of 12 s blocks), and
// RE-ANCHORS the trust root forward to the latest ③-verified anchor so neither
// trust nor memory depends on pruned data. Drive Sync on a 12 s ticker for live
// following; call it once for fast catch-up to the tip.
type MinimalClient struct {
	src             Source
	hc              *HeaderChain
	retentionBlocks uint64
	anchorEvery     uint64

	headers      map[uint64]*block.Header // retained window headers (for re-anchor)
	latestAnchor uint64                   // most recent ③-verified anchor block
	anchorKnown  bool

	reanchors uint64 // count of trust-root advances (observability)

	// ExecVerify, if set, is the layer-② check run per block after the header is
	// extended (① ) and before the anchor (③): the caller (in internal/ethel,
	// which can't be imported here without a cycle) fetches the block's
	// witness/body/code and replays it through the EVM, checking receiptRoot
	// against the now-trusted header. `ancestor` yields trusted ancestor hashes
	// for the BLOCKHASH opcode. A non-nil error stops the sync.
	ExecVerify func(n uint64, h *block.Header, ancestor func(uint64) types.Hash) error
}

// NewMinimalClient trusts `anchor` (an out-of-band checkpoint header, ideally
// recent) and follows `src`. retentionBlocks ~= 50_400 for a week at 12 s;
// anchorEvery is the producer's MPT cadence (K).
func NewMinimalClient(src Source, anchor *block.Header, retentionBlocks, anchorEvery uint64) (*MinimalClient, error) {
	hc, err := NewHeaderChain(anchor)
	if err != nil {
		return nil, err
	}
	c := &MinimalClient{
		src:             src,
		hc:              hc,
		retentionBlocks: retentionBlocks,
		anchorEvery:     anchorEvery,
		headers:         map[uint64]*block.Header{},
	}
	c.headers[anchor.Number.Uint64()] = anchor
	return c, nil
}

// Sync pulls from head+1 to the source tip: extend (①), verify each anchor (③),
// and prune/re-anchor the rolling window. Returns the new trusted head number.
// On any verification failure it stops and returns the error (the chain stays at
// the last good block).
func (c *MinimalClient) Sync() (uint64, error) {
	tip, err := c.src.Head()
	if err != nil {
		return 0, err
	}
	head, _ := c.hc.Head()

	// Learn the producer's ACTUAL anchor heights for this range when the source
	// supports it (variable cadence): the fixed n%anchorEvery==0 rule is wrong when
	// historical (K=10000) and recent (K=1000) cadences differ, and skipped anchors
	// are absent. Falls back to anchorEvery when the source can't list them.
	var anchorSet map[uint64]bool
	if al, ok := c.src.(AnchorLister); ok && tip >= head+1 {
		hs, herr := al.AnchorHeights(head+1, tip)
		if herr != nil {
			return c.headNum(), fmt.Errorf("anchor heights %d..%d: %w", head+1, tip, herr)
		}
		anchorSet = make(map[uint64]bool, len(hs))
		for _, a := range hs {
			anchorSet[a] = true
		}
	}

	for n := head + 1; n <= tip; n++ {
		h, herr := c.src.Header(n)
		if herr != nil {
			return c.headNum(), fmt.Errorf("fetch header %d: %w", n, herr)
		}
		if err := c.hc.Extend(h); err != nil { // ① parentHash chain
			return c.headNum(), fmt.Errorf("extend %d: %w", n, err)
		}
		c.headers[n] = h

		if c.ExecVerify != nil { // ② witness replay → receiptRoot
			if err := c.ExecVerify(n, h, c.ancestorHash); err != nil {
				return c.headNum(), fmt.Errorf("block %d layer②: %w", n, err)
			}
		}

		isAnchor := false
		if anchorSet != nil {
			isAnchor = anchorSet[n] // variable cadence: only producer-confirmed anchors
		} else {
			isAnchor = c.anchorEvery > 0 && n%c.anchorEvery == 0 // fixed-cadence fallback
		}
		if isAnchor {
			bp, aerr := c.src.Anchor(n)
			if aerr != nil {
				return c.headNum(), fmt.Errorf("fetch anchor %d: %w", n, aerr)
			}
			if bp == nil || bp.Number != n {
				return c.headNum(), fmt.Errorf("anchor %d: missing/mismatched proof", n)
			}
			if err := VerifyAgainstChain(c.hc, bp); err != nil { // ③ MPT state root
				return c.headNum(), fmt.Errorf("anchor %d layer③: %w", n, err)
			}
			c.latestAnchor = n
			c.anchorKnown = true
		}
		c.maybePrune()
	}
	return c.headNum(), nil
}

func (c *MinimalClient) headNum() uint64 { n, _ := c.hc.Head(); return n }

// ancestorHash yields the trusted hash of block m (for the BLOCKHASH opcode in
// the layer-② replay), or the zero hash if outside the retained window.
func (c *MinimalClient) ancestorHash(m uint64) types.Hash {
	h, _ := c.hc.TrustedHash(m)
	return h
}

// maybePrune enforces the rolling window: once the trust root lags the head by
// more than retentionBlocks, re-anchor to the latest ③-verified anchor that is
// still inside the window (justified: its stateRoot was independently verified)
// and drop everything before it. If no such anchor exists yet, prune only the
// projection (keeping the original anchor for provenance).
func (c *MinimalClient) maybePrune() {
	if c.retentionBlocks == 0 {
		return
	}
	head := c.headNum()
	anchorNum, _ := c.hc.Anchor()
	if head-anchorNum <= c.retentionBlocks {
		return
	}
	keepFrom := head - c.retentionBlocks

	if c.anchorKnown && c.latestAnchor > anchorNum && c.latestAnchor >= keepFrom {
		if c.reanchorTo(c.latestAnchor, head) {
			return
		}
	}
	// Fallback: projection-only prune (HeaderChain keeps the original anchor).
	c.hc.Prune(keepFrom)
	for n := range c.headers {
		if n < keepFrom && n != anchorNum {
			delete(c.headers, n)
		}
	}
}

// reanchorTo rebuilds the trusted HeaderChain rooted at the ③-verified anchor
// block A (re-extending the retained [A+1..head] headers) and drops headers
// before A. Returns false if the retained window can't support it (caller falls
// back to projection-only prune).
func (c *MinimalClient) reanchorTo(a, head uint64) bool {
	ah := c.headers[a]
	if ah == nil {
		return false
	}
	nhc, err := NewHeaderChain(ah)
	if err != nil {
		return false
	}
	for n := a + 1; n <= head; n++ {
		hh := c.headers[n]
		if hh == nil {
			return false
		}
		if e := nhc.Extend(hh); e != nil {
			return false
		}
	}
	c.hc = nhc
	for n := range c.headers {
		if n < a {
			delete(c.headers, n)
		}
	}
	c.reanchors++
	return true
}

// Head returns the trusted head number/hash.
func (c *MinimalClient) Head() (uint64, types.Hash) { return c.hc.Head() }

// TrustRoot returns the current trusted anchor (number, hash) — advances forward
// on re-anchor.
func (c *MinimalClient) TrustRoot() (uint64, types.Hash) { return c.hc.Anchor() }

// TrustedStateRoot returns the verified stateRoot for block n (within the
// retained window), or (zero,false) if n is outside it. A mobile client uses
// this to cross-check a per-account EIP-1186 proof's root against the header
// chain (layer ③ on demand, instead of the full-window multiproof).
func (c *MinimalClient) TrustedStateRoot(n uint64) (types.Hash, bool) {
	return c.hc.TrustedStateRoot(n)
}

// Source returns the underlying transport (e.g. to fetch per-account proofs).
func (c *MinimalClient) Source() Source { return c.src }

// TrustedHash returns the verified block hash for block n (BLOCKHASH ancestor for
// layer-② replay), or (zero,false) outside the retained window.
func (c *MinimalClient) TrustedHash(n uint64) (types.Hash, bool) { return c.hc.TrustedHash(n) }

// TrustedReceiptRoot returns the verified receiptsRoot for block n — the ② target
// a witness replay must reproduce.
func (c *MinimalClient) TrustedReceiptRoot(n uint64) (types.Hash, bool) {
	return c.hc.TrustedReceiptRoot(n)
}

// Retained returns how many window headers are held (bounded by RetentionBlocks).
func (c *MinimalClient) Retained() int { return len(c.headers) }

// ReanchorCount returns how many times the trust root advanced.
func (c *MinimalClient) ReanchorCount() uint64 { return c.reanchors }
