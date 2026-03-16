package types

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
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
