// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// coldwire.go — node-startup install point for the EIP-4444 cold-read resolver.
// A Full node retains only a recent window of bodies hot; older (cold) segments
// are offloaded and pulled on demand. Setting a process-wide ColdResolver before
// services open their readers makes every BodyCompactReader resolve trimmed
// segments transparently (see internal/ethel/coldresolve + historyexpiry).

package ethel

// defaultColdResolver, if non-nil when openN42CompactSource runs, is installed
// on every BodyCompactReader it opens. nil (the default) preserves the original
// behavior: an absent segment yields ErrBodyTrimmed. Set once at node startup
// (before Node.Start opens readers); not safe to mutate concurrently with reads.
var defaultColdResolver ColdResolver

// SetDefaultColdResolver installs the process-wide cold-read resolver consulted
// by readers opened after this call. Pass nil to disable. Intended to be called
// from node bootstrap (cmd/eth-el) when history-expiry is configured.
func SetDefaultColdResolver(cr ColdResolver) { defaultColdResolver = cr }

// DefaultColdResolver returns the currently installed resolver (may be nil).
func DefaultColdResolver() ColdResolver { return defaultColdResolver }
