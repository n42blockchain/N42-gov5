// Copyright 2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"runtime"
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus"
)

// inmemoryP1QCs bounds the cache of verified profile-1 header CommitQC
// signatures (one entry per header; a range import is at most 1024 blocks).
const inmemoryP1QCs = 4096

// maxPrewarmWorkers bounds the goroutines one PrewarmHeaders call uses: the
// pairings run beside block execution, which needs the CPU too.
const maxPrewarmWorkers = 4

// p1QCKey binds a cached verification to everything the pairing checked:
// the signed message (chain identity, view, block hash), the signer bitmap
// and the aggregate signature. The validator set is the fixed genesis set of
// the profile, so it needs no part in the key.
func p1QCKey(qc *QuorumCertificate, msg []byte) types.Hash {
	bits := make([]byte, (len(qc.Signers)+7)/8+4)
	n := len(qc.Signers)
	bits[0], bits[1], bits[2], bits[3] = byte(n>>24), byte(n>>16), byte(n>>8), byte(n)
	for i, s := range qc.Signers {
		if s {
			bits[4+i/8] |= 1 << (uint(i) % 8)
		}
	}
	return crypto.Keccak256Hash(msg, bits, qc.AggregateSignature)
}

// verifyProfile1QCSig is verifyAggregateSignature for a profile-1 header
// CommitQC, answered from the cache of verifications already done (by
// PrewarmHeaders, or for an earlier import of the same header).
func (h *HotStuff) verifyProfile1QCSig(qc *QuorumCertificate, vs *ValidatorSet, msg []byte) error {
	if h.p1QCs == nil {
		return verifyAggregateSignature(qc, vs, msg, "CommitQC")
	}
	key := p1QCKey(qc, msg)
	if _, ok := h.p1QCs.Get(key); ok {
		return nil
	}
	if err := verifyAggregateSignature(qc, vs, msg, "CommitQC"); err != nil {
		return err
	}
	h.p1QCs.Add(key, struct{}{})
	return nil
}

// PrewarmHeaders runs the BLS checks of profile-1 headers (the leader's seal
// and the embedded CommitQC) for a batch the caller is about to import one
// block at a time, in parallel and ahead of it. Only successful
// verifications are cached, keyed by exactly what was verified; it decides
// nothing about any header, and VerifyHeader still runs every rule in full
// (a cache miss verifies inline as before). A no-op off profile 1.
func (h *HotStuff) PrewarmHeaders(chain consensus.ChainHeaderReader, headers []block.IHeader) {
	if h.chainConfig == nil || !h.chainConfig.IsProfile1() || len(headers) == 0 {
		return
	}
	vs, err := h.profile1Set()
	if err != nil {
		return
	}
	identity, idErr := h.profile1Identity(chain, nil)
	workers := min(maxPrewarmWorkers, max(1, runtime.GOMAXPROCS(0)/4), len(headers))
	next := make(chan *block.Header, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for header := range next {
				view, qc, seal, derr := decodeProfile1Extra(header.Extra)
				if derr != nil {
					continue
				}
				_ = h.verifyProfile1Seal(header, view, seal)
				if qc != nil && idErr == nil && qc.View < view {
					msg := H2V4CommitSigningMessage(identity, qc.View, qc.BlockHash, types.Hash{})
					_ = h.verifyProfile1QCSig(qc, vs, msg)
				}
			}
		}()
	}
	for _, ih := range headers {
		if hd, ok := ih.(*block.Header); ok && hd != nil && hd.Number != nil && !hd.Number.IsZero() {
			next <- hd
		}
	}
	close(next)
	wg.Wait()
}
