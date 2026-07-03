// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// hashed_readonly.go — reth-2.2-style hashed-canonical state reader.
//
// HashedStateReader serves EVM execution reads directly from the hashed
// state tables (HashedAccounts / HashedStorage), hashing the lookup key on
// each access (keccak256(addr), keccak256(slot)). This lets a node store
// state ONLY in hashed form (no PlainState), halving state storage — the
// same architecture reth 2.2 uses by default (storage_v2 / hashed-canonical).
//
// It reads the LATEST hashed state (the tip), with no change-set/history
// overlay: appropriate for tip-following execution where the migrated
// hashed state already equals the parent block's post-state. Historical
// (state-as-of) reads are out of scope here.
//
// codeHash is carried inside the account leaf (HashedAccounts value), so
// code is fetched from modules.Code by codeHash exactly as the plain reader
// does — no plain address needed.

package state

import (
	"bytes"
	"os"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// codeTrace, when N42_CODETRACE is set, logs any EVM code read where the account
// carries a non-empty codeHash but modules.Code returns empty (missing bytecode)
// or bytecode whose keccak ≠ codeHash (wrong bytecode). Such reads make a CALL
// execute against empty code → under-counted gas + divergent state, while the
// state root (which only commits the codeHash in the account leaf, not the code
// bytes) still matches. Used to pin block-161-class gas under-counts.
var codeTrace = os.Getenv("N42_CODETRACE") != ""

// HashedCanonicalWriter is the commit writer for hashed-canonical mode. It does
// two things on top of the forward state (which the incremental IntermediateRoot
// writes to HashedAccounts/HashedStorage — NOT this writer):
//
//  1. CODE: persists newly-created contract code + EIP-7702 delegation
//     designators to modules.Code. Code is NOT part of the trie, so without this
//     a later block CALLing a freshly-deployed/delegated account reads empty code
//     and diverges in gas + state. Written RAW (reader's codeFromTable returns it
//     via keccak-match → consistent with the migration's raw entries; mirrors
//     reth's code_by_hash).
//
//  2. CHANGESETS: it embeds a ChangeSetWriter, so WriteChangeSets/WriteHistory
//     record per-block account/storage pre-values to AccountChangeSet/
//     StorageChangeSet (+ the history index), reth-style and plain-keyed. This
//     enables fast UNWIND (revert a block by applying its changeset in reverse)
//     — required for live-mode reorgs and a far cheaper reset than re-migrating.
//     The forward account/storage WRITES stay with IntermediateRoot; the embedded
//     writer only records the reverse-diff, never the plain forward state.
type HashedCanonicalWriter struct {
	*ChangeSetWriter
	tx kv.RwTx
}

// NewHashedCanonicalWriter builds the code-persisting + changeset-recording
// writer for hashed-canonical commit at blockNum.
func NewHashedCanonicalWriter(tx kv.RwTx, blockNum uint64) *HashedCanonicalWriter {
	return &HashedCanonicalWriter{
		ChangeSetWriter: NewChangeSetWriterPlain(tx, blockNum),
		tx:              tx,
	}
}

func (w *HashedCanonicalWriter) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	if len(code) == 0 {
		return nil
	}
	return w.tx.Put(modules.Code, codeHash[:], code)
}

// HashedStateReader implements StateReader against HashedAccounts/HashedStorage.
type HashedStateReader struct {
	tx      kv.Tx
	codeSrc CodeSource // optional codes.cdat fast path (same as PlainState)
	cache   *HashedReadCache
	trace   bool
}

func NewHashedStateReader(tx kv.Tx) *HashedStateReader {
	return &HashedStateReader{tx: tx}
}

func (r *HashedStateReader) SetCodeSource(c CodeSource) { r.codeSrc = c }
func (r *HashedStateReader) SetTrace(t bool)            { r.trace = t }

// SetCache attaches the cross-block read cache (see HashedReadCache). The
// reader itself is per-block; the cache outlives it. nil = uncached.
func (r *HashedStateReader) SetCache(c *HashedReadCache) { r.cache = c }

func (r *HashedStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	addrHash := r.cache.AddrHash(address) // memoized keccak; plain keccak when cache nil
	if enc, present, ok := r.cache.GetAccount(addrHash); ok {
		if !present {
			return nil, nil
		}
		var a account.StateAccount
		if err := a.DecodeForStorage(enc); err != nil {
			return nil, err
		}
		return &a, nil
	}
	enc, err := r.tx.GetOne(modules.HashedAccounts, addrHash[:])
	if err != nil {
		return nil, err
	}
	r.cache.PutAccount(addrHash, enc)
	if len(enc) == 0 {
		return nil, nil
	}
	var a account.StateAccount
	if err := a.DecodeForStorage(enc); err != nil {
		return nil, err
	}
	return &a, nil
}

func (r *HashedStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	// HashedStorage logical key = keccak(addr)(32) + keccak(slot)(32) = 64B.
	// AutoDupSortKeysConversion splits it transparently on read.
	var composite [64]byte
	ah := r.cache.AddrHash(address)
	sh := r.cache.SlotHash(*key)
	copy(composite[:32], ah[:])
	copy(composite[32:], sh[:])
	if val, present, ok := r.cache.GetStorage(composite); ok {
		if !present {
			return nil, nil
		}
		return val, nil
	}
	enc, err := r.tx.GetOne(modules.HashedStorage, composite[:])
	if err != nil {
		return nil, err
	}
	r.cache.PutStorage(composite, enc)
	if len(enc) == 0 {
		return nil, nil
	}
	return enc, nil
}

func (r *HashedStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	if bytes.Equal(codeHash[:], emptyCodeHashBytes) {
		return nil, nil
	}
	if raw, ok := r.cache.GetCode(codeHash); ok {
		return raw, nil
	}
	if r.codeSrc != nil {
		code, err := r.codeSrc.GetCode(address)
		if err == nil && len(code) > 0 && crypto.Keccak256Hash(code) == codeHash {
			r.cache.PutCode(codeHash, code)
			return code, nil
		}
	}
	code, err := r.tx.GetOne(modules.Code, codeHash[:])
	if err != nil {
		return nil, err
	}
	if len(code) == 0 {
		if codeTrace {
			log.Warn("CODETRACE MISSING code", "addr", address.Hex(), "codeHash", codeHash.Hex())
		}
		return nil, nil
	}
	// The migrated Code table may hold reth's Compact-wrapped bytecode; unwrap
	// on read (like reth's code_by_hash) so the EVM gets the raw deployed code.
	raw := codeFromTable(codeHash, code)
	if codeTrace && crypto.Keccak256Hash(raw) != codeHash {
		log.Warn("CODETRACE WRONG code", "addr", address.Hex(), "codeHash", codeHash.Hex(),
			"gotKeccak", crypto.Keccak256Hash(raw).Hex(), "len", len(raw))
	}
	r.cache.PutCode(codeHash, raw)
	return raw, nil
}

func (r *HashedStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, codeHash)
	return len(code), err
}

var emptyCodeHashBytes = crypto.Keccak256(nil)
