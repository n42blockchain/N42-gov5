// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Genesis alloc repair, version 2: driven by the source chain's own result.
//
// Repair v1 (engine_v2_p1repair.go) only looks at the top level of a
// transaction. A transaction that succeeded on the source can still fail
// under profile-1 rules deeper down:
//
//   - a CREATE runs out of gas on the EIP-8037 state gas (new account + code
//     deposit), so the contract never exists and every later call to it is a
//     plain transfer into an empty account (chain 94, block 2642: a
//     multisend contract whose payouts then never happen);
//   - an internal CALL with value into an empty account cannot pay the
//     new-account state gas (multisend-style fan-out).
//
// In a repair pass every transaction the source receipt marks successful is
// first executed as a dry run (snapshot, ApplyMessage, revert). When it
// fails, or succeeds but leaves an account empty that the source block made
// non-empty, the pass consults the source's state changes for the block
// (AccountChangeSet / StorageChangeSet and the state after the block,
// read-only) and injects, in this order:
//
//  1. precreate: one wei into each account the source block made non-empty
//     that is still empty here. Kept only for the accounts the transaction
//     then actually credits (balance above the one wei), recorded in the
//     delta like a v1 precreate.
//  2. predeploy (only if 1 did not make the transaction succeed): each
//     contract the source block created that has no code here gets its
//     runtime code, storage and nonce as the source left them after the
//     block. For a creation transaction that is its own contract only.
//
// Then the transaction runs for real.
//
// A creation transaction whose contract was predeployed collides with it
// (internal/vm/evm.go create: EIP-684 nonce/code check): the CREATE fails
// with ErrContractAddressCollision, consumes all its gas and the receipt has
// status 0. The predeployed contract is left untouched, the sender's nonce
// advances as for any included transaction, and the endowment (value) stays
// with the sender. The block's later transactions see the contract exactly
// as the source had it.
//
// Source format: chain 94 keeps the legacy layout. Accounts are protobuf
// records without a code hash; the code is found through PlainCodeHash
// (address + incarnation -> code hash) and Code. Storage keys and storage
// changeset keys carry a 2-byte incarnation after the address:
// Storage = addr(20) inc(2) slot(32) value, StorageChangeSet key =
// block(8) addr(20) inc(2), value = slot(32) previous value. The history
// indexes (AccountHistory, StorageHistory) carry no incarnation.

package replay

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/RoaringBitmap/roaring/roaring64"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/ethdb"
	"github.com/n42blockchain/N42/modules/ethdb/bitmapdb"
	"github.com/n42blockchain/N42/modules/state"
)

// p1SrcPlainCodeHash is the legacy source table address+incarnation -> code hash.
const p1SrcPlainCodeHash = "PlainCodeHash"

// p1SrcIncLen is the incarnation width in the source's storage keys.
const p1SrcIncLen = 2

// p1SrcAccount is one entry of the source block's AccountChangeSet.
type p1SrcAccount struct {
	addr      types.Address
	preEmpty  bool // EIP-161 empty (or absent) before the block
	postEmpty bool // empty (or absent) after the block
	created   bool // a contract the block created (nonce 0 -> >= 1, with code)
	postNonce uint64
}

// p1SrcBlock is what the repair needs of one source block, read lazily.
type p1SrcBlock struct {
	number   uint64
	accounts []p1SrcAccount
}

// p1SrcContract is a created contract as the source left it after the block.
type p1SrcContract struct {
	code    []byte
	storage map[types.Hash]types.Hash
	nonce   uint64
}

// p1SrcDecodeAccount decodes a legacy (protobuf) source account record; an
// empty record is an absent account.
func p1SrcDecodeAccount(enc []byte) (*account.StateAccount, error) {
	if len(enc) == 0 {
		return nil, nil
	}
	var a account.StateAccount
	if err := a.Unmarshal(enc); err != nil {
		return nil, err
	}
	return &a, nil
}

func p1SrcAccountEmpty(a *account.StateAccount) bool {
	return a == nil || (a.Nonce == 0 && a.Balance.IsZero() && a.IsEmptyCodeHash())
}

// p1SrcAccountAsOf returns the source account at the beginning of block n
// (the state after block n-1).
func p1SrcAccountAsOf(tx kv.Tx, addr types.Address, n uint64) (*account.StateAccount, error) {
	idx, err := tx.Cursor(modules.AccountsHistory)
	if err != nil {
		return nil, err
	}
	defer idx.Close()
	cs, err := tx.CursorDupSort(modules.AccountChangeSet)
	if err != nil {
		return nil, err
	}
	defer cs.Close()
	enc, err := state.FindByHistory(tx, idx, cs, false, addr[:], n)
	if errors.Is(err, ethdb.ErrKeyNotFound) {
		enc, err = tx.GetOne(modules.Account, addr[:])
	}
	if err != nil {
		return nil, err
	}
	return p1SrcDecodeAccount(enc)
}

// p1SrcCode returns the source contract code of addr (nil when none).
func p1SrcCode(tx kv.Tx, addr types.Address) ([]byte, error) {
	c, err := tx.Cursor(p1SrcPlainCodeHash)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	k, v, err := c.Seek(addr[:])
	if err != nil || k == nil || !bytes.HasPrefix(k, addr[:]) {
		return nil, err
	}
	if len(v) != types.HashLength {
		return nil, fmt.Errorf("source code hash of %s: %d bytes", addr.Hex(), len(v))
	}
	code, err := tx.GetOne(modules.Code, v)
	if err != nil {
		return nil, err
	}
	if crypto.Keccak256Hash(code) != types.BytesToHash(v) {
		return nil, fmt.Errorf("source code of %s does not match its hash %x", addr.Hex(), v)
	}
	return code, nil
}

// p1SrcStorageChanged returns the slots of addr that source block n changed,
// with their values before the block.
func p1SrcStorageChanged(tx kv.Tx, addr types.Address, n uint64) (map[types.Hash][]byte, error) {
	c, err := tx.Cursor(modules.StorageChangeSet)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	prefix := append(modules.EncodeBlockNumber(n), addr[:]...)
	out := map[types.Hash][]byte{}
	for k, v, err := c.Seek(prefix); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, prefix) {
			break
		}
		if len(v) < types.HashLength {
			return nil, fmt.Errorf("source storage changeset %d %s: value of %d bytes", n, addr.Hex(), len(v))
		}
		out[types.BytesToHash(v[:types.HashLength])] = bytes.Clone(v[types.HashLength:])
	}
	return out, nil
}

// p1SrcStorageAsOf returns the source value of addr/slot at the beginning of
// block n.
func p1SrcStorageAsOf(tx kv.Tx, addr types.Address, slot types.Hash, n uint64) ([]byte, error) {
	key := append(bytes.Clone(addr[:]), slot[:]...)
	idx, err := tx.Cursor(modules.StorageHistory)
	if err != nil {
		return nil, err
	}
	defer idx.Close()
	k, v, err := idx.Seek(modules.StorageIndexChunkKey(key, n))
	if err != nil {
		return nil, err
	}
	if k != nil && bytes.HasPrefix(k, key) {
		bm := roaring64.New()
		if _, err := bm.ReadFrom(bytes.NewReader(v)); err != nil {
			return nil, err
		}
		if m, ok := bitmapdb.SeekInBitmap64(bm, n); ok {
			changed, err := p1SrcStorageChanged(tx, addr, m)
			if err != nil {
				return nil, err
			}
			prev, ok := changed[slot]
			if !ok {
				return nil, fmt.Errorf("source storage %s %s: index names block %d, changeset lacks it", addr.Hex(), slot.Hex(), m)
			}
			return prev, nil
		}
	}
	// No change at or after n: the value then is the latest one.
	c, err := tx.Cursor(modules.Storage)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	for k, v, err := c.Seek(addr[:]); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, addr[:]) {
			break
		}
		kv := append(bytes.Clone(k), v...)
		off := types.AddressLength + p1SrcIncLen
		if len(kv) < off+types.HashLength {
			return nil, fmt.Errorf("source storage %s: record of %d bytes", addr.Hex(), len(kv))
		}
		if bytes.Equal(kv[off:off+types.HashLength], slot[:]) {
			return kv[off+types.HashLength:], nil
		}
	}
	return nil, nil
}

// p1SrcReadBlock reads the AccountChangeSet of source block n with each
// account's state before and after the block.
func p1SrcReadBlock(tx kv.Tx, n uint64) (*p1SrcBlock, error) {
	b := &p1SrcBlock{number: n}
	type pre struct {
		addr types.Address
		enc  []byte
	}
	var pres []pre
	c, err := tx.Cursor(modules.AccountChangeSet)
	if err != nil {
		return nil, err
	}
	key := modules.EncodeBlockNumber(n)
	for k, v, err := c.Seek(key); k != nil; k, v, err = c.Next() {
		if err != nil {
			c.Close()
			return nil, err
		}
		if !bytes.Equal(k, key) {
			break
		}
		if len(v) < types.AddressLength {
			c.Close()
			return nil, fmt.Errorf("source account changeset %d: value of %d bytes", n, len(v))
		}
		pres = append(pres, pre{types.BytesToAddress(v[:types.AddressLength]), bytes.Clone(v[types.AddressLength:])})
	}
	c.Close()
	for _, p := range pres {
		before, err := p1SrcDecodeAccount(p.enc)
		if err != nil {
			return nil, fmt.Errorf("source account %s before block %d: %w", p.addr.Hex(), n, err)
		}
		after, err := p1SrcAccountAsOf(tx, p.addr, n+1)
		if err != nil {
			return nil, fmt.Errorf("source account %s after block %d: %w", p.addr.Hex(), n, err)
		}
		a := p1SrcAccount{addr: p.addr, preEmpty: p1SrcAccountEmpty(before), postEmpty: p1SrcAccountEmpty(after)}
		if after != nil {
			a.postNonce = after.Nonce
		}
		if (before == nil || before.Nonce == 0) && a.postNonce >= 1 {
			code, err := p1SrcCode(tx, p.addr)
			if err != nil {
				return nil, err
			}
			a.created = len(code) > 0
		}
		b.accounts = append(b.accounts, a)
	}
	sort.Slice(b.accounts, func(i, j int) bool {
		return bytes.Compare(b.accounts[i].addr[:], b.accounts[j].addr[:]) < 0
	})
	return b, nil
}

// p1SrcReadContract reads a contract created by source block n as the
// source left it after the block.
func p1SrcReadContract(tx kv.Tx, addr types.Address, n uint64, nonce uint64) (*p1SrcContract, error) {
	code, err := p1SrcCode(tx, addr)
	if err != nil {
		return nil, err
	}
	changed, err := p1SrcStorageChanged(tx, addr, n)
	if err != nil {
		return nil, err
	}
	c := &p1SrcContract{code: code, nonce: nonce, storage: map[types.Hash]types.Hash{}}
	for slot := range changed {
		v, err := p1SrcStorageAsOf(tx, addr, slot, n+1)
		if err != nil {
			return nil, err
		}
		if len(v) > types.HashLength {
			return nil, fmt.Errorf("source storage %s %s: value of %d bytes", addr.Hex(), slot.Hex(), len(v))
		}
		if h := types.BytesToHash(v); h != (types.Hash{}) {
			c.storage[slot] = h
		}
	}
	return c, nil
}

// p1SrcBlockFx returns the source block effects of the block being replayed,
// read once per block.
func (e *EngineV2) p1SrcBlockFx() (*p1SrcBlock, error) {
	if e.p1SrcFx != nil && e.p1SrcFx.number == e.p1SrcNum {
		return e.p1SrcFx, nil
	}
	if e.p1SrcTx == nil {
		return nil, errors.New("repair v2: no source transaction")
	}
	b, err := p1SrcReadBlock(e.p1SrcTx, e.p1SrcNum)
	if err != nil {
		return nil, err
	}
	e.p1SrcFx = b
	return b, nil
}

// p1DryRun executes tx on ibs as the real run would, calls inspect on the
// resulting state (when the transaction applied) and reverts everything.
// ok is the receipt status the transaction would get; err is a
// block-context rejection (nonce, funds, gas), which the real run reports.
func (e *EngineV2) p1DryRun(ibs *state.IntraBlockState, tx *transaction.Transaction, i int, blockHash types.Hash,
	header *block.Header, gp *common.GasPool, blockHashFunc func(uint64) types.Hash, inspect func()) (ok bool, err error) {
	snap := ibs.Snapshot()
	defer ibs.RevertToSnapshot(snap)
	cfg := e.txRulesConfig()
	ibs.Prepare(tx.Hash(), blockHash, i)
	msg, err := tx.AsMessage(transaction.MakeSignerWithTimestamp(cfg, header.Number.ToBig(), header.Time), header.BaseFee)
	if err != nil {
		return false, err
	}
	internal.NormalizeExecutionMessage(&msg, false, false)
	evm := vm2.NewEVM(internal.NewEVMBlockContext(header, blockHashFunc, nil, cfg, &header.Coinbase), internal.NewEVMTxContext(&msg), ibs, cfg, vm2.Config{})
	pool := *gp
	res, err := internal.ApplyMessage(evm, &msg, &pool, true, false)
	if err != nil {
		return false, err
	}
	if inspect != nil {
		inspect()
	}
	return !res.Failed(), nil
}

// p1RepairV2 runs before source transaction i (repair pass, source status
// successful). It injects what the transaction needs, if anything; the
// caller then applies the transaction for real.
func (e *EngineV2) p1RepairV2(ibs *state.IntraBlockState, tx *transaction.Transaction, i int, blockHash types.Hash,
	header *block.Header, gp *common.GasPool, blockHashFunc func(uint64) types.Hash) error {
	d := &e.stats.P1Diag
	number := header.Number.Uint64() // target block, as v1 records it
	fx, err := e.p1SrcBlockFx()
	if err != nil {
		return err
	}
	// Candidates: accounts the source block made non-empty (not contracts it
	// created) that are still empty after the dry run.
	var empties []types.Address
	collect := func() {
		empties = empties[:0]
		for _, a := range fx.accounts {
			if a.preEmpty && !a.postEmpty && !a.created && ibs.Empty(a.addr) {
				empties = append(empties, a.addr)
			}
		}
	}
	dry := func(inspect func()) (bool, error) {
		return e.p1DryRun(ibs, tx, i, blockHash, header, gp, blockHashFunc, inspect)
	}

	ok, err := dry(collect)
	for tries := 0; err != nil && tries < 4; tries++ {
		// A sender that cannot pay is topped up now (as the real run's v1
		// path would) so that the dry run can tell the outcome.
		if !e.p1RepairTopup(ibs, tx, err, number) {
			break
		}
		ok, err = dry(collect)
	}
	if err != nil {
		d.Add("repair_v2_dryrun_rejected", 1)
		return nil
	}
	if !ok {
		d.Add("repair_v2_src_ok_dry_failed", 1)
	}

	// Step 1: precreate the candidates the transaction credits.
	kept := 0
	precreate := func(wasOK bool) (fixed bool, err error) {
		kept = 0
		if len(empties) == 0 {
			return false, nil
		}
		cands := append([]types.Address(nil), empties...)
		snap := ibs.Snapshot()
		for _, a := range cands {
			ibs.AddBalance(a, uint256.NewInt(1))
		}
		var used []types.Address
		okAll, err := dry(func() {
			for _, a := range cands {
				if ibs.GetBalance(a).CmpUint64(1) != 0 || ibs.GetNonce(a) != 0 {
					used = append(used, a)
				}
			}
		})
		ibs.RevertToSnapshot(snap)
		if err != nil {
			return false, err
		}
		if !wasOK && !okAll {
			return false, nil
		}
		keep := used
		for _, a := range used {
			ibs.AddBalance(a, uint256.NewInt(1))
		}
		if !wasOK {
			// The credited subset must still fix it; otherwise keep all.
			okSub, err := dry(nil)
			if err != nil {
				return false, err
			}
			if !okSub {
				inUsed := make(map[types.Address]bool, len(used))
				for _, a := range used {
					inUsed[a] = true
				}
				for _, a := range cands {
					if !inUsed[a] {
						ibs.AddBalance(a, uint256.NewInt(1))
					}
				}
				keep = cands
			}
		}
		for _, a := range keep {
			e.p1Repair.AddPrecreate(a, number)
		}
		kept = len(keep)
		d.Add("repair_v2_precreate", uint64(len(keep)))
		return !wasOK, nil
	}
	if ok {
		if len(empties) > 0 {
			if _, err := precreate(true); err != nil {
				return err
			}
			if kept > 0 {
				d.Add("repair_v2_tx_internal_precreate", 1)
			}
		}
		return nil
	}
	if fixed, err := precreate(false); err != nil {
		return err
	} else if fixed {
		d.Add("repair_v2_fixed_precreate", 1)
		return nil
	}

	// Step 2: predeploy the contracts the source block created.
	var targets []types.Address
	if tx.To() == nil {
		if from := tx.From(); from != nil {
			addr := crypto.CreateAddress(*from, tx.Nonce())
			for _, a := range fx.accounts {
				if a.addr == addr && a.created {
					targets = append(targets, addr)
				}
			}
		}
	} else {
		others := map[types.Address]bool{}
		if e.p1SrcBlk != nil {
			for j, t := range e.p1SrcBlk.Transactions() {
				if j != i && t.To() == nil && t.From() != nil {
					others[crypto.CreateAddress(*t.From(), t.Nonce())] = true
				}
			}
		}
		for _, a := range fx.accounts {
			if a.created && !others[a.addr] {
				targets = append(targets, a.addr)
			}
		}
	}
	var deploy []types.Address
	for _, a := range targets {
		if h := ibs.GetCodeHash(a); h == (types.Hash{}) || h == emptyCodeHashP1 {
			deploy = append(deploy, a)
		}
	}
	if len(deploy) == 0 {
		d.Add("repair_v2_unresolved", 1)
		return nil
	}
	for _, a := range deploy {
		var nonce uint64
		for _, s := range fx.accounts {
			if s.addr == a {
				nonce = s.postNonce
			}
		}
		c, err := p1SrcReadContract(e.p1SrcTx, a, fx.number, nonce)
		if err != nil {
			return err
		}
		ibs.SetCode(a, c.code)
		ibs.SetNonce(a, c.nonce)
		for k, v := range c.storage {
			key := k
			var val uint256.Int
			val.SetBytes(v[:])
			ibs.SetState(a, &key, val)
		}
		e.p1Repair.AddPredeploy(a, c.code, c.storage, c.nonce, number)
		d.Add("repair_v2_predeploy", 1)
		d.Add("repair_v2_predeploy_slots", uint64(len(c.storage)))
	}
	if tx.To() == nil {
		// The creation now collides with its predeployed contract.
		d.Add("repair_v2_create_predeployed", 1)
		return nil
	}
	ok, err = dry(collect)
	if err != nil {
		return err
	}
	if !ok {
		if fixed, err := precreate(false); err != nil {
			return err
		} else if fixed {
			ok = true
		}
	}
	if ok {
		d.Add("repair_v2_fixed_predeploy", 1)
	} else {
		d.Add("repair_v2_unresolved_after_predeploy", 1)
	}
	return nil
}

var emptyCodeHashP1 = crypto.Keccak256Hash(nil)

// p1SrcOK reports whether the source receipt of transaction i is successful.
func (e *EngineV2) p1SrcOK(i int) bool {
	return i < len(e.p1SrcReceipts) && e.p1SrcReceipts[i] != nil && e.p1SrcReceipts[i].Status == block.ReceiptStatusSuccessful
}

// p1RepairTopup credits the sender the shortfall an insufficient-funds
// rejection names and records it; false when err is not such a rejection.
func (e *EngineV2) p1RepairTopup(ibs *state.IntraBlockState, tx *transaction.Transaction, err error, number uint64) bool {
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("insufficient funds")) {
		return false
	}
	have, want, ok := parseHaveWant(err.Error())
	if !ok || want.Cmp(have) <= 0 {
		return false
	}
	from := tx.From()
	if from == nil {
		return false
	}
	deficit := new(uint256.Int).Sub(want, have)
	ibs.AddBalance(*from, deficit)
	acc := e.topups[*from]
	if acc == nil {
		acc = new(uint256.Int)
		e.topups[*from] = acc
	}
	acc.Add(acc, deficit)
	e.p1Repair.AddTopup(*from, deficit, number)
	e.stats.P1Diag.Add("repair_topup", 1)
	return true
}
