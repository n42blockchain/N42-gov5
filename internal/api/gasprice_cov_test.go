// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"math/big"
	"sort"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

func TestNewOracle_SanitizesInvalidConfig(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	bc := newPreciseChainStub(cfg, nil, genesis)

	gpo := conf.GpoConfig{
		Blocks:           -1,
		Percentile:       -5,
		MaxPrice:         nil,
		IgnorePrice:      nil,
		MaxHeaderHistory: 0,
		MaxBlockHistory:  0,
		Default:          big.NewInt(1),
	}
	o := NewOracle(bc, nil, cfg, gpo)
	if o.checkBlocks != 1 {
		t.Fatalf("checkBlocks = %d, want sanitized to 1", o.checkBlocks)
	}
	if o.percentile != 0 {
		t.Fatalf("percentile = %d, want sanitized to 0", o.percentile)
	}
	if o.maxHeaderHistory != 1 || o.maxBlockHistory != 1 {
		t.Fatalf("history = %d/%d, want 1/1", o.maxHeaderHistory, o.maxBlockHistory)
	}

	// Percentile > 100 also gets clamped.
	gpo2 := conf.GpoConfig{Percentile: 500, Default: big.NewInt(1)}
	o2 := NewOracle(bc, nil, cfg, gpo2)
	if o2.percentile != 100 {
		t.Fatalf("percentile = %d, want clamped to 100", o2.percentile)
	}
}

func TestBigIntArray_Sort(t *testing.T) {
	arr := bigIntArray{big.NewInt(3), big.NewInt(1), big.NewInt(2)}
	sort.Sort(arr)
	if arr[0].Int64() != 1 || arr[1].Int64() != 2 || arr[2].Int64() != 3 {
		t.Fatalf("bigIntArray sort = %v, want ascending", arr)
	}
}

func TestTxSorter_Sort(t *testing.T) {
	baseFee := uint256.NewInt(1)
	tx1 := transaction.NewTransaction(0, types.Address{}, nil, uint256.NewInt(0), 21000, uint256.NewInt(5), nil)
	tx2 := transaction.NewTransaction(1, types.Address{}, nil, uint256.NewInt(0), 21000, uint256.NewInt(2), nil)
	txs := []*transaction.Transaction{tx1, tx2}

	s := newSorter(txs, baseFee)
	if s.Len() != 2 {
		t.Fatalf("Len() = %d, want 2", s.Len())
	}
	sort.Sort(s)
	tip0, _ := s.txs[0].EffectiveGasTip(baseFee)
	tip1, _ := s.txs[1].EffectiveGasTip(baseFee)
	if tip0.Cmp(tip1) > 0 {
		t.Fatalf("txSorter did not sort ascending: %v > %v", tip0, tip1)
	}

	s2 := newSorter([]*transaction.Transaction{tx1, tx2}, baseFee)
	s2.Swap(0, 1)
	if s2.txs[0] != tx2 || s2.txs[1] != tx1 {
		t.Fatal("Swap() did not swap elements")
	}
}
