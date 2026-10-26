// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// EarlySource answers the reads of a PostState published before its block
// finished (NewEarlyPostState): the block's transactions have executed and
// their final writes are known, but the fold into the block's state, the
// fee credits, the block-end system calls and the rewards have not run. A
// reader of such a snapshot takes the transactions' writes from the source
// and waits for the complete post-state (Full) on the addresses the source
// holds back -- the ones the steps after the transactions write.
type EarlySource interface {
	// EarlyAccount returns the transactions' final write of addr: found
	// false when they did not write it; onBase when they only credited it
	// (the account is the base one plus delta); otherwise acc (nil when
	// removed) with delta already added.
	EarlyAccount(addr types.Address) (acc *account.StateAccount, delta *uint256.Int, onBase, found bool)
	// EarlyStorage returns the transactions' final value of the slot,
	// found false when the base answers it (the source applies the
	// account's removal and storage wipe).
	EarlyStorage(addr types.Address, key *types.Hash) (value []byte, found bool)
	// EarlyCode returns code the transactions deployed at addr.
	EarlyCode(addr types.Address, codeHash types.Hash) ([]byte, bool)
	// Hold reports whether reads of addr wait for the complete post-state.
	Hold(addr types.Address) bool
	// Full returns the complete post-state once published, else nil.
	Full() *PostState
	// WaitFull waits for the complete post-state; an error when the block
	// will not complete it (its execution was dropped).
	WaitFull() (*PostState, error)
}

// NewEarlyPostState returns a snapshot whose reads src answers. It serves
// only as a reader layer (PostStateReader); it holds no maps of its own.
func NewEarlyPostState(src EarlySource) *PostState {
	return &PostState{early: src}
}

// Early returns the snapshot's early source, nil for a complete one.
func (ps *PostState) Early() EarlySource {
	if ps == nil {
		return nil
	}
	return ps.early
}

// fullOrWait returns the complete post-state of an early snapshot when it
// is published, or when addr is held back (then waiting for it); nil when
// the early source answers addr.
func fullOrWait(src EarlySource, addr types.Address) (*PostState, error) {
	if f := src.Full(); f != nil {
		return f, nil
	}
	if src.Hold(addr) {
		return src.WaitFull()
	}
	return nil, nil
}

func (r *PostStateReader) earlyAccountData(src EarlySource, address types.Address) (*account.StateAccount, error) {
	f, err := fullOrWait(src, address)
	if err != nil {
		return nil, err
	}
	if f != nil {
		return (&PostStateReader{post: f, base: r.base}).ReadAccountData(address)
	}
	acc, delta, onBase, found := src.EarlyAccount(address)
	if !found {
		return r.base.ReadAccountData(address)
	}
	if onBase {
		b, err := r.base.ReadAccountData(address)
		if err != nil {
			return nil, err
		}
		acc = &account.StateAccount{Initialised: true}
		if b != nil {
			acc.Copy(b)
		}
		acc.Balance.Add(&acc.Balance, delta)
	}
	// An empty account is absent, as the complete snapshot answers.
	if acc == nil || accountIsEmpty(acc) {
		return nil, nil
	}
	return acc, nil
}

func (r *PostStateReader) earlyAccountStorage(src EarlySource, address types.Address, key *types.Hash) ([]byte, error) {
	f, err := fullOrWait(src, address)
	if err != nil {
		return nil, err
	}
	if f != nil {
		return (&PostStateReader{post: f, base: r.base}).ReadAccountStorage(address, key)
	}
	if v, found := src.EarlyStorage(address, key); found {
		return v, nil
	}
	return r.base.ReadAccountStorage(address, key)
}

func (r *PostStateReader) earlyAccountCode(src EarlySource, address types.Address, codeHash types.Hash) ([]byte, error) {
	f, err := fullOrWait(src, address)
	if err != nil {
		return nil, err
	}
	if f != nil {
		return (&PostStateReader{post: f, base: r.base}).ReadAccountCode(address, codeHash)
	}
	if c, ok := src.EarlyCode(address, codeHash); ok {
		return c, nil
	}
	return r.base.ReadAccountCode(address, codeHash)
}

// earlyForEachStorage enumerates on the complete snapshot: an enumeration
// needs every slot the block wrote, which only it has.
func (r *PostStateReader) earlyForEachStorage(src EarlySource, addr types.Address, fn func(slot types.Hash, value []byte) bool) error {
	f, err := src.WaitFull()
	if err != nil {
		return err
	}
	return (&PostStateReader{post: f, base: r.base}).ForEachStorage(addr, fn)
}

// AccountOf returns a copy of the account the snapshot holds for addr
// (nil: absent after the block); ok false when the block did not touch it.
func (ps *PostState) AccountOf(addr types.Address) (acc *account.StateAccount, ok bool) {
	a, ok := ps.accounts[addr]
	if !ok || a == nil {
		return nil, ok
	}
	c := new(account.StateAccount)
	c.Copy(a)
	return c, true
}

// AccountIsEmpty reports an account the store and the root walk treat as
// absent (no nonce, balance or code).
func AccountIsEmpty(a *account.StateAccount) bool { return accountIsEmpty(a) }
