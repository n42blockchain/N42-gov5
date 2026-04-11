// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// WitnessStateReader: state.StateReader backed by a BlockWitness.
// Decodes account proofs, storage proofs and code entries from a
// BlockWitness into address/slot/code lookup maps so that stateless
// light clients can re-execute an EVM block without a database.
// Compile-time asserts the state.StateReader interface is satisfied.
// Requires OriginalKey preimages on every KeyProof for indexing.

package witness

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// WitnessStateReader implements state.StateReader by reading from witness
// proof data instead of a database. This enables stateless EVM execution
// where the light client only needs the witness to verify and execute a block.
type WitnessStateReader struct {
	accounts map[types.Address]*account.StateAccount
	storage  map[types.Address]map[types.Hash][]byte
	codes    map[types.Hash][]byte
}

// Compile-time interface check.
var _ state.StateReader = (*WitnessStateReader)(nil)

// NewWitnessStateReader creates a StateReader from a BlockWitness by decoding
// all account proofs, storage proofs, and code entries into lookup maps.
// Each KeyProof must have OriginalKey set to the pre-image (address for
// accounts, addr||slot for storage) so that the reader can index by the
// original EVM-visible keys.
func NewWitnessStateReader(w *BlockWitness) (*WitnessStateReader, error) {
	if w == nil {
		return nil, errors.New("witness: nil block witness")
	}
	r := &WitnessStateReader{
		accounts: make(map[types.Address]*account.StateAccount, len(w.AccountProofs)),
		storage:  make(map[types.Address]map[types.Hash][]byte),
		codes:    make(map[types.Hash][]byte, len(w.Codes)),
	}

	// Decode account proofs.
	for i := range w.AccountProofs {
		ap := &w.AccountProofs[i]
		if ap.Value == nil {
			// Exclusion proof: account does not exist.
			if len(ap.OriginalKey) != types.AddressLength {
				continue // skip if no valid original key
			}
			var addr types.Address
			copy(addr[:], ap.OriginalKey)
			r.accounts[addr] = nil
			continue
		}
		if len(ap.OriginalKey) != types.AddressLength {
			return nil, fmt.Errorf("witness: account proof %d has invalid OriginalKey length %d, expected %d",
				i, len(ap.OriginalKey), types.AddressLength)
		}
		var addr types.Address
		copy(addr[:], ap.OriginalKey)
		acct := new(account.StateAccount)
		if err := commitment.DecodeAccountValue(ap.Value, acct); err != nil {
			return nil, fmt.Errorf("decode account %s: %w", addr.Hex(), err)
		}
		r.accounts[addr] = acct
	}

	// Decode storage proofs.
	const storageKeyLen = types.AddressLength + types.HashLength
	for i := range w.StorageProofs {
		sp := &w.StorageProofs[i]
		if len(sp.OriginalKey) != storageKeyLen {
			if sp.Value == nil {
				continue // exclusion proof without valid key, skip
			}
			return nil, fmt.Errorf("witness: storage proof %d has invalid OriginalKey length %d, expected %d",
				i, len(sp.OriginalKey), storageKeyLen)
		}
		var addr types.Address
		var slot types.Hash
		copy(addr[:], sp.OriginalKey[:types.AddressLength])
		copy(slot[:], sp.OriginalKey[types.AddressLength:])

		slots, ok := r.storage[addr]
		if !ok {
			slots = make(map[types.Hash][]byte)
			r.storage[addr] = slots
		}
		if sp.Value == nil {
			// Exclusion proof: slot is empty.
			slots[slot] = nil
		} else {
			val := make([]byte, len(sp.Value))
			copy(val, sp.Value)
			slots[slot] = val
		}
	}

	// Store codes.
	for _, hc := range w.Codes {
		code := make([]byte, len(hc.Code))
		copy(code, hc.Code)
		r.codes[hc.Hash] = code
	}

	return r, nil
}

// ReadAccountData returns the account state from the witness.
// Returns nil, nil if the account is proven absent (exclusion proof).
func (r *WitnessStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	acct, ok := r.accounts[address]
	if !ok {
		return nil, nil
	}
	if acct == nil {
		return nil, nil
	}
	return acct.SelfCopy(), nil
}

// ReadAccountStorage returns the value of a storage slot from the witness.
// Returns nil, nil if the slot is proven absent (exclusion proof).
func (r *WitnessStateReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	if key == nil {
		return nil, nil
	}
	slots, ok := r.storage[address]
	if !ok {
		return nil, nil
	}
	val, ok := slots[*key]
	if !ok {
		return nil, nil
	}
	if val == nil {
		return nil, nil
	}
	out := make([]byte, len(val))
	copy(out, val)
	return out, nil
}

// ReadAccountCode returns the contract code from the witness.
func (r *WitnessStateReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	emptyHash := types.Hash{}
	if codeHash == emptyHash {
		return nil, nil
	}
	code, ok := r.codes[codeHash]
	if !ok {
		return nil, nil
	}
	out := make([]byte, len(code))
	copy(out, code)
	return out, nil
}

// ReadAccountCodeSize returns the size of the contract code from the witness.
func (r *WitnessStateReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, incarnation, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

// ReadAccountIncarnation returns the incarnation from the decoded account.
func (r *WitnessStateReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	// incarnation removed from StateAccount — always return 0
	return 0, nil
}
