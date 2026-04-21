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

// DecodeKeyTag returns the tag byte of an MVHashMap key, or an error
// if the key is too short.
func DecodeKeyTag(key []byte) (byte, error) {
	if len(key) == 0 {
		return 0, fmt.Errorf("mv key: empty")
	}
	return key[0], nil
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
// zero if absent. EVM callers always get a concrete value.
func (v *EVMStateView) ReadStorage(addr types.Address, slot types.Hash) (*uint256.Int, error) {
	raw, err := v.view.Get(EncodeStorageKey(addr, slot))
	if err != nil {
		return nil, err
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

// --- Base reader adapters ---

// MVBaseFromStateReader wraps a StateReader (MDBX + PlainStateBuffer
// in production, mock in tests) to satisfy MVBaseReader. Base reads
// are typed: the adapter decodes the tag byte to dispatch to the
// correct StateReader method.
type MVBaseFromStateReader struct {
	r StateReader
}

// NewMVBaseFromStateReader creates an adapter. The underlying
// StateReader must be safe for concurrent Get calls (the 4 prefetch
// workers + N execute workers will call Get concurrently).
// BufferedPlainStateReader's read path uses s3FIFO + sync.Map-backed
// caches and a single underlying RoTx per reader, which is safe for
// concurrent reads.
func NewMVBaseFromStateReader(r StateReader) *MVBaseFromStateReader {
	return &MVBaseFromStateReader{r: r}
}

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
