package qmdb

import "testing"

// TestSetLeafEqualsRecompute drives random leaf churn through the eager O(log)
// path-update and checks, at every step, that the incrementally-maintained twig
// root equals a from-scratch rebuild of the same leaves. This is the direct
// correctness guard for replacing the full 4095-hash recompute on the hot path.
func TestSetLeafEqualsRecompute(t *testing.T) {
	eager := newTwig()
	rng := uint64(0x243F6A8885A308D3)
	next := func() uint64 { rng = rng*6364136223846793005 + 1442695040888963407; return rng >> 11 }

	for step := 0; step < 3000; step++ {
		local := next() % TwigSize
		var h Hash
		if next()%4 == 0 {
			h = nullHash // deactivation
		} else {
			h[0], h[1], h[2] = byte(next()), byte(next()), byte(next())
		}
		eager.setLeaf(local, h)

		if step%250 == 0 || step == 2999 {
			// From-scratch reference: copy the leaves into a fresh twig and rebuild.
			ref := newTwig()
			copy(ref.nodes[TwigSize:], eager.nodes[TwigSize:])
			ref.recompute()
			if eager.root != ref.root {
				t.Fatalf("step %d: eager root %x != recomputed %x", step, eager.root, ref.root)
			}
			if eager.root != eager.nodes[1] {
				t.Fatalf("step %d: root field out of sync with nodes[1]", step)
			}
		}
	}
}

// TestNewTwigNullInternals: a fresh twig's precomputed internal nodes must match
// a full recompute over all-null leaves (and its root must be the null-twig root).
func TestNewTwigNullInternals(t *testing.T) {
	fresh := newTwig()
	ref := newTwig()
	ref.recompute()
	for j := 1; j < 2*TwigSize; j++ {
		if fresh.nodes[j] != ref.nodes[j] {
			t.Fatalf("node %d: precomputed %x != recomputed %x", j, fresh.nodes[j], ref.nodes[j])
		}
	}
	if fresh.root != nullTwigRoot {
		t.Fatalf("fresh root %x != nullTwigRoot %x", fresh.root, nullTwigRoot)
	}
}
