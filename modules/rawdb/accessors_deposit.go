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

package rawdb

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/crypto/bls"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// PutDeposit stores a deposit record (public key + amount) for an address.
func PutDeposit(db kv.Putter, addr types.Address, pub types.PublicKey, amount uint256.Int) error {
	const amountLen = 32
	data := make([]byte, types.PublicKeyLength+amountLen)
	copy(data[:types.PublicKeyLength], pub.Bytes())
	amountBytes := amount.Bytes32()
	copy(data[types.PublicKeyLength:], amountBytes[:])
	if err := db.Put(modules.Deposit, addr[:], data); err != nil {
		return fmt.Errorf("failed to store address deposit: %w", err)
	}
	return nil
}

// GetDeposit retrieves the public key and amount for a deposit address.
func GetDeposit(db kv.Getter, addr types.Address) (types.PublicKey, *uint256.Int, error) {
	const amountLen = 32
	const expectedLen = types.PublicKeyLength + amountLen
	valBytes, err := db.GetOne(modules.Deposit, addr[:])
	if err != nil {
		return types.PublicKey{}, nil, err
	}
	if len(valBytes) < expectedLen {
		return types.PublicKey{}, nil, fmt.Errorf("invalid deposit data length: expected %d, got %d", expectedLen, len(valBytes))
	}
	if _, err = bls.PublicKeyFromBytes(valBytes[:types.PublicKeyLength]); err != nil {
		return types.PublicKey{}, nil, fmt.Errorf("invalid BLS public key: %w", err)
	}
	var pubkey types.PublicKey
	if err = pubkey.SetBytes(valBytes[:types.PublicKeyLength]); err != nil {
		return types.PublicKey{}, nil, fmt.Errorf("failed to set public key bytes: %w", err)
	}
	amount := new(uint256.Int).SetBytes(valBytes[types.PublicKeyLength:expectedLen])
	return pubkey, amount, nil
}

// DeleteDeposit removes Deposit data associated with an address.
func DeleteDeposit(db kv.Deleter, addr types.Address) error {
	return db.Delete(modules.Deposit, addr[:])
}

// IsDeposit checks whether the given address has a deposit record.
func IsDeposit(db kv.Getter, addr types.Address) bool {
	has, _ := db.Has(modules.Deposit, addr[:])
	return has
}

// DepositNum returns the total number of deposit records.
func DepositNum(tx kv.Tx) (uint64, error) {
	cur, err := tx.Cursor(modules.Deposit)
	if err != nil {
		return 0, err
	}
	defer cur.Close()
	return cur.Count()
}
