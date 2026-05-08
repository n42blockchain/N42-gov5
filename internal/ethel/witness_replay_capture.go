// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// witness_replay_capture.go — per-block StateWriter that captures BOTH
// the block-origin (old) values via an embedded ChangeSetWriter AND the
// post-block (new) values into private maps. Lets a parallel worker
// produce its own (acctcs, storcs) bytes via EncodeAccountChanges /
// EncodeStorageChanges without touching the shared
// BufferedPlainStateBuffer the sequential ethexec executor uses —
// full per-block isolation, zero cross-worker locking. Mirrors the
// existing snapshotOutputs pattern.

package ethel

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// WitnessCapturingWriter implements state.StateWriter (and
// WriterWithChangeSets) by delegating to an inner ChangeSetWriter for
// old-value collection and recording new values in private maps.
// Single-block scope; one instance per worker per block.
type WitnessCapturingWriter struct {
	csw *state.ChangeSetWriter

	// Post-block account encoding (omit-CodeHash format equivalent to
	// MarshalV2). nil entry means the account was deleted in this block.
	accNewVals map[types.Address][]byte

	// Post-block raw slot bytes (0-32 byte big-endian, leading zeros
	// trimmed). nil/empty means the slot was zeroed.
	stoNewVals map[[52]byte][]byte
}

func NewWitnessCapturingWriter() *WitnessCapturingWriter {
	return &WitnessCapturingWriter{
		csw:        state.NewChangeSetWriter(),
		accNewVals: make(map[types.Address][]byte, 64),
		stoNewVals: make(map[[52]byte][]byte, 256),
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

func (c *WitnessCapturingWriter) WriteAccountStorage(address types.Address, key *types.Hash, original, value *uint256.Int) error {
	var k [52]byte
	copy(k[:20], address[:])
	copy(k[20:], key[:])
	if value == nil || value.IsZero() {
		c.stoNewVals[k] = nil
	} else {
		// ByteLen + WriteToSlice avoids the over-allocation that
		// uint256.(*Int).Bytes() does internally (it allocates 32
		// bytes then re-slices). On a 10K-storage-write block, this
		// path was the top allocator alongside change_set_writer's
		// equivalent.
		bl := value.ByteLen()
		v := make([]byte, bl)
		value.WriteToSlice(v)
		c.stoNewVals[k] = v
	}
	return c.csw.WriteAccountStorage(address, key, original, value)
}

func (c *WitnessCapturingWriter) CreateContract(address types.Address) error {
	return c.csw.CreateContract(address)
}

func (c *WitnessCapturingWriter) WriteChangeSets() error { return nil }
func (c *WitnessCapturingWriter) WriteHistory() error    { return nil }

// AccountNewValueFn returns the closure expected by ethel's
// EncodeAccountChanges encoder.
func (c *WitnessCapturingWriter) AccountNewValue(addr types.Address) []byte {
	return c.accNewVals[addr]
}

// StorageNewValue returns the closure expected by EncodeStorageChanges.
func (c *WitnessCapturingWriter) StorageNewValue(addr types.Address, slot types.Hash) []byte {
	var k [52]byte
	copy(k[:20], addr[:])
	copy(k[20:], slot[:])
	return c.stoNewVals[k]
}
