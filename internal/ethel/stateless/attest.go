package stateless

import (
	"crypto/ecdsa"
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// Attestation is a minimal verifier node's signed claim that it independently
// verified block Number — its EVM execution (receiptRoot) and its state-root
// (stateRoot) — against the trusted header chain. An aggregator collects these
// from distinct verifier addresses to accumulate a verification count per block,
// so a block's correctness is backed by N independent stateless verifiers.
//
// The signed digest binds the block number and both roots, so a signature
// cannot be replayed for a different block or different roots.
type Attestation struct {
	Number      uint64
	StateRoot   types.Hash
	ReceiptRoot types.Hash
	Sig         []byte // 65-byte [R||S||V] secp256k1 signature over Digest()
}

// attestDigest = keccak256("n42-stateless-attest-v1" || number || stateRoot || receiptRoot).
func attestDigest(number uint64, stateRoot, receiptRoot types.Hash) []byte {
	var buf []byte
	buf = append(buf, []byte("n42-stateless-attest-v1")...)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], number)
	buf = append(buf, n[:]...)
	buf = append(buf, stateRoot[:]...)
	buf = append(buf, receiptRoot[:]...)
	return crypto.Keccak256(buf)
}

// Digest returns the 32-byte message the signature covers.
func (a *Attestation) Digest() []byte {
	return attestDigest(a.Number, a.StateRoot, a.ReceiptRoot)
}

// SignAttestation produces an attestation signed by key over (number, roots).
// Callers should sign ONLY after a successful VerifyAgainstChain for the block.
func SignAttestation(key *ecdsa.PrivateKey, number uint64, stateRoot, receiptRoot types.Hash) (*Attestation, error) {
	sig, err := crypto.Sign(attestDigest(number, stateRoot, receiptRoot), key)
	if err != nil {
		return nil, fmt.Errorf("attest sign: %w", err)
	}
	return &Attestation{Number: number, StateRoot: stateRoot, ReceiptRoot: receiptRoot, Sig: sig}, nil
}

// Recover returns the signer address of the attestation, verifying the signature.
func (a *Attestation) Recover() (types.Address, error) {
	if len(a.Sig) != 65 {
		return types.Address{}, fmt.Errorf("attest: bad sig len %d", len(a.Sig))
	}
	pub, err := crypto.SigToPub(a.Digest(), a.Sig)
	if err != nil {
		return types.Address{}, fmt.Errorf("attest recover: %w", err)
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// AttestationPool accumulates attestations per block from distinct signers. A
// block's verification count is the number of distinct valid signer addresses.
// An optional allowlist restricts which signers count (e.g. staked verifiers).
type AttestationPool struct {
	byBlock   map[uint64]map[string]map[types.Address]struct{} // number -> roots -> signers
	allowlist map[types.Address]bool                           // nil = accept any
}

// NewAttestationPool creates a pool. allow may be nil to accept any signer.
func NewAttestationPool(allow map[types.Address]bool) *AttestationPool {
	return &AttestationPool{
		byBlock:   map[uint64]map[string]map[types.Address]struct{}{},
		allowlist: allow,
	}
}

// HasAllowlist reports whether the pool restricts which signers count. A pool
// with no allowlist accepts ANY valid signature, which makes a distinct-signer
// threshold sybil-defeatable (an attacker signs the public roots with N
// self-generated keys) — callers exposing the count publicly must require one.
func (p *AttestationPool) HasAllowlist() bool { return len(p.allowlist) > 0 }

// rootsKey distinguishes attestations claiming different roots for the same block
// (a fork/disagreement shows as two separate counted groups).
func rootsKey(stateRoot, receiptRoot types.Hash) string {
	return string(stateRoot[:]) + string(receiptRoot[:])
}

// Add validates an attestation and records its signer. Returns the signer and
// whether it was newly counted (false on duplicate signer or not allowed).
func (p *AttestationPool) Add(a *Attestation) (types.Address, bool, error) {
	signer, err := a.Recover()
	if err != nil {
		return types.Address{}, false, err
	}
	if p.allowlist != nil && !p.allowlist[signer] {
		return signer, false, nil
	}
	rk := rootsKey(a.StateRoot, a.ReceiptRoot)
	byKey := p.byBlock[a.Number]
	if byKey == nil {
		byKey = map[string]map[types.Address]struct{}{}
		p.byBlock[a.Number] = byKey
	}
	set := byKey[rk]
	if set == nil {
		set = map[types.Address]struct{}{}
		byKey[rk] = set
	}
	if _, dup := set[signer]; dup {
		return signer, false, nil
	}
	set[signer] = struct{}{}
	return signer, true, nil
}

// Count returns how many distinct allowed verifiers attested (block, roots).
func (p *AttestationPool) Count(number uint64, stateRoot, receiptRoot types.Hash) int {
	byKey := p.byBlock[number]
	if byKey == nil {
		return 0
	}
	return len(byKey[rootsKey(stateRoot, receiptRoot)])
}

// Finalized reports whether (block, roots) reached threshold distinct attestations.
func (p *AttestationPool) Finalized(number uint64, stateRoot, receiptRoot types.Hash, threshold int) bool {
	return p.Count(number, stateRoot, receiptRoot) >= threshold
}

// Prune drops attestations for blocks below keepFrom (rolling window).
func (p *AttestationPool) Prune(keepFrom uint64) {
	for n := range p.byBlock {
		if n < keepFrom {
			delete(p.byBlock, n)
		}
	}
}
