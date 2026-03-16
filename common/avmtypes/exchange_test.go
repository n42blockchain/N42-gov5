package avmtypes

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/params"
)

func TestFromN42HeaderAllowsNilNumberAndDifficulty(t *testing.T) {
	header := FromN42Header(&block.Header{})
	if header == nil {
		t.Fatal("FromN42Header() returned nil")
	}
	if header.Number == nil || header.Number.Sign() != 0 {
		t.Fatalf("FromN42Header() number = %v", header.Number)
	}
	if header.Difficulty == nil || header.Difficulty.Sign() != 0 {
		t.Fatalf("FromN42Header() difficulty = %v", header.Difficulty)
	}
}

func TestFromN42HeaderAllowsNilInput(t *testing.T) {
	header := FromN42Header(nil)
	if header == nil {
		t.Fatal("FromN42Header(nil) returned nil")
	}
	if header.Number == nil || header.Number.Sign() != 0 {
		t.Fatalf("FromN42Header(nil) number = %v", header.Number)
	}
	if header.Difficulty == nil || header.Difficulty.Sign() != 0 {
		t.Fatalf("FromN42Header(nil) difficulty = %v", header.Difficulty)
	}
}

func TestFromN42HeaderPreservesExplicitValues(t *testing.T) {
	header := FromN42Header(&block.Header{
		Number:     uint256.NewInt(33),
		Difficulty: uint256.NewInt(44),
	})
	if header == nil {
		t.Fatal("FromN42Header() returned nil")
	}
	if got := header.Number.Uint64(); got != 33 {
		t.Fatalf("FromN42Header() number = %d, want 33", got)
	}
	if got := header.Difficulty.Uint64(); got != 44 {
		t.Fatalf("FromN42Header() difficulty = %d, want 44", got)
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
