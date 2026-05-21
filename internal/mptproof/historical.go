package mptproof

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/internal/historicalstate"
)

// HistoricalLeafSource wraps a base LeafSource (current plain state)
// with a historicalstate.Reader (per-block change history) to return
// leaf values "as of block N".
//
// Read path:
//   AccountValue(addr)   →  if history has a record at or before blockN,
//                           return that OldValue (= value at start of N).
//                           Otherwise fall back to base.AccountValue.
//   StorageValue(addr,slot) → same pattern via historicalstate.StorageAsOf.
//
// Scan path: iterates base; for each entry overlays the history value
// when present. SLOW — used by audit/rebuild flows, not RPC hot path.
//
// IMPORTANT: this returns OLD values (state JUST BEFORE the modification
// at block N). For "state at end of block N" semantics, query blockN+1.
// See internal/historicalstate doc for the OldValue convention.
type HistoricalLeafSource struct {
	base    LeafSource
	history *historicalstate.Reader
	blockN  uint64
}

// NewHistoricalLeafSource wraps base with history at the given block.
// Both base and history must outlive the returned source; Close on
// this source does NOT close base or history (caller's responsibility).
func NewHistoricalLeafSource(base LeafSource, history *historicalstate.Reader, blockN uint64) *HistoricalLeafSource {
	return &HistoricalLeafSource{base: base, history: history, blockN: blockN}
}

func (h *HistoricalLeafSource) Close() error { return nil }

func (h *HistoricalLeafSource) AccountValue(addr [20]byte) ([]byte, bool, error) {
	if h.history != nil {
		v, ok, err := h.history.AccountAsOf(addr, h.blockN)
		if err == nil && ok {
			return v, true, nil
		}
	}
	return h.base.AccountValue(addr)
}

func (h *HistoricalLeafSource) StorageValue(addr [20]byte, slot [32]byte) ([]byte, bool, error) {
	if h.history != nil {
		v, ok, err := h.history.StorageAsOf(addr, slot, h.blockN)
		if err == nil && ok {
			return v, true, nil
		}
	}
	return h.base.StorageValue(addr, slot)
}

// ScanAccounts iterates the base source; for each entry, overlays with
// history if available. The overlay rule: if history.AccountAsOf
// returns a value, use it; else use the base value. This means
// addresses created AFTER blockN still appear in the iteration but
// with their "didn't exist yet" representation (empty bytes from
// history) — caller should treat empty as absent.
func (h *HistoricalLeafSource) ScanAccounts(fn func(addr [20]byte, value []byte) error) error {
	return h.base.ScanAccounts(func(addr [20]byte, value []byte) error {
		v := value
		if h.history != nil {
			if hv, ok, err := h.history.AccountAsOf(addr, h.blockN); err == nil && ok {
				v = hv
			}
		}
		return fn(addr, v)
	})
}

func (h *HistoricalLeafSource) ScanStorage(fn func(addr [20]byte, slot [32]byte, value []byte) error) error {
	return h.base.ScanStorage(func(addr [20]byte, slot [32]byte, value []byte) error {
		v := value
		if h.history != nil {
			if hv, ok, err := h.history.StorageAsOf(addr, slot, h.blockN); err == nil && ok {
				v = hv
			}
		}
		return fn(addr, slot, v)
	})
}

// HistoricalProof bundles a Merkle proof against the LATEST trie with
// the historical leaf values from blockN. It does NOT (yet) provide a
// proof verifiable against block N's stateRoot — that would require
// either persisted commitment-history (rejected architecturally) or
// full trie rebuild at block N (Phase D.4.1 future work).
//
// Use case: state-as-of queries where the caller trusts the node and
// just needs the value at a specific block. The latest proof
// demonstrates the addr/slot exists IN OUR CURRENT TRIE, and the
// historical value reports what it was at block N.
type HistoricalProof struct {
	BlockN  uint64
	Address [20]byte

	// AccountValueAtBlockN: as-of value via historicalstate (OldValue
	// convention — value at START of blockN). Empty bytes mean the
	// account had no history record ≤ blockN (= didn't exist yet OR
	// no modifications in the recorded range).
	AccountValueAtBlockN []byte
	// AccountValueLatest: current value (from base LeafSource).
	AccountValueLatest []byte

	// LatestAccountProof: EIP-1186 standard proof against the current
	// trie's stateRoot (NOT block N's). The slot value embedded is
	// the LATEST value; caller substitutes AccountValueAtBlockN to
	// reason about block N's state.
	LatestAccountProof ProofBytes
	LatestStateRoot    [32]byte

	StorageProofs []HistoricalStorageProof

	// Notes explains the verification scope (filled by generator).
	Notes string
}

type HistoricalStorageProof struct {
	Slot                 [32]byte
	ValueAtBlockN        []byte
	ValueLatest          []byte
	LatestStorageProof   ProofBytes
	LatestStorageRoot    [32]byte
}

// HistoricalProofRequest captures one historical state-as-of query.
type HistoricalProofRequest struct {
	Address [20]byte
	Slots   [][32]byte
	BlockN  uint64
}

// HistoricalProof produces a historical state-as-of bundle:
//   - canonical Merkle proof against the LATEST trie (verifies on
//     any spec-compliant Ethereum verifier against the latest
//     stateRoot)
//   - PLUS historical leaf values from historicalstate (BlockN
//     OldValue convention)
//
// The latest proof + historical values together let a caller answer
// "what was X at block N" with a trust assumption (trust the node's
// historicalstate reader). For zero-trust historical proofs, full
// trie rebuild at block N is required (Phase D.4.1).
func (g *Generator) HistoricalProof(req HistoricalProofRequest) (*HistoricalProof, error) {
	if g.history == nil {
		return nil, errors.New("mptproof: HistoricalProof requires HistoryDir in Config")
	}

	res := &HistoricalProof{
		BlockN:  req.BlockN,
		Address: req.Address,
		Notes: fmt.Sprintf("LatestAccountProof verifies against current stateRoot (NOT block %d). "+
			"AccountValueAtBlockN is the historicalstate.AsOf value (trust the node's history "+
			"reader). For zero-trust proof at block %d, full trie rebuild needed.",
			req.BlockN, req.BlockN),
	}

	// Historical leaf value
	if v, ok, err := g.history.AccountAsOf(req.Address, req.BlockN); err != nil {
		return nil, fmt.Errorf("AccountAsOf: %w", err)
	} else if ok {
		res.AccountValueAtBlockN = v
	}

	// Latest leaf value
	if v, ok, err := g.leaves.AccountValue(req.Address); err != nil {
		return nil, fmt.Errorf("AccountValue (latest): %w", err)
	} else if ok {
		res.AccountValueLatest = v
	}

	// Latest Merkle proof
	latestAcct, err := g.LatestAccountProof(req.Address)
	if err != nil {
		return nil, fmt.Errorf("LatestAccountProof: %w", err)
	}
	pb, err := g.FullAccountProofBytes(latestAcct)
	if err != nil {
		return nil, fmt.Errorf("FullAccountProofBytes: %w", err)
	}
	res.LatestAccountProof = pb
	res.LatestStateRoot = latestAcct.StateRoot

	// Storage slots
	if len(req.Slots) > 0 {
		latestStor, err := g.LatestStorageProofs(req.Address, req.Slots)
		if err != nil {
			return nil, fmt.Errorf("LatestStorageProofs: %w", err)
		}
		res.StorageProofs = make([]HistoricalStorageProof, len(req.Slots))
		for i, slot := range req.Slots {
			sp := HistoricalStorageProof{
				Slot:              slot,
				LatestStorageRoot: latestStor[i].StateRoot,
			}
			if v, ok, err := g.history.StorageAsOf(req.Address, slot, req.BlockN); err != nil {
				return nil, fmt.Errorf("StorageAsOf slot %d: %w", i, err)
			} else if ok {
				sp.ValueAtBlockN = v
			}
			if v, ok, err := g.leaves.StorageValue(req.Address, slot); err != nil {
				return nil, fmt.Errorf("StorageValue (latest) slot %d: %w", i, err)
			} else if ok {
				sp.ValueLatest = v
			}
			sbytes, err := g.FullStorageProofBytes(latestStor[i])
			if err != nil {
				return nil, fmt.Errorf("FullStorageProofBytes slot %d: %w", i, err)
			}
			sp.LatestStorageProof = sbytes
			res.StorageProofs[i] = sp
		}
	}

	return res, nil
}
