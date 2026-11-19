// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"crypto/sha256"
	"encoding/binary"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/log"
)

// Verified-QC cache for the import's header check (r97a I4,
// N42_IMPORT_HEADER_VERIFY_CACHE, default off; 1, or check).
//
// The import re-verifies every header (VerifyHeaders, blockchain.go) before
// it executes the block; its dominant cost is the QC's aggregate BLS check
// (twice when the QC is a commit QC: the vote domain is tried first). The
// import is the first VerifyHeader of a header in this node -- nothing
// verifies the header earlier -- so a cache keyed by the header's hash would
// only hit on a re-import. What the node HAS verified before is the QC
// itself: the consensus engine checks the justify QC of every proposal, the
// QCs embedded in timeouts and new-views, prepare QCs and decide/commit QCs
// as they arrive, usually well before the block is imported.
//
// What is cached: the success of verifyAggregateSignature, which is a pure
// function of (signing message, signers bitmap, aggregate signature, the
// validator set's keys and fault tolerance). The key is a SHA-256 over
// exactly those inputs, the set represented by a fingerprint of its
// addresses, public keys and fault tolerance; the signing message carries
// the view, the block hash and the chain's signing domain. Every successful
// aggregate check in the process records its key while the switch is on; the
// import's header check consults it for either signing domain (it accepts
// either, verifyQCAnyDomainWithSet). A hit skips the BLS work only; the set
// is still resolved for the QC's view at the time of the check (a set change
// is a different fingerprint, so a miss).
//
// What is deliberately NOT cached: failures; a header check that found no
// validator set (it verifies nothing today, and a later one may); every other
// part of VerifyHeader -- the parent lookup and timestamp order, the gas-used
// bound's ancestor walk (both depend on what the store holds), the fork-shape
// checks, the QC-view bound and the committee-evidence link (depends on the
// committee pool) -- which all still run on every call. HotStuff's
// VerifyHeader has no wall-clock (future-time) check.
//
// =check runs the BLS check every time and compares it with the cache's
// answer; a cached success the check rejects is counted and logged (a panic
// in a test binary).

const (
	qcCacheOff uint32 = iota
	qcCacheOn
	qcCacheCheck
)

var qcCacheMode atomic.Uint32

func init() { qcCacheMode.Store(parseQCCache(os.Getenv("N42_IMPORT_HEADER_VERIFY_CACHE"))) }

func parseQCCache(v string) uint32 {
	switch v {
	case "1", "true", "TRUE", "on", "ON", "yes":
		return qcCacheOn
	case "check", "CHECK":
		return qcCacheCheck
	}
	return qcCacheOff
}

// SetImportHeaderVerifyCacheForTest switches N42_IMPORT_HEADER_VERIFY_CACHE
// ("", "1" or "check"), empties the cache and returns the restore.
func SetImportHeaderVerifyCacheForTest(v string) func() {
	prev := qcCacheMode.Load()
	qcCacheMode.Store(parseQCCache(v))
	verifiedQCs.reset()
	return func() { qcCacheMode.Store(prev); verifiedQCs.reset() }
}

// Process counters: hits, misses, check-mode disagreements.
var qcCacheHits, qcCacheMisses, qcCacheMismatches atomic.Uint64

// QCVerifyCacheCounts reports the import header check's cache hits and
// misses and the check mode's disagreements (since start).
func QCVerifyCacheCounts() (hits, misses, mismatches uint64) {
	return qcCacheHits.Load(), qcCacheMisses.Load(), qcCacheMismatches.Load()
}

const (
	qcCacheCap   = 4096 // verified aggregate checks kept
	qcCacheSetsN = 64   // validator-set fingerprints kept
)

type qcKey [32]byte

// qcVerifiedCache is a bounded set of verified aggregate checks (FIFO).
type qcVerifiedCache struct {
	mu   sync.Mutex
	m    map[qcKey]struct{}
	ring []qcKey
	next int
	// fps memoises set fingerprints by pointer. The map holds the pointer,
	// so a set's address is never reused for another set while its entry
	// lives; reset drops all of them together.
	fps map[*ValidatorSet][32]byte
}

var verifiedQCs qcVerifiedCache

func (c *qcVerifiedCache) reset() {
	c.mu.Lock()
	c.m, c.ring, c.next, c.fps = nil, nil, 0, nil
	c.mu.Unlock()
}

// fingerprint returns vs's fingerprint. Caller holds c.mu.
func (c *qcVerifiedCache) fingerprintLocked(vs *ValidatorSet) [32]byte {
	if fp, ok := c.fps[vs]; ok {
		return fp
	}
	h := sha256.New()
	var b [8]byte
	binary.BigEndian.PutUint32(b[:4], vs.faultTolerance)
	binary.BigEndian.PutUint32(b[4:], uint32(len(vs.validators)))
	h.Write(b[:])
	for _, v := range vs.validators {
		h.Write(v.Address[:])
		var pk []byte
		if v.PublicKey != nil {
			pk = v.PublicKey.Marshal()
		}
		binary.BigEndian.PutUint32(b[:4], uint32(len(pk)))
		h.Write(b[:4])
		h.Write(pk)
	}
	var fp [32]byte
	h.Sum(fp[:0])
	if c.fps == nil || len(c.fps) >= qcCacheSetsN {
		c.fps = make(map[*ValidatorSet][32]byte, 8)
	}
	c.fps[vs] = fp
	return fp
}

// keyLocked is the cache key of an aggregate check. Caller holds c.mu.
func (c *qcVerifiedCache) keyLocked(qc *QuorumCertificate, vs *ValidatorSet, message []byte) qcKey {
	fp := c.fingerprintLocked(vs)
	h := sha256.New()
	var b [4]byte
	h.Write(fp[:])
	binary.BigEndian.PutUint32(b[:], uint32(len(message)))
	h.Write(b[:])
	h.Write(message)
	binary.BigEndian.PutUint32(b[:], uint32(len(qc.Signers)))
	h.Write(b[:])
	for _, s := range qc.Signers {
		if s {
			h.Write([]byte{1})
		} else {
			h.Write([]byte{0})
		}
	}
	binary.BigEndian.PutUint32(b[:], uint32(len(qc.AggregateSignature)))
	h.Write(b[:])
	h.Write(qc.AggregateSignature)
	var k qcKey
	h.Sum(k[:0])
	return k
}

// add records a successful aggregate check.
func (c *qcVerifiedCache) add(qc *QuorumCertificate, vs *ValidatorSet, message []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.keyLocked(qc, vs, message)
	if c.m == nil {
		c.m = make(map[qcKey]struct{}, qcCacheCap)
		c.ring = make([]qcKey, qcCacheCap)
	}
	if _, ok := c.m[k]; ok {
		return
	}
	if old := c.ring[c.next]; old != (qcKey{}) {
		delete(c.m, old)
	}
	c.ring[c.next] = k
	c.next = (c.next + 1) % qcCacheCap
	c.m[k] = struct{}{}
}

// has reports whether the check was recorded as successful.
func (c *qcVerifiedCache) has(qc *QuorumCertificate, vs *ValidatorSet, message []byte) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		return false
	}
	_, ok := c.m[c.keyLocked(qc, vs, message)]
	return ok
}

// noteVerifiedAggregate records a successful verifyAggregateSignature while
// the switch is on.
func noteVerifiedAggregate(qc *QuorumCertificate, vs *ValidatorSet, message []byte) {
	if qcCacheMode.Load() == qcCacheOff || vs == nil {
		return
	}
	verifiedQCs.add(qc, vs, message)
}

// verifyHeaderQC is the import header check's verifyQCAnyDomainWithSet
// through the cache. The first result names how it was answered (the
// stamp's hdrQcCache): off, hit, miss, or check-hit / check-miss.
func (e *ConsensusEngine) verifyHeaderQC(qc *QuorumCertificate, vs *ValidatorSet) (string, error) {
	mode := qcCacheMode.Load()
	if mode == qcCacheOff {
		return "off", e.verifyQCAnyDomainWithSet(qc, vs)
	}
	hit := verifiedQCs.has(qc, vs, e.voteSigningMessage(qc.View, qc.BlockHash)) ||
		verifiedQCs.has(qc, vs, e.commitSigningMessage(qc.View, qc.BlockHash))
	if mode == qcCacheOn {
		if hit {
			qcCacheHits.Add(1)
			return "hit", nil
		}
		qcCacheMisses.Add(1)
		return "miss", e.verifyQCAnyDomainWithSet(qc, vs)
	}
	err := e.verifyQCAnyDomainWithSet(qc, vs)
	if !hit {
		qcCacheMisses.Add(1)
		return "check-miss", err
	}
	qcCacheHits.Add(1)
	if err != nil {
		qcCacheMismatches.Add(1)
		if testing.Testing() {
			panic("header QC cache check: a cached success failed verification")
		}
		log.Error("header QC cache check: a cached success failed verification", "view", qc.View, "block", qc.BlockHash, "err", err)
	}
	return "check-hit", err
}
