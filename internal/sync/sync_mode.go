// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// S5 sync modes (sync v2 design §5, standard §3.4 step 7), active only with
// N42_SYNC_PIPELINE=1.
//
//   - Live: the consensus path imports proposals and the node produces and
//     votes as usual.
//   - Range: the node is far behind. The sync importer is the only inserter
//     of heights up to the range end, the live ingress paths (push, gossip,
//     fetch-on-miss, passive parent fetch, deferred checks) leave those
//     heights alone, and a leader does not produce. Votes stay import-gated
//     with no rule change: a height the live path may not import is a height
//     this node cannot vote on.
//   - State is reserved for S6.
//
// The input is the authenticated gap of stage A: the highest verified
// CommitQC target or the (f+1)-th highest advertised height, minus the local
// head. Advertised heights alone never come from fewer than f+1 peers.
//
// Hysteresis (no flapping on a jittering gap):
//
//   - Live -> Range: gap > enterGap at enterCommits consecutive verified
//     commits, or continuously for enterDwell.
//   - Range -> Live: gap <= exitGap at exitCommits consecutive verified
//     commits, or continuously for exitDwell (a stalled chain still lets a
//     caught-up leader produce again), and at least minRangeDwell after
//     entering Range.
//
// A gap between exitGap and enterGap never changes the mode.

package sync

import (
	gosync "sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

type syncMode int32

const (
	syncModeLive syncMode = iota
	syncModeRange
	// syncModeState is reserved for S6 (QMDB state sync).
	syncModeState
)

func (m syncMode) String() string {
	switch m {
	case syncModeLive:
		return "live"
	case syncModeRange:
		return "range"
	case syncModeState:
		return "state"
	default:
		return "unknown"
	}
}

// syncModeParams are the thresholds. enterGap is the live window
// L = k + d + 2 (about 8 at deferred depth 3); exitGap is the standard's 2.
type syncModeParams struct {
	enterGap      uint64
	enterCommits  int
	enterDwell    time.Duration
	exitGap       uint64
	exitCommits   int
	exitDwell     time.Duration
	minRangeDwell time.Duration
}

func defaultSyncModeParams() syncModeParams {
	return syncModeParams{
		enterGap:      8,
		enterCommits:  2,
		enterDwell:    3 * time.Second,
		exitGap:       2,
		exitCommits:   3,
		exitDwell:     2 * time.Second,
		minRangeDwell: 2 * time.Second,
	}
}

// syncModeMachine is safe for concurrent use.
type syncModeMachine struct {
	mu          gosync.Mutex
	p           syncModeParams
	initialised bool
	mode        syncMode
	since       time.Time // when the current mode was entered
	// Live: how long and over how many commits the gap has exceeded enterGap.
	overSince   time.Time
	overCommits int
	// Range: how long and over how many commits it has stayed within exitGap.
	underSince   time.Time
	underCommits int
}

func (m *syncModeMachine) initLocked() {
	if !m.initialised {
		m.p = defaultSyncModeParams()
		m.initialised = true
	}
}

func (m *syncModeMachine) current() syncMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

// observe feeds one gap sample. commit marks a sample taken at a verified
// commit (Decide); others are time samples. It returns the mode after the
// sample and whether it changed.
func (m *syncModeMachine) observe(gap uint64, commit bool, now time.Time) (syncMode, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initLocked()
	switch m.mode {
	case syncModeLive:
		if gap <= m.p.enterGap {
			m.overSince, m.overCommits = time.Time{}, 0
			return m.mode, false
		}
		if m.overSince.IsZero() {
			m.overSince = now
		}
		if commit {
			m.overCommits++
		}
		if m.overCommits >= m.p.enterCommits || now.Sub(m.overSince) >= m.p.enterDwell {
			m.enterLocked(syncModeRange, now)
			return m.mode, true
		}
	case syncModeRange:
		if gap > m.p.exitGap {
			m.underSince, m.underCommits = time.Time{}, 0
			return m.mode, false
		}
		if m.underSince.IsZero() {
			m.underSince = now
		}
		if commit {
			m.underCommits++
		}
		if now.Sub(m.since) >= m.p.minRangeDwell &&
			(m.underCommits >= m.p.exitCommits || now.Sub(m.underSince) >= m.p.exitDwell) {
			m.enterLocked(syncModeLive, now)
			return m.mode, true
		}
	}
	return m.mode, false
}

func (m *syncModeMachine) enterLocked(mode syncMode, now time.Time) {
	m.mode, m.since = mode, now
	m.overSince, m.overCommits = time.Time{}, 0
	m.underSince, m.underCommits = time.Time{}, 0
}

// authenticatedGap is the stage-A gap and the height it is measured to.
func (s *Service) authenticatedGap() (gap, target uint64) {
	self := currentBlockNumber(s.cfg.chain)
	if self == nil {
		return 0, 0
	}
	cur := self.Uint64()
	target = s.verifiedCommitTarget.Load()
	quorum := s.catchUpClaimQuorum(len(s.cfg.p2p.Peers().Connected()))
	if q, _ := s.cfg.p2p.Peers().QuorumHeight(quorum, self); q != nil && q.Uint64() > target {
		target = q.Uint64()
	}
	if target > cur {
		gap = target - cur
	}
	return gap, target
}

// observeSyncMode samples the gap into the mode machine (pipeline on only)
// and applies a transition: entering Range sets the range end and starts the
// catch-up; leaving it hands every height back to the live path.
func (s *Service) observeSyncMode(commit bool) syncMode {
	if !PipelineOn() {
		return syncModeLive
	}
	gap, target := s.authenticatedGap()
	mode, changed := s.syncModes.observe(gap, commit, time.Now())
	if mode == syncModeRange {
		s.raiseRangeEnd(target)
	}
	if !changed {
		return mode
	}
	syncPipelineTransitions.WithLabelValues(mode.String()).Inc()
	if mode == syncModeRange {
		syncPipelineMode.Set(1)
		log.Warn("sync mode: entering Range (production paused, the sync importer owns heights up to the range end)",
			"gap", gap, "target", target, "rangeEnd", s.rangeEnd.Load())
		go s.enqueueCatchUp(target)
		return mode
	}
	syncPipelineMode.Set(0)
	s.rangeEnd.Store(0)
	log.Info("sync mode: back to Live", "gap", gap, "target", target)
	// Children future-queued on heights the sync importer owned retry now.
	if head := s.cfg.chain.CurrentBlock(); head != nil {
		go s.retryDeferredChildren(head.Hash())
	}
	return mode
}

func (s *Service) raiseRangeEnd(n uint64) {
	for {
		prev := s.rangeEnd.Load()
		if n <= prev || s.rangeEnd.CompareAndSwap(prev, n) {
			return
		}
	}
}

// syncOwnsHeight reports whether a live ingress path must leave height n to
// the sync importer (Range mode, n at or below the range end).
func (s *Service) syncOwnsHeight(n uint64) bool {
	if !PipelineOn() || s.syncModes.current() != syncModeRange {
		return false
	}
	return n <= s.rangeEnd.Load()
}

// noteLiveSkip counts a live-path insert left to the sync importer.
func (s *Service) noteLiveSkip(path string, number uint64, hash types.Hash) {
	syncPipelineLiveSkipped.WithLabelValues(path).Inc()
	log.Debug("sync mode Range: live insert left to the sync importer", "path", path,
		"number", number, "hash", hash, "rangeEnd", s.rangeEnd.Load())
}

// SyncProductionPaused reports whether a leader must not produce: the node
// is in Range mode. Always false with N42_SYNC_PIPELINE off. Probed by the
// HotStuff leader gate; each call is also a time sample for the mode machine.
func (s *Service) SyncProductionPaused() bool {
	return s.observeSyncMode(false) == syncModeRange
}
