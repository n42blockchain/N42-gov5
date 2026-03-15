package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

type feeHistoryHeaderStub struct {
	*block.Header
}

func TestProcessBlockRejectsUnexpectedHeaderType(t *testing.T) {
	oracle := &Oracle{
		chainConfig: &params.ChainConfig{
			LondonBlock: big.NewInt(0),
		},
	}
	header := &feeHistoryHeaderStub{
		Header: &block.Header{
			Number:     uint256.NewInt(1),
			Difficulty: uint256.NewInt(0),
			BaseFee:    uint256.NewInt(1),
		},
	}
	bf := &blockFees{
		blockNumber: 1,
		header:      header,
		block:       block.NewBlock(header.Header, nil),
	}

	oracle.processBlock(bf, nil)
	if bf.err == nil || bf.err.Error() != "unexpected header type in processBlock" {
		t.Fatalf("processBlock() error = %v", bf.err)
	}
}

func TestProcessBlockRejectsNilBlock(t *testing.T) {
	oracle := &Oracle{
		chainConfig: &params.ChainConfig{
			LondonBlock: big.NewInt(10),
		},
	}
	header := &block.Header{
		Number:     uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
		BaseFee:    uint256.NewInt(1),
	}
	bf := &blockFees{
		blockNumber: 1,
		header:      header,
		block:       nil,
	}

	oracle.processBlock(bf, nil)
	if bf.err == nil || bf.err.Error() != "block is nil in processBlock" {
		t.Fatalf("processBlock() error = %v", bf.err)
	}
}

func TestResolveBlockRangeRejectsNilHeadNumber(t *testing.T) {
	oracle := &Oracle{
		backend: &gasPriceChainStub{current: &gasPriceBlockStub{header: &block.Header{}}},
	}

	_, _, _, _, err := oracle.resolveBlockRange(context.Background(), jsonrpc.LatestBlockNumber, 1)
	if err == nil || err.Error() != "current block number not available" {
		t.Fatalf("resolveBlockRange() error = %v", err)
	}
}
