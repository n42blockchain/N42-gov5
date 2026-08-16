// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Pool is the LIVE committee-signing provider: the same simulated 200K-voter /
// 512-committee BLS multi-sig that the replay reseal stamps onto historical
// blocks, packaged for block PRODUCTION on the converted chain — plus the
// gradual hand-over interface for real validators.
//
// Early phase: the node holds the simulated pool seed and signs the whole
// committee itself (scalar-sum fast path — one G2 multiplication per block).
// As real users join, Register(slot, pubkey) hands their pool slot over: the
// simulated key for that slot is dropped, and from then on that member's
// signature must arrive from the real validator (passed to BuildCE as an
// external partial signature, collected by the consensus/network layer).
// Members without a signature are simply left out of the aggregate and the
// signer bitmap — VerifyCE then verifies the aggregate against exactly the
// bitmap's public keys, mixing simulated and real keys freely.
//
// For an all-simulated committee, BuildCE output is byte-identical to the
// replay resealer's (asserted by tests), so a node producing block N+1 on a
// resealed chain continues it seamlessly.

package blspool

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// blsOrderU256 is r, the BLS12-381 scalar field modulus.
var blsOrderU256 = *uint256.MustFromHex("0x73eda753299d7d483339d80809a1d80553bda402fffe5bfeffffffff00000001")

// SigningMessage is the per-block committee message: view (8B LE) || blockHash.
// Kept in sync with hotstuff.SigningMessage (asserted by tests) without
// importing the consensus package.
func SigningMessage(view uint64, blockHash types.Hash) []byte {
	msg := make([]byte, 40)
	binary.LittleEndian.PutUint64(msg[:8], view)
	copy(msg[8:], blockHash[:])
	return msg
}

// PoolConfig configures the live committee pool.
type PoolConfig struct {
	Seed          [32]byte // master seed; keys derived as blsKeygen(sha256(seed||i))
	PoolSize      int
	CommitteeSize int
	RampBlocks    uint64
	AllowHandover bool // enable RegisterValidator / SubmitSignature (real-validator phase)
}

// HandoverAllowed reports whether validator hand-over is enabled.
func (p *Pool) HandoverAllowed() bool { return p.cfg.AllowHandover }

// Pool is the pluggable committee-signing provider. Safe for concurrent use
// (Register vs BuildCE/VerifyCE).
type Pool struct {
	cfg PoolConfig

	mu        sync.RWMutex
	sks       []common.SecretKey // simulated secret per slot; nil once replaced
	skScalars []uint256.Int      // cached scalar per simulated slot
	pks       []common.PublicKey // CURRENT pubkey per slot (simulated or real)
	replaced  []bool             // slot handed over to a real validator
	scratch   *CommitteeScratch

	// onRegister persists a hand-over (slot → pubkey) so it survives restart.
	// Set by the node; nil leaves registrations in-memory only.
	onRegister func(slot int, pubkey []byte) error

	// pendingSigs collects real validators' partial signatures per block,
	// keyed by block hash, consumed when that block's evidence is built.
	pendingSigs map[types.Hash]*pendingBlock
}

// pendingBlock is the set of real validator signatures collected for one block.
type pendingBlock struct {
	num  uint64
	sigs map[int]common.Signature // pool slot → signature
}

// NewSimulatedPool derives the full simulated pool from the master seed.
func NewSimulatedPool(cfg PoolConfig) (*Pool, error) {
	if cfg.PoolSize <= 0 || cfg.CommitteeSize <= 0 || cfg.CommitteeSize > cfg.PoolSize {
		return nil, fmt.Errorf("blspool: invalid pool/committee size")
	}
	sks, pks, err := DeriveKeys(cfg.Seed, cfg.PoolSize, true)
	if err != nil {
		return nil, fmt.Errorf("blspool: %w", err)
	}
	p := &Pool{
		cfg:         cfg,
		sks:         sks,
		pks:         pks,
		replaced:    make([]bool, cfg.PoolSize),
		scratch:     NewCommitteeScratch(),
		pendingSigs: make(map[types.Hash]*pendingBlock),
	}
	p.skScalars = make([]uint256.Int, len(sks))
	for i, sk := range sks {
		p.skScalars[i].SetBytes(sk.Marshal())
	}
	return p, nil
}

// Register hands pool slot `slot` over to a real validator: the simulated key
// is dropped and pk becomes the slot's public key. From the next block on, the
// committee aggregate covers this member only when its real signature is
// supplied to BuildCE.
func (p *Pool) Register(slot int, pk common.PublicKey) error {
	if slot < 0 || slot >= p.cfg.PoolSize {
		return fmt.Errorf("blspool: slot %d out of range [0,%d)", slot, p.cfg.PoolSize)
	}
	if pk == nil {
		return fmt.Errorf("blspool: nil public key")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sks[slot] = nil // drop the simulated secret — the slot is no longer ours
	p.pks[slot] = pk
	p.replaced[slot] = true
	return nil
}

// Replaced reports whether a slot has been handed over to a real validator.
func (p *Pool) Replaced(slot int) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return slot >= 0 && slot < len(p.replaced) && p.replaced[slot]
}

// ReplacedCount returns how many slots real validators have taken over.
func (p *Pool) ReplacedCount() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	n := 0
	for _, r := range p.replaced {
		if r {
			n++
		}
	}
	return n
}

// ActiveSize returns the ramped active-pool size at blockNum.
func (p *Pool) ActiveSize(blockNum uint64) int {
	return ActivePool(blockNum, p.cfg.PoolSize, p.cfg.CommitteeSize, p.cfg.RampBlocks)
}

// CommitteeAt returns the deterministic committee for (blockNum, blockHash).
// The returned slice is owned by the pool and overwritten by the next call.
func (p *Pool) CommitteeAt(blockNum uint64, blockHash types.Hash) []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.scratch.Committee(blockNum, blockHash, p.ActiveSize(blockNum), p.cfg.CommitteeSize)
}

// BuildCE produces the committee QC for a freshly produced block. Simulated
// members are signed locally via the scalar-sum fast path; replaced members are
// covered only if their real signature is present in extSigs (keyed by pool
// slot). Members without a signature are excluded from the aggregate and the
// bitmap and returned in missing — the consensus layer decides whether the
// coverage meets its threshold before committing the block.
func (p *Pool) BuildCE(blockNum uint64, blockHash types.Hash, receiptRoot types.Hash,
	extSigs map[int]common.Signature) (ce *rawdb.ConsensusEvidence, missing []int, err error) {

	// Full lock: the committee scratch is shared mutable state (block
	// production/verification is sequential, so this is uncontended).
	p.mu.Lock()
	defer p.mu.Unlock()

	agg, bitmap, n, missing, err := p.aggregateLocked(blockNum, blockHash, extSigs)
	if err != nil {
		return nil, missing, err
	}
	ce = &rawdb.ConsensusEvidence{
		View:        blockNum,
		BlockHash:   blockHash,
		SignerCount: uint16(n),
	}
	copy(ce.AggregateSignature[:], agg.Marshal())
	ce.SignersPacked = bitmap
	ce.HasMobile = true
	ce.MobReceiptsRoot = receiptRoot
	ce.MobParticipantCount = uint16(n)
	ce.MobParticipantsPacked = ce.SignersPacked
	copy(ce.MobAggSignature[:], agg.Marshal())
	return ce, missing, nil
}

// BuildSimulatedCE builds the all-simulated committee evidence for a block — no
// external real-validator signatures, the node signs the whole committee via the
// scalar-sum fast path. This is the early-phase / block-production entry point;
// its output is byte-identical to the replay resealer's BuildCE for an
// all-simulated committee, so a node producing block N+1 continues the resealed
// chain seamlessly.
func (p *Pool) BuildSimulatedCE(blockNum uint64, blockHash, receiptRoot types.Hash) (*rawdb.ConsensusEvidence, error) {
	ce, _, err := p.BuildCE(blockNum, blockHash, receiptRoot, nil)
	return ce, err
}

// aggregateLocked signs the committee for (blockNum, blockHash): scalar-sum for
// simulated members, external signatures for replaced ones. Caller holds p.mu.
func (p *Pool) aggregateLocked(blockNum uint64, blockHash types.Hash,
	extSigs map[int]common.Signature) (agg common.Signature, bitmap []byte, n int, missing []int, err error) {

	members := p.scratchCommitteeLocked(blockNum, blockHash)
	n = len(members)
	precomp := bls.PrecomputeHash(SigningMessage(blockNum, blockHash))

	var sum uint256.Int
	simulated := 0
	bitmap = make([]byte, (n+7)/8)
	var realSigs []common.Signature
	for i, m := range members {
		if !p.replaced[m] {
			sum.AddMod(&sum, &p.skScalars[m], &blsOrderU256)
			simulated++
			bitmap[i/8] |= 1 << uint(i%8)
			continue
		}
		if sig, ok := extSigs[m]; ok && sig != nil {
			realSigs = append(realSigs, sig)
			bitmap[i/8] |= 1 << uint(i%8)
		} else {
			missing = append(missing, m)
		}
	}
	if simulated == 0 && len(realSigs) == 0 {
		return nil, nil, n, missing, fmt.Errorf("blspool: no coverage for committee at block %d", blockNum)
	}
	if simulated > 0 {
		agg = precomp.SignWithScalarSum(sum.Bytes32())
	}
	if len(realSigs) > 0 {
		if agg != nil {
			realSigs = append(realSigs, agg)
		}
		agg = bls.AggregateSignatures(realSigs)
	}
	return agg, bitmap, n, missing, nil
}

// MobileAttestation is the mobile-verifier layer's aggregate for one block —
// per the N42 architecture, block PROPOSAL runs HotStuff among a small set of
// high-performance always-online IDC servers (their QC fills the CE's QC
// section), while the massively scalable mobile pool (200K now, 1M+ like ETH)
// attests to the executed receipts (this struct fills the CE's mobile section).
type MobileAttestation struct {
	AggSig  [96]byte
	Bitmap  []byte
	Count   uint16 // committee size (bitmap gaps = uncovered members)
	Missing []int  // replaced slots whose real signature was not supplied
}

// BuildMobileAttestation signs the mobile committee for a block: simulated
// members locally (scalar-sum), replaced members from extSigs. See BuildCE for
// the coverage semantics.
func (p *Pool) BuildMobileAttestation(blockNum uint64, blockHash types.Hash,
	extSigs map[int]common.Signature) (*MobileAttestation, error) {

	p.mu.Lock()
	defer p.mu.Unlock()
	agg, bitmap, n, missing, err := p.aggregateLocked(blockNum, blockHash, extSigs)
	if err != nil {
		return nil, err
	}
	ma := &MobileAttestation{Bitmap: bitmap, Count: uint16(n), Missing: missing}
	copy(ma.AggSig[:], agg.Marshal())
	return ma, nil
}

// AssembleCE combines the IDC HotStuff quorum certificate (the block-proposal
// layer) with the mobile pool's attestation into the on-chain evidence. qcSig
// is the QC's aggregate signature bytes and qcSigners its validator bitmap —
// plain fields so this package stays independent of the consensus package.
func AssembleCE(view uint64, blockHash types.Hash,
	qcSig []byte, qcSigners []bool,
	receiptRoot types.Hash, mobile *MobileAttestation) *rawdb.ConsensusEvidence {

	ce := &rawdb.ConsensusEvidence{
		View:        view,
		BlockHash:   blockHash,
		SignerCount: uint16(len(qcSigners)),
	}
	copy(ce.AggregateSignature[:], qcSig)
	ce.SignersPacked = make([]byte, (len(qcSigners)+7)/8)
	for i, signed := range qcSigners {
		if signed {
			ce.SignersPacked[i/8] |= 1 << uint(i%8)
		}
	}
	if mobile != nil {
		ce.HasMobile = true
		ce.MobReceiptsRoot = receiptRoot
		ce.MobAggSignature = mobile.AggSig
		ce.MobParticipantCount = mobile.Count
		ce.MobParticipantsPacked = mobile.Bitmap
	}
	return ce
}

// scratchCommitteeLocked samples under the held lock (scratch is not
// concurrency-safe).
func (p *Pool) scratchCommitteeLocked(blockNum uint64, blockHash types.Hash) []int {
	return p.scratch.Committee(blockNum, blockHash, p.ActiveSize(blockNum), p.cfg.CommitteeSize)
}

// VerifyCE re-derives the committee and checks the aggregate signature against
// the CURRENT public keys of the bitmap's members (simulated or real). Returns
// the number of covered signers so callers can apply threshold rules.
// The lock covers p.scratch (not concurrency-safe) and p.pks (rotated by
// SetPubkey). Everything after it — signature deserialisation, pubkey
// aggregation and the pairing — touches only the local slice and is where
// essentially all the time goes, so it runs unlocked. Holding the pool mutex
// across a pairing serialises every other pool user behind millisecond-scale
// crypto for no benefit, and makes concurrent verification impossible.
func (p *Pool) VerifyCE(ce *rawdb.ConsensusEvidence) (covered int, ok bool, err error) {
	pubs, err := p.signerPubkeys(ce)
	if err != nil {
		return 0, false, err
	}

	aggSig, err := bls.SignatureFromBytes(ce.AggregateSignature[:])
	if err != nil {
		return 0, false, err
	}
	msg := SigningMessage(ce.View, ce.BlockHash)
	if !aggSig.Verify(bls.AggregateMultiplePubkeys(pubs), msg) {
		return len(pubs), false, nil
	}
	return len(pubs), true, nil
}

// signerPubkeys resolves the bitmap's members to their current public keys.
// This is the only part of VerifyCE that needs the pool lock; the returned
// slice is owned by the caller.
func (p *Pool) signerPubkeys(ce *rawdb.ConsensusEvidence) ([]common.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	members := p.scratchCommitteeLocked(ce.View, ce.BlockHash)
	if int(ce.SignerCount) != len(members) {
		return nil, fmt.Errorf("signer count %d != committee %d", ce.SignerCount, len(members))
	}
	pubs := make([]common.PublicKey, 0, len(members))
	for i := range members {
		if i < len(ce.SignersPacked)*8 && ce.SignersPacked[i/8]&(1<<uint(i%8)) != 0 {
			pubs = append(pubs, p.pks[members[i]])
		}
	}
	if len(pubs) == 0 {
		return nil, fmt.Errorf("empty signer bitmap")
	}
	return pubs, nil
}
