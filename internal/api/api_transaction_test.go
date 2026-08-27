package api

import (
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestTransactionHeadAndRulesUsesEngineOverlay(t *testing.T) {
	canonical := block.NewBlock(&block.Header{
		Number: uint256.NewInt(0),
		Time:   9,
	}, nil)
	overlayHead := block.NewBlock(&block.Header{
		Number: uint256.NewInt(1),
		Time:   10,
	}, nil)
	backend := &API{
		bc:            &gasPriceChainStub{current: canonical},
		chainConfig:   &params.ChainConfig{ShanghaiTime: big.NewInt(10)},
		engineOverlay: newEngineOverlay(),
	}
	backend.engineOverlay.importBlock(overlayHead, types.HexToHash("0x01"), nil)

	head, rules, err := NewTransactionAPI(backend, new(AddrLocker)).transactionHeadAndRules()
	if err != nil {
		t.Fatal(err)
	}
	if head.Number64().Uint64() != 1 || head.Time() != 10 {
		t.Fatalf("transaction head = (%d, %d), want overlay head (1, 10)", head.Number64().Uint64(), head.Time())
	}
	if !rules.IsShanghai {
		t.Fatal("transaction rules did not activate Shanghai at the overlay head")
	}
}
