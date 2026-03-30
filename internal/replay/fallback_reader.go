// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/modules/state"
)

// emptyCodeHash is Keccak256(nil) — the canonical "no code" marker.
var emptyCodeHash = crypto.Keccak256Hash(nil)

// FallbackStateReader reads from the primary (target chain) first;
// if the account/storage is not found, falls back to the secondary (source chain).
// This allows full EVM replay on a fresh target DB: new state changes are read
// from the target, while unmodified original state comes from the source.
type FallbackStateReader struct {
	primary  state.StateReader // target chain (has replay-modified state)
	fallback state.StateReader // source chain (has original state)
}

func NewFallbackStateReader(primary, fallback state.StateReader) *FallbackStateReader {
	return &FallbackStateReader{primary: primary, fallback: fallback}
}

func (r *FallbackStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	acct, err := r.primary.ReadAccountData(address)
	if err != nil {
		return nil, err
	}
	if acct != nil {
		return acct, nil
	}
	acct, err = r.fallback.ReadAccountData(address)
	if err != nil || acct == nil {
		return acct, err
	}
	// Old protobuf-encoded accounts may have a non-standard CodeHash
	// for EOAs (e.g. a serialization artifact). If the account has no
	// actual code, normalize CodeHash to the canonical empty value so
	// EIP-3607 doesn't reject it as a contract sender.
	if !account.IsEmptyCodeHash(acct.CodeHash) {
		code, _ := r.fallback.ReadAccountCode(address, acct.Incarnation, acct.CodeHash)
		if len(code) == 0 {
			acct.CodeHash = emptyCodeHash
		}
	}
	return acct, nil
}

func (r *FallbackStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	val, err := r.primary.ReadAccountStorage(address, incarnation, key)
	if err != nil {
		return nil, err
	}
	if val != nil {
		return val, nil
	}
	return r.fallback.ReadAccountStorage(address, incarnation, key)
}

func (r *FallbackStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	code, err := r.primary.ReadAccountCode(address, incarnation, codeHash)
	if err != nil {
		return nil, err
	}
	if code != nil {
		return code, nil
	}
	return r.fallback.ReadAccountCode(address, incarnation, codeHash)
}

func (r *FallbackStateReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	size, err := r.primary.ReadAccountCodeSize(address, incarnation, codeHash)
	if err != nil {
		return 0, err
	}
	if size > 0 {
		return size, nil
	}
	return r.fallback.ReadAccountCodeSize(address, incarnation, codeHash)
}

func (r *FallbackStateReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	inc, err := r.primary.ReadAccountIncarnation(address)
	if err != nil {
		return 0, err
	}
	if inc > 0 {
		return inc, nil
	}
	return r.fallback.ReadAccountIncarnation(address)
}
