package internal

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
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
	if header.TxHash != hash.EmptyRootHash {
		t.Fatalf("TxHash = %s, want Ethereum empty tx root", header.TxHash)
	}
	if header.ReceiptHash != hash.EmptyRootHash {
		t.Fatalf("ReceiptHash = %s, want Ethereum empty receipt root", header.ReceiptHash)
	}
	if header.Number == nil || header.Number.Cmp(uint256.NewInt(0)) != 0 {
		t.Fatalf("Number = %v, want genesis number 0", header.Number)
	}
}

func TestGenesisBlockToBlockUsesExplicitHeaderFields(t *testing.T) {
	t.Parallel()

	genesis := &GenesisBlock{
		GenesisConfig: &conf.Genesis{
			Config:      &params.ChainConfig{},
			Alloc:       conf.GenesisAlloc{},
			StateRoot:   types.HexToHash("0x95a6b74fbcb35dd5bd4dc03e03236164da625fc661cadfe58674b7cd27e664e1"),
			TxHash:      hash.EmptyRootHash,
			ReceiptHash: hash.EmptyRootHash,
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
}
