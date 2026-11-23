// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package synclegacy holds the escape hatches of the catch-up / block-sync
// hardening (stage A). Every hardening fix is ON by default because it closes
// a hole an untrusted peer can use; each one has an N42_SYNC_LEGACY_<NAME>=1
// switch that restores the previous behaviour exactly. The switches are
// parsed once, at package init, and are logged at WARN the first time a code
// path consults one that is set.
//
// The package is a leaf (it imports only the logger) so that the chain,
// consensus, sync and p2p packages can all share it without import cycles.
package synclegacy

import (
	"os"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/log"
)

// Switch is one escape hatch. The zero value is unusable; use newSwitch.
type Switch struct {
	env    string
	what   string
	on     atomic.Bool
	warned sync.Once
}

func newSwitch(env, what string) *Switch {
	s := &Switch{env: env, what: what}
	s.on.Store(parse(os.Getenv(env)))
	return s
}

func parse(v string) bool {
	switch v {
	case "1", "true", "TRUE", "yes", "on":
		return true
	default:
		return false
	}
}

// On reports whether the legacy (pre-hardening) behaviour is selected. The
// first call that returns true logs a WARN naming the reopened hole.
func (s *Switch) On() bool {
	if !s.on.Load() {
		return false
	}
	s.warned.Do(func() {
		log.Warn("sync hardening DISABLED by escape hatch; the old, exploitable behaviour is in effect",
			"env", s.env, "restores", s.what)
	})
	return true
}

// Env returns the environment variable that controls the switch.
func (s *Switch) Env() string { return s.env }

// SetForTest overrides the switch and returns a function restoring the
// previous value. Tests only.
func (s *Switch) SetForTest(on bool) (restore func()) {
	prev := s.on.Swap(on)
	return func() { s.on.Store(prev) }
}

var (
	// HeightGate (S0a): the leader production gate trusts the single highest
	// advertised peer height again instead of a verified CommitQC gap or the
	// (f+1)-th highest advertised height.
	HeightGate = newSwitch("N42_SYNC_LEGACY_HEIGHT_GATE",
		"production gate driven by the single highest advertised peer height")

	// FutureAuthority (S0b): every future-queued block is retried with
	// branch-switch authority again, and a missing parent named only by a
	// pushed or gossiped block is fetched with authority.
	FutureAuthority = newSwitch("N42_SYNC_LEGACY_FUTURE_AUTHORITY",
		"branch-switch authority for every future-queued, pushed or gossiped block")

	// CommitRewrite (S0e): a CommitQC that conflicts with the committed chain
	// rewrites and truncates canonical rows again instead of failing closed.
	CommitRewrite = newSwitch("N42_SYNC_LEGACY_COMMIT_REWRITE",
		"silent canonical rewrite on a conflicting commit at or below the committed height")

	// PeerSelect (S0d): catch-up asks the first five connected peers in
	// series with a fixed 30 s timeout and never scores or backs them off.
	PeerSelect = newSwitch("N42_SYNC_LEGACY_PEER_SELECT",
		"catch-up from Connected()[:5] with fixed 30 s timeouts and no peer scoring")

	// InitialSync (Q7): the prysm-port initial sync runs on HotStuff chains
	// again, next to the HotStuff range catch-up.
	InitialSync = newSwitch("N42_SYNC_LEGACY_INITIAL_SYNC",
		"initial sync running on HotStuff chains next to the HotStuff catch-up")
)
