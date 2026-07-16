// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package mobileverify

import (
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func TestRegistrationWireRoundTrip(t *testing.T) {
	d := newDevice(t)
	pop := d.pop()
	enc := encodeRegistration(d.pubkey, pop)
	if len(enc) != registrationLen {
		t.Fatalf("encoded len = %d, want %d", len(enc), registrationLen)
	}
	pubkey, gotPop, err := decodeRegistration(enc)
	if err != nil || pubkey != d.pubkey || gotPop != pop {
		t.Fatalf("round trip mismatch: %v", err)
	}
	if _, _, err := decodeRegistration(enc[:len(enc)-1]); err == nil {
		t.Fatal("short registration accepted")
	}
}

// TestRegistrationHookAnnouncesNewOnly confirms the gossip hook fires for
// a first registration but not for a duplicate or an adopted one.
func TestRegistrationHookAnnouncesNewOnly(t *testing.T) {
	reg := NewRegistry()
	var announced int
	reg.SetOnRegister(func(_ [48]byte, _ [96]byte) { announced++ })

	d := newDevice(t)
	reg.Register(d.pubkey, d.pop())
	reg.Register(d.pubkey, d.pop()) // duplicate pending — no announce
	if announced != 1 {
		t.Fatalf("announced = %d, want 1", announced)
	}
	// AdoptPending must NOT re-announce (the sender already did).
	other := newDevice(t)
	reg.AdoptPending(other.pubkey, other.pop())
	if announced != 1 {
		t.Fatalf("adoption announced: %d, want 1", announced)
	}
}

func TestEpochCommitterLifecycle(t *testing.T) {
	reg := NewRegistry()
	d := newDevice(t)
	reg.Register(d.pubkey, d.pop())

	var committedRoot types.Hash
	c := NewEpochCommitter(reg, 20*time.Millisecond)
	c.SetOnCommit(func(root types.Hash, _ int) { committedRoot = root })
	c.Start()

	deadline := time.Now().Add(2 * time.Second)
	for reg.Count() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	c.Stop()
	if reg.Count() != 1 {
		t.Fatalf("committer did not commit the pending device: count=%d", reg.Count())
	}
	if committedRoot == (types.Hash{}) {
		t.Fatal("commit callback never fired with a root")
	}
	last, epochs := c.LastRoot()
	if last == (types.Hash{}) || epochs == 0 {
		t.Fatalf("LastRoot = (%x, %d), want non-empty", last[:4], epochs)
	}
}

// TestEpochCommitterStopDrainsPending ensures shutdown commits the tail so
// no registration is stranded.
func TestEpochCommitterStopDrainsPending(t *testing.T) {
	reg := NewRegistry()
	c := NewEpochCommitter(reg, time.Hour) // never ticks on its own
	c.Start()
	d := newDevice(t)
	reg.Register(d.pubkey, d.pop())
	c.Stop() // the final commitOnce must drain it
	if reg.Count() != 1 {
		t.Fatalf("Stop did not drain pending: count=%d", reg.Count())
	}
}

func TestAlarmBufferRingAndOrder(t *testing.T) {
	b := NewAlarmBuffer(3)
	for i := 1; i <= 5; i++ {
		b.Record(DivergenceAlarm{BlockNumber: uint64(i), Cohorts: 2})
	}
	if b.Len() != 3 {
		t.Fatalf("len = %d, want 3 (ring cap)", b.Len())
	}
	recent := b.Recent(10)
	// Newest first: 5, 4, 3.
	if len(recent) != 3 || recent[0].BlockNumber != 5 || recent[2].BlockNumber != 3 {
		t.Fatalf("recent order wrong: %+v", recent)
	}
}

// TestWindowManagerDivergenceSink confirms a multi-cohort window fires the
// alarm sink.
func TestWindowManagerDivergenceSink(t *testing.T) {
	reg := NewRegistry()
	da := newDevice(t)
	db := newDevice(t)
	registerCommitted(t, reg, da.pubkey, da.pop())
	registerCommitted(t, reg, db.pubkey, db.pop())

	blockHash := h(0x99)
	store := NewCertStore(4)
	m := NewWindowManager(reg, fixedLookup(map[types.Hash]uint64{blockHash: 7}), 30*time.Millisecond, store)
	alarms := NewAlarmBuffer(8)
	m.SetDivergenceSink(alarms.Record)

	// Two devices, two different roots → divergence.
	if _, err := m.Submit(da.receipt(blockHash, 7, h(0x01))); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Submit(db.receipt(blockHash, 7, h(0x02))); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for alarms.Len() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if alarms.Len() != 1 {
		t.Fatalf("alarms = %d, want 1", alarms.Len())
	}
	if a := alarms.Recent(1)[0]; a.BlockNumber != 7 || a.Cohorts != 2 {
		t.Fatalf("alarm = %+v, want block 7 cohorts 2", a)
	}
	m.Stop()
}
