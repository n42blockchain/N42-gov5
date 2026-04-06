// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"encoding/binary"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

var deletedSentinel = []byte{}

type storageEntry struct {
	incarnation uint16
	value       []byte
}

// accountCacheEntry wraps a cached account. The sync.Map stores interface{},
// so we use a pointer type. nil *accountCacheEntry = key exists but absent.
type accountCacheEntry struct {
	acct *account.StateAccount
}

// PlainStateBuffer holds write buffer + lock-free read cache.
// Read cache uses sync.Map for zero-lock reads on the hot EVM path.
type PlainStateBuffer struct {
	// Write buffer (single-writer, executor only).
	accounts       map[types.Address][]byte
	storage        map[types.Address]map[types.Hash]storageEntry
	code           map[types.Hash][]byte
	contractCode   map[string][]byte
	incarnationMap map[types.Address][]byte

	// Read cache: lock-free via sync.Map.
	// readAccounts: key=types.Address, value=*accountCacheEntry (nil acct = absent)
	// readStorage:  key=types.Address, value=*sync.Map (inner key=types.Hash, value=[]byte, nil=absent)
	// readCode:     key=types.Hash, value=[]byte
	readAccounts sync.Map
	readStorage  sync.Map
	readCode     sync.Map

	hits   atomic.Uint64
	misses atomic.Uint64
}

func NewPlainStateBuffer() *PlainStateBuffer {
	return &PlainStateBuffer{
		accounts:       make(map[types.Address][]byte, 4096),
		storage:        make(map[types.Address]map[types.Hash]storageEntry, 4096),
		code:           make(map[types.Hash][]byte, 64),
		contractCode:   make(map[string][]byte, 64),
		incarnationMap: make(map[types.Address][]byte),
	}
}

func (b *PlainStateBuffer) CacheStats() (hits, misses uint64) {
	return b.hits.Load(), b.misses.Load()
}

// CacheAccount populates the read cache (used by prefetcher, lock-free).
func (b *PlainStateBuffer) CacheAccount(address types.Address, acct *account.StateAccount) {
	b.readAccounts.Store(address, &accountCacheEntry{acct: acct})
}

// CacheStorage populates the storage read cache (used by prefetcher, lock-free).
func (b *PlainStateBuffer) CacheStorage(address types.Address, key types.Hash, value []byte) {
	slotsI, _ := b.readStorage.LoadOrStore(address, &sync.Map{})
	slotsI.(*sync.Map).Store(key, value)
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

	// 2. Storage: build composite keys, sort, then write.
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
			compositeKey := modules.PlainGenerateCompositeStorageKey(addr[:], entry.incarnation, hash[:])
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

	// 3. Code, ContractCode, IncarnationMap — small tables, sort for consistency.
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

	incAddrs := make([]types.Address, 0, len(b.incarnationMap))
	for addr := range b.incarnationMap {
		incAddrs = append(incAddrs, addr)
	}
	sort.Slice(incAddrs, func(i, j int) bool {
		return bytes.Compare(incAddrs[i][:], incAddrs[j][:]) < 0
	})
	for _, addr := range incAddrs {
		if err := tx.Put(modules.IncarnationMap, addr[:], b.incarnationMap[addr]); err != nil {
			return err
		}
	}
	return nil
}

// Clear resets write buffer. Invalidates read cache entries for written keys.
func (b *PlainStateBuffer) Clear() {
	for addr := range b.accounts {
		b.readAccounts.Delete(addr)
	}
	for addr, slots := range b.storage {
		if slotsI, ok := b.readStorage.Load(addr); ok {
			sm := slotsI.(*sync.Map)
			for hash := range slots {
				sm.Delete(hash)
			}
		}
	}
	b.accounts = make(map[types.Address][]byte, 4096)
	b.storage = make(map[types.Address]map[types.Hash]storageEntry, 4096)
	b.code = make(map[types.Hash][]byte, 64)
	b.contractCode = make(map[string][]byte, 64)
	b.incarnationMap = make(map[types.Address][]byte)
	b.hits.Store(0)
	b.misses.Store(0)
}

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
	// 2. Read cache (lock-free).
	if v, ok := r.buf.readAccounts.Load(address); ok {
		r.buf.hits.Add(1)
		return v.(*accountCacheEntry).acct, nil
	}
	// 3. MDBX → cache.
	r.buf.misses.Add(1)
	enc, err := r.db.GetOne(modules.Account, address[:])
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		r.buf.readAccounts.Store(address, &accountCacheEntry{acct: nil})
		return nil, nil
	}
	var a account.StateAccount
	if err = a.DecodeForStorage(enc); err != nil {
		return nil, err
	}
	r.buf.readAccounts.Store(address, &accountCacheEntry{acct: &a})
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
	// 2. Read cache (lock-free).
	if slotsI, ok := r.buf.readStorage.Load(address); ok {
		if val, ok2 := slotsI.(*sync.Map).Load(*key); ok2 {
			r.buf.hits.Add(1)
			if val == nil {
				return nil, nil
			}
			return val.([]byte), nil
		}
	}
	// 3. MDBX → cache.
	r.buf.misses.Add(1)
	compositeKey := modules.PlainGenerateCompositeStorageKey(address[:], incarnation, key[:])
	enc, err := r.db.GetOne(modules.Storage, compositeKey)
	if err != nil {
		return nil, err
	}
	slotsI, _ := r.buf.readStorage.LoadOrStore(address, &sync.Map{})
	if len(enc) == 0 {
		slotsI.(*sync.Map).Store(*key, nil)
		return nil, nil
	}
	cached := make([]byte, len(enc))
	copy(cached, enc)
	slotsI.(*sync.Map).Store(*key, cached)
	return cached, nil
}

func (r *BufferedPlainStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	if bytes.Equal(codeHash[:], emptyCodeHash) {
		return nil, nil
	}
	if code, ok := r.buf.code[codeHash]; ok {
		return code, nil
	}
	if v, ok := r.buf.readCode.Load(codeHash); ok {
		return v.([]byte), nil
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
	r.buf.readCode.Store(codeHash, cached)
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
	w.buf.contractCode[string(modules.PlainGenerateStoragePrefix(address[:], incarnation))] = codeHash[:]
	return nil
}

func (w *BufferedPlainStateWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	if w.csw != nil {
		if err := w.csw.DeleteAccount(address, original); err != nil {
			return err
		}
	}
	w.buf.accounts[address] = deletedSentinel
	if original.Incarnation > 0 {
		var b [2]byte
		binary.BigEndian.PutUint16(b[:], original.Incarnation)
		w.buf.incarnationMap[address] = b[:]
	}
	return nil
}

func (w *BufferedPlainStateWriter) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	if w.csw != nil {
		if err := w.csw.WriteAccountStorage(address, incarnation, key, original, value); err != nil {
			return err
		}
	}
	if *original == *value {
		return nil
	}
	slots := w.buf.storage[address]
	if slots == nil {
		slots = make(map[types.Hash]storageEntry, 8)
		w.buf.storage[address] = slots
	}
	v := value.Bytes()
	if len(v) == 0 {
		slots[*key] = storageEntry{incarnation: incarnation, value: deletedSentinel}
	} else {
		slots[*key] = storageEntry{incarnation: incarnation, value: v}
	}
	return nil
}

func (w *BufferedPlainStateWriter) CreateContract(address types.Address) error {
	if w.csw != nil {
		if err := w.csw.CreateContract(address); err != nil {
			return err
		}
	}
	return nil
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
