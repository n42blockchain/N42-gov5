// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package params

import (
	"testing"
	"time"
)

// A17 (AM27): s = max(sP+1, ceil((now-G)/I)), stamp G + s*I.
func TestProfile1SlotTime(t *testing.T) {
	const g, iv = uint64(1_000_000), uint64(8)
	at := func(sec uint64, ms int64) time.Time {
		return time.Unix(int64(sec), ms*int64(time.Millisecond))
	}
	cases := []struct {
		name   string
		parent uint64
		now    time.Time
		want   uint64
	}{
		{"on-time parent, just after its slot start", g + 8, at(g+8, 300), g + 16},
		{"on-time parent, before its slot start (clock behind)", g + 8, at(g+5, 0), g + 16},
		{"exact boundary of the next slot", g + 8, at(g+16, 0), g + 16},
		{"late parent, mid next slot -> following boundary", g + 8, at(g+19, 500), g + 24},
		{"late parent, 1 ms into the next slot", g + 8, at(g+16, 1), g + 24},
		{"after a TC three slots later, mid-slot", g + 8, at(g+8+24+3, 0), g + 40},
		{"after a TC, exactly on a boundary", g + 8, at(g+40, 0), g + 40},
		{"block 1 before genesis time", g, at(g-100, 0), g + 8},
		{"block 1 at genesis time", g, at(g, 0), g + 8},
		{"block 1 mid slot 0", g, at(g+3, 0), g + 8},
		{"block 1 long after genesis", g, at(g+81, 0), g + 88},
	}
	for _, c := range cases {
		got := Profile1SlotTime(g, iv, c.parent, c.now)
		if got != c.want {
			t.Errorf("%s: %d, want %d", c.name, got-g, c.want-g)
		}
		if !Profile1OnGrid(g, iv, got) {
			t.Errorf("%s: %d off the grid", c.name, got)
		}
		if got < c.parent+iv {
			t.Errorf("%s: %d not after the parent's slot", c.name, got)
		}
	}
}

func TestProfile1OnGrid(t *testing.T) {
	const g, iv = uint64(100), uint64(8)
	for _, c := range []struct {
		t    uint64
		want bool
	}{{100, true}, {108, true}, {116, true}, {109, false}, {115, false}, {92, false}, {0, false}} {
		if got := Profile1OnGrid(g, iv, c.t); got != c.want {
			t.Errorf("OnGrid(%d) = %v, want %v", c.t, got, c.want)
		}
	}
	if Profile1OnGrid(g, 0, 100) {
		t.Error("a zero interval has no grid")
	}
}

func TestProfile1SlotSeconds(t *testing.T) {
	c := &ChainConfig{N42Profile: N42Profile1, HotStuff: &HotStuffConfig{BlockIntervalMs: 8000}}
	if got := c.Profile1SlotSeconds(); got != 8 {
		t.Fatalf("8000 ms = %d s", got)
	}
	c.HotStuff.BlockIntervalMs = 8500
	if got := c.Profile1SlotSeconds(); got != 0 {
		t.Fatalf("8500 ms = %d s, want 0 (not whole seconds)", got)
	}
	if got := (&ChainConfig{HotStuff: &HotStuffConfig{BlockIntervalMs: 8000}}).Profile1SlotSeconds(); got != 0 {
		t.Fatalf("legacy chain = %d, want 0", got)
	}
}
