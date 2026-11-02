package mobileverify

import (
	"errors"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// A receipt that fails admission must not leave a window behind: windows are
// capped, so junk that opens them locks honest devices out of new blocks.
func TestSubmitRejectedReceiptOpensNoWindow(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	coord := NewCohortCoordinator(reg, fixedLookupAt(blockHash, number), NewCertStore(64), types.Address{19: 1}, DefaultCohortConfig())

	unregistered := newDevice(t)
	if _, err := coord.Submit(unregistered.receipt(blockHash, number, root)); !errors.Is(err, ErrNotRegistered) {
		t.Fatalf("unregistered device: err = %v", err)
	}
	forged := newDevice(t)
	registerCommitted(t, reg, forged.pubkey, forged.pop())
	receipt := forged.receipt(blockHash, number, root)
	receipt.Signature[0] ^= 0xFF
	if _, err := coord.Submit(receipt); err == nil {
		t.Fatal("forged signature accepted")
	}
	if got := coord.OpenWindows(); got != 0 {
		t.Fatalf("rejected receipts left %d windows open", got)
	}
}

// Once a block is certified, a late receipt must not reopen its window: the
// new window would finalize with that one signer and replace the stored
// certificate.
func TestSubmitAfterFinalizationKeepsTheCertificate(t *testing.T) {
	blockHash, number, root := h(1), uint64(1000), h(2)
	reg := NewRegistry()
	store := NewCertStore(64)
	cfg := DefaultCohortConfig()
	coord := NewCohortCoordinator(reg, fixedLookupAt(blockHash, number), store, types.Address{19: 1}, cfg)

	devices := []*device{newDevice(t), newDevice(t), newDevice(t)}
	for _, d := range devices {
		registerCommitted(t, reg, d.pubkey, d.pop())
	}
	for _, d := range devices[:2] {
		if _, err := coord.Submit(d.receipt(blockHash, number, root)); err != nil {
			t.Fatal(err)
		}
	}
	coord.OnBlockCommitted(number + cfg.MergeDelay)
	certs := store.Get(blockHash)
	if len(certs) != 1 {
		t.Fatalf("stored %d certificates, want 1", len(certs))
	}

	if _, err := coord.Submit(devices[2].receipt(blockHash, number, h(3))); !errors.Is(err, ErrWindowClosed) {
		t.Fatalf("late receipt: err = %v, want %v", err, ErrWindowClosed)
	}
	coord.OnBlockCommitted(number + cfg.MergeDelay + 1)
	got := store.Get(blockHash)
	if len(got) != 1 || got[0] != certs[0] {
		t.Fatal("a late receipt replaced the stored certificate")
	}
}
