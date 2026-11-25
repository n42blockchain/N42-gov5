// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshotjoin

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// DumpAccount is one account line of reth's init-state JSONL.
type DumpAccount struct {
	Address string            `json:"address"`
	Balance string            `json:"balance"`
	Nonce   string            `json:"nonce"`
	Code    string            `json:"code,omitempty"`
	Storage map[string]string `json:"storage,omitempty"`
}

// ExportAccounts writes every plain-state account of tx, with its storage
// and code, as reth init-state JSONL lines in ascending address order. Each
// account and slot must be a live leaf of tree with the same bytes; with no
// limit, every live leaf must also be covered (EmptyAccountCoverage). It
// returns the numbers of accounts and storage slots written.
func ExportAccounts(tx kv.Tx, tree *qmdb.Tree, encoder *json.Encoder, limit uint64, logger log.Logger, started time.Time) (uint64, uint64, error) {
	accountCursor, err := tx.Cursor(modules.Account)
	if err != nil {
		return 0, 0, fmt.Errorf("open Account cursor: %w", err)
	}
	defer accountCursor.Close()
	storageCursor, err := tx.Cursor(modules.Storage)
	if err != nil {
		return 0, 0, fmt.Errorf("open Storage cursor: %w", err)
	}
	defer storageCursor.Close()

	storageKey, storageValue, storageErr := storageCursor.First()
	var accounts, slots uint64
	var exportedEmptyAccounts uint64
	for address, encoded, err := accountCursor.First(); ; address, encoded, err = accountCursor.Next() {
		if err != nil {
			return accounts, slots, fmt.Errorf("iterate Account: %w", err)
		}
		if address == nil {
			break
		}
		if limit > 0 && accounts >= limit {
			break
		}
		if len(address) != types.AddressLength {
			return accounts, slots, fmt.Errorf("malformed Account key length %d", len(address))
		}
		var stateAccount account.StateAccount
		if err := stateAccount.DecodeForStorage(encoded); err != nil {
			return accounts, slots, fmt.Errorf("decode account %x: %w", address, err)
		}
		addr := types.BytesToAddress(address)
		accountValue := commitment.EncodeAccountValue(&stateAccount)
		if err := VerifyExportValue(tree, qmdb.Hash(commitment.AccountKeyHash(addr)), accountValue); err != nil {
			return accounts, slots, fmt.Errorf("authenticate account %x: %w", address, err)
		}
		if bytes.Equal(accountValue, emptyAccountLeaf) {
			exportedEmptyAccounts++
		}

		if storageErr != nil {
			return accounts, slots, fmt.Errorf("iterate Storage: %w", storageErr)
		}
		if storageKey != nil && len(storageKey) < types.AddressLength {
			return accounts, slots, fmt.Errorf("malformed Storage key length %d", len(storageKey))
		}
		if storageKey != nil && bytes.Compare(storageKey[:types.AddressLength], address) < 0 {
			return accounts, slots, fmt.Errorf("orphan Storage row %x has no Account row", storageKey)
		}
		storage := make(map[string]string)
		for storageKey != nil && bytes.Equal(storageKey[:types.AddressLength], address) {
			if len(storageKey) != types.AddressLength+types.HashLength || len(storageValue) > types.HashLength {
				return accounts, slots, fmt.Errorf("malformed Storage row key=%d value=%d", len(storageKey), len(storageValue))
			}
			var value [types.HashLength]byte
			copy(value[len(value)-len(storageValue):], storageValue)
			key := types.BytesToHash(storageKey[types.AddressLength:])
			if err := VerifyExportValue(tree, qmdb.Hash(commitment.StorageKeyHash(addr, key)), value[:]); err != nil {
				return accounts, slots, fmt.Errorf("authenticate storage %x: %w", storageKey, err)
			}
			storage["0x"+hex.EncodeToString(storageKey[types.AddressLength:])] = "0x" + hex.EncodeToString(value[:])
			slots++
			storageKey, storageValue, storageErr = storageCursor.Next()
			if storageErr != nil {
				return accounts, slots, fmt.Errorf("iterate Storage: %w", storageErr)
			}
			if storageKey != nil && len(storageKey) < types.AddressLength {
				return accounts, slots, fmt.Errorf("malformed Storage key length %d", len(storageKey))
			}
		}

		item := DumpAccount{
			Address: "0x" + hex.EncodeToString(address),
			Balance: Quantity(stateAccount.Balance.Bytes()),
			Nonce:   fmt.Sprintf("0x%x", stateAccount.Nonce),
		}
		if len(storage) != 0 {
			item.Storage = storage
		}
		if !stateAccount.IsEmptyCodeHash() {
			code, err := tx.GetOne(modules.Code, stateAccount.CodeHash[:])
			if err != nil {
				return accounts, slots, fmt.Errorf("read code for %x: %w", address, err)
			}
			if len(code) == 0 {
				return accounts, slots, fmt.Errorf("missing bytecode %x for account %x", stateAccount.CodeHash, address)
			}
			if crypto.Keccak256Hash(code) != stateAccount.CodeHash {
				return accounts, slots, fmt.Errorf("bytecode hash mismatch for account %x", address)
			}
			item.Code = "0x" + hex.EncodeToString(code)
		}
		if err := encoder.Encode(&item); err != nil {
			return accounts, slots, fmt.Errorf("encode account %x: %w", address, err)
		}
		accounts++
		if accounts%100_000 == 0 {
			logger.Info("exporting reth state JSONL", "accounts", accounts, "storageSlots", slots, "elapsed", time.Since(started).Round(time.Second))
		}
	}
	if limit == 0 && storageKey != nil {
		return accounts, slots, fmt.Errorf("orphan Storage rows remain at %x", storageKey)
	}
	if storageErr != nil {
		return accounts, slots, fmt.Errorf("iterate Storage: %w", storageErr)
	}
	if limit == 0 {
		if err := VerifyExportCoverage(tx, tree, accounts+slots, exportedEmptyAccounts); err != nil {
			return accounts, slots, err
		}
	}
	return accounts, slots, nil
}

// emptyAccountLeaf is gov5's account leaf for an account without nonce,
// balance or code.
var emptyAccountLeaf = []byte{0}

// VerifyExportCoverage checks that the exported rows cover every live key of
// tree. QMDB retains EIP-161 empty accounts that the plain table removes;
// their canonical account value is one zero byte, storage values are 32
// bytes. All other live keys must appear exactly once among the rows.
func VerifyExportCoverage(tx kv.Tx, tree *qmdb.Tree, exported, exportedEmpty uint64) error {
	var retainedEmpty uint64
	err := tx.ForEach(qmdb.EntryTable, nil, func(k, v []byte) error {
		if len(k) != 8 {
			return fmt.Errorf("malformed QMDB entry key length %d", len(k))
		}
		active, ok := tree.SlotActive(binary.BigEndian.Uint64(k))
		if ok && active && len(v) == 33 && v[32] == 0 {
			retainedEmpty++
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("check export coverage: %w", err)
	}
	return checkCoverage(uint64(tree.LiveCount()), retainedEmpty, exported, exportedEmpty)
}

// checkCoverage is the coverage rule both the exporter and the importer
// apply (n42-rs init_snapshot.rs DumpCheck::finish is the same rule).
func checkCoverage(live, retainedEmpty, matched, emptyRows uint64) error {
	if retainedEmpty < emptyRows || retainedEmpty > live || matched != live-retainedEmpty+emptyRows {
		return fmt.Errorf("plain state is incomplete: %d rows for %d QMDB live keys (%d retained empty accounts)", matched, live, retainedEmpty)
	}
	return nil
}

// VerifyExportValue binds one exported value to a live leaf of tree. A
// matching forest root alone does not authenticate plain-table data.
func VerifyExportValue(tree *qmdb.Tree, key qmdb.Hash, value []byte) error {
	committed, ok, err := tree.GetChecked(key)
	if err != nil {
		return err
	}
	if !ok || !bytes.Equal(committed, value) {
		return fmt.Errorf("value is not authenticated by the QMDB root")
	}
	return nil
}

// Quantity renders big-endian bytes as a JSON-RPC quantity.
func Quantity(value []byte) string {
	digits := strings.TrimLeft(hex.EncodeToString(value), "0")
	if digits == "" {
		return "0x0"
	}
	return "0x" + digits
}
