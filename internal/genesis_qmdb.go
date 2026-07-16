// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// QMDB genesis seeding. On a fresh chain whose stateScheme is "qmdb", the
// genesis alloc must be inserted into the QMDB twig forest at init and the
// genesis header root set to the QMDB root — otherwise the forest starts
// empty (node.go LoadFrom warns and continues), the alloc is not reflected in
// any commitment, and a funded genesis is impossible on QMDB. This mirrors the
// known-good replay conversion path (internal/replay/engine_v2 readAllState →
// ComputeRoot → FlushTo): read the plain state the alloc just wrote, compute
// the QMDB root over it, and (in WriteGenesisState) persist the forest to the
// genesis tx so the node's block-producing computer reloads it.

package internal

import (
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// qmdbStateScheme is the ChainConfig.StateScheme value that selects the QMDB
// twig-forest commitment (params.StateCommitmentPresetQMDB).
const qmdbStateScheme = "qmdb"

var qmdbEmptyCodeHash = crypto.Keccak256Hash(nil)

// readPlainStateForQMDB scans the plain Account/Storage tables the genesis
// alloc just wrote into tx and returns them in the shape QMDBRootComputer
// expects. Byte-for-byte identical to the replay seeding read so genesis and
// later block production compose on the same key/value derivation.
func readPlainStateForQMDB(tx kv.Tx) (
	map[types.Address]*account.StateAccount,
	map[types.Address]map[types.Hash]*uint256.Int,
	error,
) {
	accounts := make(map[types.Address]*account.StateAccount)
	ac, err := tx.Cursor(modules.Account)
	if err != nil {
		return nil, nil, err
	}
	defer ac.Close()
	for k, v, err := ac.First(); k != nil; k, v, err = ac.Next() {
		if err != nil {
			return nil, nil, err
		}
		if len(k) != 20 {
			continue
		}
		var acct account.StateAccount
		if err := acct.DecodeForStorage(v); err != nil {
			return nil, nil, fmt.Errorf("qmdb genesis: decode account %x: %w", k, err)
		}
		if acct.CodeHash == (types.Hash{}) {
			acct.CodeHash = qmdbEmptyCodeHash
		}
		var addr types.Address
		copy(addr[:], k)
		accounts[addr] = acct.SelfCopy()
	}

	storage := make(map[types.Address]map[types.Hash]*uint256.Int)
	sc, err := tx.Cursor(modules.Storage)
	if err != nil {
		return nil, nil, err
	}
	defer sc.Close()
	for k, v, err := sc.First(); k != nil; k, v, err = sc.Next() {
		if err != nil {
			return nil, nil, err
		}
		if len(k) < 52 {
			continue
		}
		var addr types.Address
		copy(addr[:], k[:20])
		var slot types.Hash
		switch {
		case len(k) == 54:
			copy(slot[:], k[22:54])
		case len(k) >= 60:
			copy(slot[:], k[28:60])
		default:
			continue
		}
		val := new(uint256.Int)
		if len(v) > 0 {
			val.SetBytes(v)
		}
		if val.IsZero() {
			continue
		}
		if storage[addr] == nil {
			storage[addr] = make(map[types.Hash]*uint256.Int)
		}
		storage[addr][slot] = val
	}
	return accounts, storage, nil
}

// QMDBGenesisRoot computes the QMDB world root over the alloc that has been
// written to tx as plain state. Used by ToBlock to stamp the genesis header
// root (no persistence — the caller there uses a throwaway tx).
func QMDBGenesisRoot(tx kv.Tx) (types.Hash, error) {
	accounts, storage, err := readPlainStateForQMDB(tx)
	if err != nil {
		return types.Hash{}, err
	}
	rc := commitment.NewQMDBRootComputer()
	return rc.ComputeRoot(accounts, storage)
}

// SeedQMDBGenesis computes the QMDB root over the alloc on the real genesis
// tx AND flushes the twig forest into the QMDB tables on that tx, so the
// node's block-producing computer (LoadFrom at start) reconstructs the seeded
// forest. Returns the root for the caller to cross-check against the header.
func SeedQMDBGenesis(tx kv.RwTx) (types.Hash, error) {
	accounts, storage, err := readPlainStateForQMDB(tx)
	if err != nil {
		return types.Hash{}, err
	}
	rc := commitment.NewQMDBRootComputer()
	root, err := rc.ComputeRoot(accounts, storage)
	if err != nil {
		return types.Hash{}, err
	}
	if _, err := rc.FlushTo(tx); err != nil {
		return types.Hash{}, fmt.Errorf("qmdb genesis: flush forest: %w", err)
	}
	return root, nil
}

// usesQMDBGenesis reports whether the genesis config selects QMDB state.
func usesQMDBGenesis(stateScheme string) bool { return stateScheme == qmdbStateScheme }
