// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// mv_evm_adapter.go: EVM-oriented API layered on MVStateView.
//
// Phase 3 bridge between the Block-STM primitives (MVHashMap +
// MVStateView + Scheduler) and the existing state.StateReader /
// StateWriter contract that n42's EVM expects. The raw MVStateView
// speaks in []byte keys; this file provides typed Account / Storage /
// Code methods that encode/decode keys and values to match what EVM
// code reads and writes.
//
// Key encoding (compact and unambiguous):
//
//   Account: tag(1B=0) + addr(20B)        = 21 B
//   Storage: tag(1B=1) + addr(20B) + slot(32B) = 53 B
//   Code:    tag(1B=2) + codeHash(32B)    = 33 B
//
// The tag byte prevents collision between different key classes (e.g.
// 52-byte storage composite vs 32-byte code hash can't alias).
//
// This adapter keeps MVBaseReader small (one Get method). Callers that
// want the sequential state reader (MDBX + PlainStateBuffer) as the
// base store use MVBaseFromStateReader which wraps a StateReader.

package state

import (
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// --- Key encoding ---

const (
	mvKeyTagAccount byte = 0
	mvKeyTagStorage byte = 1
	mvKeyTagCode    byte = 2
	// mvKeyTagWipe marks an entire address's storage as wiped by a
	// SELFDESTRUCT (or CREATE-on-existing-addr metamorphism). When a
	// reader of (addr, slot) at txIdx=R encounters a wipe entry from
	// some tx_w < R that is NEWER than the highest slot writer < R,
	// the read returns 0 — the wipe shadows any pre-wipe slot value.
	// Pre-Cancun semantics; post-Cancun (EIP-6780) only same-tx CREATE
	// triggers the storage wipe path.
	mvKeyTagWipe byte = 3
)

// EncodeAccountKey returns the MVHashMap key for an account.
func EncodeAccountKey(addr types.Address) []byte {
	b := make([]byte, 1+20)
	b[0] = mvKeyTagAccount
	copy(b[1:], addr[:])
	return b
}

// EncodeStorageKey returns the MVHashMap key for a storage slot.
func EncodeStorageKey(addr types.Address, slot types.Hash) []byte {
	b := make([]byte, 1+20+32)
	b[0] = mvKeyTagStorage
	copy(b[1:21], addr[:])
	copy(b[21:], slot[:])
	return b
}

// EncodeCodeKey returns the MVHashMap key for a code hash.
func EncodeCodeKey(codeHash types.Hash) []byte {
	b := make([]byte, 1+32)
	b[0] = mvKeyTagCode
	copy(b[1:], codeHash[:])
	return b
}

// EncodeWipeKey returns the MVHashMap key for an address-level
// storage wipe marker (SELFDESTRUCT / CREATE-on-existing-addr).
func EncodeWipeKey(addr types.Address) []byte {
	b := make([]byte, 1+20)
	b[0] = mvKeyTagWipe
	copy(b[1:], addr[:])
	return b
}

// DecodeKeyTag returns the tag byte of an MVHashMap key, or an error
// if the key is too short.
func DecodeKeyTag(key []byte) (byte, error) {
	if len(key) == 0 {
		return 0, fmt.Errorf("mv key: empty")
	}
	return key[0], nil
}

// DecodeAccountKey reports whether key is an account key and, if so, the address.
// Read-only inverse of EncodeAccountKey, exposed for out-of-consensus consumers
// (e.g. EIP-7928 block-access-list harvesting) to classify MVHashMap keys.
func DecodeAccountKey(key []byte) (types.Address, bool) {
	if len(key) != 1+20 || key[0] != mvKeyTagAccount {
		return types.Address{}, false
	}
	var a types.Address
	copy(a[:], key[1:])
	return a, true
}

// DecodeStorageKey reports whether key is a storage key and, if so, the address
// and slot. Read-only inverse of EncodeStorageKey.
func DecodeStorageKey(key []byte) (types.Address, types.Hash, bool) {
	if len(key) != 1+20+32 || key[0] != mvKeyTagStorage {
		return types.Address{}, types.Hash{}, false
	}
	var a types.Address
	var s types.Hash
	copy(a[:], key[1:21])
	copy(s[:], key[21:])
	return a, s, true
}

// --- Typed view API ---

// EVMStateView wraps an MVStateView with account / storage / code
// accessors that match n42's state.StateReader + StateWriter contract.
// Each call encodes the appropriate MV key and delegates to the
// underlying view for dependency tracking + write buffering.
//
// One EVMStateView belongs to a single (txIdx, incarnation) — never
// share across workers. Lifetime ends when the worker flushes or
// discards the view.
type EVMStateView struct {
	view *MVStateView
}

// NewEVMStateView constructs a typed view.
func NewEVMStateView(view *MVStateView) *EVMStateView {
	return &EVMStateView{view: view}
}

// Inner returns the underlying MVStateView for scheduler interaction
// (AbortPending, FlushWrites, Validate, ...).
func (v *EVMStateView) Inner() *MVStateView { return v.view }

// ReadAccount returns the account at addr, or nil if absent. Returns
// nil+true for "account exists but is empty"? No — we mirror
// StateReader semantics: nil means absent (caller treats as EOA with
// zero balance/nonce).
func (v *EVMStateView) ReadAccount(addr types.Address) (*account.StateAccount, error) {
	enc, err := v.view.Get(EncodeAccountKey(addr))
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		return nil, nil
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(enc); err != nil {
		return nil, fmt.Errorf("decode account %x: %w", addr, err)
	}
	return &a, nil
}

// WriteAccount writes the encoded account. Passing nil deletes.
func (v *EVMStateView) WriteAccount(addr types.Address, a *account.StateAccount) {
	if a == nil {
		v.view.Set(EncodeAccountKey(addr), nil)
		return
	}
	v.view.Set(EncodeAccountKey(addr), a.MarshalV2())
}

// ReadStorage returns the slot value at (addr, slot) as a uint256.Int,
// zero if absent.
//
// Wipe semantics: if a SELFDESTRUCT wipe marker (mvKeyTagWipe) exists
// for `addr` from some tx_w < readerTxIdx, AND that wipe's txIdx is
// greater than the highest direct-slot writer's txIdx, the slot is
// treated as zero (the wipe shadows older slot values). This avoids
// per-slot enumeration on SELFDESTRUCT — the wipe acts as a single
// "address invalidated at tx_w" marker.
//
// Both reads (slot AND wipe) enter the readSet so validation can
// detect either being invalidated by a later commit.
func (v *EVMStateView) ReadStorage(addr types.Address, slot types.Hash) (*uint256.Int, error) {
	raw, err := v.view.Get(EncodeStorageKey(addr, slot))
	if err != nil {
		return nil, err
	}
	// Determine which writer (if any) provided `raw`. The view's
	// readSet records this; the most recent entry is the slot read.
	var slotWriter Version
	var slotFromMV bool
	if rs := v.view.ReadSet(); len(rs) > 0 {
		last := rs[len(rs)-1]
		if last.Source == ReadFromMV {
			slotWriter = last.WriterVer
			slotFromMV = true
		}
	}

	// Probe wipe marker. Same Get call goes through readSet so a
	// later commit-wipe invalidates this read.
	wipeRaw, err := v.view.Get(EncodeWipeKey(addr))
	if err != nil {
		return nil, err
	}
	if len(wipeRaw) > 0 {
		// A wipe is visible. Compare its txIdx to the slot writer's.
		var wipeWriter Version
		if rs := v.view.ReadSet(); len(rs) > 0 {
			last := rs[len(rs)-1]
			if last.Source == ReadFromMV {
				wipeWriter = last.WriterVer
			}
		}
		// Wipe overrides if:
		//   - no slot writer in MV (slot came from base — definitely
		//     pre-wipe data), OR
		//   - wipe writer's txIdx > slot writer's txIdx.
		if !slotFromMV || wipeWriter.TxIdx > slotWriter.TxIdx {
			return new(uint256.Int), nil
		}
	}

	val := new(uint256.Int)
	if len(raw) > 0 {
		val.SetBytes(raw)
	}
	return val, nil
}

// WriteStorage writes the slot value. Zero value is encoded as nil
// (matching how MDBX plain state skips zero slots).
func (v *EVMStateView) WriteStorage(addr types.Address, slot types.Hash, val *uint256.Int) {
	key := EncodeStorageKey(addr, slot)
	if val == nil || val.IsZero() {
		v.view.Set(key, nil)
		return
	}
	v.view.Set(key, val.Bytes())
}

// ReadCode returns bytecode for codeHash, or nil if absent.
func (v *EVMStateView) ReadCode(codeHash types.Hash) ([]byte, error) {
	return v.view.Get(EncodeCodeKey(codeHash))
}

// WriteCode stores bytecode (content-addressed by codeHash).
func (v *EVMStateView) WriteCode(codeHash types.Hash, code []byte) {
	v.view.Set(EncodeCodeKey(codeHash), code)
}

// WipeAddress marks `addr`'s entire storage as wiped at this tx's
// (txIdx, incarnation). All concurrent / later readers of any slot of
// `addr` whose direct-slot writer is older than this wipe will see 0
// instead of the stale value. Use for SELFDESTRUCT (pre-Cancun) and
// CREATE-on-existing-addr metamorphism.
//
// The wipe is a single MV write per addr (cheap), regardless of how
// many slots the contract holds — avoids the per-slot enumeration
// the sequential PlainStateBuffer.collectPreWipeSlots needs.
//
// Note: wiping an addr does NOT delete the account record — caller
// is expected to also call WriteAccount(addr, nil) (or write a fresh
// account for re-creation in the same tx).
func (v *EVMStateView) WipeAddress(addr types.Address) {
	// Marker value is a non-empty byte (0x01) so MVHashMap.Read
	// returns "found"; the value content is irrelevant — its
	// presence is what shadows storage.
	v.view.Set(EncodeWipeKey(addr), []byte{0x01})
}

// IsAddressWiped reports whether the in-flight tx's view sees a wipe
// for addr from any earlier committed tx_w < this tx's idx.
func (v *EVMStateView) IsAddressWiped(addr types.Address) bool {
	wipeRaw, _ := v.view.Get(EncodeWipeKey(addr))
	return len(wipeRaw) > 0
}

// --- Base reader adapters ---

// MVBaseFromStateReader wraps a StateReader (MDBX + PlainStateBuffer
// in production, mock in tests) to satisfy MVBaseReader. Base reads
// are typed: the adapter decodes the tag byte to dispatch to the
// correct StateReader method.
//
// IMPORTANT thread-safety note: instances of MVBaseFromStateReader are
// NOT designed to be shared across goroutines when the underlying
// StateReader is backed by an MDBX RoTx — the erigon mdbx-go binding
// is not goroutine-safe. For parallel execution, use MVBaseFactory
// (see below) to construct one MVBaseFromStateReader per worker,
// each with its own kv.Tx.
type MVBaseFromStateReader struct {
	r StateReader
}

// NewMVBaseFromStateReader creates an adapter. Single-goroutine use
// only when backed by MDBX. Tests backed by in-memory map readers
// (MapBaseReader) can share freely.
func NewMVBaseFromStateReader(r StateReader) *MVBaseFromStateReader {
	return &MVBaseFromStateReader{r: r}
}

// MVBaseFactory is a constructor + disposer pair.
// The caller invokes the factory once per parallel worker to obtain
// a fresh MVBaseReader (backed by that worker's own kv.Tx), and then
// invokes the returned closer when the worker exits to release the
// underlying MDBX RoTx.
//
// Mirrors erigon/execution/commitment's TrieContextFactory pattern.
// This is the correct way to plumb per-goroutine state IO through
// Block-STM without tripping mdbx-go's cgo goroutine-safety checks.
type MVBaseFactory func() (MVBaseReader, func())

// Get implements MVBaseReader. Dispatches on the key tag.
func (b *MVBaseFromStateReader) Get(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("mv base: empty key")
	}
	switch key[0] {
	case mvKeyTagAccount:
		if len(key) != 21 {
			return nil, fmt.Errorf("mv base: account key len=%d want 21", len(key))
		}
		var addr types.Address
		copy(addr[:], key[1:])
		acct, err := b.r.ReadAccountData(addr)
		if err != nil {
			return nil, err
		}
		if acct == nil {
			return nil, nil
		}
		return acct.MarshalV2(), nil
	case mvKeyTagStorage:
		if len(key) != 53 {
			return nil, fmt.Errorf("mv base: storage key len=%d want 53", len(key))
		}
		var addr types.Address
		var slot types.Hash
		copy(addr[:], key[1:21])
		copy(slot[:], key[21:])
		return b.r.ReadAccountStorage(addr, &slot)
	case mvKeyTagCode:
		if len(key) != 33 {
			return nil, fmt.Errorf("mv base: code key len=%d want 33", len(key))
		}
		var codeHash types.Hash
		copy(codeHash[:], key[1:])
		// BufferedPlainStateReader.ReadAccountCode wants an address
		// too, but we only have the code hash. Code is content-
		// addressed in modules.Code; a zero address works because the
		// reader goes through the Code table by codeHash. Keeping
		// this clean requires a separate code-only reader — for now
		// we use zero addr and rely on the code lookup to be
		// address-independent.
		return b.r.ReadAccountCode(types.Address{}, codeHash)
	case mvKeyTagWipe:
		// Base store has no concept of an "address wipe" — wipes are
		// in-block markers maintained only in MVHashMap. Return nil
		// (no wipe visible from base) so MV.Read falls through cleanly
		// when no in-block tx has wiped this addr.
		return nil, nil
	}
	// Fall through: unknown tag. Treat as opaque passthrough (tests
	// use raw keys without tags for simplicity).
	return nil, fmt.Errorf("mv base: unknown key tag %d", key[0])
}

// --- Small helpers ---

// ValidateStorageKey returns nil if the key parses as a storage key,
// else a descriptive error. Useful for test assertions.
func ValidateStorageKey(key []byte) error {
	if len(key) != 53 || key[0] != mvKeyTagStorage {
		return fmt.Errorf("not a storage key: len=%d tag=%d", len(key), key[0])
	}
	return nil
}

// asUint64Hex formats a uint64 as a 16-char lowercase hex string.
// Used internally for compact log formatting where strconv is not
// imported. Keeping it private; callers should use fmt.
func asUint64Hex(v uint64) [16]byte {
	var b [16]byte
	const hex = "0123456789abcdef"
	for i := 15; i >= 0; i-- {
		b[i] = hex[v&0xf]
		v >>= 4
	}
	return b
}

var _ = binary.BigEndian // keep import in case of future block-number keys
