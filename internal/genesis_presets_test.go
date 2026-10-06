// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Covers GenesisByChainName's dispatch table (every embedded preset builds a
// non-nil, internally consistent *conf.Genesis and produces a genesis block
// via ToBlock), plus GenesisBlock.Write's full DB-write path and
// EthereumCompatibleGenesisHash.

package internal

import (
	"testing"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params/networkname"
)

func TestGenesisByChainNameKnownChains(t *testing.T) {
	names := []string{
		networkname.MainnetChainName,
		"mainnet_compat",
		"mainnet_v2",
		"mainnet_v2_staggered",
		"mainnet_mpt",
		"mainnet_qmdb",
		"mainnet_qmdb_staggered",
		"qs_epoch_test",
		networkname.TestnetChainName,
	}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			g := GenesisByChainName(name)
			if g == nil {
				t.Fatalf("GenesisByChainName(%q) = nil, want a genesis", name)
			}
			if g.Config == nil {
				t.Fatalf("GenesisByChainName(%q).Config = nil", name)
			}
			blk, _, err := (&GenesisBlock{GenesisConfig: g}).ToBlock()
			if err != nil {
				t.Fatalf("ToBlock() for %q failed: %v", name, err)
			}
			if blk.Number64().Uint64() != 0 {
				t.Fatalf("ToBlock() for %q produced block number %d, want 0", name, blk.Number64().Uint64())
			}
		})
	}
}

func TestGenesisByChainNameUnknownReturnsNil(t *testing.T) {
	if g := GenesisByChainName("does-not-exist"); g != nil {
		t.Fatalf("GenesisByChainName(unknown) = %v, want nil", g)
	}
}

func TestGenesisWritePersistsBlockAndChainConfig(t *testing.T) {
	g := GenesisByChainName(networkname.TestnetChainName)
	if g == nil {
		t.Fatal("GenesisByChainName(testnet) = nil")
	}
	db := newRealignTestDB(t)

	var writtenHash [32]byte
	if err := db.Update(t.Context(), func(tx kv.RwTx) error {
		blk, _, err := (&GenesisBlock{GenesisConfig: g}).Write(tx)
		if err != nil {
			return err
		}
		writtenHash = blk.Hash()
		return nil
	}); err != nil {
		t.Fatalf("GenesisBlock.Write() failed: %v", err)
	}

	if err := db.View(t.Context(), func(tx kv.Tx) error {
		canon, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		if canon != writtenHash {
			t.Fatalf("canonical hash at 0 = %x, want %x", canon, writtenHash)
		}
		cfg, err := rawdb.ReadChainConfig(tx, writtenHash)
		if err != nil {
			return err
		}
		if cfg == nil {
			t.Fatalf("ReadChainConfig(genesis hash) = nil")
		}
		td, err := rawdb.ReadTd(tx, writtenHash, 0)
		if err != nil {
			return err
		}
		if td == nil || td.Uint64() != 0 {
			t.Fatalf("genesis Td = %v, want 0", td)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGenesisWriteRejectsNilConfig(t *testing.T) {
	db := newRealignTestDB(t)
	err := db.Update(t.Context(), func(tx kv.RwTx) error {
		_, _, err := (&GenesisBlock{}).Write(tx)
		return err
	})
	if err != ErrGenesisNoConfig {
		t.Fatalf("Write() with nil config = %v, want ErrGenesisNoConfig", err)
	}
}

func TestEthereumCompatibleGenesisHashMatchesToBlock(t *testing.T) {
	g := GenesisByChainName(networkname.TestnetChainName)
	if g == nil {
		t.Fatal("GenesisByChainName(testnet) = nil")
	}
	gotHash, err := EthereumCompatibleGenesisHash(g)
	if err != nil {
		t.Fatal(err)
	}
	blk, _, err := (&GenesisBlock{GenesisConfig: g}).ToBlock()
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != blk.Hash() {
		t.Fatalf("EthereumCompatibleGenesisHash() = %s, want %s", gotHash.Hex(), blk.Hash().Hex())
	}
}

func TestEthereumCompatibleGenesisHashRejectsNilConfig(t *testing.T) {
	if _, err := EthereumCompatibleGenesisHash(nil); err != ErrGenesisNoConfig {
		t.Fatalf("EthereumCompatibleGenesisHash(nil) = %v, want ErrGenesisNoConfig", err)
	}
}

func TestReadGenesisAllocMissingFileErrors(t *testing.T) {
	if _, err := readGenesisAlloc("allocs/does-not-exist.json"); err == nil {
		t.Fatal("readGenesisAlloc(missing file) = nil error, want error")
	}
}

func TestMustReadGenesisAllocPanicsOnMissingFile(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("mustReadGenesisAlloc(missing file) did not panic")
		}
	}()
	mustReadGenesisAlloc("allocs/does-not-exist.json")
}
