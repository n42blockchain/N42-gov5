// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStaggerAheadPrep: a preparation waits for a running wave only when its
// block is at least two above the wave's, and only until the wave ends, its
// bound, or its claim.
func TestStaggerAheadPrep(t *testing.T) {
	defer SetAheadPrepStaggerForTest(true)()
	never := func() bool { return false }

	// No wave: no wait.
	t0 := time.Now()
	staggerAheadPrep(12, never)
	require.Less(t, time.Since(t0), 20*time.Millisecond)

	end := waveBegin(10)
	// The next block's preparation is needed at this wave's end: no wait.
	t0 = time.Now()
	staggerAheadPrep(11, never)
	require.Less(t, time.Since(t0), 20*time.Millisecond)

	// Two above: waits until the wave ends.
	ended := make(chan time.Time, 1)
	go func() {
		time.Sleep(30 * time.Millisecond)
		ended <- time.Now()
		end()
	}()
	staggerAheadPrep(12, never)
	require.False(t, time.Now().Before(<-ended), "the preparation started before the wave ended")
	end() // idempotent
	_, _, ok := waveRunning()
	require.False(t, ok)

	// Claimed meanwhile: stops waiting.
	end = waveBegin(20)
	defer end()
	claimed := &atomicFlag{ch: make(chan struct{})}
	go func() {
		time.Sleep(10 * time.Millisecond)
		claimed.set()
	}()
	t0 = time.Now()
	staggerAheadPrep(25, claimed.get)
	require.Less(t, time.Since(t0), aheadPrepStaggerMax-10*time.Millisecond)

	// Bounded.
	prev := aheadPrepStaggerMax
	aheadPrepStaggerMax = 15 * time.Millisecond
	defer func() { aheadPrepStaggerMax = prev }()
	t0 = time.Now()
	staggerAheadPrep(25, never)
	require.GreaterOrEqual(t, time.Since(t0), 15*time.Millisecond)

	// Off: never waits.
	defer SetAheadPrepStaggerForTest(false)()
	t0 = time.Now()
	staggerAheadPrep(30, never)
	require.Less(t, time.Since(t0), 10*time.Millisecond)
}

type atomicFlag struct{ ch chan struct{} }

func (f *atomicFlag) set() {
	close(f.ch)
}

func (f *atomicFlag) get() bool {
	select {
	case <-f.ch:
		return true
	default:
		return false
	}
}

// TestCapWaveBG: the background goroutine counts are capped only while a
// wave runs and only with the switch on.
func TestCapWaveBG(t *testing.T) {
	require.Equal(t, 8, capWaveBG(8))
	defer SetWaveBGCapForTest(3)()
	require.Equal(t, 8, capWaveBG(8), "no wave")
	end := waveBegin(5)
	require.Equal(t, 3, capWaveBG(8))
	require.Equal(t, 2, capWaveBG(2))
	end()
	require.Equal(t, 8, capWaveBG(8))
}

// TestWaveOverlapPipeline: the pipeline harness with both levers on (the
// stagger and a cap of one goroutine) ends with the same rows in every
// table as with both off, each block equal to the serial chain's (the
// harness checks), with early snapshots as on the fleet.
func TestWaveOverlapPipeline(t *testing.T) {
	defer SetAheadPrepForTest(true)()
	defer SetLagWriteAsyncForTest(true)()
	defer SetLagWritePrepareForTest(true)()
	defer SetLagEarlySnapshotForTest(true)()
	defer SetLagEarlyPrefillForTest(true)()
	defer SetAheadPreEarlyForTest(true)()
	lagPipeAheadClaimedForTest = waitOwnChildForTest
	defer func() { lagPipeAheadClaimedForTest = nil }()
	run := func(on bool) map[string]string {
		defer SetAheadPrepStaggerForTest(on)()
		capN := 0
		if on {
			capN = 1
		}
		defer SetWaveBGCapForTest(capN)()
		s0, c0 := aheadPrepStaggers.Load(), waveBGCapped.Load()
		tables := r69aRunTables(t, arrivalAny, nil)
		t.Logf("levers=%v: staggered preparations %d, capped counts %d", on, aheadPrepStaggers.Load()-s0, waveBGCapped.Load()-c0)
		if !on {
			require.Zero(t, aheadPrepStaggers.Load()-s0)
			require.Zero(t, waveBGCapped.Load()-c0)
		}
		return tables
	}
	want := run(false)
	got := run(true)
	requireSameTables(t, want, got, "wave overlap levers")
}
