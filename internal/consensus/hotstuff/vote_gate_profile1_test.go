// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hotstuff

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	prometheus "github.com/n42blockchain/N42/common/metrics"
)

func refusedCount(reason string) uint64 {
	return prometheus.GetOrCreateCounter(fmt.Sprintf(`n42_blocks_refused_total{reason=%q}`, reason)).Get()
}

// M3d: a header failing the profile-1 static checks is refused at the vote
// gate and counted in n42_blocks_refused_total{reason}; a valid header is not.
func TestProfile1VoteGateRefusesAndCounts(t *testing.T) {
	f := newP1Fixture(t)
	hs := f.chainOf(1)
	parent := hs[1]

	valid := f.seal(f.child(parent, 2, nil))
	before := refusedCount("time_future")
	if err := f.hs.VoteGateHeaderCheck(f.chain, valid, time.Unix(int64(valid.Time), 0)); err != nil {
		t.Fatalf("valid header refused: %v", err)
	}
	if refusedCount("time_future") != before {
		t.Fatal("a valid header was counted as refused")
	}

	cases := []struct {
		reason string
		want   error
		header func() (*block.Header, time.Time)
	}{
		{"time_future", ErrProfile1TimeFuture, func() (*block.Header, time.Time) {
			h := f.seal(f.child(parent, 2, nil))
			return h, time.Unix(int64(h.Time)-3, 0)
		}},
		{"extra", ErrProfile1Extra, func() (*block.Header, time.Time) {
			h := f.child(parent, 2, nil)
			h.Extra = h.Extra[:extraMinLen] // unsealed
			return h, time.Unix(int64(h.Time), 0)
		}},
		{"seal", ErrProfile1Seal, func() (*block.Header, time.Time) {
			h := f.sealBy(f.child(parent, 2, nil), (f.leader(2)+1)%len(f.sks))
			return h, time.Unix(int64(h.Time), 0)
		}},
	}
	for _, c := range cases {
		h, now := c.header()
		before := refusedCount(c.reason)
		err := f.hs.VoteGateHeaderCheck(f.chain, h, now)
		if !errors.Is(err, c.want) {
			t.Fatalf("%s: err = %v, want %v", c.reason, err, c.want)
		}
		if Profile1RefusalReason(err) != c.reason {
			t.Fatalf("%s: labelled %q", c.reason, Profile1RefusalReason(err))
		}
		if got := refusedCount(c.reason); got != before+1 {
			t.Fatalf("%s: n42_blocks_refused_total %d -> %d, want +1", c.reason, before, got)
		}
	}
}
