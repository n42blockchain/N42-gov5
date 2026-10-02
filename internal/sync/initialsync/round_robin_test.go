package initialsync

import (
	"context"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
)

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "30s"},
		{90 * time.Second, "1m30s"},
		{90 * time.Minute, "1h30m"},
		{50 * time.Hour, "2d2h"},
	}
	for _, c := range cases {
		if got := formatDuration(c.d); got != c.want {
			t.Fatalf("formatDuration(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestSkipProcessedBlocksAllPast(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(10)}}
	s := &Service{cfg: &Config{Chain: chain}}

	blocks := []block.IBlock{
		&initialSyncBlockStub{number: uint256.NewInt(5)},
		&initialSyncBlockStub{number: uint256.NewInt(8)},
	}
	out, err := s.skipProcessedBlocks(context.Background(), blocks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Fatalf("expected nil output for all-processed blocks, got %v", out)
	}
}

func TestSkipProcessedBlocksPartial(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(10)}}
	s := &Service{cfg: &Config{Chain: chain}}

	b11 := &initialSyncBlockStub{number: uint256.NewInt(11)}
	blocks := []block.IBlock{
		&initialSyncBlockStub{number: uint256.NewInt(5)},
		b11,
	}
	out, err := s.skipProcessedBlocks(context.Background(), blocks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0] != b11 {
		t.Fatalf("expected only the unprocessed block, got %v", out)
	}
}

func TestSkipProcessedBlocksNoneProcessed(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	s := &Service{cfg: &Config{Chain: chain}}

	blocks := []block.IBlock{
		&initialSyncBlockStub{number: uint256.NewInt(1)},
		&initialSyncBlockStub{number: uint256.NewInt(2)},
	}
	out, err := s.skipProcessedBlocks(context.Background(), blocks)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected both blocks unprocessed, got %v", out)
	}
}

func TestSkipProcessedBlocksChainError(t *testing.T) {
	s := &Service{cfg: &Config{Chain: nil}}
	if _, err := s.skipProcessedBlocks(context.Background(), []block.IBlock{&initialSyncBlockStub{number: uint256.NewInt(1)}}); err == nil {
		t.Fatalf("expected error for nil chain")
	}
}

func TestProcessBatchedBlocksEmptyInput(t *testing.T) {
	s := &Service{}
	if _, err := s.processBatchedBlocks(context.Background(), nil, nil); err == nil {
		t.Fatalf("expected error for empty block list")
	}
}
