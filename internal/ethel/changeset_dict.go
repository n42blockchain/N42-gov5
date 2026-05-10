// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// changeset_dict.go provides the address / codeHash dictionary backing
// changeset codec V1. Encoders intern raw 20B addresses and 32B codeHashes
// into 3B big-endian ids that go into the wire blob; decoders resolve ids
// back to the raw values via MDBX lookups (with an in-memory LRU on top).
//
// MDBX layout:
//
//	AddrDict       3B id -> 20B addr
//	AddrIndex      20B addr -> 3B id
//	CodeHashDict   3B id -> 32B hash (id=0 reserved for "no code")
//	CodeHashIndex  32B hash -> 3B id
//	DictMeta       "addr_next" / "codehash_next" -> uint32 BE counters
//
// Concurrency: DictWriter is bound to a single kv.RwTx and is therefore
// single-writer by transaction scope. DictReader holds a kv.Tx and is safe
// for concurrent read use within that transaction's lifetime.

package ethel

import (
	"encoding/binary"
	"fmt"
	"sync"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

const (
	// MaxDictID is the exclusive upper bound on dictionary ids — 3 BE bytes
	// can address 0..16777215, but we reserve 0 as a sentinel so the usable
	// range is 1..MaxDictID-1.
	MaxDictID = 1 << 24

	addrCounterKey     = "addr_next"
	codeHashCounterKey = "codehash_next"

	defaultAddrLRUSize     = 8192
	defaultCodeHashLRUSize = 2048
)

// putBE24 writes v as 3 big-endian bytes into buf[:3].
func putBE24(buf []byte, v uint32) {
	_ = buf[2]
	buf[0] = byte(v >> 16)
	buf[1] = byte(v >> 8)
	buf[2] = byte(v)
}

// readBE24 reads 3 big-endian bytes from buf[:3] as uint32.
func readBE24(buf []byte) uint32 {
	_ = buf[2]
	return uint32(buf[0])<<16 | uint32(buf[1])<<8 | uint32(buf[2])
}

// DictInterner abstracts the "intern raw value -> 3B id" operation so
// encoders can be wired against either an eager (RwTx-bound) DictWriter
// or a buffered (RoTx + Flush) BufferedDictWriter without changing call
// sites. Returning a uint32 id and an error keeps the contract minimal.
type DictInterner interface {
	InternAddr(addr types.Address) (uint32, error)
	InternCodeHash(h types.Hash) (uint32, error)
}

// DictWriter assigns ids to addresses and codeHashes and persists the
// bidirectional mapping in MDBX eagerly through an RwTx. A per-transaction
// memo cache avoids repeated index lookups when the same value appears in
// many entries of one block.
//
// Eager mode is appropriate for tests and migration tools that already hold
// an RwTx and don't run concurrently with other commit phases. The hot
// production path (async output writer) uses BufferedDictWriter instead.
type DictWriter struct {
	tx kv.RwTx

	mu            sync.Mutex
	addrCache     map[types.Address]uint32
	codeHashCache map[types.Hash]uint32
}

// Compile-time interface assertion.
var _ DictInterner = (*DictWriter)(nil)

// NewDictWriter binds a writer to an MDBX rw-transaction.
func NewDictWriter(tx kv.RwTx) *DictWriter {
	return &DictWriter{
		tx:            tx,
		addrCache:     make(map[types.Address]uint32, 1024),
		codeHashCache: make(map[types.Hash]uint32, 256),
	}
}

// InternAddr returns the dictionary id for addr, creating a new entry on
// first encounter. Ids are stable within a database lifetime.
func (w *DictWriter) InternAddr(addr types.Address) (uint32, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if id, ok := w.addrCache[addr]; ok {
		return id, nil
	}
	idBytes, err := w.tx.GetOne(modules.AddrIndex, addr[:])
	if err != nil {
		return 0, fmt.Errorf("AddrIndex.Get: %w", err)
	}
	if len(idBytes) == 3 {
		id := readBE24(idBytes)
		w.addrCache[addr] = id
		return id, nil
	}
	id, err := w.allocID(addrCounterKey)
	if err != nil {
		return 0, fmt.Errorf("alloc addr id: %w", err)
	}
	var idBuf [3]byte
	putBE24(idBuf[:], id)
	if err := w.tx.Put(modules.AddrDict, idBuf[:], addr[:]); err != nil {
		return 0, fmt.Errorf("AddrDict.Put id=%d addr=%x: %w", id, addr, err)
	}
	if err := w.tx.Put(modules.AddrIndex, addr[:], idBuf[:]); err != nil {
		return 0, fmt.Errorf("AddrIndex.Put addr=%x id=%d: %w", addr, id, err)
	}
	w.addrCache[addr] = id
	return id, nil
}

// InternCodeHash returns the dictionary id for h. The empty / zero hash is
// not interned and always returns 0 (sentinel meaning "no code").
func (w *DictWriter) InternCodeHash(h types.Hash) (uint32, error) {
	if h == (types.Hash{}) {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if id, ok := w.codeHashCache[h]; ok {
		return id, nil
	}
	idBytes, err := w.tx.GetOne(modules.CodeHashIndex, h[:])
	if err != nil {
		return 0, fmt.Errorf("CodeHashIndex.Get: %w", err)
	}
	if len(idBytes) == 3 {
		id := readBE24(idBytes)
		w.codeHashCache[h] = id
		return id, nil
	}
	id, err := w.allocID(codeHashCounterKey)
	if err != nil {
		return 0, fmt.Errorf("alloc codeHash id: %w", err)
	}
	var idBuf [3]byte
	putBE24(idBuf[:], id)
	if err := w.tx.Put(modules.CodeHashDict, idBuf[:], h[:]); err != nil {
		return 0, fmt.Errorf("CodeHashDict.Put id=%d hash=%x: %w", id, h, err)
	}
	if err := w.tx.Put(modules.CodeHashIndex, h[:], idBuf[:]); err != nil {
		return 0, fmt.Errorf("CodeHashIndex.Put hash=%x id=%d: %w", h, id, err)
	}
	w.codeHashCache[h] = id
	return id, nil
}

// allocID reads the counter at counterKey, post-increments it in MDBX, and
// returns the value before increment. Ids start at 1 — 0 is reserved as a
// sentinel ("no code" for codeHash; never produced for addr).
func (w *DictWriter) allocID(counterKey string) (uint32, error) {
	cur, err := w.tx.GetOne(modules.DictMeta, []byte(counterKey))
	if err != nil {
		return 0, err
	}
	var next uint32 = 1
	if len(cur) == 4 {
		next = binary.BigEndian.Uint32(cur)
		if next == 0 {
			next = 1
		}
	}
	if next >= MaxDictID {
		return 0, fmt.Errorf("dict %s full at id=%d (max %d)", counterKey, next, MaxDictID)
	}
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], next+1)
	if err := w.tx.Put(modules.DictMeta, []byte(counterKey), buf[:]); err != nil {
		return 0, err
	}
	return next, nil
}

// BufferedDictWriter assigns ids without touching MDBX during intern. A
// kv.Tx (RoTx is fine) backs the index lookups; new assignments are
// buffered in maps and persisted on Flush(rwTx). This decouples encode
// from the MDBX write path so the async output goroutine can intern
// freely without holding an RwTx.
//
// Counters are read lazily from MDBX on first allocation. Subsequent
// allocations bump in-memory counters until Flush writes them back.
//
// Concurrency: all methods take an internal mutex, so multiple
// goroutines may call Intern* concurrently. The flush call must happen
// after writers have stopped — the caller is responsible for ordering
// (typically: drain async queue, then flush).
type BufferedDictWriter struct {
	roTx kv.Tx

	mu sync.Mutex

	// Cache: id resolved either from MDBX or from a pending entry.
	addrCache     map[types.Address]uint32
	codeHashCache map[types.Hash]uint32

	// Pending: not yet written to MDBX. Flush moves these into MDBX and
	// clears these maps. All entries here also live in the *Cache maps
	// for fast subsequent lookups.
	addrPending     map[types.Address]uint32
	codeHashPending map[types.Hash]uint32

	// Lazily initialized from DictMeta on first allocation. nextX is the
	// id to assign for the next NEW value.
	countersInit bool
	addrNext     uint32
	codeHashNext uint32
}

// Compile-time interface assertion.
var _ DictInterner = (*BufferedDictWriter)(nil)

// NewBufferedDictWriter binds the writer to a read transaction. The roTx
// must remain valid until Flush is called.
func NewBufferedDictWriter(roTx kv.Tx) *BufferedDictWriter {
	return &BufferedDictWriter{
		roTx:            roTx,
		addrCache:       make(map[types.Address]uint32, 4096),
		codeHashCache:   make(map[types.Hash]uint32, 1024),
		addrPending:     make(map[types.Address]uint32),
		codeHashPending: make(map[types.Hash]uint32),
	}
}

// InternAddr returns the dictionary id for addr, allocating in-memory if
// the value is not yet known. Allocation does not touch MDBX — call Flush
// to persist new assignments.
func (w *BufferedDictWriter) InternAddr(addr types.Address) (uint32, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if id, ok := w.addrCache[addr]; ok {
		return id, nil
	}
	idBytes, err := w.roTx.GetOne(modules.AddrIndex, addr[:])
	if err != nil {
		return 0, fmt.Errorf("AddrIndex.Get: %w", err)
	}
	if len(idBytes) == 3 {
		id := readBE24(idBytes)
		w.addrCache[addr] = id
		return id, nil
	}
	if err := w.ensureCountersLocked(); err != nil {
		return 0, err
	}
	if w.addrNext >= MaxDictID {
		return 0, fmt.Errorf("addr dict full at id=%d (max %d)", w.addrNext, MaxDictID)
	}
	id := w.addrNext
	w.addrNext++
	w.addrPending[addr] = id
	w.addrCache[addr] = id
	return id, nil
}

// InternCodeHash mirrors InternAddr; the empty hash always returns 0
// (sentinel "no code") and is never persisted.
func (w *BufferedDictWriter) InternCodeHash(h types.Hash) (uint32, error) {
	if h == (types.Hash{}) {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()

	if id, ok := w.codeHashCache[h]; ok {
		return id, nil
	}
	idBytes, err := w.roTx.GetOne(modules.CodeHashIndex, h[:])
	if err != nil {
		return 0, fmt.Errorf("CodeHashIndex.Get: %w", err)
	}
	if len(idBytes) == 3 {
		id := readBE24(idBytes)
		w.codeHashCache[h] = id
		return id, nil
	}
	if err := w.ensureCountersLocked(); err != nil {
		return 0, err
	}
	if w.codeHashNext >= MaxDictID {
		return 0, fmt.Errorf("codeHash dict full at id=%d", w.codeHashNext)
	}
	id := w.codeHashNext
	w.codeHashNext++
	w.codeHashPending[h] = id
	w.codeHashCache[h] = id
	return id, nil
}

// Flush writes all pending assignments to MDBX through rwTx. After Flush
// returns successfully, the in-memory pending maps are cleared but the
// resolution cache is preserved so subsequent Intern* calls keep hitting
// in memory. Counters are updated to reflect the high-water mark.
//
// The caller must guarantee no concurrent Intern* calls during Flush.
// Typically this means: drain the async writer queue first.
func (w *BufferedDictWriter) Flush(rwTx kv.RwTx) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.addrPending) == 0 && len(w.codeHashPending) == 0 && !w.countersInit {
		return nil
	}

	for addr, id := range w.addrPending {
		var idBuf [3]byte
		putBE24(idBuf[:], id)
		if err := rwTx.Put(modules.AddrDict, idBuf[:], addr[:]); err != nil {
			return fmt.Errorf("AddrDict.Put id=%d: %w", id, err)
		}
		if err := rwTx.Put(modules.AddrIndex, addr[:], idBuf[:]); err != nil {
			return fmt.Errorf("AddrIndex.Put: %w", err)
		}
	}
	for h, id := range w.codeHashPending {
		var idBuf [3]byte
		putBE24(idBuf[:], id)
		if err := rwTx.Put(modules.CodeHashDict, idBuf[:], h[:]); err != nil {
			return fmt.Errorf("CodeHashDict.Put id=%d: %w", id, err)
		}
		if err := rwTx.Put(modules.CodeHashIndex, h[:], idBuf[:]); err != nil {
			return fmt.Errorf("CodeHashIndex.Put: %w", err)
		}
	}

	if w.countersInit {
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], w.addrNext)
		if err := rwTx.Put(modules.DictMeta, []byte(addrCounterKey), buf[:]); err != nil {
			return fmt.Errorf("DictMeta addr counter: %w", err)
		}
		binary.BigEndian.PutUint32(buf[:], w.codeHashNext)
		if err := rwTx.Put(modules.DictMeta, []byte(codeHashCounterKey), buf[:]); err != nil {
			return fmt.Errorf("DictMeta codehash counter: %w", err)
		}
	}

	w.addrPending = make(map[types.Address]uint32)
	w.codeHashPending = make(map[types.Hash]uint32)
	return nil
}

// ensureCountersLocked seeds addrNext/codeHashNext from MDBX on first use.
// Caller must hold w.mu.
func (w *BufferedDictWriter) ensureCountersLocked() error {
	if w.countersInit {
		return nil
	}
	addrCur, err := w.roTx.GetOne(modules.DictMeta, []byte(addrCounterKey))
	if err != nil {
		return fmt.Errorf("read addr counter: %w", err)
	}
	w.addrNext = 1
	if len(addrCur) == 4 {
		w.addrNext = binary.BigEndian.Uint32(addrCur)
		if w.addrNext == 0 {
			w.addrNext = 1
		}
	}
	codeHashCur, err := w.roTx.GetOne(modules.DictMeta, []byte(codeHashCounterKey))
	if err != nil {
		return fmt.Errorf("read codehash counter: %w", err)
	}
	w.codeHashNext = 1
	if len(codeHashCur) == 4 {
		w.codeHashNext = binary.BigEndian.Uint32(codeHashCur)
		if w.codeHashNext == 0 {
			w.codeHashNext = 1
		}
	}
	w.countersInit = true
	return nil
}

// PendingCount returns the number of yet-unflushed assignments. Useful
// for instrumentation / sizing decisions before Flush.
func (w *BufferedDictWriter) PendingCount() (addrs, codeHashes int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.addrPending), len(w.codeHashPending)
}

// BindRoTx swaps the underlying read transaction. Required at every
// commit-interval boundary in the executor — the old RoTx becomes invalid
// once the main loop rotates. Must be called only when no Intern* call
// is in flight (typically: after asyncOut.waitDrain).
func (w *BufferedDictWriter) BindRoTx(roTx kv.Tx) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.roTx = roTx
}

// DictPending is a captured snapshot of the writer's pending state, ready
// to be persisted to MDBX by an external goroutine. Once a snapshot is
// taken, the writer's internal pending maps are reset so the main path
// can continue allocating new ids without racing against the flusher.
type DictPending struct {
	Addrs        map[types.Address]uint32
	CodeHashes   map[types.Hash]uint32
	AddrNext     uint32
	CodeHashNext uint32
	HasCounters  bool
}

// IsEmpty reports whether the snapshot has nothing to persist. Callers
// typically use this to skip the bg-tx round-trip on no-op intervals.
func (p *DictPending) IsEmpty() bool {
	if p == nil {
		return true
	}
	return len(p.Addrs) == 0 && len(p.CodeHashes) == 0 && !p.HasCounters
}

// TakePending atomically extracts pending entries and counters from the
// writer, leaving fresh empty maps in their place. The returned pointer
// is ALONE — the writer no longer references the maps so the caller may
// hand them to a bg goroutine without further synchronization.
//
// Counters are captured so the bg flusher can write them back even though
// the writer keeps incrementing them after takePending returns.
func (w *BufferedDictWriter) TakePending() *DictPending {
	w.mu.Lock()
	defer w.mu.Unlock()

	p := &DictPending{
		Addrs:        w.addrPending,
		CodeHashes:   w.codeHashPending,
		AddrNext:     w.addrNext,
		CodeHashNext: w.codeHashNext,
		HasCounters:  w.countersInit,
	}
	w.addrPending = make(map[types.Address]uint32)
	w.codeHashPending = make(map[types.Hash]uint32)
	return p
}

// FlushPending persists a previously-captured DictPending into rwTx. Safe
// to call from any goroutine because it never touches the BufferedDictWriter
// — the snapshot is fully self-contained.
func FlushPending(rwTx kv.RwTx, p *DictPending) error {
	if p.IsEmpty() {
		return nil
	}
	for addr, id := range p.Addrs {
		var idBuf [3]byte
		putBE24(idBuf[:], id)
		if err := rwTx.Put(modules.AddrDict, idBuf[:], addr[:]); err != nil {
			return fmt.Errorf("AddrDict.Put id=%d: %w", id, err)
		}
		if err := rwTx.Put(modules.AddrIndex, addr[:], idBuf[:]); err != nil {
			return fmt.Errorf("AddrIndex.Put: %w", err)
		}
	}
	for h, id := range p.CodeHashes {
		var idBuf [3]byte
		putBE24(idBuf[:], id)
		if err := rwTx.Put(modules.CodeHashDict, idBuf[:], h[:]); err != nil {
			return fmt.Errorf("CodeHashDict.Put id=%d: %w", id, err)
		}
		if err := rwTx.Put(modules.CodeHashIndex, h[:], idBuf[:]); err != nil {
			return fmt.Errorf("CodeHashIndex.Put: %w", err)
		}
	}
	if p.HasCounters {
		var buf [4]byte
		binary.BigEndian.PutUint32(buf[:], p.AddrNext)
		if err := rwTx.Put(modules.DictMeta, []byte(addrCounterKey), buf[:]); err != nil {
			return fmt.Errorf("DictMeta addr counter: %w", err)
		}
		binary.BigEndian.PutUint32(buf[:], p.CodeHashNext)
		if err := rwTx.Put(modules.DictMeta, []byte(codeHashCounterKey), buf[:]); err != nil {
			return fmt.Errorf("DictMeta codehash counter: %w", err)
		}
	}
	return nil
}

// DictReader resolves ids back to raw addresses and codeHashes. A small LRU
// keeps the hottest ids hot in RAM; the long tail falls through to MDBX
// (page-cached anyway). Safe for concurrent use within the bound tx.
type DictReader struct {
	tx kv.Tx

	addrLRU     *lru.Cache[uint32, types.Address]
	codeHashLRU *lru.Cache[uint32, types.Hash]
}

// NewDictReader binds a reader to an MDBX read-transaction. The LRU sizes
// are conservative defaults; tune via NewDictReaderSized for batch jobs.
func NewDictReader(tx kv.Tx) (*DictReader, error) {
	return NewDictReaderSized(tx, defaultAddrLRUSize, defaultCodeHashLRUSize)
}

// NewDictReaderSized lets callers pick LRU capacities. zero = use default.
func NewDictReaderSized(tx kv.Tx, addrCap, codeHashCap int) (*DictReader, error) {
	if addrCap <= 0 {
		addrCap = defaultAddrLRUSize
	}
	if codeHashCap <= 0 {
		codeHashCap = defaultCodeHashLRUSize
	}
	addrLRU, err := lru.New[uint32, types.Address](addrCap)
	if err != nil {
		return nil, err
	}
	codeHashLRU, err := lru.New[uint32, types.Hash](codeHashCap)
	if err != nil {
		return nil, err
	}
	return &DictReader{
		tx:          tx,
		addrLRU:     addrLRU,
		codeHashLRU: codeHashLRU,
	}, nil
}

// LookupAddr returns the address registered under id. Returns an error if
// id is 0 (sentinel) or unknown.
func (r *DictReader) LookupAddr(id uint32) (types.Address, error) {
	if id == 0 {
		return types.Address{}, fmt.Errorf("LookupAddr: id=0 reserved")
	}
	if v, ok := r.addrLRU.Get(id); ok {
		return v, nil
	}
	var idBuf [3]byte
	putBE24(idBuf[:], id)
	v, err := r.tx.GetOne(modules.AddrDict, idBuf[:])
	if err != nil {
		return types.Address{}, fmt.Errorf("AddrDict.Get id=%d: %w", id, err)
	}
	if len(v) != 20 {
		return types.Address{}, fmt.Errorf("AddrDict id=%d size=%d (want 20)", id, len(v))
	}
	var addr types.Address
	copy(addr[:], v)
	r.addrLRU.Add(id, addr)
	return addr, nil
}

// LookupCodeHash returns the codeHash registered under id. id=0 returns the
// zero hash (sentinel "no code") with no error.
func (r *DictReader) LookupCodeHash(id uint32) (types.Hash, error) {
	if id == 0 {
		return types.Hash{}, nil
	}
	if v, ok := r.codeHashLRU.Get(id); ok {
		return v, nil
	}
	var idBuf [3]byte
	putBE24(idBuf[:], id)
	v, err := r.tx.GetOne(modules.CodeHashDict, idBuf[:])
	if err != nil {
		return types.Hash{}, fmt.Errorf("CodeHashDict.Get id=%d: %w", id, err)
	}
	if len(v) != 32 {
		return types.Hash{}, fmt.Errorf("CodeHashDict id=%d size=%d (want 32)", id, len(v))
	}
	var h types.Hash
	copy(h[:], v)
	r.codeHashLRU.Add(id, h)
	return h, nil
}
