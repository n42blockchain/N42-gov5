// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package syncanchor holds the shared vocabulary of sync step S1 (anchored
// range catch-up, N42 sync standard §3.3/§3.4): the rollout switch and the
// verdict a consensus-side verifier returns for a QC embedded in a header.
//
// The package is a leaf (it imports only common/types and the logger) so the
// sync and HotStuff packages can share it without an import cycle.
package syncanchor

import (
	"os"
	"strings"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

// EnvVar selects the S1 mode: "off", "observe" (default) or "enforce".
const EnvVar = "N42_SYNC_ANCHOR"

// Mode is the S1 rollout mode.
type Mode int32

const (
	// Off is the pre-S1 behaviour, byte for byte: no anchor bookkeeping, no
	// QC verification on the sync path.
	Off Mode = iota
	// Observe leaves behaviour unchanged but logs and counts every block the
	// sync importer would refuse under Enforce (standard §7.2 step 4).
	Observe
	// Enforce executes only blocks hash-linked to a verified commit-domain
	// QC and holds the unanchored tail unexecuted.
	Enforce
)

func (m Mode) String() string {
	switch m {
	case Off:
		return "off"
	case Observe:
		return "observe"
	case Enforce:
		return "enforce"
	default:
		return "unknown"
	}
}

// ParseMode maps an environment value to a mode. Empty selects Observe;
// an unrecognised value also selects Observe (and ok reports false).
func ParseMode(v string) (m Mode, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "":
		return Observe, true
	case "off", "0", "false", "no", "disable", "disabled":
		return Off, true
	case "observe", "observe-only", "log":
		return Observe, true
	case "enforce", "on", "1", "true", "yes":
		return Enforce, true
	default:
		return Observe, false
	}
}

var current atomic.Int32

func init() {
	raw := os.Getenv(EnvVar)
	m, ok := ParseMode(raw)
	if !ok {
		log.Warn("unrecognised sync anchor mode, using observe", "env", EnvVar, "value", raw)
	}
	current.Store(int32(m))
	if m != Observe {
		log.Info("sync anchor mode", "env", EnvVar, "mode", m.String())
	}
}

// Current returns the active mode.
func Current() Mode { return Mode(current.Load()) }

// SetForTest overrides the mode and returns a function restoring the
// previous value. Tests only.
func SetForTest(m Mode) (restore func()) {
	prev := current.Swap(int32(m))
	return func() { current.Store(prev) }
}

// Verdict classifies the QC embedded in one header's Extra.
type Verdict int

const (
	// NoQC: a consensus header without an embedded QC (legacy producers,
	// n42-26 leaders, or a view-0 placeholder). Not a fault.
	NoQC Verdict = iota
	// NonConsensus: the header carries no N42H magic (pre-consensus replay
	// block). Not a fault; such blocks are anchored only through descendants.
	NonConsensus
	// Verified: a commit-domain QC that verified against the validator set
	// of its own view's epoch. Its BlockHash is an anchor.
	Verified
	// PrepareDomain: the QC verifies only in the prepare/vote domain. Not an
	// anchor (standard §3.3 rule 3), not scored.
	PrepareDomain
	// BadQC: the QC decodes but fails verification against a known set of
	// its epoch. A provable fault of the peer that served the header.
	BadQC
	// UnknownEpoch: no validator set is known for the QC's epoch. Fails
	// closed (not an anchor) without scoring the peer.
	UnknownEpoch
	// BadExtra: the header claims N42H but its Extra does not decode.
	// A screening failure, i.e. a provable fault.
	BadExtra
	// NoVerifier: the consensus layer offers no verifier (not a HotStuff
	// chain, or not wired yet). The gate stays inactive.
	NoVerifier
	// Unverified: DecodeHeaderQC found a QC with view > 0; its signature has
	// not been checked.
	Unverified
)

func (v Verdict) String() string {
	switch v {
	case NoQC:
		return "no_qc"
	case NonConsensus:
		return "non_consensus"
	case Verified:
		return "verified"
	case PrepareDomain:
		return "prepare_domain"
	case BadQC:
		return "bad_qc"
	case UnknownEpoch:
		return "unknown_epoch"
	case BadExtra:
		return "bad_extra"
	case NoVerifier:
		return "no_verifier"
	case Unverified:
		return "unverified"
	default:
		return "unknown"
	}
}

// Result is the verdict on one header QC plus what the QC names.
type Result struct {
	Verdict   Verdict
	BlockHash types.Hash // the block the QC names (zero unless a QC decoded)
	View      uint64     // the QC's view
	Epoch     uint64     // EpochForView(View)
	// Cached reports that no BLS verification ran for this call.
	Cached bool
	Err    error
}

// Verifier is implemented by the HotStuff service. DecodeHeaderQC only
// decodes (no signature work); VerifyHeaderQC verifies the QC of one header
// in the commit domain against the validator set of the QC's own epoch, with
// no fallback set.
type Verifier interface {
	DecodeHeaderQC(extra []byte) Result
	VerifyHeaderQC(extra []byte) Result
}
