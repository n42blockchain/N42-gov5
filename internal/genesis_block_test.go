package internal

import (
	"bytes"
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

func TestDecodeGenesisBalance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "decimal", input: "1000000000000000000", want: "1000000000000000000"},
		{name: "hex", input: "0x1234", want: "4660"},
		{name: "hex with leading zero", input: "0x00", want: "0"},
		{name: "empty", input: "", want: "0"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := decodeGenesisBalance(tc.input)
			if err != nil {
				t.Fatalf("decodeGenesisBalance(%q) failed: %v", tc.input, err)
			}
			if got.Dec() != tc.want {
				t.Fatalf("decodeGenesisBalance(%q) = %s, want %s", tc.input, got.Dec(), tc.want)
			}
		})
	}
}

func TestGenesisBlockToBlockRejectsMissingConfig(t *testing.T) {
	t.Parallel()

	genesis := &GenesisBlock{
		GenesisConfig: &conf.Genesis{},
	}

	_, _, err := genesis.ToBlock()
	if !errors.Is(err, ErrGenesisNoConfig) {
		t.Fatalf("ToBlock() error = %v, want %v", err, ErrGenesisNoConfig)
	}
}

func TestGenesisBlockToBlockUsesEthereumEmptyRoots(t *testing.T) {
	t.Parallel()

	genesis := &GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: &params.ChainConfig{},
			Alloc:  conf.GenesisAlloc{},
		},
	}

	blk, _, err := genesis.ToBlock()
	if err != nil {
		t.Fatalf("ToBlock() error = %v", err)
	}

	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil {
		t.Fatalf("Header() type = %T, want *block.Header", blk.Header())
	}
	// N42 mainnet uses NilHash (keccak256(nil)) for empty tx/receipt tries.
	if header.TxHash != hash.NilHash {
		t.Fatalf("TxHash = %s, want NilHash %s", header.TxHash, hash.NilHash)
	}
	if header.ReceiptHash != hash.NilHash {
		t.Fatalf("ReceiptHash = %s, want NilHash %s", header.ReceiptHash, hash.NilHash)
	}
	if header.Number == nil || header.Number.Cmp(uint256.NewInt(0)) != 0 {
		t.Fatalf("Number = %v, want genesis number 0", header.Number)
	}
}

func TestGenesisBlockToBlockUsesExplicitHeaderFields(t *testing.T) {
	t.Parallel()

	genesis := &GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config:        &params.ChainConfig{},
			Alloc:         conf.GenesisAlloc{},
			StateRoot:     types.HexToHash("0x95a6b74fbcb35dd5bd4dc03e03236164da625fc661cadfe58674b7cd27e664e1"),
			TxHash:        hash.EmptyRootHash,
			ReceiptHash:   hash.EmptyRootHash,
			BlobGasUsed:   0,
			ExcessBlobGas: transaction.BlobTxTargetBlobGasPerBlock,
		},
	}

	blk, _, err := genesis.ToBlock()
	if err != nil {
		t.Fatalf("ToBlock() error = %v", err)
	}

	header := blk.Header().(*block.Header)
	if header.Root != genesis.GenesisConfig.StateRoot {
		t.Fatalf("Root = %s, want %s", header.Root, genesis.GenesisConfig.StateRoot)
	}
	if header.TxHash != genesis.GenesisConfig.TxHash {
		t.Fatalf("TxHash = %s, want %s", header.TxHash, genesis.GenesisConfig.TxHash)
	}
	if header.ReceiptHash != genesis.GenesisConfig.ReceiptHash {
		t.Fatalf("ReceiptHash = %s, want %s", header.ReceiptHash, genesis.GenesisConfig.ReceiptHash)
	}
	if header.BlobGasUsed != genesis.GenesisConfig.BlobGasUsed {
		t.Fatalf("BlobGasUsed = %d, want %d", header.BlobGasUsed, genesis.GenesisConfig.BlobGasUsed)
	}
	if header.ExcessBlobGas != genesis.GenesisConfig.ExcessBlobGas {
		t.Fatalf("ExcessBlobGas = %d, want %d", header.ExcessBlobGas, genesis.GenesisConfig.ExcessBlobGas)
	}
}

func TestBuildConsensusExtraDataSortsSigners(t *testing.T) {
	t.Parallel()

	genesis := &conf.Genesis{
		Config: &params.ChainConfig{Consensus: params.CliqueConsensus},
		Miners: []string{
			"0x0000000000000000000000000000000000000002",
			"0x0000000000000000000000000000000000000001",
		},
	}

	extraData, err := buildConsensusExtraData(genesis)
	if err != nil {
		t.Fatalf("buildConsensusExtraData() error = %v", err)
	}
	if len(extraData) != 32+2*types.AddressLength+65 {
		t.Fatalf("extra data len = %d, want %d", len(extraData), 32+2*types.AddressLength+65)
	}

	first := types.HexToAddress("0x0000000000000000000000000000000000000001")
	second := types.HexToAddress("0x0000000000000000000000000000000000000002")
	if !bytes.Equal(extraData[32:32+types.AddressLength], first[:]) {
		t.Fatalf("first signer bytes = %x, want %x", extraData[32:32+types.AddressLength], first[:])
	}
	if !bytes.Equal(extraData[32+types.AddressLength:32+2*types.AddressLength], second[:]) {
		t.Fatalf("second signer bytes = %x, want %x", extraData[32+types.AddressLength:32+2*types.AddressLength], second[:])
	}
}

func TestGenesisBlockToBlockUsesLegacyZeroRootsForAPOS(t *testing.T) {
	t.Parallel()

	genesis := &GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config: &params.ChainConfig{Consensus: params.AposConsensu},
			Alloc:  conf.GenesisAlloc{},
		},
	}

	blk, _, err := genesis.ToBlock()
	if err != nil {
		t.Fatalf("ToBlock() error = %v", err)
	}

	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil {
		t.Fatalf("Header() type = %T, want *block.Header", blk.Header())
	}
	if header.TxHash != (types.Hash{}) {
		t.Fatalf("TxHash = %s, want zero hash", header.TxHash)
	}
	if header.ReceiptHash != (types.Hash{}) {
		t.Fatalf("ReceiptHash = %s, want zero hash", header.ReceiptHash)
	}
}
