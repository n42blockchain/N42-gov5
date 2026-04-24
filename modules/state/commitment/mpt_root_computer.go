// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// MPT branch-node stores and Erigon-style hashed state helpers.
// memBranchStore is an in-memory map[prefix]->node used for tests
// and transient builds; mdbxBranchStore wraps a kv.Tx against a
// named branch table for production persistence.
// Both implement the Get/Put contract expected by the HPH/Erigon
// trie loader integration used to produce standard Ethereum roots.

package commitment

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash"
	"sync"

	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/lib/common/empty"
	"github.com/n42blockchain/N42/common/types"
	libcommit "github.com/n42blockchain/N42/lib/commitment"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state"
)

// --- Branch store (memory + MDBX) ---

// memBranchStore is the in-memory branch cache used by mptContext.
//
// Thread-safety: Get and Put are guarded by mu so multiple per-worker
// mptContexts (created by the concurrent factory below) can read from a
// shared store safely. The concurrent PutBranch path typically writes
// to a per-worker etl.Collector instead of this store — see
// concurrentMPTContextFactory — but Get still hits this shared store
// for lookups of branches created earlier in the block.
type memBranchStore struct {
	mu   sync.RWMutex
	data map[string][]byte
}

func newMemBranchStore() *memBranchStore {
	return &memBranchStore{data: make(map[string][]byte)}
}

func (s *memBranchStore) Get(prefix []byte) ([]byte, error) {
	s.mu.RLock()
	d, ok := s.data[string(prefix)]
	s.mu.RUnlock()
	if ok {
		return d, nil
	}
	return nil, nil
}

func (s *memBranchStore) Put(prefix []byte, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(data) == 0 {
		delete(s.data, string(prefix))
	} else {
		cp := make([]byte, len(data))
		copy(cp, data)
		s.data[string(prefix)] = cp
	}
}

func (s *memBranchStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

// mdbxBranchStore reads persisted branch data from MDBX. Like
// PlainStateMPTReader it is NOT goroutine-safe when the underlying tx
// is shared. The concurrent MPT context factory creates one per worker
// via cloneWithTx.
type mdbxBranchStore struct {
	roTx  kv.Tx
	table string
}

func newMDBXBranchStore(table string) *mdbxBranchStore {
	return &mdbxBranchStore{table: table}
}

func (s *mdbxBranchStore) Get(prefix []byte) ([]byte, error) {
	if s.roTx == nil {
		return nil, nil
	}
	data, err := s.roTx.GetOne(s.table, prefix)
	if err != nil || data == nil {
		return nil, err
	}
	cp := make([]byte, len(data))
	copy(cp, data)
	return cp, nil
}

func (s *mdbxBranchStore) SetReadTx(tx kv.Tx) { s.roTx = tx }

// cloneWithTx returns a new mdbxBranchStore bound to a different tx,
// preserving the table name. Used by the concurrent MPT context factory
// so each worker goroutine reads through its own MDBX RoTx.
// If s is nil, returns nil — matches the "no persistence" mode of
// MPTRootComputer (see NewMPTRootComputer vs NewPersistentMPTRootComputer).
func (s *mdbxBranchStore) cloneWithTx(tx kv.Tx) *mdbxBranchStore {
	if s == nil {
		return nil
	}
	return &mdbxBranchStore{roTx: tx, table: s.table}
}

// --- PlainState reader ---

// PlainStateMPTReader reads Account/Storage rows from PlainState. It is
// NOT goroutine-safe when backed by an MDBX kv.Tx because the erigon
// mdbx-go binding is not thread-safe at the tx level. For concurrent
// use, construct one reader per goroutine via Clone(newTx).
type PlainStateMPTReader struct {
	tx kv.Tx
}

func NewPlainStateMPTReader(tx kv.Tx) *PlainStateMPTReader {
	return &PlainStateMPTReader{tx: tx}
}

// Clone returns a new reader with the same semantics but bound to a
// different kv.Tx. Used by the concurrent MPT context factory so each
// worker goroutine has its own independent MDBX RoTx.
func (r *PlainStateMPTReader) Clone(tx kv.Tx) *PlainStateMPTReader {
	return &PlainStateMPTReader{tx: tx}
}

func (r *PlainStateMPTReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	data, err := r.tx.GetOne(modules.Account, addr[:])
	if err != nil || data == nil {
		return nil, err
	}
	var acct account.StateAccount
	if err := acct.DecodeForStorage(data); err != nil {
		return nil, err
	}
	if acct.CodeHash == (types.Hash{}) {
		acct.CodeHash = empty.CodeHash
	}
	return &acct, nil
}

func (r *PlainStateMPTReader) ReadAccountStorage(addr types.Address, slot types.Hash) ([]byte, error) {
	key := modules.PlainGenerateCompositeStorageKey(addr[:], slot[:])
	data, err := r.tx.GetOne(modules.Storage, key)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (r *PlainStateMPTReader) ReadAllStorage(addr types.Address) (map[types.Hash]*uint256.Int, error) {
	prefix := addr[:]

	c, err := r.tx.Cursor(modules.Storage)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	result := make(map[types.Hash]*uint256.Int)
	for k, v, err := c.Seek(prefix); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if len(k) < 20 || !bytes.Equal(k[:20], prefix) {
			break
		}
		if len(k) != 52 {
			continue
		}
		var slot types.Hash
		copy(slot[:], k[20:52])
		val := new(uint256.Int)
		if len(v) > 0 {
			val.SetBytes(v)
		}
		if !val.IsZero() {
			result[slot] = val
		}
	}
	return result, nil
}


// --- Persistence helpers ---

func WriteMPTRoot(tx kv.RwTx, root types.Hash) error {
	return tx.Put(modules.MPTRoot, []byte("root"), root[:])
}

func ReadMPTRoot(tx kv.Tx) (types.Hash, error) {
	data, err := tx.GetOne(modules.MPTRoot, []byte("root"))
	if err != nil {
		return types.Hash{}, err
	}
	if data == nil || len(data) < 32 {
		return types.Hash{}, nil
	}
	var h types.Hash
	copy(h[:], data[:32])
	return h, nil
}

func WriteMPTTrieState(tx kv.RwTx, blockNum uint64, trieState []byte) error {
	buf := make([]byte, 12+len(trieState))
	binary.BigEndian.PutUint64(buf[0:8], blockNum)
	binary.BigEndian.PutUint32(buf[8:12], uint32(len(trieState)))
	copy(buf[12:], trieState)
	return tx.Put(modules.MPTRoot, []byte("trie_state"), buf)
}

func ReadMPTTrieState(tx kv.Tx) (blockNum uint64, trieState []byte, err error) {
	data, err := tx.GetOne(modules.MPTRoot, []byte("trie_state"))
	if err != nil {
		return 0, nil, err
	}
	if data == nil || len(data) < 12 {
		return 0, nil, nil
	}
	blockNum = binary.BigEndian.Uint64(data[0:8])
	stateLen := binary.BigEndian.Uint32(data[8:12])
	if uint32(len(data)-12) < stateLen {
		return 0, nil, fmt.Errorf("mpt trie state truncated")
	}
	trieState = make([]byte, stateLen)
	copy(trieState, data[12:12+stateLen])
	return blockNum, trieState, nil
}

// --- PatriciaContext implementation ---

// mptContext implements libcommit.PatriciaContext backed by mem/MDBX branch store
// and PlainState account/storage reader.
//
// When `collector` is non-nil (set by the concurrent factory), PutBranch
// writes go to the per-worker collector instead of the shared mem store.
// After the parallel phase the main goroutine drains all collectors and
// replays them into the real mem (see NewConcurrentMPTRootComputer).
// For sequential use, `collector` is nil and PutBranch writes directly
// to mem as before.
type mptContext struct {
	mem       *memBranchStore
	persist   *mdbxBranchStore
	reader    *PlainStateMPTReader
	collector *etl.Collector // non-nil only in concurrent-worker mode
}

func (c *mptContext) Branch(prefix []byte) ([]byte, kv.Step, error) {
	// Goroutine-safe read from mem (mutex-protected).
	if d, err := c.mem.Get(prefix); err != nil {
		return nil, 0, err
	} else if d != nil {
		return d, 0, nil
	}
	if c.persist != nil {
		data, err := c.persist.Get(prefix)
		return data, 0, err
	}
	return nil, 0, nil
}

func (c *mptContext) PutBranch(prefix []byte, data []byte, prevData []byte) error {
	// Concurrent path: write to per-worker collector. Main thread will
	// drain and merge into mem after ParallelHashSort completes.
	if c.collector != nil {
		return c.collector.Collect(prefix, data)
	}
	// Sequential path: write directly to mem.
	if len(data) == 0 {
		c.mem.Put(prefix, nil)
	} else {
		c.mem.Put(prefix, data)
	}
	return nil
}

func (c *mptContext) Account(plainKey []byte) (*libcommit.Update, error) {
	if c.reader == nil || len(plainKey) < 20 {
		return &libcommit.Update{Flags: libcommit.DeleteUpdate}, nil
	}
	var addr types.Address
	copy(addr[:], plainKey[:20])
	acct, err := c.reader.ReadAccountData(addr)
	if err != nil {
		return nil, err
	}
	if acct == nil {
		return &libcommit.Update{Flags: libcommit.DeleteUpdate}, nil
	}
	u := &libcommit.Update{
		Flags:    libcommit.BalanceUpdate | libcommit.NonceUpdate | libcommit.CodeUpdate,
		Nonce:    acct.Nonce,
		CodeHash: acct.CodeHash,
	}
	u.Balance.Set(&acct.Balance)
	if acct.CodeHash == (types.Hash{}) || acct.CodeHash == empty.CodeHash {
		u.CodeHash = empty.CodeHash
	}
	return u, nil
}

func (c *mptContext) Storage(plainKey []byte) (*libcommit.Update, error) {
	if c.reader == nil || len(plainKey) < 52 {
		return &libcommit.Update{Flags: libcommit.DeleteUpdate}, nil
	}
	var addr types.Address
	copy(addr[:], plainKey[:20])
	var slot types.Hash
	copy(slot[:], plainKey[20:52])
	val, err := c.reader.ReadAccountStorage(addr, slot)
	if err != nil {
		return nil, err
	}
	if len(val) == 0 {
		return &libcommit.Update{Flags: libcommit.DeleteUpdate}, nil
	}
	u := &libcommit.Update{
		Flags:      libcommit.StorageUpdate,
		StorageLen: int8(len(val)),
	}
	copy(u.Storage[:], val)
	return u, nil
}

func (c *mptContext) TxNum() uint64 { return 0 }

// --- MPTRootComputer ---

type MPTRootComputer struct {
	trie    *libcommit.HexPatriciaHashed
	ctx     *mptContext
	mem     *memBranchStore
	persist *mdbxBranchStore
	updates *libcommit.Updates // reused across ComputeRoot calls
	hasher  func([]byte) []byte
}

func NewMPTRootComputer() *MPTRootComputer {
	return newMPTRootComputer(nil)
}

func NewPersistentMPTRootComputer() *MPTRootComputer {
	persist := newMDBXBranchStore(modules.MPTBranch)
	return newMPTRootComputer(persist)
}

func newMPTRootComputer(persist *mdbxBranchStore) *MPTRootComputer {
	mem := newMemBranchStore()
	ctx := &mptContext{mem: mem, persist: persist}
	m := &MPTRootComputer{
		mem:     mem,
		persist: persist,
		ctx:     ctx,
		trie: libcommit.NewHexPatriciaHashed(int16(length.Addr), ctx),
	}
	// Reuse a single keccak hasher across calls (closure captures it).
	m.hasher = makeSharedHasher()
	return m
}

// makeSharedHasher returns a hasher closure suitable for libcommit.Updates.
// The closure captures a single keccak state and rehashes; callers that
// need goroutine-local hashers must construct their own via this helper.
func makeSharedHasher() func([]byte) []byte {
	keccak := sha3.NewLegacyKeccak256()
	return func(key []byte) []byte {
		if len(key) == length.Addr {
			return keccakToNibbles(keccak, key)
		}
		return storageKeccakToNibbles(keccak, key[:length.Addr], key[length.Addr:])
	}
}

func (m *MPTRootComputer) SetStateReader(r *PlainStateMPTReader) {
	m.ctx.reader = r
}

func (m *MPTRootComputer) SetReadTx(tx kv.Tx) {
	if m.persist != nil {
		m.persist.SetReadTx(tx)
	}
}

func (m *MPTRootComputer) Trie() *libcommit.HexPatriciaHashed {
	return m.trie
}

func (m *MPTRootComputer) BranchCount() int {
	return m.mem.Len()
}

func (m *MPTRootComputer) ResetTrie() {
	m.trie.Reset()
}

func (m *MPTRootComputer) RootScheme() state.RootScheme {
	return state.RootSchemeEthereumMPT
}

func (m *MPTRootComputer) FlushBranches(tx kv.RwTx) error {
	for k, v := range m.mem.data {
		if len(v) == 0 {
			if err := tx.Delete(modules.MPTBranch, []byte(k)); err != nil {
				return err
			}
		} else {
			if err := tx.Put(modules.MPTBranch, []byte(k), v); err != nil {
				return err
			}
		}
	}
	// Do NOT clear mem.data — HPH needs branch data for subsequent
	// unfold operations across batch boundaries.
	return nil
}

func (m *MPTRootComputer) EncodeTrieState() ([]byte, error) {
	return m.trie.EncodeCurrentState(nil)
}

func (m *MPTRootComputer) RestoreTrieState(trieState []byte) error {
	if len(trieState) == 0 {
		return nil
	}
	return m.trie.SetState(trieState)
}

func (m *MPTRootComputer) SaveCheckpoint(tx kv.RwTx, blockNum uint64, root types.Hash) error {
	if err := m.FlushBranches(tx); err != nil {
		return fmt.Errorf("flush branches: %w", err)
	}
	if err := WriteMPTRoot(tx, root); err != nil {
		return fmt.Errorf("write root: %w", err)
	}
	trieState, err := m.EncodeTrieState()
	if err != nil {
		return fmt.Errorf("encode trie state: %w", err)
	}
	if err := WriteMPTTrieState(tx, blockNum, trieState); err != nil {
		return fmt.Errorf("write trie state: %w", err)
	}
	return nil
}

// --- ComputeRoot using Erigon's Process() API ---

// ComputeRoot computes the MPT state root from dirty accounts+storage.
// Uses Erigon's HexPatriciaHashed Process() which achieves O(dirty) per call
// via stateHash memoization and PatriciaContext lazy loading.
func (m *MPTRootComputer) ComputeRoot(
	accounts map[types.Address]*account.StateAccount,
	storage map[types.Address]map[types.Hash]*uint256.Int,
) (types.Hash, error) {
	// Build Updates for HPH Process() using ModeUpdate (btree, no ETL alloc).
	// TouchPlainKey with TouchAccount/TouchStorage callbacks that decode
	// the encoded account/storage bytes and set Update fields.
	if m.updates == nil {
		m.updates = libcommit.NewUpdates(libcommit.ModeUpdate, "", m.hasher)
	} else {
		m.updates.Reset()
	}

	for addr, acct := range accounts {
		pk := string(addr.Bytes())
		if acct == nil || isAccountEmpty(acct) {
			m.updates.TouchPlainKey(pk, nil, m.updates.TouchAccount)
			continue
		}
		enc := acct.MarshalV2()
		m.updates.TouchPlainKey(pk, enc, m.updates.TouchAccount)
	}

	for addr, slots := range storage {
		for slot, val := range slots {
			var pk [52]byte
			copy(pk[:20], addr[:])
			copy(pk[20:], slot[:])
			if val == nil || val.IsZero() {
				m.updates.TouchPlainKey(string(pk[:]), nil, m.updates.TouchStorage)
			} else {
				b := val.Bytes32()
				start := 0
				for start < 31 && b[start] == 0 {
					start++
				}
				m.updates.TouchPlainKey(string(pk[:]), b[start:], m.updates.TouchStorage)
			}
		}
	}

	if m.updates.Size() == 0 {
		root, err := m.trie.RootHash()
		if err != nil {
			return types.Hash{}, err
		}
		var h types.Hash
		copy(h[:], root)
		return h, nil
	}

	root, err := m.trie.Process(context.Background(), m.updates, "", nil, libcommit.WarmupConfig{})
	if err != nil {
		return types.Hash{}, err
	}

	var h types.Hash
	copy(h[:], root)
	return h, nil
}

// --- Keccak helpers ---

func keccakToNibbles(hasher hash.Hash, data []byte) []byte {
	hasher.Reset()
	hasher.Write(data)
	hash := hasher.Sum(nil)
	nibbles := make([]byte, len(hash)*2)
	for i, b := range hash {
		nibbles[i*2] = b >> 4
		nibbles[i*2+1] = b & 0x0f
	}
	return nibbles
}

func storageKeccakToNibbles(hasher hash.Hash, addr, slot []byte) []byte {
	hasher.Reset()
	hasher.Write(addr)
	addrHash := hasher.Sum(nil)
	hasher.Reset()
	hasher.Write(slot)
	slotHash := hasher.Sum(nil)
	nibbles := make([]byte, 128)
	for i, b := range addrHash {
		nibbles[i*2] = b >> 4
		nibbles[i*2+1] = b & 0x0f
	}
	for i, b := range slotHash {
		nibbles[64+i*2] = b >> 4
		nibbles[64+i*2+1] = b & 0x0f
	}
	return nibbles
}

