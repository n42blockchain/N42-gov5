// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package internal

import (
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// makeTestBlock creates a real block for testing
func makeTestBlock(num uint64, stateRoot types.Hash) block.IBlock {
	header := &block.Header{
		Number:   uint256.NewInt(num),
		Root:     stateRoot,
		BaseFee:  uint256.NewInt(0),
		GasLimit: 1000000,
	}
	return block.NewBlock(header, nil)
}

func TestNewReorgAudit(t *testing.T) {
	// Default config
	audit := NewReorgAudit(nil)
	if audit == nil {
		t.Fatal("NewReorgAudit(nil) returned nil")
	}
	if !audit.config.Enable {
		t.Error("Default config should have Enable=true")
	}

	// Custom config
	config := &ReorgAuditConfig{
		Enable:        false,
		WarnDepth:     10,
		CriticalDepth: 20,
	}
	audit = NewReorgAudit(config)
	if audit.config.Enable {
		t.Error("Custom config should have Enable=false")
	}
	if audit.config.WarnDepth != 10 {
		t.Errorf("WarnDepth = %d, want 10", audit.config.WarnDepth)
	}
}

func TestDefaultReorgAuditConfig(t *testing.T) {
	config := DefaultReorgAuditConfig()

	if !config.Enable {
		t.Error("Enable should be true")
	}
	if config.DetailedLogs {
		t.Error("DetailedLogs should be false")
	}
	if config.WarnDepth != 5 {
		t.Errorf("WarnDepth = %d, want 5", config.WarnDepth)
	}
	if config.CriticalDepth != 10 {
		t.Errorf("CriticalDepth = %d, want 10", config.CriticalDepth)
	}
	if !config.ValidateStateRoot {
		t.Error("ValidateStateRoot should be true")
	}
}

func TestReorgAuditStartEnd(t *testing.T) {
	audit := NewReorgAudit(&ReorgAuditConfig{Enable: true})

	oldHead := makeTestBlock(100, types.HexToHash("0xaaaa"))
	newHead := makeTestBlock(101, types.HexToHash("0xbbbb"))
	commonBlock := makeTestBlock(99, types.HexToHash("0xcccc"))

	// Start reorg
	event := audit.StartReorg(oldHead, newHead)
	if event == nil {
		t.Fatal("StartReorg returned nil")
	}
	if event.OldHead.Hash() != oldHead.Hash() {
		t.Error("OldHead not set correctly")
	}
	if event.NewHead.Hash() != newHead.Hash() {
		t.Error("NewHead not set correctly")
	}

	// End reorg
	oldChain := []block.IBlock{oldHead}
	newChain := []block.IBlock{newHead}
	audit.EndReorg(event, commonBlock, oldChain, newChain, 5, 3, nil)

	if event.Depth != 1 {
		t.Errorf("Depth = %d, want 1", event.Depth)
	}
	if event.OldChainLen != 1 {
		t.Errorf("OldChainLen = %d, want 1", event.OldChainLen)
	}
	if event.NewChainLen != 1 {
		t.Errorf("NewChainLen = %d, want 1", event.NewChainLen)
	}
	if event.DeletedTxs != 5 {
		t.Errorf("DeletedTxs = %d, want 5", event.DeletedTxs)
	}
	if event.AddedTxs != 3 {
		t.Errorf("AddedTxs = %d, want 3", event.AddedTxs)
	}
	if !event.Success {
		t.Error("Success should be true")
	}
	if event.Duration == 0 {
		t.Error("Duration should be > 0")
	}
}

func TestReorgAuditStats(t *testing.T) {
	audit := NewReorgAudit(&ReorgAuditConfig{Enable: true})

	// Record some reorgs
	for i := 0; i < 5; i++ {
		oldHead := makeTestBlock(uint64(100+i), types.HexToHash("0xaaaa"))
		newHead := makeTestBlock(uint64(101+i), types.HexToHash("0xbbbb"))
		commonBlock := makeTestBlock(99, types.HexToHash("0xcccc"))

		event := audit.StartReorg(oldHead, newHead)
		audit.EndReorg(event, commonBlock, nil, nil, 0, 0, nil)
	}

	stats := audit.Stats()

	if stats.TotalReorgs != 5 {
		t.Errorf("TotalReorgs = %d, want 5", stats.TotalReorgs)
	}
	if stats.MaxDepthSeen == 0 {
		t.Error("MaxDepthSeen should be > 0")
	}
	if stats.LastReorgTime.IsZero() {
		t.Error("LastReorgTime should be set")
	}
}

func TestReorgAuditHooks(t *testing.T) {
	audit := NewReorgAudit(&ReorgAuditConfig{Enable: true})

	startCalled := false
	endCalled := false

	audit.SetOnReorgStart(func(event *ReorgEvent) {
		startCalled = true
	})
	audit.SetOnReorgEnd(func(event *ReorgEvent) {
		endCalled = true
	})

	oldHead := makeTestBlock(100, types.HexToHash("0xaaaa"))
	newHead := makeTestBlock(101, types.HexToHash("0xbbbb"))

	event := audit.StartReorg(oldHead, newHead)
	if !startCalled {
		t.Error("OnReorgStart hook not called")
	}

	audit.EndReorg(event, nil, nil, nil, 0, 0, nil)
	if !endCalled {
		t.Error("OnReorgEnd hook not called")
	}
}

func TestReorgAuditConcurrency(t *testing.T) {
	audit := NewReorgAudit(&ReorgAuditConfig{Enable: true})

	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()

			oldHead := makeTestBlock(uint64(100+i), types.HexToHash("0xaaaa"))
			newHead := makeTestBlock(uint64(101+i), types.HexToHash("0xbbbb"))
			commonBlock := makeTestBlock(99, types.HexToHash("0xcccc"))

			event := audit.StartReorg(oldHead, newHead)
			time.Sleep(time.Microsecond)
			audit.EndReorg(event, commonBlock, nil, nil, i, i, nil)
			_ = audit.Stats()
		}(i)
	}

	wg.Wait()

	stats := audit.Stats()
	if stats.TotalReorgs != 100 {
		t.Errorf("TotalReorgs = %d, want 100", stats.TotalReorgs)
	}
	t.Log("✓ ReorgAudit concurrent operations completed without race")
}

func TestGlobalReorgAudit(t *testing.T) {
	audit := GetReorgAudit()
	if audit == nil {
		t.Error("GetReorgAudit() returned nil")
	}

	// Set new config
	SetReorgAuditConfig(&ReorgAuditConfig{
		Enable:        true,
		WarnDepth:     3,
		CriticalDepth: 6,
	})

	newAudit := GetReorgAudit()
	if newAudit.config.WarnDepth != 3 {
		t.Errorf("WarnDepth = %d, want 3", newAudit.config.WarnDepth)
	}
}

func TestFormatBlockInfo(t *testing.T) {
	// nil block
	result := formatBlockInfo(nil)
	if result != "nil" {
		t.Errorf("formatBlockInfo(nil) = %s, want nil", result)
	}

	// valid block
	blk := makeTestBlock(12345, types.HexToHash("0x1234567890abcdef"))
	result = formatBlockInfo(blk)
	if result == "" || result == "nil" {
		t.Errorf("formatBlockInfo(block) should return non-empty string, got %s", result)
	}
}

