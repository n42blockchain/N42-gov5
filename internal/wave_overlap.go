// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// The Block-STM wave against the background stages beside it (run 72:
// 3.5 us of CPU per transaction when the wave runs alone, 4.7-4.8 when it
// overlaps the previous block's finalize and root, the writer's rows and
// the next blocks' preparation; the preparation is the largest term, +1.4
// us/tx per full overlap). Two cheap levers, each opt-in:
//
// N42_AHEAD_PREP_STAGGER=1: a block's preparation (its computation and the
// base prefill started with it) does not start while the wave of a block
// at least two below it runs; it starts when that wave ends (at most
// aheadPrepStaggerMax later). Such a block launches no earlier than the
// Block-STM end of the block between them, a whole run after the wave
// ends, so the delay costs it nothing; the preparation of the very next
// block (needed at this wave's end) is never delayed. The preparation's
// result does not depend on when it runs.
//
// N42_WAVE_BG_CAP=N: while a wave runs, the preparation's and the base
// prefill's goroutine counts (aheadPrepWorkers) are capped at N. Their
// results do not depend on the goroutine count (the keys are computed per
// transaction, the partition for the executor's worker count, the prefill
// reads a fixed key set).
var (
	aheadPrepStaggerOn = parseOptIn(os.Getenv("N42_AHEAD_PREP_STAGGER"))
	waveBGCap          = parseWaveBGCap(os.Getenv("N42_WAVE_BG_CAP"))
)

// aheadPrepStaggerMax bounds the wait of a staggered preparation.
var aheadPrepStaggerMax = 60 * time.Millisecond

func parseWaveBGCap(v string) int {
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 {
		return 0
	}
	return n
}

// SetAheadPrepStaggerForTest switches N42_AHEAD_PREP_STAGGER and returns
// the restore.
func SetAheadPrepStaggerForTest(on bool) func() {
	prev := aheadPrepStaggerOn
	aheadPrepStaggerOn = on
	return func() { aheadPrepStaggerOn = prev }
}

// SetWaveBGCapForTest sets N42_WAVE_BG_CAP (0: off) and returns the
// restore.
func SetWaveBGCapForTest(n int) func() {
	prev := waveBGCap
	waveBGCap = n
	return func() { waveBGCap = prev }
}

// Preparations staggered behind a wave, and their summed wait in
// microseconds; goroutine counts capped while a wave ran (process-wide).
var aheadPrepStaggers, aheadPrepStaggerUs, waveBGCapped atomic.Uint64

// waveSignal is the Block-STM wave in flight on this node (at most one: a
// child starts its wave at its parent's Block-STM end).
var waveSignal struct {
	mu     sync.Mutex
	active bool
	number uint64
	done   chan struct{}
}

// waveActive is a lock-free view of waveSignal.active.
var waveActive atomic.Bool

// waveTracked: some switch reads the wave signal.
func waveTracked() bool { return aheadPrepStaggerOn || waveBGCap > 0 }

// waveBegin records that block number's wave runs; the returned end (call
// once; later calls do nothing) records that it ended.
func waveBegin(number uint64) func() {
	if !waveTracked() {
		return func() {}
	}
	done := make(chan struct{})
	waveSignal.mu.Lock()
	waveSignal.active, waveSignal.number, waveSignal.done = true, number, done
	waveSignal.mu.Unlock()
	waveActive.Store(true)
	var once sync.Once
	return func() {
		once.Do(func() {
			waveSignal.mu.Lock()
			if waveSignal.done == done {
				waveSignal.active, waveSignal.done = false, nil
				waveActive.Store(false)
			}
			waveSignal.mu.Unlock()
			close(done)
		})
	}
}

// waveRunning returns the running wave's block and its end, if one runs.
func waveRunning() (number uint64, done <-chan struct{}, ok bool) {
	waveSignal.mu.Lock()
	defer waveSignal.mu.Unlock()
	if !waveSignal.active {
		return 0, nil, false
	}
	return waveSignal.number, waveSignal.done, true
}

// staggerAheadPrep delays the preparation of block number while the wave
// of a block at least two below it runs (N42_AHEAD_PREP_STAGGER), until
// that wave ends, at most aheadPrepStaggerMax, or abort (the preparation
// was claimed or dropped) reports true.
func staggerAheadPrep(number uint64, abort func() bool) {
	if !aheadPrepStaggerOn {
		return
	}
	w, done, ok := waveRunning()
	if !ok || number < w+2 {
		return
	}
	t0 := time.Now()
	deadline := time.NewTimer(aheadPrepStaggerMax)
	defer deadline.Stop()
	poll := time.NewTicker(time.Millisecond)
	defer poll.Stop()
	for {
		select {
		case <-done:
		case <-deadline.C:
		case <-poll.C:
			if !abort() {
				continue
			}
		}
		break
	}
	aheadPrepStaggers.Add(1)
	aheadPrepStaggerUs.Add(uint64(time.Since(t0).Microseconds()))
}

// capWaveBG caps a background stage's goroutine count n while a wave runs
// (N42_WAVE_BG_CAP).
func capWaveBG(n int) int {
	if c := waveBGCap; c > 0 && n > c && waveActive.Load() {
		waveBGCapped.Add(1)
		return c
	}
	return n
}
