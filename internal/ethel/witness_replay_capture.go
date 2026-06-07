// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Per-worker per-block StateWriter that captures old values via an
// embedded ChangeSetWriter and new values into private maps, so the
// encoder closures can emit (acctcs, storcs, wipes) without touching
// any shared buffer.

package ethel

import (
	"bytes"
	"sort"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

type WitnessCapturingWriter struct {
	csw *state.ChangeSetWriter

	// nil entry = account deleted in this block.
	accNewVals map[types.Address][]byte

	// addr → slot → post-block value (nil/empty = zeroed).
	// Keyed by addr first so CreateContract can wipe via single map delete.
	stoNewVals map[types.Address]map[types.Hash][]byte

	// Addresses that received CreateContract this block. Emitted as the
	// per-block wipes column so rebuild-state can prefix-delete MDBX
	// rows from prior segments — covers same-codeHash SELFDESTRUCT+CREATE2
	// that acctcs Path A/B/C can't detect from end-of-block state alone.
	wipedAddrs map[types.Address]struct{}
}

func NewWitnessCapturingWriter() *WitnessCapturingWriter {
	return &WitnessCapturingWriter{
		csw:        state.NewChangeSetWriter(),
		accNewVals: make(map[types.Address][]byte, 64),
		stoNewVals: make(map[types.Address]map[types.Hash][]byte, 64),
		wipedAddrs: make(map[types.Address]struct{}, 8),
	}
}

func (c *WitnessCapturingWriter) ChangeSetWriter() *state.ChangeSetWriter { return c.csw }

func (c *WitnessCapturingWriter) UpdateAccountData(address types.Address, original, acct *account.StateAccount) error {
	if acct != nil {
		c.accNewVals[address] = acct.MarshalV2()
	} else {
		c.accNewVals[address] = nil
	}
	return c.csw.UpdateAccountData(address, original, acct)
}

func (c *WitnessCapturingWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	c.accNewVals[address] = nil
	return c.csw.DeleteAccount(address, original)
}

func (c *WitnessCapturingWriter) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	return c.csw.UpdateAccountCode(address, codeHash, code)
}

func (c *WitnessCapturingWriter) WriteAccountStorage(address types.Address, key types.Hash, original, value uint256.Int) error {
	inner, ok := c.stoNewVals[address]
	if !ok {
		inner = make(map[types.Hash][]byte, 8)
		c.stoNewVals[address] = inner
	}
	if value.IsZero() {
		inner[key] = nil
	} else {
		bl := value.ByteLen()
		v := make([]byte, bl)
		value.WriteToSlice(v)
		inner[key] = v
	}
	return c.csw.WriteAccountStorage(address, key, original, value)
}

func (c *WitnessCapturingWriter) CreateContract(address types.Address) error {
	// Mirror PlainStateWriter.CreateContract wipe semantics for the
	// witness-replay path — drop pre-wipe stoNewVals + record addr.
	delete(c.stoNewVals, address)
	c.wipedAddrs[address] = struct{}{}
	return c.csw.CreateContract(address)
}

// WipedAddrsBytes packs CreateContract addresses as sorted addr20×N
// for the per-block wipes column. Sorted so consumers do one cursor
// seek per addr for MDBX prefix delete.
func (c *WitnessCapturingWriter) WipedAddrsBytes() []byte {
	if len(c.wipedAddrs) == 0 {
		return nil
	}
	addrs := make([]types.Address, 0, len(c.wipedAddrs))
	for a := range c.wipedAddrs {
		addrs = append(addrs, a)
	}
	sort.Slice(addrs, func(i, j int) bool {
		return bytes.Compare(addrs[i][:], addrs[j][:]) < 0
	})
	out := make([]byte, len(addrs)*20)
	for i, a := range addrs {
		copy(out[i*20:], a[:])
	}
	return out
}

func (c *WitnessCapturingWriter) WriteChangeSets() error { return nil }
func (c *WitnessCapturingWriter) WriteHistory() error    { return nil }

// PostState is the per-block changeset a capture-mode replay produces: the
// new account/storage values written this block, plus the addresses wiped by
// CreateContract. nil account value = deleted; nil/absent slot value = zeroed.
// Maps are the writer's own (not copied) — the caller takes ownership once the
// writer is discarded, which the replay does immediately after capture.
type PostState struct {
	// addr -> account V2 bytes (account.MarshalV2). nil = deleted/absent.
	Accounts map[types.Address][]byte
	// addr -> slot -> big-endian trimmed value bytes. nil/absent = zeroed.
	Storage map[types.Address]map[types.Hash][]byte
	// addresses that received CreateContract this block (storage must be
	// wiped before this block's Storage entries are applied).
	Wiped map[types.Address]struct{}
}

// PostState exposes the captured per-block changeset for overlay consumers.
func (c *WitnessCapturingWriter) PostState() *PostState {
	return &PostState{
		Accounts: c.accNewVals,
		Storage:  c.stoNewVals,
		Wiped:    c.wipedAddrs,
	}
}

func (c *WitnessCapturingWriter) AccountNewValue(addr types.Address) []byte {
	return c.accNewVals[addr]
}

func (c *WitnessCapturingWriter) StorageNewValue(addr types.Address, slot types.Hash) []byte {
	if inner, ok := c.stoNewVals[addr]; ok {
		return inner[slot]
	}
	return nil
}
