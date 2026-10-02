package modestate

import (
	"errors"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
)

// TestStateReaderForErrorPaths exercises the error branches not covered by the
// happy-path tests: nil tx for Full/Archive, nil segment for M1, M0's
// not-state-backed sentinel, and an unknown mode falling to the default case.
func TestStateReaderForErrorPaths(t *testing.T) {
	if _, err := StateReaderFor(rpccaps.Full, nil, nil, nil, 0); err == nil {
		t.Error("Full with nil tx: want error, got nil")
	}
	if _, err := StateReaderFor(rpccaps.Archive, nil, nil, nil, 0); err == nil {
		t.Error("Archive with nil tx: want error, got nil")
	}
	if _, err := StateReaderFor(rpccaps.M1, nil, nil, nil, 0); err == nil {
		t.Error("M1 with nil segment: want error, got nil")
	}
	r, err := StateReaderFor(rpccaps.M0, nil, nil, nil, 0)
	if r != nil {
		t.Errorf("M0: want nil reader, got %v", r)
	}
	if !errors.Is(err, ErrNotStateBacked) {
		t.Errorf("M0: want ErrNotStateBacked, got %v", err)
	}
	if _, err := StateReaderFor(rpccaps.Mode(99), nil, nil, nil, 0); err == nil {
		t.Error("unknown mode: want error, got nil")
	} else if !strings.Contains(err.Error(), "unknown mode") {
		t.Errorf("unknown mode: unexpected error text %q", err.Error())
	}
}
