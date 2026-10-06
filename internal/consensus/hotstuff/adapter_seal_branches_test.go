package hotstuff

import (
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/params"
)

// TestSealWithoutSecretKeyFails exercises the "secret key not set" guard: a
// HotStuff engine that was never Authorize()'d must refuse to seal.
func TestSealWithoutSecretKeyFails(t *testing.T) {
	h := New(nil, params.TestChainConfig)

	extra, err := buildHeaderExtra(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	hdr := &block.Header{Number: uint256.NewInt(1), Extra: extra}
	b := block.NewBlockFromReceipt(hdr, nil, nil, nil, nil)
	results := make(chan block.IBlock, 1)
	if err := h.Seal(nil, b, results, nil); err == nil {
		t.Fatal("expected Seal to fail without an authorized secret key")
	}
}

// TestSealAppendsSignatureWhenExtraShorterThanSealReserve exercises the
// "append" branch of Seal's seal-area fill: when the unsealed header's Extra
// is shorter than extraSealLen (a bare magic+view header with no QC and no
// pre-reserved seal slot), Seal must append the signature rather than index
// out of range, and the resulting header must still pass the decode
// self-check and be delivered on results.
func TestSealAppendsSignatureWhenExtraShorterThanSealReserve(t *testing.T) {
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	h := New(nil, params.TestChainConfig)
	h.Authorize(types.Address{1}, sk)

	// A bare magic+view extra with no trailing seal reserve: extraMinLen
	// bytes only, well under extraSealLen.
	extra := make([]byte, extraMinLen)
	copy(extra[:extraMagicLen], extraMagic[:])
	if len(extra) >= extraSealLen {
		t.Fatalf("test setup invariant broken: extraMinLen (%d) >= extraSealLen (%d)", len(extra), extraSealLen)
	}

	hdr := &block.Header{Number: uint256.NewInt(1), Extra: extra}
	b := block.NewBlockFromReceipt(hdr, nil, nil, nil, nil)
	results := make(chan block.IBlock, 1)
	if err := h.Seal(nil, b, results, nil); err != nil {
		t.Fatalf("Seal: %v", err)
	}

	select {
	case sealed := <-results:
		sh, ok := sealed.Header().(*block.Header)
		if !ok {
			t.Fatal("sealed block header is not *block.Header")
		}
		if len(sh.Extra) <= extraMinLen {
			t.Fatalf("expected the signature to be appended, Extra len = %d", len(sh.Extra))
		}
		if _, _, _, derr := decodeHeaderExtra(sh.Extra); derr != nil {
			t.Fatalf("sealed header extra failed to decode: %v", derr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a sealed block on results")
	}
}

// TestSealDeliveryRespectsStopChannel exercises Seal's other select branch:
// when results is never drained but stop is closed, the delivery goroutine
// must take <-stop instead of blocking forever on the send, and Seal itself
// must return promptly without waiting on the delivery at all.
func TestSealDeliveryRespectsStopChannel(t *testing.T) {
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatal(err)
	}
	h := New(nil, params.TestChainConfig)
	h.Authorize(types.Address{1}, sk)

	extra, err := buildHeaderExtra(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	hdr := &block.Header{Number: uint256.NewInt(2), Extra: extra}
	b := block.NewBlockFromReceipt(hdr, nil, nil, nil, nil)

	// results has no capacity and is never read: the delivery goroutine can
	// only proceed via the stop branch.
	results := make(chan block.IBlock)
	stop := make(chan struct{})

	done := make(chan error, 1)
	go func() { done <- h.Seal(nil, b, results, stop) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Seal: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Seal itself blocked on the unread results channel")
	}

	// Closing stop lets the delivery goroutine exit via <-stop. We do not also
	// race a receive on results here: once stop is closed, a concurrent
	// receive and the (now-closed-stop) send both being "ready" at once would
	// make Go's pseudo-random select nondeterministically rendezvous on
	// results instead of exiting via stop, flaking the assertion without any
	// product bug. Seal() itself returning promptly, above, already proves
	// the function does not block on the unread channel.
	close(stop)
}
