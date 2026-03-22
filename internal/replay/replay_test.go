// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestNewEngine_Defaults(t *testing.T) {
	e, err := NewEngine(Config{
		SourcePath: "/tmp/src",
		TargetPath: "/tmp/dst",
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.cfg.BatchSize != 1000 {
		t.Errorf("default BatchSize: got %d, want 1000", e.cfg.BatchSize)
	}
	if e.cfg.FromBlock != 1 {
		t.Errorf("default FromBlock: got %d, want 1", e.cfg.FromBlock)
	}
	if e.cfg.ChainConfig == nil {
		t.Error("ChainConfig should default to MainnetChainConfig")
	}
	if len(e.cfg.SkipAddresses) != 3 {
		t.Errorf("default SkipAddresses: got %d, want 3", len(e.cfg.SkipAddresses))
	}
}

func TestNewEngine_MissingPaths(t *testing.T) {
	_, err := NewEngine(Config{TargetPath: "/tmp/dst"})
	if err == nil {
		t.Error("expected error for missing source path")
	}
	_, err = NewEngine(Config{SourcePath: "/tmp/src"})
	if err == nil {
		t.Error("expected error for missing target path")
	}
}

func TestStats_BlocksPerSecond(t *testing.T) {
	s := NewStats()
	s.StartTime = time.Now().Add(-1 * time.Second) // 1 second ago
	s.BlocksProcessed.Store(100)
	bps := s.BlocksPerSecond()
	if bps < 50 || bps > 200 {
		t.Errorf("expected BPS ~100, got %f", bps)
	}
}

func TestStats_SkipReasons(t *testing.T) {
	s := NewStats()
	s.skipReason("deposit_contract")
	s.skipReason("deposit_contract")
	s.skipReason("evm_error")
	if s.SkipReasons["deposit_contract"].Load() != 2 {
		t.Errorf("deposit_contract: got %d, want 2", s.SkipReasons["deposit_contract"].Load())
	}
	if s.SkipReasons["evm_error"].Load() != 1 {
		t.Errorf("evm_error: got %d, want 1", s.SkipReasons["evm_error"].Load())
	}
}

func TestDefaultSkipAddresses(t *testing.T) {
	deposit := types.HexToAddress("0x85A5E24ef94fe5bDD5055133E6bd00DcEA25F37D")
	nft := types.HexToAddress("0xF762E4Aa8Da0B9FC8113ECBFf6c84B3a6B7B5544")
	fuji := types.HexToAddress("0x8018c0ba6717FE077cB37Db5D6187B400ee76Eeb")
	random := types.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")

	if !DefaultSkipAddresses[deposit] {
		t.Error("deposit contract should be skipped")
	}
	if !DefaultSkipAddresses[nft] {
		t.Error("NFT contract should be skipped")
	}
	if !DefaultSkipAddresses[fuji] {
		t.Error("FUJI contract should be skipped")
	}
	if DefaultSkipAddresses[random] {
		t.Error("random address should not be skipped")
	}
}
