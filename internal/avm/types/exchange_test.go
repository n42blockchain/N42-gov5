package types

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestFromN42HeaderAllowsNilNumberAndDifficulty(t *testing.T) {
	converted := FromN42Header(&block.Header{})
	if converted == nil {
		t.Fatal("FromN42Header() returned nil")
	}
	if converted.Number == nil || converted.Number.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("FromN42Header() number = %v, want 0", converted.Number)
	}
	if converted.Difficulty == nil || converted.Difficulty.Cmp(big.NewInt(0)) != 0 {
		t.Fatalf("FromN42Header() difficulty = %v, want 0", converted.Difficulty)
	}
}

func TestFromN42HeaderPreservesNumberAndDifficulty(t *testing.T) {
	header := &block.Header{
		Number:     uint256.NewInt(9),
		Difficulty: uint256.NewInt(7),
	}

	converted := FromN42Header(header)
	if converted == nil {
		t.Fatal("FromN42Header() returned nil")
	}
	if converted.Number == nil || converted.Number.Cmp(big.NewInt(9)) != 0 {
		t.Fatalf("FromN42Header() number = %v, want 9", converted.Number)
	}
	if converted.Difficulty == nil || converted.Difficulty.Cmp(big.NewInt(7)) != 0 {
		t.Fatalf("FromN42Header() difficulty = %v, want 7", converted.Difficulty)
	}
}

func TestFromN42HeaderPreservesForkFields(t *testing.T) {
	withdrawalsHash := types.HexToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	parentBeaconRoot := types.HexToHash("0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	requestsHash := types.HexToHash("0xcccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
	blobGasUsed := uint64(3)
	excessBlobGas := uint64(5)

	converted := FromN42Header(&block.Header{
		UncleHash:        types.HexToHash("0xdddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"),
		WithdrawalsHash:  &withdrawalsHash,
		BlobGasUsed:      &blobGasUsed,
		ExcessBlobGas:    &excessBlobGas,
		ParentBeaconRoot: &parentBeaconRoot,
		RequestsHash:     &requestsHash,
	})
	if converted == nil {
		t.Fatal("FromN42Header() returned nil")
	}
	if converted.WithdrawalsHash == nil || ToastHash(*converted.WithdrawalsHash) != withdrawalsHash {
		t.Fatalf("FromN42Header() withdrawalsRoot = %v", converted.WithdrawalsHash)
	}
	if converted.BlobGasUsed == nil || *converted.BlobGasUsed != blobGasUsed {
		t.Fatalf("FromN42Header() blobGasUsed = %v", converted.BlobGasUsed)
	}
	if converted.ExcessBlobGas == nil || *converted.ExcessBlobGas != excessBlobGas {
		t.Fatalf("FromN42Header() excessBlobGas = %v", converted.ExcessBlobGas)
	}
	if converted.ParentBeaconRoot == nil || ToastHash(*converted.ParentBeaconRoot) != parentBeaconRoot {
		t.Fatalf("FromN42Header() parentBeaconBlockRoot = %v", converted.ParentBeaconRoot)
	}
	if converted.RequestsHash == nil || ToastHash(*converted.RequestsHash) != requestsHash {
		t.Fatalf("FromN42Header() requestsHash = %v", converted.RequestsHash)
	}
}

func TestMakeSignerAllowsNilInputs(t *testing.T) {
	if !MakeSigner(nil, nil).Equal(FrontierSigner{}) {
		t.Fatalf("MakeSigner(nil, nil) did not return FrontierSigner")
	}

	config := &params.ChainConfig{
		ChainID:     big.NewInt(1),
		LondonBlock: big.NewInt(0),
	}
	if !MakeSigner(config, nil).Equal(NewLondonSigner(config.ChainID)) {
		t.Fatalf("MakeSigner(config, nil) did not return LondonSigner")
	}
}
