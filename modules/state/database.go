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
// Incarnation constants and NoopWriter StateWriter.
// FirstContractIncarnation and NonContractIncarnation are retained
// for backward compatibility after the incarnation removal and are
// no longer consulted in the write path.
// NoopWriter implements StateWriter with empty method bodies, used
// in tests and speculative execution paths that must discard state.

package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
)

// Incarnation constants — deprecated (incarnation removed from state model).
// Kept for backward compatibility with old imports.
const (
	FirstContractIncarnation = 1
	NonContractIncarnation   = 0
)

// NoopWriter is a StateWriter implementation that does nothing.
// Useful for testing or when state changes should be discarded.
type NoopWriter struct{}

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

func (nw *NoopWriter) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	return nil
}

func (nw *NoopWriter) WriteAccountStorage(address types.Address, key types.Hash, original, value uint256.Int) error {
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
