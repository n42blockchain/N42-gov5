// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Validator hand-over and partial-signature collection — the bridge from the
// simulated committee to a real distributed one. A real validator proves it
// controls a BLS key (RegisterValidator), taking over a simulated pool slot;
// from then on that slot's per-block signature must arrive from the validator
// (SubmitSignature), collected here and folded into the block's evidence by
// BuildBlockEvidence. Slots still held by the node keep signing locally via the
// scalar-sum fast path, so simulated and real members mix freely.
//
// NOTE: once any slot is handed over and real signatures flow in, a block's
// evidence depends on which signatures the producing node collected — it is no
// longer deterministically reproducible by other nodes. Multi-node operation in
// that regime therefore requires the producer's evidence to be propagated with
// the block (a P2P concern); until then, hand-over is an operator opt-in and the
// all-simulated path stays fully deterministic.

package blspool

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// registrationDomain separates the hand-over proof message from block-signing
// messages so a block signature can never be replayed as a registration proof.
var registrationDomain = []byte("n42-committee-register-v1")

// RegistrationMessage is the message a validator signs to prove it controls the
// BLS key it is registering for a slot: domain || slot(4B BE) || pubkey.
func RegistrationMessage(slot int, pubkey []byte) []byte {
	msg := make([]byte, 0, len(registrationDomain)+4+len(pubkey))
	msg = append(msg, registrationDomain...)
	var s [4]byte
	binary.BigEndian.PutUint32(s[:], uint32(slot))
	msg = append(msg, s[:]...)
	msg = append(msg, pubkey...)
	return msg
}

// SetPersistHook installs the callback that persists a hand-over (slot →
// pubkey). Called by the node with a chain-DB-backed writer.
func (p *Pool) SetPersistHook(fn func(slot int, pubkey []byte) error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.onRegister = fn
}

// RegisterValidator hands slot over to the validator that owns pubkeyBytes,
// having verified proofSig = sign(RegistrationMessage(slot, pubkey)) under that
// key (proof of key ownership). The hand-over is persisted via the persist hook
// before the in-memory swap, so a persistence failure leaves the slot untouched.
func (p *Pool) RegisterValidator(slot int, pubkeyBytes, proofSig []byte) error {
	if !p.cfg.AllowHandover {
		return fmt.Errorf("blspool: validator hand-over is disabled")
	}
	pk, err := bls.PublicKeyFromBytes(pubkeyBytes)
	if err != nil {
		return fmt.Errorf("blspool: invalid public key: %w", err)
	}
	sig, err := bls.SignatureFromBytes(proofSig)
	if err != nil {
		return fmt.Errorf("blspool: invalid proof signature: %w", err)
	}
	if !sig.Verify(pk, RegistrationMessage(slot, pubkeyBytes)) {
		return fmt.Errorf("blspool: registration proof does not verify for slot %d", slot)
	}

	p.mu.Lock()
	hook := p.onRegister
	p.mu.Unlock()
	if hook != nil {
		if err := hook(slot, pubkeyBytes); err != nil {
			return fmt.Errorf("blspool: persist registration: %w", err)
		}
	}
	return p.Register(slot, pk)
}

// LoadRegistrations applies persisted hand-overs (slot → pubkey) at startup.
// Does not re-persist (the entries came from the store).
func (p *Pool) LoadRegistrations(regs map[int][]byte) error {
	for slot, pkb := range regs {
		pk, err := bls.PublicKeyFromBytes(pkb)
		if err != nil {
			return fmt.Errorf("blspool: load registration slot %d: %w", slot, err)
		}
		if err := p.Register(slot, pk); err != nil {
			return err
		}
	}
	return nil
}

// SubmitSignature records a real validator's partial signature for a block. The
// signature must verify against the slot's CURRENT public key over
// SigningMessage(blockNum, blockHash); only members of the block's committee are
// accepted. Safe for concurrent submission.
func (p *Pool) SubmitSignature(blockNum uint64, blockHash types.Hash, slot int, sigBytes []byte) error {
	if !p.cfg.AllowHandover {
		return fmt.Errorf("blspool: validator hand-over is disabled")
	}
	sig, err := bls.SignatureFromBytes(sigBytes)
	if err != nil {
		return fmt.Errorf("blspool: invalid signature: %w", err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if slot < 0 || slot >= p.cfg.PoolSize {
		return fmt.Errorf("blspool: slot %d out of range", slot)
	}
	// The slot must be in this block's committee, else the signature is useless.
	members := p.scratch.Committee(blockNum, blockHash, p.ActiveSizeLocked(blockNum), p.cfg.CommitteeSize)
	inCommittee := false
	for _, m := range members {
		if m == slot {
			inCommittee = true
			break
		}
	}
	if !inCommittee {
		return fmt.Errorf("blspool: slot %d not in committee for block %d", slot, blockNum)
	}
	if !sig.Verify(p.pks[slot], SigningMessage(blockNum, blockHash)) {
		return fmt.Errorf("blspool: signature does not verify for slot %d block %d", slot, blockNum)
	}

	pb := p.pendingSigs[blockHash]
	if pb == nil {
		pb = &pendingBlock{num: blockNum, sigs: make(map[int]common.Signature)}
		p.pendingSigs[blockHash] = pb
	}
	pb.sigs[slot] = sig
	p.prunePendingLocked(blockNum)
	return nil
}

// ActiveSizeLocked is ActiveSize for callers already holding p.mu.
func (p *Pool) ActiveSizeLocked(blockNum uint64) int {
	return ActivePool(blockNum, p.cfg.PoolSize, p.cfg.CommitteeSize, p.cfg.RampBlocks)
}

// prunePendingLocked drops collected signatures for blocks far below blockNum
// (they will never be built). Caller holds p.mu.
func (p *Pool) prunePendingLocked(blockNum uint64) {
	const window = 64
	if blockNum < window {
		return
	}
	cutoff := blockNum - window
	for h, pb := range p.pendingSigs {
		if pb.num < cutoff {
			delete(p.pendingSigs, h)
		}
	}
}

// collectedSigsLocked returns (and removes) the partial signatures collected for
// a block. Caller holds p.mu.
func (p *Pool) collectedSigsLocked(blockHash types.Hash) map[int]common.Signature {
	pb := p.pendingSigs[blockHash]
	if pb == nil {
		return nil
	}
	delete(p.pendingSigs, blockHash)
	return pb.sigs
}

// BuildBlockEvidence builds a block's consensus evidence, folding in any real
// validator signatures collected for it. With no hand-overs and no submissions
// this is byte-identical to BuildSimulatedCE (every member signs locally), so it
// is a safe drop-in for the all-simulated phase.
func (p *Pool) BuildBlockEvidence(blockNum uint64, blockHash, receiptRoot types.Hash) (*rawdb.ConsensusEvidence, error) {
	p.mu.Lock()
	ext := p.collectedSigsLocked(blockHash)
	p.mu.Unlock()
	ce, _, err := p.BuildCE(blockNum, blockHash, receiptRoot, ext)
	return ce, err
}
