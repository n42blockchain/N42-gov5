// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// sender_cache.go — a process-wide memo of recovered transaction senders.
//
// Sender() already memoizes on the transaction object, but that only helps a
// caller holding the same object. A node recovers the sender of every incoming
// transaction when its pool admits it, and then recovers it AGAIN when the
// block carrying it arrives: the block is decoded from the wire into fresh
// Transaction values, so the per-object memo is gone. Under load that second
// recovery is expensive — secp256k1_ext_ecdsa_recover was 28% of all cgo time
// and ~20% of total CPU on a fleet node at 7.3k tx/s.
//
// The cache is keyed by transaction hash. Two transactions can only share a
// hash by being byte-identical, which makes their signatures — and therefore
// their senders under a given signer — identical too. Computing the hash to
// probe costs a keccak over ~110 bytes (~200 ns) against ~50 µs for the
// recovery it avoids, and the hash is memoized on the transaction and needed
// downstream regardless.
//
// It is direct-mapped and lock-free: a slot holds an immutable entry behind an
// atomic pointer, and a colliding key simply overwrites it. There is no
// eviction bookkeeping and no mutex for the recovery workers to contend on.
// Every outcome except a verified hit falls through to the real recovery, so a
// stale, evicted or racing entry can only cost time, never change a verdict.

package transaction

import (
	"encoding/binary"
	"os"
	"strconv"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/types"
)

// senderCacheEntry is immutable once published.
type senderCacheEntry struct {
	hash   types.Hash
	signer Signer
	from   types.Address
}

var (
	senderCache     []atomic.Pointer[senderCacheEntry]
	senderCacheMask uint64

	senderCacheHits   atomic.Uint64
	senderCacheMisses atomic.Uint64
)

// defaultSenderCacheSlots is a power of two: 262144 entries is ~19 MB fully
// populated, and comfortably covers the in-flight window of a node importing
// blocks whose transactions its pool saw moments earlier.
const defaultSenderCacheSlots = 1 << 18

func init() {
	slots := defaultSenderCacheSlots
	if v := os.Getenv("N42_SENDER_CACHE_SLOTS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			slots = n
		}
	}
	if slots == 0 {
		return // disabled: lookups miss, Sender behaves exactly as before
	}
	// Round up to a power of two so indexing is a mask, not a division.
	size := 1
	for size < slots {
		size <<= 1
	}
	senderCache = make([]atomic.Pointer[senderCacheEntry], size)
	senderCacheMask = uint64(size - 1)
}

func senderCacheSlot(hash types.Hash) uint64 {
	// The hash is keccak output, so any 8 bytes are as good as any other.
	return binary.LittleEndian.Uint64(hash[0:8]) & senderCacheMask
}

// senderCacheGet returns the cached sender for hash under signer.
func senderCacheGet(hash types.Hash, signer Signer) (types.Address, bool) {
	if senderCache == nil {
		return types.Address{}, false
	}
	e := senderCache[senderCacheSlot(hash)].Load()
	// The full hash is re-checked because the slot is shared by every key that
	// maps to it, and the signer because the same bytes recover differently
	// under different chain rules.
	if e == nil || e.hash != hash || !e.signer.Equal(signer) {
		senderCacheMisses.Add(1)
		return types.Address{}, false
	}
	senderCacheHits.Add(1)
	return e.from, true
}

func senderCachePut(hash types.Hash, signer Signer, from types.Address) {
	if senderCache == nil {
		return
	}
	senderCache[senderCacheSlot(hash)].Store(&senderCacheEntry{
		hash: hash, signer: signer, from: from,
	})
}

// SenderCacheStats reports cumulative hits and misses. Exported so operators
// and tests can confirm the cache is being reached rather than assume it.
func SenderCacheStats() (hits, misses uint64) {
	return senderCacheHits.Load(), senderCacheMisses.Load()
}

// ResetSenderCacheStats zeroes the counters (tests).
func ResetSenderCacheStats() {
	senderCacheHits.Store(0)
	senderCacheMisses.Store(0)
}
