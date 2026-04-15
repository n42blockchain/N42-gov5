// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package ethel

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	statemod "github.com/n42blockchain/N42/modules/state"
)

func readIncarnation(tx kv.Getter, addr types.Address) (uint16, error) {
	b, err := tx.GetOne(modules.IncarnationMap, addr[:])
	if err != nil || len(b) == 0 {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func writeIncarnation(tx kv.RwTx, addr types.Address, incarnation uint16) error {
	if incarnation == 0 {
		return tx.Delete(modules.IncarnationMap, addr[:])
	}
	var enc [2]byte
	binary.BigEndian.PutUint16(enc[:], incarnation)
	return tx.Put(modules.IncarnationMap, addr[:], enc[:])
}

func valueIncarnation(tx kv.Getter, addr types.Address, encodedValue []byte) (uint16, error) {
	if len(encodedValue) == 0 {
		return readIncarnation(tx, addr)
	}
	incarnation, ok, err := statemod.DecodeAccountHistoryIncarnation(encodedValue)
	if err != nil {
		return 0, err
	}
	if ok {
		return incarnation, nil
	}
	return readIncarnation(tx, addr)
}

func valueHasCode(encodedValue []byte) (bool, error) {
	if len(encodedValue) == 0 {
		return false, nil
	}
	var acc account.StateAccount
	if err := acc.DecodeForStorage(encodedValue); err != nil {
		return false, err
	}
	return !acc.IsEmptyCodeHash(), nil
}

func revertIncarnation(tx kv.Getter, addr types.Address, oldValue, newValue []byte) (uint16, error) {
	if len(oldValue) > 0 {
		incarnation, ok, err := statemod.DecodeAccountHistoryIncarnation(oldValue)
		if err != nil {
			return 0, err
		}
		if ok {
			return incarnation, nil
		}
		return readIncarnation(tx, addr)
	}
	if len(newValue) == 0 {
		return 0, nil
	}
	currentIncarnation, err := valueIncarnation(tx, addr, newValue)
	if err != nil {
		return 0, err
	}
	hasCode, err := valueHasCode(newValue)
	if err != nil {
		return 0, err
	}
	if hasCode && currentIncarnation > 0 {
		return currentIncarnation - 1, nil
	}
	return currentIncarnation, nil
}

func applyAccountValue(tx kv.RwTx, addr types.Address, encodedValue []byte, incarnation uint16) error {
	if len(encodedValue) == 0 {
		if err := tx.Delete(modules.Account, addr[:]); err != nil {
			return err
		}
		if err := tx.Delete(modules.PlainContractCode, modules.PlainGenerateStoragePrefix(addr[:])); err != nil {
			return err
		}
		return writeIncarnation(tx, addr, incarnation)
	}

	restored, err := statemod.RestoreHistoricalAccountCodeHash(tx, addr[:], encodedValue)
	if err != nil {
		return err
	}
	if err := tx.Put(modules.Account, addr[:], restored); err != nil {
		return err
	}
	if err := writeIncarnation(tx, addr, incarnation); err != nil {
		return err
	}

	var acc account.StateAccount
	if err := acc.DecodeForStorage(restored); err != nil {
		return err
	}
	if acc.IsEmptyCodeHash() {
		return tx.Delete(modules.PlainContractCode, modules.PlainGenerateStoragePrefix(addr[:]))
	}
	if incarnation > 0 {
		if err := tx.Put(modules.PlainContractCode, modules.PlainGenerateStoragePrefixWithIncarnation(addr[:], incarnation), acc.CodeHash[:]); err != nil {
			return err
		}
	}
	return tx.Put(modules.PlainContractCode, modules.PlainGenerateStoragePrefix(addr[:]), acc.CodeHash[:])
}
