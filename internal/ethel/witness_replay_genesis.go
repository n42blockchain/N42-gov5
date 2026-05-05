// Copyright 2022-2026 The N42 Authors
// witness_replay_genesis.go — block 0 (genesis) encoding for witness-
// replay. The user's chaindata MDBX is post-replay (Account/Storage
// already hold latest mainnet state), so it can't be iterated for the
// pristine genesis allocation. Build a fresh in-memory chaindata,
// load the embedded mainnet genesis JSON, then run the same iterator-
// based encoders ethexec uses. Result: byte-identical block 0
// acctcs/storcs.

package ethel

import (
	"context"
	"fmt"

	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

// encodeEthMainnetGenesis returns the V2 acctcs and storcs bytes for
// block 0 of Ethereum mainnet, derived from the embedded genesis JSON.
// The bytes match what ethexec's snapshotOutputs emits at block 0.
func encodeEthMainnetGenesis() (acctcs, storcs []byte, err error) {
	db := memdb.New("")
	defer db.Close()

	rwtx, err := db.BeginRw(context.Background())
	if err != nil {
		return nil, nil, fmt.Errorf("genesis memdb begin: %w", err)
	}
	defer rwtx.Rollback()

	if _, err := InitEthGenesisStateFromBytes(rwtx, params.EthMainnetGenesisJSON()); err != nil {
		return nil, nil, fmt.Errorf("genesis init: %w", err)
	}

	acctcs, err = EncodeGenesisAccounts(newGenesisAccountIterator(rwtx))
	if err != nil {
		return nil, nil, fmt.Errorf("genesis encode accounts: %w", err)
	}
	storcs, err = EncodeGenesisStorages(newGenesisStorageIterator(rwtx))
	if err != nil {
		return nil, nil, fmt.Errorf("genesis encode storages: %w", err)
	}
	return acctcs, storcs, nil
}
