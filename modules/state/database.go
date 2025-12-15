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

//nolint:scopelint
package state

import (
	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

const (
	//FirstContractIncarnation - first incarnation for contract accounts. After 1 it increases by 1.
	FirstContractIncarnation = 1
	//NonContractIncarnation incarnation for non contracts
	NonContractIncarnation = 0
)

// Note: StateReader, StateWriter, and WriterWithChangeSets interfaces
// are now defined in interfaces.go for better organization.

// NoopWriter is a StateWriter implementation that does nothing.
// Useful for testing or when state changes should be discarded.
type NoopWriter struct {
}

var noopWriter = &NoopWriter{}

func NewNoopWriter() *NoopWriter {
	return noopWriter
}

func (nw *NoopWriter) UpdateAccountData(address types.Address, original, account *account.StateAccount) error {
	return nil
}

func (nw *NoopWriter) DeleteAccount(address types.Address, original *account.StateAccount) error {
	return nil
}

func (nw *NoopWriter) UpdateAccountCode(address types.Address, incarnation uint16, codeHash types.Hash, code []byte) error {
	return nil
}

func (nw *NoopWriter) WriteAccountStorage(address types.Address, incarnation uint16, key *types.Hash, original, value *uint256.Int) error {
	return nil
}

func (nw *NoopWriter) CreateContract(address types.Address) error {
	return nil
}

func (nw *NoopWriter) WriteChangeSets() error {
	return nil
}

func (nw *NoopWriter) WriteHistory() error {
	return nil
}
