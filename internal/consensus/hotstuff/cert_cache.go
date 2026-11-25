// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Verified-certificate cache (AM22(b)). On a timed-out view every one of
// the n timeouts carries a HighQC, and most of them carry the SAME one;
// votes and NewView messages carry the same HighTC. Verifying each copy
// costs one pairing check, so the receive side was O(n) pairings per view
// per node for certificates it had already accepted. The cache remembers
// certificates that VERIFIED, keyed by every input of the verification, so
// a repeat costs a hash instead of a pairing.
//
// Soundness: the key covers the signing message (domain, chain identity,
// view, block hash), the aggregate signature bytes, the signer bitmap, the
// signers' public keys, the set width and the quorum size: a hit means this
// exact verification already succeeded. Only successes are cached; a
// failure is never remembered, so a certificate that fails once (or a
// forgery sharing view, block and bitmap with a valid one) is verified
// again in full every time it is presented.

package hotstuff

import (
	"container/list"
	"crypto/sha256"
	"encoding/binary"
	"sync"
)

// verifiedCertCacheSize bounds the cache. A timed-out view needs a handful
// of entries (the HighQCs in flight plus one TC); the bound only has to
// cover the certificates that are live across a few views.
const verifiedCertCacheSize = 1024

const (
	certKindQC byte = 1
	certKindTC byte = 2
)

type certCacheKey [32]byte

// verifiedCertCache is a bounded LRU set of certificate verifications that
// succeeded. A nil cache caches nothing.
type verifiedCertCache struct {
	mu    sync.Mutex
	cap   int
	ll    *list.List
	items map[certCacheKey]*list.Element
	hits  uint64
}

func newVerifiedCertCache(capacity int) *verifiedCertCache {
	if capacity <= 0 {
		capacity = verifiedCertCacheSize
	}
	return &verifiedCertCache{cap: capacity, ll: list.New(), items: make(map[certCacheKey]*list.Element, capacity)}
}

// certKey derives the cache key of verifying (aggSig, signers) over msg
// against vs. ok is false when a signer has no public key in vs; the caller
// then verifies uncached (and fails).
func certKey(kind byte, msg, aggSig []byte, signers []bool, vs *ValidatorSet) (certCacheKey, bool) {
	if vs == nil {
		return certCacheKey{}, false
	}
	h := sha256.New()
	var hdr [17]byte
	hdr[0] = kind
	binary.LittleEndian.PutUint32(hdr[1:5], uint32(len(msg)))
	binary.LittleEndian.PutUint32(hdr[5:9], uint32(len(aggSig)))
	binary.LittleEndian.PutUint32(hdr[9:13], vs.Len())
	binary.LittleEndian.PutUint32(hdr[13:17], uint32(vs.QuorumSize()))
	h.Write(hdr[:])
	h.Write(msg)
	h.Write(aggSig)
	var bits [4]byte
	binary.LittleEndian.PutUint32(bits[:], uint32(len(signers)))
	h.Write(bits[:])
	bitmap := make([]byte, (len(signers)+7)/8)
	for i, s := range signers {
		if s {
			bitmap[i/8] |= 1 << (i % 8)
		}
	}
	h.Write(bitmap)
	for i, s := range signers {
		if !s {
			continue
		}
		pk, err := vs.GetPublicKey(ValidatorIndex(i))
		if err != nil || pk == nil {
			return certCacheKey{}, false
		}
		h.Write(pk.Marshal())
	}
	var k certCacheKey
	copy(k[:], h.Sum(nil))
	return k, true
}

func (c *verifiedCertCache) contains(k certCacheKey) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if ok {
		c.ll.MoveToFront(el)
		c.hits++
	}
	return ok
}

func (c *verifiedCertCache) add(k certCacheKey) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.ll.MoveToFront(el)
		return
	}
	c.items[k] = c.ll.PushFront(k)
	for c.ll.Len() > c.cap {
		old := c.ll.Back()
		c.ll.Remove(old)
		delete(c.items, old.Value.(certCacheKey))
	}
}

func (c *verifiedCertCache) len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ll.Len()
}

// verifyQCCached is verifyAggregateSignature behind the cache.
func (c *verifiedCertCache) verifyQCCached(qc *QuorumCertificate, vs *ValidatorSet, msg []byte, certKind string) error {
	k, ok := certKey(certKindQC, msg, qc.AggregateSignature, qc.Signers, vs)
	if ok && c.contains(k) {
		return nil
	}
	if err := verifyAggregateSignature(qc, vs, msg, certKind); err != nil {
		return err
	}
	if ok {
		c.add(k)
	}
	return nil
}

// qcCached reports whether qc over msg against vs already verified.
func (c *verifiedCertCache) qcCached(qc *QuorumCertificate, vs *ValidatorSet, msg []byte) bool {
	if c == nil {
		return false
	}
	k, ok := certKey(certKindQC, msg, qc.AggregateSignature, qc.Signers, vs)
	return ok && c.contains(k)
}

// verifyTCCached is verifyTCAgainstMessage behind the cache.
func (c *verifiedCertCache) verifyTCCached(tc *TimeoutCertificate, vs *ValidatorSet, msg []byte) error {
	k, ok := certKey(certKindTC, msg, tc.AggregateSignature, tc.Signers, vs)
	if ok && c.contains(k) {
		return nil
	}
	if err := verifyTCAgainstMessage(tc, vs, msg); err != nil {
		return err
	}
	if ok {
		c.add(k)
	}
	return nil
}
