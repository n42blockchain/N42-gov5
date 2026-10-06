// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Tests for EpochManager construction/persistence helpers and
// ValidatorSet.IndexOfPublicKey / LeaderTenure in validator.go that are not
// already exercised by reconfig_test.go.

package hotstuff

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls/blst"
)

func TestIndexOfPublicKey(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)

	for i, v := range validators {
		if got := vs.IndexOfPublicKey(v.PublicKey); got != i {
			t.Fatalf("expected index %d, got %d", i, got)
		}
	}

	other, _ := blst.RandKey()
	if got := vs.IndexOfPublicKey(other.PublicKey()); got != -1 {
		t.Fatalf("expected -1 for unknown key, got %d", got)
	}
	if got := vs.IndexOfPublicKey(nil); got != -1 {
		t.Fatalf("expected -1 for nil key, got %d", got)
	}
}

func TestLeaderTenureDefault(t *testing.T) {
	if os.Getenv("N42_HOTSTUFF_LEADER_TENURE") != "" {
		t.Skip("N42_HOTSTUFF_LEADER_TENURE set in environment; default not observable")
	}
	if got := LeaderTenure(); got != 1 {
		t.Fatalf("expected default leader tenure 1, got %d", got)
	}
}

func TestNewEpochManagerFromEpoch(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerFromEpoch(vs, 10, 5)

	if em.CurrentEpoch() != 5 {
		t.Fatalf("expected current epoch 5, got %d", em.CurrentEpoch())
	}
	if em.EpochLength() != 10 {
		t.Fatalf("expected epoch length 10, got %d", em.EpochLength())
	}
	if em.CurrentValidatorSet().Len() != 4 {
		t.Fatalf("expected validator set len 4, got %d", em.CurrentValidatorSet().Len())
	}
}

func TestEpochManagerCurrentEpochInfoAndString(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)

	epoch, got, f, ok := em.CurrentEpochInfo()
	if !ok {
		t.Fatal("expected ok=true for a populated set")
	}
	if epoch != 0 {
		t.Fatalf("expected epoch 0, got %d", epoch)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 validators, got %d", len(got))
	}
	if f != 1 {
		t.Fatalf("expected f=1, got %d", f)
	}

	str := em.String()
	if !strings.Contains(str, "EpochManager") || !strings.Contains(str, "validators=4") {
		t.Fatalf("unexpected String() output: %q", str)
	}
}

func TestEpochManagerCurrentEpochInfoNilSet(t *testing.T) {
	em := &EpochManager{}
	if _, _, _, ok := em.CurrentEpochInfo(); ok {
		t.Fatal("expected ok=false when currentSet is nil")
	}
}

func TestEpochManagerStagedEpochInfo(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)

	if _, _, _, ok := em.StagedEpochInfo(); ok {
		t.Fatal("expected ok=false before staging")
	}

	next := makeTestValidators(5)
	em.StageNextEpoch(next, 1)

	epoch, got, f, ok := em.StagedEpochInfo()
	if !ok {
		t.Fatal("expected ok=true after staging")
	}
	if epoch != 1 {
		t.Fatalf("expected staged epoch 1, got %d", epoch)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 staged validators, got %d", len(got))
	}
	if f != 1 {
		t.Fatalf("expected f=1, got %d", f)
	}
}

func TestEpochManagerRestoreActiveSet(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)

	restored := makeTestValidators(6)
	em.RestoreActiveSet(3, restored, 1)

	if em.CurrentEpoch() != 3 {
		t.Fatalf("expected epoch 3 after restore, got %d", em.CurrentEpoch())
	}
	if em.CurrentValidatorSet().Len() != 6 {
		t.Fatalf("expected restored set len 6, got %d", em.CurrentValidatorSet().Len())
	}
	// Since the set size changed, the old set should be retained as prevSet
	// and resolvable via FindValidatorSetByLen.
	if got := em.FindValidatorSetByLen(4); got == nil || got.Len() != 4 {
		t.Fatalf("expected previous 4-validator set to be retained, got %v", got)
	}
}

// buildBLSHexSchedule builds an EpochSchedule JSON-ready struct with real
// random BLS keys, hex-encoded as ParseValidators expects.
func buildEpochScheduleEntry(epoch uint64, n int) (EpochScheduleEntry, []ValidatorInfo) {
	infos := makeTestValidators(n)
	entry := EpochScheduleEntry{Epoch: epoch}
	for _, v := range infos {
		entry.Validators = append(entry.Validators, EpochScheduleValidator{
			Address: v.Address.Hex(),
			BLSKey:  "0x" + hex.EncodeToString(v.PublicKey.Marshal()),
		})
	}
	return entry, infos
}

func TestNewEpochManagerFromSchedule(t *testing.T) {
	entry0, _ := buildEpochScheduleEntry(0, 4)
	entry1, infos1 := buildEpochScheduleEntry(1, 4)
	schedule := &EpochSchedule{Entries: []EpochScheduleEntry{entry0, entry1}}

	em, err := NewEpochManagerFromSchedule(schedule, 1, 10)
	if err != nil {
		t.Fatalf("NewEpochManagerFromSchedule: %v", err)
	}
	if em.CurrentEpoch() != 1 {
		t.Fatalf("expected current epoch 1, got %d", em.CurrentEpoch())
	}
	if em.CurrentValidatorSet().Len() != uint32(len(infos1)) {
		t.Fatalf("expected %d validators, got %d", len(infos1), em.CurrentValidatorSet().Len())
	}
	// Historical set for epoch 0 should have been rebuilt.
	if got := em.FindValidatorSetByLen(4); got == nil {
		t.Fatal("expected a 4-validator set to be resolvable (current or historical)")
	}
}

func TestNewEpochManagerFromScheduleNilSchedule(t *testing.T) {
	if _, err := NewEpochManagerFromSchedule(nil, 1, 10); err == nil {
		t.Fatal("expected error for nil schedule")
	}
}

func TestNewEpochManagerFromScheduleNoEntry(t *testing.T) {
	schedule := &EpochSchedule{}
	if _, err := NewEpochManagerFromSchedule(schedule, 1, 10); err == nil {
		t.Fatal("expected error for schedule with no matching entry")
	}
}

func TestPreStageFromSchedule(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)

	entry1, _ := buildEpochScheduleEntry(1, 5)
	schedule := &EpochSchedule{Entries: []EpochScheduleEntry{entry1}}

	if !em.PreStageFromSchedule(schedule) {
		t.Fatal("expected PreStageFromSchedule to stage the next epoch")
	}
	if !em.HasStagedNext() {
		t.Fatal("expected HasStagedNext to be true")
	}
	// Staging twice should be a no-op (nextSet already set).
	if em.PreStageFromSchedule(schedule) {
		t.Fatal("expected PreStageFromSchedule to no-op once staged")
	}
}

func TestPreStageFromScheduleNilOrNoEntry(t *testing.T) {
	validators := makeTestValidators(4)
	vs := NewValidatorSet(validators, 1)
	em := NewEpochManagerWithLength(vs, 10)

	if em.PreStageFromSchedule(nil) {
		t.Fatal("expected false for nil schedule")
	}

	empty := &EpochSchedule{}
	if em.PreStageFromSchedule(empty) {
		t.Fatal("expected false when schedule has no entry for next epoch")
	}
}

func TestEpochScheduleEntryParseValidatorsBadKey(t *testing.T) {
	entry := EpochScheduleEntry{
		Epoch: 0,
		Validators: []EpochScheduleValidator{
			{Address: types.Address{0x01}.Hex(), BLSKey: "0xnotavalidhexkey"},
		},
	}
	if _, err := entry.ParseValidators(); err == nil {
		t.Fatal("expected error for malformed BLS key")
	}
}
