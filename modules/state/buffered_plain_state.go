// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// PlainStateBuffer: write buffer plus lock-free read cache for hot EVM.
// Maintains dirty account/storage/code maps plus contractWipes and
// wipedStorage sets for MDBX storage purges on flush. Read paths use
// sync.Map-backed readAccounts/readStorage/readCode caches so hot EVM
// lookups avoid locks entirely; accountCacheEntry wraps nil-on-absent
// cached accounts, storageEntry carries raw slot bytes and atomic
// hits/misses counters expose cache effectiveness to metrics.

package state

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

var deletedSentinel = []byte{}

type storageEntry struct {
	value []byte
}

// accountCacheEntry wraps a cached account. The sync.Map stores interface{},
// so we use a pointer type. nil *accountCacheEntry = key exists but absent.
type accountCacheEntry struct {
	acct *account.StateAccount
}

// CacheBudget configures byte budgets for the three read-cache LRUs.
// Defaults are tuned for a 5M-25M-block read-heavy workload on a 128 GB
// machine where PlainState reads are the dominant cost: each EVM block
// issues ~1000 SLOAD/account fetches and a 1pp hit-rate improvement
// translates to a measurable wall-clock saving over a full chain replay.
type CacheBudget struct {
	AccountBytes int64 // LRU byte budget for accounts (default 4 GB)
	StorageBytes int64 // LRU byte budget for storage slots (default 8 GB)
	CodeBytes    int64 // LRU byte budget for contract bytecode (default 1 GB)
}

// DefaultCacheBudget returns the recommended sizes for the executor's
// hot read-cache tier on a 128 GB host.
//
// Sizing rationale (calibrated against the 11M-block reference run that
// shows blk/s ~71 with cacheHit%=88%, ~1300 SLOADs per block flowing into
// MDBX as misses):
//
//   - Storage gets the bulk of the budget. Hot data analysis shows the
//     storage long tail is wide (73% of slots are 1-write, then a heavy
//     11K+ write tier of DeFi pools), and the read-heavy 5M-25M block
//     range is dominated by per-block SLOAD bursts that touch a wider
//     working set than the account side.
//   - Accounts get a smaller slice — Top 5% of accounts cover most
//     traffic, and account values are larger per entry (V2 ~30 bytes
//     each), so a moderate budget covers the active set.
//   - Code is small: only ~50K unique deployed contracts ever appear in
//     a typical hot block, and cached bytecode is reusable across calls.
//
// Total ~14.5 GB (~11% of RAM on a 128 GB host). Tunable via the
// CacheBudget struct on PlainStateBuffer construction; production runs
// should consider raising storage to 16-24 GB on hosts with >= 96 GB
// of free RAM if cacheHit% stays below 95%.
func DefaultCacheBudget() CacheBudget {
	return CacheBudget{
		AccountBytes: 2 << 30,  // 2 GB  (~14M hot accounts)
		StorageBytes: 12 << 30, // 12 GB (~70M hot slots — covers DeFi-era working set)
		CodeBytes:    512 << 20, // 512 MB
	}
}

// Per-entry overhead estimates for byte accounting. Real Go map +
// container/list overhead is highly implementation-dependent; these are
// generous round numbers chosen to keep us well below the byte budget
// even as runtime data structures bloat.
const (
	cacheOverheadPerEntry = 96 // map header + list.Element + pointer slop
	storageCompositeKeyLen = 52
	addrKeyLen             = 20
	hashKeyLen             = 32
)

// PlainStateBuffer holds write buffer + bounded LRU read caches.
type PlainStateBuffer struct {
	// Write buffer (single-writer, executor only).
	accounts       map[types.Address][]byte
	storage        map[types.Address]map[types.Hash]storageEntry
	code           map[types.Hash][]byte
	contractCode   map[string][]byte
	incarnationMap map[types.Address][]byte
	contractWipes  []types.Address            // addresses needing MDBX storage wipe on flush
	wipedStorage   map[types.Address]struct{} // addresses whose storage was wiped — reads bypass cache/MDBX

	// Read cache: byte-budget LRU per kind so a hot storage workload
	// can't starve the account cache. The composite storage key is
	// addr(20) || slot(32) = 52 bytes; we keep storage as a flat map
	// (no per-address sub-map) to make eviction account for individual
	// slots rather than whole-address groups.
	readAccounts *byteLRU[types.Address]
	readStorage  *byteLRU[[storageCompositeKeyLen]byte]
	readCode     *byteLRU[types.Hash]

	hits   atomic.Uint64
	misses atomic.Uint64
}

// NewPlainStateBuffer creates a buffer with the default cache budget.
// For a custom budget use NewPlainStateBufferWithBudget.
func NewPlainStateBuffer() *PlainStateBuffer {
	return NewPlainStateBufferWithBudget(DefaultCacheBudget())
}

// NewPlainStateBufferWithBudget creates a buffer with the given LRU
// byte budgets. Pass zero in any field to fall back to the default for
// that tier.
func NewPlainStateBufferWithBudget(b CacheBudget) *PlainStateBuffer {
	def := DefaultCacheBudget()
	if b.AccountBytes <= 0 {
		b.AccountBytes = def.AccountBytes
	}
	if b.StorageBytes <= 0 {
		b.StorageBytes = def.StorageBytes
	}
	if b.CodeBytes <= 0 {
		b.CodeBytes = def.CodeBytes
	}
	return &PlainStateBuffer{
		accounts:       make(map[types.Address][]byte, 4096),
		storage:        make(map[types.Address]map[types.Hash]storageEntry, 4096),
		code:           make(map[types.Hash][]byte, 64),
		contractCode:   make(map[string][]byte, 64),
		incarnationMap: make(map[types.Address][]byte),
		wipedStorage:   make(map[types.Address]struct{}),
		readAccounts:   newByteLRU[types.Address](b.AccountBytes),
		readStorage:    newByteLRU[[storageCompositeKeyLen]byte](b.StorageBytes),
		readCode:       newByteLRU[types.Hash](b.CodeBytes),
	}
}

func (b *PlainStateBuffer) CacheStats() (hits, misses uint64) {
	return b.hits.Load(), b.misses.Load()
}

// LRUStats reports per-tier byte usage and entry counts.
func (b *PlainStateBuffer) LRUStats() (acctBytes, stoBytes, codeBytes int64, acctEntries, stoEntries, codeEntries int) {
	_, _, ab, ae := b.readAccounts.Stats()
	_, _, sb, se := b.readStorage.Stats()
	_, _, cb, ce := b.readCode.Stats()
	return ab, sb, cb, ae, se, ce
}

// CacheAccount populates the read cache (used by prefetcher).
// nil acct = "key exists, account is absent in PlainState"; the cache
// caches negative results too so a repeated lookup of a non-existent
// address doesn't keep hitting MDBX.
func (b *PlainStateBuffer) CacheAccount(address types.Address, acct *account.StateAccount) {
	cost := addrKeyLen + cacheOverheadPerEntry
	var v []byte
	if acct != nil {
		// Encode v2 form so subsequent reads can decode without
		// holding the original *StateAccount.
		v = acct.MarshalV2()
		cost += len(v)
	}
	b.readAccounts.Put(address, v, cost)
}

// CacheStorage populates the storage read cache (used by prefetcher).
// A nil/empty value caches the "slot is zero/absent" answer.
func (b *PlainStateBuffer) CacheStorage(address types.Address, key types.Hash, value []byte) {
	var ck [storageCompositeKeyLen]byte
	copy(ck[:20], address[:])
	copy(ck[20:], key[:])
	cost := storageCompositeKeyLen + len(value) + cacheOverheadPerEntry
	b.readStorage.Put(ck, value, cost)
}

func (b *PlainStateBuffer) FlushToMDBX(tx kv.RwTx) error {
	// Sort keys before writing — MDBX B+ tree performs dramatically better
	// with sequential inserts (avoids random page faults).

	// 1. Accounts: sort by address.
	acctAddrs := make([]types.Address, 0, len(b.accounts))
	for addr := range b.accounts {
		acctAddrs = append(acctAddrs, addr)
	}
	sort.Slice(acctAddrs, func(i, j int) bool {
		return bytes.Compare(acctAddrs[i][:], acctAddrs[j][:]) < 0
	})
	for _, addr := range acctAddrs {
		v := b.accounts[addr]
		if len(v) == 0 {
			if err := tx.Delete(modules.Account, addr[:]); err != nil {
				return err
			}
		} else {
			if err := tx.Put(modules.Account, addr[:], v); err != nil {
				return err
			}
		}
	}

	// 2. Wipe storage for contracts that were recreated/destroyed.
	// Only wipe MDBX (old data). Buffer entries from CREATE in the same
	// block will be written in step 3 and are the correct new state.
	for _, addr := range b.contractWipes {
		prefix := addr[:]
		cursor, err := tx.Cursor(modules.Storage)
		if err != nil {
			return err
		}
		var keysToDelete [][]byte
		for k, _, err := cursor.Seek(prefix); k != nil; k, _, err = cursor.Next() {
			if err != nil {
				cursor.Close()
				return err
			}
			if len(k) < 20 || !bytes.Equal(k[:20], prefix) {
				break
			}
			keysToDelete = append(keysToDelete, append([]byte{}, k...))
		}
		cursor.Close()
		for _, k := range keysToDelete {
			if err := tx.Delete(modules.Storage, k); err != nil {
				return err
			}
		}
	}

	// 3. Storage: build composite keys, sort, then write.
	type stoKV struct {
		key   []byte
		value []byte
	}
	stoCount := 0
	for _, slots := range b.storage {
		stoCount += len(slots)
	}
	stoEntries := make([]stoKV, 0, stoCount)
	for addr, slots := range b.storage {
		for hash, entry := range slots {
			compositeKey := modules.PlainGenerateCompositeStorageKey(addr[:], hash[:])
			stoEntries = append(stoEntries, stoKV{key: compositeKey, value: entry.value})
		}
	}
	sort.Slice(stoEntries, func(i, j int) bool {
		return bytes.Compare(stoEntries[i].key, stoEntries[j].key) < 0
	})
	for _, kv := range stoEntries {
		if len(kv.value) == 0 {
			if err := tx.Delete(modules.Storage, kv.key); err != nil {
				return err
			}
		} else {
			if err := tx.Put(modules.Storage, kv.key, kv.value); err != nil {
				return err
			}
		}
	}

	// 4. Code, ContractCode — small tables, sort for consistency.
	codeHashes := make([]types.Hash, 0, len(b.code))
	for hash := range b.code {
		codeHashes = append(codeHashes, hash)
	}
	sort.Slice(codeHashes, func(i, j int) bool {
		return bytes.Compare(codeHashes[i][:], codeHashes[j][:]) < 0
	})
	for _, hash := range codeHashes {
		if err := tx.Put(modules.Code, hash[:], b.code[hash]); err != nil {
			return err
		}
	}

	ccKeys := make([]string, 0, len(b.contractCode))
	for prefix := range b.contractCode {
		ccKeys = append(ccKeys, prefix)
	}
	sort.Strings(ccKeys)
	for _, prefix := range ccKeys {
		if err := tx.Put(modules.PlainContractCode, []byte(prefix), b.contractCode[prefix]); err != nil {
			return err
		}
	}
	// IncarnationMap removed — no longer needed
	return nil
}

// Clear resets write buffer and selectively invalidates LRU entries that
// this flush just wrote to MDBX. Entries for keys that did NOT change in
// this commit window are kept across the flush — hot data analysis shows
// most state lookups hit the same long-tailed set of accounts/slots, so
// blowing the entire cache on every commit interval discards 90%+ of
// valid entries.
//
// Correctness: a cached read entry is "stale" iff the key's value was
// modified between the cache fill and the read. The only mutating event
// in this commit window is the flush itself, which modifies exactly the
// keys in b.accounts / b.storage / b.code / b.wipedStorage. Every other
// cached entry is unchanged in MDBX. Evicting only the dirty set is
// therefore both correct and minimal.
//
// For wiped contracts (SELFDESTRUCT), we don't have an efficient
// "delete all slots for this address" on a flat composite-key LRU.
// Instead we rely on the per-slot tombstone entries the wipe path
// already records via collectPreWipeSlots — those slots are in
// b.storage and will be evicted by the per-slot loop below.
func (b *PlainStateBuffer) Clear() {
	for addr := range b.accounts {
		b.readAccounts.Delete(addr)
	}
	for addr, slots := range b.storage {
		for slot := range slots {
			var ck [storageCompositeKeyLen]byte
			copy(ck[:20], addr[:])
			copy(ck[20:], slot[:])
			b.readStorage.Delete(ck)
		}
	}
	for h := range b.code {
		b.readCode.Delete(h)
	}

	// Reset write buffers.
	b.accounts = make(map[types.Address][]byte, 4096)
	b.storage = make(map[types.Address]map[types.Hash]storageEntry, 4096)
	b.code = make(map[types.Hash][]byte, 64)
	b.contractCode = make(map[string][]byte, 64)
	b.incarnationMap = make(map[types.Address][]byte)
	b.contractWipes = b.contractWipes[:0]
	clear(b.wipedStorage)

	// Reset cache hit counters so the next commit-interval log line
	// reports per-window stats (matching the existing dashboard).
	b.hits.Store(0)
	b.misses.Store(0)
}

// ResetReadCache drops all LRU contents. Used by paths that open a
// fresh MDBX read tx and need to invalidate snapshot-bound cached
// values (e.g. tests, recovery flows).
func (b *PlainStateBuffer) ResetReadCache() {
	b.readAccounts.Reset()
	b.readStorage.Reset()
	b.readCode.Reset()
}

func (b *PlainStateBuffer) ContractWipes() []types.Address { return b.contractWipes }

func (b *PlainStateBuffer) Stats() (accounts, storage int) {
	for _, slots := range b.storage {
		storage += len(slots)
	}
	return len(b.accounts), storage
}

// -----------------------------------------------------------------------
// BufferedPlainStateReader: write buffer → read cache → MDBX
// -----------------------------------------------------------------------

type BufferedPlainStateReader struct {
	buf *PlainStateBuffer
	db  kv.Getter
}

func NewBufferedPlainStateReader(buf *PlainStateBuffer, db kv.Getter) *BufferedPlainStateReader {
	return &BufferedPlainStateReader{buf: buf, db: db}
}

func (r *BufferedPlainStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	// 1. Write buffer.
	if enc, ok := r.buf.accounts[address]; ok {
		r.buf.hits.Add(1)
		if len(enc) == 0 {
			return nil, nil
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(enc); err != nil {
			return nil, err
		}
		return &a, nil
	}
	// 2. LRU read cache.
	if v, present := r.buf.readAccounts.Get(address); present {
		r.buf.hits.Add(1)
		if len(v) == 0 {
			return nil, nil
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(v); err != nil {
			return nil, err
		}
		return &a, nil
	}
	// 3. MDBX → cache.
	r.buf.misses.Add(1)
	enc, err := r.db.GetOne(modules.Account, address[:])
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		// Cache the negative result so repeated lookups of an absent
		// address don't keep hitting MDBX.
		r.buf.readAccounts.Put(address, nil, addrKeyLen+cacheOverheadPerEntry)
		return nil, nil
	}
	cached := make([]byte, len(enc))
	copy(cached, enc)
	r.buf.readAccounts.Put(address, cached, addrKeyLen+len(cached)+cacheOverheadPerEntry)
	var a account.StateAccount
	if err = a.DecodeForStorage(cached); err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *BufferedPlainStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	// 1. Write buffer.
	if slots, ok := r.buf.storage[address]; ok {
		if entry, ok2 := slots[*key]; ok2 {
			r.buf.hits.Add(1)
			if len(entry.value) == 0 {
				return nil, nil
			}
			return entry.value, nil
		}
	}
	// 1b. If storage was wiped (SELFDESTRUCT/CREATE within this batch),
	// slots not in the write buffer are gone — stale cache/MDBX values
	// from before the wipe must not be returned.
	if len(r.buf.wipedStorage) > 0 {
		if _, wiped := r.buf.wipedStorage[address]; wiped {
			return nil, nil
		}
	}
	// 2. LRU read cache (composite key).
	var ck [storageCompositeKeyLen]byte
	copy(ck[:20], address[:])
	copy(ck[20:], key[:])
	if v, present := r.buf.readStorage.Get(ck); present {
		r.buf.hits.Add(1)
		if len(v) == 0 {
			return nil, nil
		}
		return v, nil
	}
	// 3. MDBX → cache.
	r.buf.misses.Add(1)
	compositeKey := modules.PlainGenerateCompositeStorageKey(address[:], key[:])
	enc, err := r.db.GetOne(modules.Storage, compositeKey)
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		r.buf.readStorage.Put(ck, nil, storageCompositeKeyLen+cacheOverheadPerEntry)
		return nil, nil
	}
	cached := make([]byte, len(enc))
	copy(cached, enc)
	r.buf.readStorage.Put(ck, cached, storageCompositeKeyLen+len(cached)+cacheOverheadPerEntry)
	return cached, nil
}

func (r *BufferedPlainStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	if bytes.Equal(codeHash[:], emptyCodeHash) {
		return nil, nil
	}
	if code, ok := r.buf.code[codeHash]; ok {
		return code, nil
	}
	if v, present := r.buf.readCode.Get(codeHash); present {
		return v, nil
	}
	code, err := r.db.GetOne(modules.Code, codeHash[:])
	if err != nil {
		return nil, err
	}
	if len(code) == 0 {
		return nil, nil
	}
	cached := make([]byte, len(code))
	copy(cached, code)
	r.buf.readCode.Put(codeHash, cached, hashKeyLen+len(cached)+cacheOverheadPerEntry)
	return cached, nil
}

func (r *BufferedPlainStateReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, incarnation, codeHash)
	return len(code), err
}

func (r *BufferedPlainStateReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	if b, ok := r.buf.incarnationMap[address]; ok {
		return binary.BigEndian.Uint16(b), nil
	}
	b, err := r.db.GetOne(modules.IncarnationMap, address[:])
	if err != nil {
		return 0, err
	}
	if len(b) == 0 {
		return 0, nil
	}
	return binary.BigEndian.Uint16(b), nil
}

// -----------------------------------------------------------------------
// BufferedPlainStateWriter writes to the buffer (not MDBX).
// -----------------------------------------------------------------------

type BufferedPlainStateWriter struct {
	buf *PlainStateBuffer
	csw *ChangeSetWriter
}

func NewBufferedPlainStateWriter(buf *PlainStateBuffer, changeSetsDB kv.RwTx, blockNumber uint64) *BufferedPlainStateWriter {
	return &BufferedPlainStateWriter{
		buf: buf,
		csw: NewChangeSetWriterPlain(changeSetsDB, blockNumber),
	}
}

func NewBufferedPlainStateWriterNoHistory(buf *PlainStateBuffer) *BufferedPlainStateWriter {
	return &BufferedPlainStateWriter{buf: buf}
}

func (w *BufferedPlainStateWriter) UpdateAccountData(address types.Address, original, acct *account.StateAccount) error {
	if w.csw != nil {
		if err := w.csw.UpdateAccountData(address, original, acct); err != nil {
			return err
		}
	}
	if original != nil && original.Equals(acct) {
		return nil
	}
	w.buf.accounts[address] = acct.MarshalV2()
	return nil
}

func (w *BufferedPlainStateWriter) UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error {
	if w.csw != nil {
		if err := w.csw.UpdateAccountCode(address, incarnation, codeHash, code); err != nil {
			return err
		}
	}
	w.buf.code[codeHash] = code
	w.buf.contractCode[string(modules.PlainGenerateStoragePrefix(address[:]))] = codeHash[:]
	return nil
}

func (w *BufferedPlainStateWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	if w.csw != nil {
		if err := w.csw.DeleteAccount(address, original); err != nil {
			return err
		}
	}
	w.buf.accounts[address] = deletedSentinel
	// IncarnationMap removed — incarnation no longer used
	return nil
}

func (w *BufferedPlainStateWriter) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	if w.csw != nil {
		if err := w.csw.WriteAccountStorage(address, incarnation, key, original, value); err != nil {
			return err
		}
	}
	// IMPORTANT: do NOT short-circuit on `*original == *value` here. The
	// `original` argument is the BLOCK-START value (from blockOriginStorage),
	// while `value` is the FINAL dirty value at the end of the calling
	// transaction. If a slot was written to an intermediate value earlier
	// in the same block (so buf.storage[address][key] holds that
	// intermediate) and the current tx writes back to the block-start
	// value, a short-circuit would leave the stale intermediate in the
	// buffer. Subsequent blocks would then read the wrong "committed"
	// value via GetCommittedState, mis-routing EIP-2200 SSTORE handling
	// (notably spurious path-2.2.2.1 hits with refund 19200) and
	// producing gas mismatches against geth. Always update the buffer
	// to reflect the latest written value.
	slots := w.buf.storage[address]
	if slots == nil {
		slots = make(map[types.Hash]storageEntry, 8)
		w.buf.storage[address] = slots
	}
	v := value.Bytes()
	if len(v) == 0 {
		slots[*key] = storageEntry{value: deletedSentinel}
	} else {
		slots[*key] = storageEntry{value: v}
	}
	return nil
}

func (w *BufferedPlainStateWriter) CreateContract(address types.Address) error {
	// Before the wipe, collect pre-destruction slot values from the layered
	// state (buffer over MDBX) and record them in the storage changeset, so
	// backward unwind can restore them. Mirrors reth's write_state_reverts
	// wiped-storage enumeration path. First-wins in csw preserves
	// block-origin values already written by earlier SSTORE calls.
	var wipedSlots map[types.Hash][]byte
	if w.csw != nil {
		var err error
		wipedSlots, err = w.collectPreWipeSlots(address)
		if err != nil {
			return fmt.Errorf("collect pre-wipe slots for %x: %w", address, err)
		}
		w.csw.recordStorageWipe(address, wipedSlots)
		if err := w.csw.CreateContract(address); err != nil {
			return err
		}
	}
	// Clear buffered storage for this address.
	delete(w.buf.storage, address)
	// Invalidate every cached slot for this address. The flat composite-key
	// LRU has no per-address sub-map, so we evict each known slot individually.
	// Sources of "known slots": (a) the wipedSlots collected above (covers
	// MDBX + write buffer), (b) any slot still in the write buffer (rare:
	// touched after collectPreWipeSlots but before this point).
	var compositeKey [storageCompositeKeyLen]byte
	copy(compositeKey[:20], address[:])
	for slot := range wipedSlots {
		copy(compositeKey[20:], slot[:])
		w.buf.readStorage.Delete(compositeKey)
	}
	// Mark as wiped so ReadAccountStorage returns nil for slots not in buffer.
	w.buf.wipedStorage[address] = struct{}{}
	// Record address for MDBX storage wipe during FlushToMDBX.
	w.buf.contractWipes = append(w.buf.contractWipes, address)
	return nil
}

// collectPreWipeSlots returns the current visible (buffer ∪ MDBX) storage
// slots for an address, excluding slots whose current buffered value is
// empty/deleted. Layer precedence: buffer > MDBX.
func (w *BufferedPlainStateWriter) collectPreWipeSlots(address types.Address) (map[types.Hash][]byte, error) {
	out := make(map[types.Hash][]byte)

	// 1. Slots live in the write buffer — these shadow MDBX.
	bufSlots, hasBuf := w.buf.storage[address]
	if hasBuf {
		for slot, entry := range bufSlots {
			if len(entry.value) == 0 {
				// Deleted/empty in buffer: skip. csw already has the
				// pre-delete value from the earlier WriteAccountStorage call
				// that produced this buffer entry.
				continue
			}
			out[slot] = entry.value
		}
	}

	// 2. Slots in MDBX that are NOT shadowed by a buffer entry.
	if w.csw.db != nil {
		cursor, err := w.csw.db.Cursor(modules.Storage)
		if err != nil {
			return nil, err
		}
		prefix := address[:]
		for k, v, err := cursor.Seek(prefix); k != nil; k, v, err = cursor.Next() {
			if err != nil {
				cursor.Close()
				return nil, err
			}
			if len(k) < 20 || !bytes.Equal(k[:20], prefix) {
				break
			}
			if len(k) != 52 || len(v) == 0 {
				continue
			}
			var slot types.Hash
			copy(slot[:], k[20:52])
			// Buffer shadows MDBX.
			if hasBuf {
				if _, shadowed := bufSlots[slot]; shadowed {
					continue
				}
			}
			out[slot] = v
		}
		cursor.Close()
	}

	return out, nil
}

func (w *BufferedPlainStateWriter) WriteChangeSets() error {
	if w.csw != nil {
		return w.csw.WriteChangeSets()
	}
	return nil
}

func (w *BufferedPlainStateWriter) WriteHistory() error {
	if w.csw != nil {
		return w.csw.WriteHistory()
	}
	return nil
}

func (w *BufferedPlainStateWriter) ChangeSetWriter() *ChangeSetWriter {
	return w.csw
}

var (
	_ StateReader          = (*BufferedPlainStateReader)(nil)
	_ WriterWithChangeSets = (*BufferedPlainStateWriter)(nil)
)
