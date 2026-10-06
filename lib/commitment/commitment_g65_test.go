package commitment

import (
	"context"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/lib/common/length"
)

// g65DeferredAccounts generates n account updates driven through a deferred
// (SetDeferUpdates(true)) HexPatriciaHashed commitment, which routes through
// ApplyDeferredBranchUpdates on fold; with n large enough, some branch holds
// more than runtime.NumCPU() deferred updates and the parallel pipeline path
// runs, not just the small-batch sequential fast path.
func g65DeferredAccounts(t *testing.T, n int) (*UpdateBuilder, *rand.Rand) {
	t.Helper()
	rnd := rand.New(rand.NewSource(7))
	ub := NewUpdateBuilder()
	for i := 0; i < n; i++ {
		b := make([]byte, length.Addr)
		rnd.Read(b)
		ub.Balance(hexEncodeG65(b), uint64(i+1))
	}
	return ub, rnd
}

func hexEncodeG65(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexdigits[c>>4]
		out[i*2+1] = hexdigits[c&0xf]
	}
	return string(out)
}

// TestApplyDeferredBranchUpdatesViaDeferredCommitment drives the Domain-level
// deferred-update machinery through a real commitment run large enough to
// exercise both ApplyDeferredBranchUpdates' sequential fast path (small
// batches) and its parallel worker-pool path (batches bigger than the
// worker count), then checks the resulting root matches a non-deferred run
// over the same state.
func TestApplyDeferredBranchUpdatesViaDeferredCommitment(t *testing.T) {
	ctx := context.Background()
	ub, _ := g65DeferredAccounts(t, 256)
	plainKeys, updates := ub.Build()

	msDirect := NewMockState(t)
	require.NoError(t, msDirect.applyPlainUpdates(plainKeys, updates))
	hphDirect := NewHexPatriciaHashed(length.Addr, msDirect)
	hphDirect.branchEncoder.SetDeferUpdates(false)
	updsDirect := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, updates)
	defer updsDirect.Close()
	rootDirect, err := hphDirect.Process(ctx, updsDirect, "", nil, WarmupConfig{})
	require.NoError(t, err)

	msDeferred := NewMockState(t)
	require.NoError(t, msDeferred.applyPlainUpdates(plainKeys, updates))
	hphDeferred := NewHexPatriciaHashed(length.Addr, msDeferred)
	hphDeferred.branchEncoder.SetDeferUpdates(true)
	updsDeferred := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, updates)
	defer updsDeferred.Close()
	rootDeferred, err := hphDeferred.Process(ctx, updsDeferred, "", nil, WarmupConfig{})
	require.NoError(t, err)

	require.Equal(t, rootDirect, rootDeferred, "deferred and direct branch encoding must produce the same root")
}

// TestApplyDeferredBranchUpdatesEmptyIsNoOp exercises the len(deferred)==0
// fast-return branch directly.
func TestApplyDeferredBranchUpdatesEmptyIsNoOp(t *testing.T) {
	written, err := ApplyDeferredBranchUpdates(nil, 4, func(prefix, data, prev []byte) error {
		t.Fatalf("putBranch must not be called with no deferred updates")
		return nil
	})
	require.NoError(t, err)
	require.Zero(t, written)
}

// TestHexPatriciaHashedReleaseAndReuse drives the sync.Pool-backed
// Release/resetForReuse/NewHexPatriciaHashed cycle: build a small trie,
// Release it, then pull a (possibly the same, pooled) instance back out and
// assert it behaves like a fresh one rather than carrying over state from
// the released run.
func TestHexPatriciaHashedReleaseAndReuse(t *testing.T) {
	ctx := context.Background()
	ms := NewMockState(t)
	hph := NewHexPatriciaHashed(length.Addr, ms)

	ub := NewUpdateBuilder().Balance("111111111111111111111111111111111111111a", 42)
	plainKeys, updates := ub.Build()
	require.NoError(t, ms.applyPlainUpdates(plainKeys, updates))
	upds := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, updates)
	defer upds.Close()

	root, err := hph.Process(ctx, upds, "", nil, WarmupConfig{})
	require.NoError(t, err)
	require.NotEmpty(t, root)

	hph.Release()

	// Borrow again (may or may not be the same pooled object) and run an
	// independent, unrelated commitment: a stale grid/touchMap/root from the
	// released instance must not leak into this one.
	ms2 := NewMockState(t)
	hph2 := NewHexPatriciaHashed(length.Addr, ms2)
	ub2 := NewUpdateBuilder().Balance("222222222222222222222222222222222222222b", 7)
	plainKeys2, updates2 := ub2.Build()
	require.NoError(t, ms2.applyPlainUpdates(plainKeys2, updates2))
	upds2 := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys2, updates2)
	defer upds2.Close()

	root2, err := hph2.Process(ctx, upds2, "", nil, WarmupConfig{})
	require.NoError(t, err)
	require.NotEmpty(t, root2)
	require.NotEqual(t, root, root2, "a reused instance must compute the new state's own root, not reuse the old one")
	hph2.Release()
}

// TestConcurrentPatriciaHashedReleaseReturnsMountsToPool exercises
// ConcurrentPatriciaHashed.Release in isolation (no EnableCsvMetrics beforehand,
// per the documented hazard in hex_concurrent_accessors_test.go): after
// Release, a freshly constructed instance must still work correctly, proving
// the released mounts/root were returned to the pool in a clean state.
func TestConcurrentPatriciaHashedReleaseReturnsMountsToPool(t *testing.T) {
	ctx := context.Background()
	ms := NewMockState(t)
	root := NewHexPatriciaHashed(length.Addr, ms)
	p := NewConcurrentPatriciaHashed(root, ms)

	ub := NewUpdateBuilder().Balance("333333333333333333333333333333333333333c", 99)
	plainKeys, updates := ub.Build()
	require.NoError(t, ms.applyPlainUpdates(plainKeys, updates))
	upds := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, updates)
	defer upds.Close()

	_, err := p.Process(ctx, upds, "", nil, WarmupConfig{})
	require.NoError(t, err)

	p.Release()

	ms2 := NewMockState(t)
	root2 := NewHexPatriciaHashed(length.Addr, ms2)
	p2 := NewConcurrentPatriciaHashed(root2, ms2)
	ub2 := NewUpdateBuilder().Balance("444444444444444444444444444444444444444d", 1)
	plainKeys2, updates2 := ub2.Build()
	require.NoError(t, ms2.applyPlainUpdates(plainKeys2, updates2))
	upds2 := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys2, updates2)
	defer upds2.Close()

	root3, err := p2.Process(ctx, upds2, "", nil, WarmupConfig{})
	require.NoError(t, err)
	require.NotEmpty(t, root3)
}
