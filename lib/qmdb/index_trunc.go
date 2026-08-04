// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// index_trunc.go stores a prefix of each key hash instead of the whole thing,
// and confirms the full hash against the entry the slot points at.
//
// The in-RAM index is the largest single allocation in a loaded node. Measured
// on a 13.3M-block chain: 6,004,661 live keys per tree, two trees resident
// (execution and the pre-warmed miner computer), about 282 MB each. A Go map
// from a 32-byte key to a uint64 spends 40 bytes of key and value per entry; a
// 16-byte prefix spends 24, which is 40% less.
//
// A prefix is not a key. Two live hashes sharing one costs more than a wrong
// answer: the map holds a single slot per prefix, so the second Put would
// SILENTLY DROP the first key's mapping, and the tree would later deactivate
// the wrong entry. Detecting that on read is not enough -- by then the mapping
// is already gone. So collisions are held in a full-key overflow map, and Get
// consults it first.
//
// The verification is what makes the prefix safe to trust: a hit is confirmed
// against the entry's own key hash before the slot is returned. That costs one
// entry read per lookup, which is the trade being made -- RAM for a read that
// is usually already resident (the tree keeps the active twig's leaves) and
// otherwise goes to the cold reader.
//
// At 6M keys over a 128-bit prefix space the expected number of collisions is
// far below one, so the overflow map is expected to stay empty. It exists
// because "expected to stay empty" is not a correctness argument.

package qmdb

import (
	"os"
	"strconv"
)

// keyPrefix is the leading bytes of a key hash retained by truncIndex.
type keyPrefix [16]byte

func prefixOf(k Hash) keyPrefix {
	var p keyPrefix
	copy(p[:], k[:len(p)])
	return p
}

// SlotResolver reports the key hash the entry at slot holds. ok is false when
// the slot cannot be read, which must be treated as "cannot confirm" rather
// than "confirmed different".
type SlotResolver func(slot uint64) (keyHash Hash, ok bool)

// truncIndex is an Index keyed by a hash prefix, with full-key verification.
type truncIndex struct {
	m       map[keyPrefix]uint64
	ovf     map[Hash]uint64 // keys whose prefix was already taken by another key
	resolve SlotResolver

	collisions int // holder resolved to a different key
	unverified int // holder could not be read
}

// NewTruncIndex returns an index that stores 16-byte key prefixes and confirms
// each hit against the entry it points at.
//
// resolve must be supplied. Without it a prefix hit cannot be distinguished
// from a collision, and the index would answer with a slot belonging to a
// different key -- the tree would then deactivate that key's entry.
func NewTruncIndex(resolve SlotResolver, sizeHint int) Index {
	if resolve == nil {
		panic("qmdb: truncated index requires a slot resolver")
	}
	return &truncIndex{
		m:       make(map[keyPrefix]uint64, sizeHint),
		ovf:     make(map[Hash]uint64),
		resolve: resolve,
	}
}

// holderMatches reports whether the entry at slot is the key k.
//
// A slot that cannot be read returns false: the caller must then treat the
// prefix as taken by someone else and use the overflow map. Guessing "it is
// probably ours" would put the wrong slot in the index.
// holderVerdict distinguishes the two reasons a prefix holder is not this key,
// because they mean different things and only one of them is a collision.
type holderVerdict int

const (
	holderIsKey    holderVerdict = iota // resolved, and it is this key
	holderIsOther                       // resolved to a DIFFERENT key: a real prefix collision
	holderUnknown                       // could not be read: unverifiable, treat as not-ours
)

func (t *truncIndex) holderStatus(slot uint64, k Hash) holderVerdict {
	kh, ok := t.resolve(slot)
	switch {
	case !ok:
		return holderUnknown
	case kh == k:
		return holderIsKey
	default:
		return holderIsOther
	}
}

func (t *truncIndex) holderMatches(slot uint64, k Hash) bool {
	return t.holderStatus(slot, k) == holderIsKey
}

func (t *truncIndex) Get(k Hash) (uint64, bool) {
	if s, ok := t.ovf[k]; ok {
		idxGetHit.Add(1)
		return s, true
	}
	s, ok := t.m[prefixOf(k)]
	if !ok {
		idxGetMiss.Add(1)
		return 0, false
	}
	if !t.holderMatches(s, k) {
		// The prefix belongs to a different key. This key is simply not live:
		// had it been, Put would have placed it in the overflow map, which was
		// already consulted.
		idxGetMiss.Add(1)
		return 0, false
	}
	idxGetHit.Add(1)
	return s, true
}

func (t *truncIndex) Put(k Hash, s uint64) {
	idxPuts.Add(1)
	p := prefixOf(k)
	if held, ok := t.m[p]; ok && held != s && func() bool {
		switch t.holderStatus(held, k) {
		case holderIsOther:
			t.collisions++
			return true
		case holderUnknown:
			// Conservative: an unreadable holder is not proof the prefix is
			// ours, and assuming it were would overwrite another key's
			// mapping. Counted apart from collisions -- during a load the
			// holder's slot is routinely not yet readable, and reporting that
			// as a prefix collision made a Blake3 key space look as if it
			// collided 53 times in 6M keys, which is 26 orders of magnitude
			// off what a uniform hash does.
			t.unverified++
			return true
		}
		return false
	}() {
		// Another key owns this prefix. Overwriting would drop its mapping and
		// the tree would later deactivate its entry instead of this one.
		t.ovf[k] = s
		return
	}
	// If this key was in the overflow map (its former prefix holder has since
	// gone), the main map now owns it and the overflow entry must not linger:
	// Get consults the overflow map first and would return the stale slot.
	if len(t.ovf) != 0 {
		delete(t.ovf, k)
	}
	t.m[p] = s
}

func (t *truncIndex) Delete(k Hash) {
	idxDeletes.Add(1)
	if _, ok := t.ovf[k]; ok {
		delete(t.ovf, k)
		return
	}
	p := prefixOf(k)
	if held, ok := t.m[p]; ok && t.holderMatches(held, k) {
		delete(t.m, p)
	}
	// A prefix held by a different key is left alone: deleting it would drop
	// that key's mapping.
}

func (t *truncIndex) Len() int { return len(t.m) + len(t.ovf) }

// OverflowLen reports how many keys lost the race for their prefix. Expected to
// be zero; a non-zero value that grows means the prefix is too short for the
// live set and the memory saving is being paid for in full-key entries.
func (t *truncIndex) OverflowLen() int { return len(t.ovf) }

// OverflowReasons splits the overflow map by why each key landed there. Only
// collisions say anything about the prefix length; unverified says the holder's
// slot was unreadable at the time, which a load produces routinely.
func (t *truncIndex) OverflowReasons() (collisions, unverified int) {
	return t.collisions, t.unverified
}

// SlotKeyResolver resolves a slot to its entry's key hash.
//
// It reads the key hash directly and deliberately does NOT go through
// Tree.entryAt. entryAt derives the entry's LIVENESS, and it derives it by
// consulting the index -- so verifying an index hit through it recurses:
// Get -> verify -> entryAt -> index.Get -> verify -> ... Enabling the prefix
// index with an entryAt-based resolver killed three of seven fleet nodes with
// a stack overflow inside a minute.
//
// Liveness is what the index answers; verification only needs identity. So
// this reads the resident entry or the cold log and returns the key hash
// alone, touching no index.
func (t *Tree) SlotKeyResolver() SlotResolver {
	return func(slot uint64) (Hash, bool) {
		if slot >= t.entriesBase {
			i := slot - t.entriesBase
			if i < uint64(len(t.entries)) {
				return t.entries[i].keyHash, true
			}
			return Hash{}, false
		}
		if t.cold == nil {
			return Hash{}, false
		}
		kh, _, ok := t.cold.ColdEntry(slot)
		return kh, ok
	}
}

// truncIndexEnabled reports whether new indexes should store key prefixes.
//
// Off by default: it trades an entry read per lookup for the RAM, and the
// lookup is on the write path -- every Set consults the index to find the slot
// to deactivate -- so the cost needs measuring on a live node before this
// becomes a default.
//
// The first attempt at this verified through Tree.entryAt and killed three of
// seven fleet nodes with a stack overflow inside a minute: entryAt derives
// liveness by consulting the index, so verification recursed. SlotKeyResolver
// now reads the key hash without any liveness check; see its comment.
func truncIndexEnabled() bool {
	v, _ := strconv.ParseBool(os.Getenv("N42_QMDB_TRUNC_INDEX"))
	return v
}

// NewIndexFor returns a fresh in-RAM index of the configured shape for t.
func NewIndexFor(t *Tree, sizeHint int) Index {
	if truncIndexEnabled() {
		return NewTruncIndex(t.SlotKeyResolver(), sizeHint)
	}
	if sizeHint > 0 {
		return newMapIndexSized(sizeHint)
	}
	return newMapIndex()
}

// IndexOverflow reports an index's full-key overflow size, or -1 when the
// shape has none. Expected to stay 0; anything else means the prefix is too
// short for the live set.
func IndexOverflow(idx Index) int {
	if t, ok := idx.(*truncIndex); ok {
		return t.OverflowLen()
	}
	return -1
}

// IndexOverflowReasons splits an index's overflow by cause; -1,-1 when the
// shape has no overflow.
func IndexOverflowReasons(idx Index) (collisions, unverified int) {
	if t, ok := idx.(*truncIndex); ok {
		return t.OverflowReasons()
	}
	return -1, -1
}
