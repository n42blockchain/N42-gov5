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
// bal_prewarm.go — EIP-7928 consume side: warm the read cache from a decoded
// Block Access List before execution. A BAL enumerates exactly the accounts and
// slots a block touches, so warming them up front lets the state reads overlap
// the EVM instead of blocking it (the parallel-prefetch lever). This mirrors
// prewarmStaticAL but is driven by the richer BAL rather than tx access lists.

package ethel

import (
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/bal"
	"github.com/n42blockchain/N42/modules/state"
)

// BALPrewarmReader is the read surface PrewarmFromBAL needs.
// *state.BufferedPlainStateReader satisfies it.
type BALPrewarmReader interface {
	ReadAccountData(address types.Address) (*account.StateAccount, error)
	ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error)
}

// The production reader must satisfy the prewarm contract.
var _ BALPrewarmReader = (*state.BufferedPlainStateReader)(nil)

// VerifyAndPrewarmBAL decodes a raw out-of-band BAL (fetched via
// eldevp2p.FetchBlockAccessLists), verifies it against the header's
// BlockAccessListHash, and prewarms the reader from it before execution. Returns
// an error if the BAL is empty, malformed, or does not match the expected hash —
// the caller then executes without prefetch (correctness is unaffected; only the
// prefetch speedup is lost). This is the import-side consumer of the out-of-band
// BAL: the header carries only the hash, the full BAL arrives out of band.
func VerifyAndPrewarmBAL(reader BALPrewarmReader, rawBAL []byte, expected *types.Hash) error {
	if len(rawBAL) == 0 {
		return errors.New("bal: empty")
	}
	b, err := bal.DecodeBAL(rawBAL)
	if err != nil {
		return err
	}
	if expected != nil {
		got, err := b.Hash()
		if err != nil {
			return err
		}
		if got != *expected {
			return fmt.Errorf("bal: hash mismatch, got %s want %s", got.Hex(), expected.Hex())
		}
	}
	PrewarmFromBAL(reader, b)
	return nil
}

// PrewarmFromBAL preloads the read cache from a block access list: every account
// it touches, plus every storage slot it writes or reads. Errors are ignored — a
// prewarm miss only loses the cache benefit, and the EVM re-reads on its own.
//
// This is the consume side of EIP-7928. It is intentionally decoupled and has no
// live call site yet: a BAL only becomes available at import time once it is
// transmitted with the block, which is the header-binding phase (consensus-
// gated). Until then this is exercised by tests and ready to wire.
func PrewarmFromBAL(reader BALPrewarmReader, b *bal.BlockAccessList) {
	if reader == nil || b == nil {
		return
	}
	for i := range b.Accounts {
		ac := &b.Accounts[i]
		addr := ac.Address
		_, _ = reader.ReadAccountData(addr)
		for j := range ac.StorageChanges {
			slot := ac.StorageChanges[j].Slot
			_, _ = reader.ReadAccountStorage(addr, &slot)
		}
		for j := range ac.StorageReads {
			slot := ac.StorageReads[j]
			_, _ = reader.ReadAccountStorage(addr, &slot)
		}
	}
}
