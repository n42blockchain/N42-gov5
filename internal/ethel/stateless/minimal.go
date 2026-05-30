package stateless

import (
	"crypto/ecdsa"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// MinimalVerifier is the orchestrator for an eth-el minimal node. It ties the
// trust layers into one object:
//
//	① HeaderChain  — anchor + parentHash chain → trusted per-block stateRoot/
//	                 receiptRoot (the verification targets).
//	② receipts     — EVM-execution check (engine_state_adapter verifies
//	                 receiptRoot vs header; stateless EVM uses the witness).
//	③ state root   — VerifyBatch over BlockProofs recomputes each block's root
//	                 from a multiproof and checks it against the chain.
//
// After verifying a block it can emit a signed Attestation to broadcast to an
// aggregator, accumulating distinct-verifier counts.
//
// VerifyBlock/VerifyWindow/AttestVerified are read-only on the HeaderChain and
// safe to call concurrently. ExtendHeaders mutates the chain and takes the lock;
// the caller (sync loop) serializes extension.
type MinimalVerifier struct {
	mu  sync.Mutex
	hc  *HeaderChain
	key *ecdsa.PrivateKey // optional; nil → verify-only, no attestations
}

// NewMinimalVerifier starts a verifier trusting anchor. key may be nil for a
// verify-only node (no attestation signing).
func NewMinimalVerifier(anchor *block.Header, key *ecdsa.PrivateKey) (*MinimalVerifier, error) {
	hc, err := NewHeaderChain(anchor)
	if err != nil {
		return nil, err
	}
	return &MinimalVerifier{hc: hc, key: key}, nil
}

// ExtendHeaders advances the trusted header chain with a contiguous run of
// headers (number ascending, starting at head+1). Returns accepted count.
func (m *MinimalVerifier) ExtendHeaders(headers []*block.Header) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hc.ExtendBatch(headers)
}

// Head returns the trusted head (number, hash).
func (m *MinimalVerifier) Head() (uint64, types.Hash) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hc.Head()
}

// VerifyBlock verifies one block's state transition against the trusted chain.
func (m *MinimalVerifier) VerifyBlock(bp *BlockProof) error {
	return VerifyAgainstChain(m.hc, bp)
}

// VerifyWindow verifies a batch of blocks in parallel (the fast-bootstrap path).
func (m *MinimalVerifier) VerifyWindow(proofs []*BlockProof, workers int) []BlockResult {
	return VerifyBatch(m.hc, proofs, workers)
}

// AttestVerified signs an attestation for a block AFTER verifying it. Returns an
// error if verification fails or the node has no signing key.
func (m *MinimalVerifier) AttestVerified(bp *BlockProof) (*Attestation, error) {
	if m.key == nil {
		return nil, fmt.Errorf("minimal: no signing key (verify-only node)")
	}
	if err := m.VerifyBlock(bp); err != nil {
		return nil, err
	}
	sr, ok := m.hc.TrustedStateRoot(bp.Number)
	if !ok {
		return nil, fmt.Errorf("minimal: block %d not in trusted chain", bp.Number)
	}
	rr, _ := m.hc.TrustedReceiptRoot(bp.Number)
	return SignAttestation(m.key, bp.Number, sr, rr)
}
