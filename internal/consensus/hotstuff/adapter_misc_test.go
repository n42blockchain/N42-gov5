package hotstuff

import (
	"time"

	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// TestHotStuff_VerifyHeaders covers the concurrent batch-verification entry
// point: two genesis-shaped headers (VerifyHeader always accepts number-0
// headers) run through the abort/results channel pair to completion.
func TestHotStuff_VerifyHeaders(t *testing.T) {
	h := New(nil, nil)
	chain := newMockChainReader()

	headers := []block.IHeader{
		&block.Header{Number: uint256.NewInt(0)},
		&block.Header{Number: uint256.NewInt(0)},
	}
	abort, results := h.VerifyHeaders(chain, headers, []bool{true, true})
	defer close(abort)

	for i := 0; i < len(headers); i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatalf("unexpected verify error: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timed out waiting for VerifyHeaders result %d", i)
		}
	}
}

// TestHotStuff_VerifyHeaders_Abort confirms closing abort early stops the
// worker goroutine without a panic or deadlock (best-effort: just drains
// whatever came through before closing).
func TestHotStuff_VerifyHeaders_Abort(t *testing.T) {
	h := New(nil, nil)
	chain := newMockChainReader()
	headers := []block.IHeader{&block.Header{Number: uint256.NewInt(0)}}
	abort, results := h.VerifyHeaders(chain, headers, []bool{true})
	close(abort)
	// Draining is best-effort; the point is this does not hang the test.
	select {
	case <-results:
	case <-time.After(2 * time.Second):
	}
}

// TestHotStuff_VerifyUncles always returns nil.
func TestHotStuff_VerifyUncles(t *testing.T) {
	h := New(nil, nil)
	if err := h.VerifyUncles(nil, nil); err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
}

// TestHotStuff_NotifyBlockSealed covers the nil-engine no-op and the
// engine-present delegation to ProcessEvent.
func TestHotStuff_NotifyBlockSealed(t *testing.T) {
	h := New(&params.HotStuffConfig{}, nil)
	// Nil engine: must not panic.
	h.NotifyBlockSealed(types.Hash{0x01}, types.Hash{0x02})

	setup := newTestSetup(t, 4)
	h.Authorize(setup.validators[0].Address, setup.keys[0])
	if err := h.InitEngine(setup.validators, setup.f); err != nil {
		t.Fatal(err)
	}
	h.NotifyBlockSealed(types.Hash{0x03}, types.Hash{0x04})
}

// TestHotStuff_WaitForCommitVoteJournal_AdapterLevel covers the nil-engine
// "off" fallback at the HotStuff (not ConsensusEngine) level.
func TestHotStuff_WaitForCommitVoteJournal_AdapterLevel(t *testing.T) {
	h := New(nil, nil)
	waited, why := h.WaitForCommitVoteJournal(types.Hash{0x05}, 10*time.Millisecond)
	if why != "off" || waited != 0 {
		t.Fatalf("expected off/0 with nil engine, got why=%q waited=%v", why, waited)
	}
}

// TestHotStuff_SealHash covers the invalid-header-type guard and the
// delegation to sealHash for a valid header.
func TestHotStuff_SealHash(t *testing.T) {
	h := New(nil, nil)
	if got := h.SealHash(nil); got != (types.Hash{}) {
		t.Fatalf("expected zero hash for invalid header type, got %x", got)
	}
	hdr := &block.Header{Number: uint256.NewInt(1)}
	if got := h.SealHash(hdr); got == (types.Hash{}) {
		t.Fatalf("expected non-zero seal hash for a valid header")
	}
}

// TestHotStuff_CalcDifficulty always returns zero.
func TestHotStuff_CalcDifficulty(t *testing.T) {
	h := New(nil, nil)
	if got := h.CalcDifficulty(nil, 0, nil); got == nil || !got.IsZero() {
		t.Fatalf("expected zero difficulty, got %v", got)
	}
}

// TestHotStuff_APIs covers the RPC API registration surface.
func TestHotStuff_APIs(t *testing.T) {
	h := New(nil, nil)
	apis := h.APIs(nil)
	if len(apis) != 1 || apis[0].Namespace != "hotstuff" {
		t.Fatalf("unexpected APIs: %+v", apis)
	}
}

// TestHotStuff_Close covers the shutdown hook.
func TestHotStuff_Close(t *testing.T) {
	h := New(nil, nil)
	if err := h.Close(); err != nil {
		t.Fatalf("Close: unexpected error: %v", err)
	}
}
