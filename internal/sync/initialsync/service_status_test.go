package initialsync

import (
	"testing"

	"github.com/holiman/uint256"
)

func TestServiceStatus(t *testing.T) {
	s := &Service{}
	s.syncing.Store(true)
	if err := s.Status(); err == nil {
		t.Fatalf("expected error while syncing")
	}
	s.syncing.Store(false)
	if err := s.Status(); err != nil {
		t.Fatalf("expected nil error when not syncing, got %v", err)
	}
}

func TestServiceSyncedMarkSyncingMarkSynced(t *testing.T) {
	s := &Service{}
	if s.Synced() {
		t.Fatalf("expected not synced initially")
	}
	s.markSyncing()
	if !s.syncing.Load() {
		t.Fatalf("expected syncing after markSyncing")
	}
	s.markSynced()
	if s.syncing.Load() {
		t.Fatalf("expected not syncing after markSynced")
	}
	if !s.Synced() {
		t.Fatalf("expected synced after markSynced")
	}
}

func TestServiceIsNearingSyncedNilTarget(t *testing.T) {
	s := &Service{}
	if s.isNearingSynced() {
		t.Fatalf("expected false when highestExpectedBlockNr is nil")
	}
}

func TestServiceIsNearingSyncedZeroTarget(t *testing.T) {
	s := &Service{highestExpectedBlockNr: uint256.NewInt(0)}
	if s.isNearingSynced() {
		t.Fatalf("expected false when highestExpectedBlockNr is zero")
	}
}

func TestServiceIsNearingSyncedPastTarget(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(100)}}
	s := &Service{
		cfg:                    &Config{Chain: chain},
		highestExpectedBlockNr: uint256.NewInt(90),
	}
	if !s.isNearingSynced() {
		t.Fatalf("expected true when current block is past target")
	}
}

func TestServiceIsNearingSyncedWithinThreshold(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(80)}}
	s := &Service{
		cfg:                    &Config{Chain: chain},
		highestExpectedBlockNr: uint256.NewInt(100),
	}
	if !s.isNearingSynced() {
		t.Fatalf("expected true when within nearSyncedThreshold blocks")
	}
}

func TestServiceIsNearingSyncedFarFromTarget(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	s := &Service{
		cfg:                    &Config{Chain: chain},
		highestExpectedBlockNr: uint256.NewInt(1000),
	}
	if s.isNearingSynced() {
		t.Fatalf("expected false when far from target")
	}
}

func TestServiceSyncing(t *testing.T) {
	chain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(0)}}
	s := &Service{
		cfg:                    &Config{Chain: chain},
		highestExpectedBlockNr: uint256.NewInt(1000),
	}
	s.syncing.Store(true)
	if !s.Syncing() {
		t.Fatalf("expected Syncing() true while far behind target")
	}

	// Within threshold: Syncing() should report false even though internal flag is true.
	nearChain := &initialSyncChainStub{current: &initialSyncBlockStub{number: uint256.NewInt(950)}}
	s2 := &Service{
		cfg:                    &Config{Chain: nearChain},
		highestExpectedBlockNr: uint256.NewInt(1000),
	}
	s2.syncing.Store(true)
	if s2.Syncing() {
		t.Fatalf("expected Syncing() false when nearing synced")
	}

	s.syncing.Store(false)
	if s.Syncing() {
		t.Fatalf("expected Syncing() false when not syncing")
	}
}
