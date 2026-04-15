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
// PlainState: historical state snapshot for a given block number.
// Opens AccountsHistory/StorageHistory cursors plus Account/Storage
// changeset dupsort cursors and uses a btree.BTree per-contract
// storageItem cache to iterate slot order efficiently.
// storageItem wraps key/seckey/value and implements btree.Item Less
// for ordering by raw slot key bytes.

package state

import (
	"bytes"
	"fmt"

	"github.com/google/btree"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

type storageItem struct {
	key, seckey types.Hash
	value       uint256.Int
}

func (a *storageItem) Less(b btree.Item) bool {
	bi := b.(*storageItem)
	return bytes.Compare(a.key[:], bi.key[:]) < 0
}

// State at the beginning of blockNr
type PlainState struct {
	accHistoryC, storageHistoryC kv.Cursor
	accChangesC, storageChangesC kv.CursorDupSort
	tx                           kv.Tx
	blockNr                      uint64
	storage                      map[types.Address]*btree.BTree
	trace                        bool
}

func NewPlainState(tx kv.Tx, blockNr uint64) *PlainState {
	c1, err := tx.Cursor(modules.AccountsHistory)
	if err != nil {
		log.Error("failed to create AccountsHistory cursor", "err", err)
	}
	c2, err := tx.Cursor(modules.StorageHistory)
	if err != nil {
		log.Error("failed to create StorageHistory cursor", "err", err)
	}
	c3, err := tx.CursorDupSort(modules.AccountChangeSet)
	if err != nil {
		log.Error("failed to create AccountChangeSet cursor", "err", err)
	}
	c4, err := tx.CursorDupSort(modules.StorageChangeSet)
	if err != nil {
		log.Error("failed to create StorageChangeSet cursor", "err", err)
	}

	return &PlainState{
		tx:          tx,
		blockNr:     blockNr,
		storage:     make(map[types.Address]*btree.BTree),
		accHistoryC: c1, storageHistoryC: c2, accChangesC: c3, storageChangesC: c4,
	}
}

func (s *PlainState) SetTrace(trace bool) {
	s.trace = trace
}

func (s *PlainState) SetBlockNr(blockNr uint64) {
	s.blockNr = blockNr
}

func (s *PlainState) GetBlockNr() uint64 {
	return s.blockNr
}

func (s *PlainState) ForEachStorage(addr types.Address, startLocation types.Hash, cb func(key, seckey types.Hash, value uint256.Int) bool, maxResults int) error {
	st := btree.New(16)
	var k [types.AddressLength + types.HashLength]byte
	copy(k[:], addr[:])
	copy(k[types.AddressLength:], startLocation[:])
	var lastKey types.Hash
	overrideCounter := 0
	min := &storageItem{key: startLocation}
	if t, ok := s.storage[addr]; ok {
		t.AscendGreaterOrEqual(min, func(i btree.Item) bool {
			item := i.(*storageItem)
			st.ReplaceOrInsert(item)
			if !item.value.IsZero() {
				copy(lastKey[:], item.key[:])
				// Only count non-zero items
				overrideCounter++
			}
			return overrideCounter < maxResults
		})
	}
	numDeletes := st.Len() - overrideCounter
	if err := WalkAsOfStorage(s.tx, addr, 0, startLocation, s.blockNr, func(kAddr, kLoc, vs []byte) (bool, error) {
		if !bytes.Equal(kAddr, addr[:]) {
			return false, nil
		}
		if len(vs) == 0 {
			// Skip deleted entries
			return true, nil
		}
		keyHash, err1 := types.HashData(kLoc)
		if err1 != nil {
			return false, err1
		}
		si := storageItem{}
		copy(si.key[:], kLoc)
		copy(si.seckey[:], keyHash[:])
		if st.Has(&si) {
			return true, nil
		}
		si.value.SetBytes(vs)
		st.ReplaceOrInsert(&si)
		if bytes.Compare(kLoc, lastKey[:]) > 0 {
			// Beyond overrides
			return st.Len() < maxResults+numDeletes, nil
		}
		return st.Len() < maxResults+overrideCounter+numDeletes, nil
	}); err != nil {
		log.Error("ForEachStorage walk error", "err", err)
		return err
	}
	results := 0
	var innerErr error
	st.AscendGreaterOrEqual(min, func(i btree.Item) bool {
		item := i.(*storageItem)
		if !item.value.IsZero() {
			// Skip if value == 0
			cb(item.key, item.seckey, item.value)
			results++
		}
		return results < maxResults
	})
	return innerErr
}

func (s *PlainState) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	enc, err := GetAsOf(s.tx, s.accHistoryC, s.accChangesC, false /* storage */, address[:], s.blockNr)
	if err != nil {
		return nil, err
	}
	if len(enc) == 0 {
		if s.trace {
			fmt.Printf("ReadAccountData [%x] => []\n", address)
		}
		return nil, nil
	}
	var a account.StateAccount
	if err = a.DecodeForStorage(enc); err != nil {
		return nil, err
	}
	// Phase B made historical Account entries self-contained (CodeHash inline);
	// no fallback to PlainContractCode is needed.
	if s.trace {
		fmt.Printf("ReadAccountData [%x] => [nonce: %d, balance: %d, codeHash: %x]\n", address, a.Nonce, &a.Balance, a.CodeHash)
	}
	return &a, nil
}

func (s *PlainState) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	compositeKey := modules.PlainGenerateCompositeStorageKey(address.Bytes(), key.Bytes())
	enc, err := GetAsOf(s.tx, s.storageHistoryC, s.storageChangesC, true /* storage */, compositeKey, s.blockNr)
	if err != nil {
		return nil, err
	}
	if s.trace {
		fmt.Printf("ReadAccountStorage [%x] [%x] => [%x]\n", address, *key, enc)
	}
	if len(enc) == 0 {
		return nil, nil
	}
	return enc, nil
}

func (s *PlainState) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	if bytes.Equal(codeHash[:], emptyCodeHash) {
		return nil, nil
	}
	code, err := s.tx.GetOne(modules.Code, codeHash[:])
	if s.trace {
		fmt.Printf("ReadAccountCode [%x %x] => [%x]\n", address, codeHash, code)
	}
	if len(code) == 0 {
		return nil, nil
	}
	return code, err
}

func (s *PlainState) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	code, err := s.ReadAccountCode(address, codeHash)
	return len(code), err
}

func (s *PlainState) UpdateAccountData(address types.Address, original, account *account.StateAccount) error {
	return nil
}

func (s *PlainState) DeleteAccount(address types.Address, original *account.StateAccount) error {
	return nil
}

func (s *PlainState) UpdateAccountCode(address types.Address, codeHash types.Hash, code []byte) error {
	return nil
}

func (s *PlainState) WriteAccountStorage(address types.Address, key *types.Hash, original, value *uint256.Int) error {
	t, ok := s.storage[address]
	if !ok {
		t = btree.New(16)
		s.storage[address] = t
	}
	h := types.NewHasher()
	defer types.ReturnHasherToPool(h)
	h.Sha.Reset()
	_, err := h.Sha.Write(key[:])
	if err != nil {
		return err
	}
	i := &storageItem{key: *key, value: *value}
	_, err = h.Sha.Read(i.seckey[:])
	if err != nil {
		return err
	}

	t.ReplaceOrInsert(i)
	return nil
}

func (s *PlainState) CreateContract(address types.Address) error {
	delete(s.storage, address)
	return nil
}
