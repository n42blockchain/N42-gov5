// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package packetd

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/modules/state"
)

// RPCStateReader is a state.StateReader answered by a node's JSON-RPC at one
// block (the parent of the block being re-executed): eth_getTransactionCount,
// eth_getBalance and eth_getCode for an account (one batch), eth_getStorageAt
// for a slot. Every answer is cached for the life of the reader (one block).
//
// Accounts are built exactly as the phone's StreamReplayStateReader rebuilds
// them from a read-log entry (nonce, balance, code hash), so a re-execution
// over this reader runs what a device runs.
//
// Absence: JSON-RPC cannot tell an account that does not exist from one that
// exists with nonce 0, balance 0 and no code. Such an account is reported as
// absent (nil), which the recorder logs as AccountNotFound. Since EIP-161 an
// empty account cannot be created by execution, so the only place such an
// account can live is a genesis alloc entry (for example storage-only); a gov5
// leader would log it as Account{0, 0, emptyCodeHash}. Execution and receipts
// agree either way (post-Spurious-Dragon rules test Empty, not Exist), only
// the read-log bytes differ.
//
// A read the node cannot answer is returned as an error and also kept: the
// IntraBlockState swallows some reader errors, so the caller must check Err
// after the re-execution.
type RPCStateReader struct {
	ctx   context.Context
	c     *Client
	block any // EIP-1898 block parameter
	conc  int

	mu       sync.Mutex
	accounts map[types.Address]*account.StateAccount // nil value = absent
	storage  map[storageKey][]byte
	codes    map[types.Hash][]byte
	err      error
}

type storageKey struct {
	addr types.Address
	slot types.Hash
}

// NewRPCStateReader returns a reader over c at block, an EIP-1898 block
// parameter (BlockHashParam or a hex number). conc bounds the concurrent
// requests Prefetch issues.
func NewRPCStateReader(ctx context.Context, c *Client, block any, conc int) *RPCStateReader {
	if conc < 1 {
		conc = 1
	}
	return &RPCStateReader{
		ctx:      ctx,
		c:        c,
		block:    block,
		conc:     conc,
		accounts: make(map[types.Address]*account.StateAccount),
		storage:  make(map[storageKey][]byte),
		codes:    make(map[types.Hash][]byte),
	}
}

// BlockHashParam is the EIP-1898 block parameter naming a block by hash.
func BlockHashParam(h types.Hash) map[string]any {
	return map[string]any{"blockHash": h.Hex()}
}

// Err returns the first read the node could not answer.
func (r *RPCStateReader) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

func (r *RPCStateReader) fail(err error) error {
	r.mu.Lock()
	if r.err == nil {
		r.err = err
	}
	r.mu.Unlock()
	return err
}

// copyAccount returns a fresh account as StreamReplayStateReader builds it.
func copyAccount(src *account.StateAccount) *account.StateAccount {
	if src == nil {
		return nil
	}
	acc := account.NewAccount()
	acc.Initialised = true
	acc.Nonce = src.Nonce
	acc.Balance.Set(&src.Balance)
	acc.CodeHash = src.CodeHash
	return &acc
}

// fetchAccount loads one account (and its code) into the cache.
func (r *RPCStateReader) fetchAccount(addr types.Address) error {
	var nonce, balance string
	var code hexutil.Bytes
	b := []BatchElem{
		{Method: "eth_getTransactionCount", Params: []any{addr.Hex(), r.block}, Result: &nonce},
		{Method: "eth_getBalance", Params: []any{addr.Hex(), r.block}, Result: &balance},
		{Method: "eth_getCode", Params: []any{addr.Hex(), r.block}, Result: &code},
	}
	if err := r.c.Batch(r.ctx, b); err != nil {
		return fmt.Errorf("account %v: %w", addr, err)
	}
	for _, e := range b {
		if e.Err != nil {
			return fmt.Errorf("account %v: %s: %w", addr, e.Method, e.Err)
		}
	}
	n, err := parseQuantity(nonce)
	if err != nil || !n.IsUint64() {
		return fmt.Errorf("account %v: nonce %q: bad quantity", addr, nonce)
	}
	bal, err := parseQuantity(balance)
	if err != nil {
		return fmt.Errorf("account %v: balance %q: %w", addr, balance, err)
	}
	var acc *account.StateAccount
	if n.Sign() != 0 || bal.Sign() != 0 || len(code) > 0 {
		a := account.NewAccount()
		a.Initialised = true
		a.Nonce = n.Uint64()
		b, overflow := uint256.FromBig(bal)
		if overflow {
			return fmt.Errorf("account %v: balance overflows 256 bits", addr)
		}
		a.Balance.Set(b)
		a.CodeHash = evmsdk.EmptyCodeHash
		if len(code) > 0 {
			a.CodeHash = crypto.Keccak256Hash(code)
		}
		acc = &a
	}
	r.mu.Lock()
	if _, ok := r.accounts[addr]; !ok {
		r.accounts[addr] = acc
	}
	if len(code) > 0 {
		r.codes[acc.CodeHash] = []byte(code)
	}
	r.mu.Unlock()
	return nil
}

// ReadAccountData answers an account read.
func (r *RPCStateReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	r.mu.Lock()
	acc, ok := r.accounts[addr]
	r.mu.Unlock()
	if !ok {
		if err := r.fetchAccount(addr); err != nil {
			return nil, r.fail(err)
		}
		r.mu.Lock()
		acc = r.accounts[addr]
		r.mu.Unlock()
	}
	return copyAccount(acc), nil
}

func (r *RPCStateReader) fetchSlots(addr types.Address, slots []types.Hash) error {
	vals := make([]string, len(slots))
	b := make([]BatchElem, len(slots))
	for i, s := range slots {
		b[i] = BatchElem{Method: "eth_getStorageAt", Params: []any{addr.Hex(), s.Hex(), r.block}, Result: &vals[i]}
	}
	if err := r.c.Batch(r.ctx, b); err != nil {
		return fmt.Errorf("storage %v: %w", addr, err)
	}
	for i, e := range b {
		if e.Err != nil {
			return fmt.Errorf("storage %v/%v: %w", addr, slots[i], e.Err)
		}
		v, err := parseQuantity(vals[i])
		if err != nil || v.BitLen() > 256 {
			return fmt.Errorf("storage %v/%v: bad value %q", addr, slots[i], vals[i])
		}
		var val []byte // zero is nil, as PlainStateReader and the replay readers answer
		if v.Sign() != 0 {
			val = v.Bytes()
		}
		r.mu.Lock()
		if _, ok := r.storage[storageKey{addr, slots[i]}]; !ok {
			r.storage[storageKey{addr, slots[i]}] = val
		}
		r.mu.Unlock()
	}
	return nil
}

// ReadAccountStorage answers a storage read.
func (r *RPCStateReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	k := storageKey{addr, *key}
	r.mu.Lock()
	v, ok := r.storage[k]
	r.mu.Unlock()
	if !ok {
		if err := r.fetchSlots(addr, []types.Hash{*key}); err != nil {
			return nil, r.fail(err)
		}
		r.mu.Lock()
		v = r.storage[k]
		r.mu.Unlock()
	}
	return append([]byte(nil), v...), nil
}

// ReadAccountCode answers a bytecode read, checking the code against its hash.
func (r *RPCStateReader) ReadAccountCode(addr types.Address, codeHash types.Hash) ([]byte, error) {
	if codeHash == evmsdk.EmptyCodeHash || codeHash == (types.Hash{}) {
		return nil, nil
	}
	r.mu.Lock()
	code, ok := r.codes[codeHash]
	r.mu.Unlock()
	if ok {
		return code, nil
	}
	var got hexutil.Bytes
	if err := r.c.Call(r.ctx, &got, "eth_getCode", addr.Hex(), r.block); err != nil {
		return nil, r.fail(fmt.Errorf("code %v of %v: %w", codeHash, addr, err))
	}
	if h := crypto.Keccak256Hash(got); h != codeHash {
		return nil, r.fail(fmt.Errorf("code of %v hashes to %v, the account names %v", addr, h, codeHash))
	}
	r.mu.Lock()
	r.codes[codeHash] = []byte(got)
	r.mu.Unlock()
	return got, nil
}

// ReadAccountCodeSize returns the length of a bytecode.
func (r *RPCStateReader) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(addr, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

// Prefetch loads the given accounts and slots concurrently (bounded by the
// reader's concurrency) so the sequential re-execution finds them cached.
// It only warms the cache: what is read, and in which order, is decided by
// the execution, never by the prefetch. Errors are left for the execution's
// own reads to report.
func (r *RPCStateReader) Prefetch(addrs []types.Address, slots map[types.Address][]types.Hash) {
	sem := make(chan struct{}, r.conc)
	var wg sync.WaitGroup
	run := func(f func()) {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			f()
		}()
	}
	seen := make(map[types.Address]struct{}, len(addrs))
	for _, a := range addrs {
		if _, dup := seen[a]; dup {
			continue
		}
		seen[a] = struct{}{}
		a := a
		run(func() { _ = r.fetchAccount(a) })
	}
	for a, ss := range slots {
		if len(ss) == 0 {
			continue
		}
		a, ss := a, ss
		run(func() { _ = r.fetchSlots(a, ss) })
	}
	wg.Wait()
}

// parseQuantity parses a hex quantity or hex data string (leading zeros
// allowed: eth_getStorageAt answers 32 bytes).
func parseQuantity(s string) (*big.Int, error) {
	t := strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(t) == len(s) {
		return nil, fmt.Errorf("missing 0x prefix in %q", s)
	}
	if t == "" {
		return new(big.Int), nil
	}
	v, ok := new(big.Int).SetString(t, 16)
	if !ok {
		return nil, fmt.Errorf("bad hex %q", s)
	}
	return v, nil
}

// Compile-time interface check.
var _ state.StateReader = (*RPCStateReader)(nil)
