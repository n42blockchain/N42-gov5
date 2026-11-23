// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Strict header-QC verification for the sync importer (sync step S1, N42
// sync standard §3.3). The consensus path keeps its own, more permissive
// resolution (resolveQCValidatorSet falls back to any same-size set, and
// ValidatorSetForView to the current set); the sync path must not: a QC is an
// anchor only when it verifies in the COMMIT domain against the validator set
// of its OWN view's epoch. An epoch without a known set fails closed.

package hotstuff

import (
	gosync "sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/syncanchor"
	"github.com/n42blockchain/N42/log"
)

// StrictSetForEpoch returns the validator set of epoch, or false when this
// manager does not know it exactly. Unlike ValidatorSetForView it never falls
// back to the current set, and unlike FindValidatorSetByLen it never matches
// by size.
func (em *EpochManager) StrictSetForEpoch(epoch uint64) (*ValidatorSet, bool) {
	if em == nil {
		return nil, false
	}
	if em.epochLength == 0 {
		// A single epoch: the active set is the set.
		if epoch == 0 && em.currentSet != nil {
			return em.currentSet, true
		}
		return nil, false
	}
	if epoch == em.currentEpoch && em.currentSet != nil {
		return em.currentSet, true
	}
	if epoch == em.currentEpoch+1 && em.nextSet != nil {
		return em.nextSet, true
	}
	if set, ok := em.historicalSets[epoch]; ok && set != nil {
		return set, true
	}
	return nil, false
}

// SyncCommitSetForView resolves, under the engine lock, the epoch of view and
// that epoch's exact validator set (see StrictSetForEpoch).
func (e *ConsensusEngine) SyncCommitSetForView(view ViewNumber) (*ValidatorSet, uint64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.epochManager == nil {
		return nil, 0, false
	}
	epoch := e.epochManager.EpochForView(uint64(view))
	vs, ok := e.epochManager.StrictSetForEpoch(epoch)
	return vs, epoch, ok
}

// VerifySyncCommitQC checks qc against vs in this chain's commit domain
// (legacy or H2-v4 with a zero changesHash). A QC that fails there but
// verifies in the prepare/vote domain is reported as PrepareDomain so the
// caller can tell a non-anchor from a forgery. No engine state is touched.
func (e *ConsensusEngine) VerifySyncCommitQC(qc *QuorumCertificate, vs *ValidatorSet) (syncanchor.Verdict, error) {
	if qc == nil || vs == nil || vs.IsEmpty() {
		return syncanchor.UnknownEpoch, nil
	}
	err := e.verifyCommitQCWithSet(qc, vs)
	if err == nil {
		return syncanchor.Verified, nil
	}
	if e.verifyQCWithSet(qc, vs) == nil {
		return syncanchor.PrepareDomain, err
	}
	return syncanchor.BadQC, err
}

// maxSyncQCCache bounds the verdict cache of header QCs (keyed by keccak of
// the QC wire bytes). Each header normally carries a new QC; the cache only
// spares re-verification when the same range or tail is screened again.
const maxSyncQCCache = 4096

type syncQCCache struct {
	mu      gosync.Mutex
	results map[types.Hash]syncanchor.Result
	order   []types.Hash
	// scheduleSets caches validator sets parsed from the epoch schedule.
	scheduleSets map[uint64]*ValidatorSet
}

func (c *syncQCCache) get(key types.Hash) (syncanchor.Result, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.results[key]
	return r, ok
}

func (c *syncQCCache) put(key types.Hash, r syncanchor.Result) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.results == nil {
		c.results = make(map[types.Hash]syncanchor.Result)
	}
	if _, ok := c.results[key]; ok {
		return
	}
	if len(c.order) >= maxSyncQCCache {
		delete(c.results, c.order[0])
		c.order = c.order[1:]
	}
	c.results[key] = r
	c.order = append(c.order, key)
}

// decodeSyncHeaderQC classifies a header's Extra without signature work and
// returns the decoded QC when one is present.
func decodeSyncHeaderQC(extra []byte) (syncanchor.Result, *QuorumCertificate) {
	if len(extra) < extraMagicLen || string(extra[:extraMagicLen]) != string(extraMagic[:]) {
		return syncanchor.Result{Verdict: syncanchor.NonConsensus}, nil
	}
	_, qc, _, err := decodeHeaderExtra(extra)
	if err != nil {
		return syncanchor.Result{Verdict: syncanchor.BadExtra, Err: err}, nil
	}
	if isEmptyHeaderQC(qc) || qc.View == 0 {
		return syncanchor.Result{Verdict: syncanchor.NoQC}, nil
	}
	return syncanchor.Result{Verdict: syncanchor.Unverified, BlockHash: qc.BlockHash, View: qc.View}, qc
}

// DecodeHeaderQC implements syncanchor.Verifier.
func (s *Service) DecodeHeaderQC(extra []byte) syncanchor.Result {
	r, _ := decodeSyncHeaderQC(extra)
	return r
}

// VerifyHeaderQC implements syncanchor.Verifier: it verifies the QC embedded
// in extra strictly (commit domain, the set of the QC's own epoch from the
// engine or the epoch schedule, no fallback). Verdicts that cannot change
// (verified, prepare domain, bad) are cached; an unknown epoch is not, since
// the set may become known later.
func (s *Service) VerifyHeaderQC(extra []byte) syncanchor.Result {
	r, qc := decodeSyncHeaderQC(extra)
	if qc == nil {
		return r
	}
	var ce *ConsensusEngine
	if s.engine != nil {
		ce = s.engine.Engine()
	}
	if ce == nil {
		r.Verdict = syncanchor.NoVerifier
		return r
	}
	wire, err := encodeQC(qc)
	if err != nil {
		r.Verdict, r.Err = syncanchor.BadExtra, err
		return r
	}
	key := utils.Keccak256Hash(wire)
	if cached, ok := s.syncQCs.get(key); ok {
		cached.Cached = true
		return cached
	}
	vs, epoch, ok := ce.SyncCommitSetForView(qc.View)
	r.Epoch = epoch
	if !ok {
		vs, ok = s.scheduleSetForEpoch(epoch)
	}
	if !ok {
		r.Verdict = syncanchor.UnknownEpoch
		return r
	}
	r.Verdict, r.Err = ce.VerifySyncCommitQC(qc, vs)
	if r.Verdict == syncanchor.Verified || r.Verdict == syncanchor.PrepareDomain || r.Verdict == syncanchor.BadQC {
		s.syncQCs.put(key, r)
	}
	return r
}

// scheduleSetForEpoch is the third set source of the sync set provider (the
// epoch schedule file, standard §3.6): the last entry at or below epoch.
func (s *Service) scheduleSetForEpoch(epoch uint64) (*ValidatorSet, bool) {
	if s.epochSchedule == nil {
		return nil, false
	}
	s.syncQCs.mu.Lock()
	if vs, ok := s.syncQCs.scheduleSets[epoch]; ok {
		s.syncQCs.mu.Unlock()
		return vs, vs != nil
	}
	s.syncQCs.mu.Unlock()
	entry := s.epochSchedule.GetForEpoch(epoch)
	var vs *ValidatorSet
	if entry != nil {
		validators, err := entry.ParseValidators()
		if err != nil {
			log.Warn("sync anchor: epoch schedule entry does not parse", "epoch", epoch, "err", err)
		} else if len(validators) > 0 {
			vs = NewValidatorSet(validators, uint32((len(validators)-1)/3))
		}
	}
	s.syncQCs.mu.Lock()
	if s.syncQCs.scheduleSets == nil {
		s.syncQCs.scheduleSets = make(map[uint64]*ValidatorSet)
	}
	s.syncQCs.scheduleSets[epoch] = vs
	s.syncQCs.mu.Unlock()
	return vs, vs != nil
}

var _ syncanchor.Verifier = (*Service)(nil)
