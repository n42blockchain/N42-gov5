// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"testing"

	"github.com/n42blockchain/N42/modules/state/commitment"
)

// settleStartKeys names why a claim took the start-keys prefill or not.
func TestSettleStartKeysWhy(t *testing.T) {
	newSK := func(done bool, phase int32, pf *commitment.BasePrefill, skip string) *aheadBase {
		ab := &aheadBase{stop: make(chan struct{}), done: make(chan struct{}), pf: pf, skip: skip, at: 7, depth: 2}
		ab.phase.Store(phase)
		if done {
			close(ab.done)
		}
		return ab
	}
	cases := []struct {
		name string
		pr   *aheadPrep
		want string
	}{
		{"prep not ok", &aheadPrep{startKeys: newSK(true, abPhaseFill, &commitment.BasePrefill{}, "")}, "prep not ok"},
		{"base usable", &aheadPrep{ok: true, base: &aheadBase{}, startKeys: newSK(true, abPhaseFill, &commitment.BasePrefill{}, "")}, "base usable"},
		{"waiting", &aheadPrep{ok: true, startKeys: newSK(false, abPhaseTree, nil, "")}, "not done: waiting for the tree"},
		{"filling", &aheadPrep{ok: true, baseDetached: true, base: &aheadBase{}, startKeys: newSK(false, abPhaseFill, nil, "")}, "not done: filling"},
		{"skipped", &aheadPrep{ok: true, startKeys: newSK(true, abPhaseFill, nil, "tree changed under the fill")}, "skip: tree changed under the fill"},
		{"used", &aheadPrep{ok: true, startKeys: newSK(true, abPhaseFill, &commitment.BasePrefill{}, "")}, "used"},
	}
	for _, c := range cases {
		c.pr.settleStartKeys()
		f := c.pr.startKeysLogFields()
		if f[1] != c.want {
			t.Fatalf("%s: why = %q, want %q", c.name, f[1], c.want)
		}
		if c.want == "used" && (f[3] != uint64(7) || f[5] != uint64(2)) {
			t.Fatalf("used: at/depth = %v/%v, want 7/2", f[3], f[5])
		}
	}
	// A taken prefill the claim's layering dropped afterwards.
	pr := &aheadPrep{ok: true, startKeys: newSK(true, abPhaseFill, &commitment.BasePrefill{}, "")}
	pr.settleStartKeys()
	pr.startKeys.pf, pr.startKeys.skip = nil, "an ancestor's post-state is not kept"
	if why := pr.startKeysLogFields()[1]; why != "dropped: an ancestor's post-state is not kept" {
		t.Fatalf("dropped: why = %q", why)
	}
	if f := (*aheadPrep)(nil).startKeysLogFields(); f[1] != "" {
		t.Fatal("nil prep: non-empty why")
	}
}

// The run drops its preparation before it logs (parallel_processor.go): the
// snapshot taken before the drop keeps the stamps, including a later drop of
// the prefill by the claim's layering.
func TestStartKeysStampOutlivesPrep(t *testing.T) {
	ab := &aheadBase{stop: make(chan struct{}), done: make(chan struct{}), pf: &commitment.BasePrefill{}, at: 9, depth: 3}
	ab.phase.Store(abPhaseFill)
	close(ab.done)
	prep := &aheadPrep{ok: true, startKeys: ab}
	prep.settleStartKeys()
	snap := prep.startKeysLog()
	prep = nil
	f := snap.fields()
	if f[1] != "used" || f[3] != uint64(9) || f[5] != uint64(3) {
		t.Fatalf("snapshot fields = %v, want used/9/3", f)
	}
	_ = prep
	ab.pf, ab.skip = nil, "gone"
	if why := snap.fields()[1]; why != "dropped: gone" {
		t.Fatalf("dropped after the snapshot: why = %q", why)
	}
	if f := (*aheadPrep)(nil).startKeysLog().fields(); f[1] != "" {
		t.Fatal("nil prep: non-empty why")
	}
}
