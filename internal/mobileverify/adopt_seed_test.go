package mobileverify

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestAdoptPendingRejectsInvalidPoP(t *testing.T) {
	reg := NewRegistry()
	d := newDevice(t)
	other := newDevice(t)
	// A valid PoP signature from a different key does not verify for d.pubkey.
	if _, err := reg.AdoptPending(d.pubkey, other.pop()); err == nil {
		t.Fatal("expected error for a PoP signed by the wrong key")
	}
}

func TestAdoptPendingNewAndDuplicatePending(t *testing.T) {
	reg := NewRegistry()
	d := newDevice(t)

	added, err := reg.AdoptPending(d.pubkey, d.pop())
	if err != nil || !added {
		t.Fatalf("first AdoptPending() = (%v,%v), want (true,nil)", added, err)
	}

	added, err = reg.AdoptPending(d.pubkey, d.pop())
	if err != nil || added {
		t.Fatalf("duplicate-pending AdoptPending() = (%v,%v), want (false,nil)", added, err)
	}
}

func TestAdoptPendingAlreadyKnown(t *testing.T) {
	reg := NewRegistry()
	d := newDevice(t)
	if _, _, err := reg.Register(d.pubkey, d.pop()); err != nil {
		t.Fatal(err)
	}
	reg.CommitEpoch()

	added, err := reg.AdoptPending(d.pubkey, d.pop())
	if err != nil || added {
		t.Fatalf("AdoptPending() for an already-known key = (%v,%v), want (false,nil)", added, err)
	}
}

func TestPacketSeedsPutGetAndDedup(t *testing.T) {
	ps := newPacketSeeds(2)
	h1 := types.HexToHash("0x01")
	ps.put(h1, "magnet:1")
	if got, ok := ps.get(h1); !ok || got != "magnet:1" {
		t.Fatalf("get(h1) = (%q,%v), want (magnet:1,true)", got, ok)
	}

	// Re-putting the same hash is a no-op (first magnet wins).
	ps.put(h1, "magnet:1-overwrite")
	if got, _ := ps.get(h1); got != "magnet:1" {
		t.Fatalf("get(h1) after duplicate put = %q, want original magnet:1", got)
	}
}

func TestPacketSeedsEvictsOldestOverMax(t *testing.T) {
	ps := newPacketSeeds(2)
	h1 := types.HexToHash("0x01")
	h2 := types.HexToHash("0x02")
	h3 := types.HexToHash("0x03")
	ps.put(h1, "m1")
	ps.put(h2, "m2")
	ps.put(h3, "m3") // evicts h1

	if _, ok := ps.get(h1); ok {
		t.Fatal("h1 should have been evicted once max was exceeded")
	}
	if _, ok := ps.get(h2); !ok {
		t.Fatal("h2 should still be present")
	}
	if _, ok := ps.get(h3); !ok {
		t.Fatal("h3 should still be present")
	}
}

func TestNewPacketSeedsDefaultsNonPositiveMax(t *testing.T) {
	ps := newPacketSeeds(0)
	if ps.max != 256 {
		t.Fatalf("max = %d, want default 256 for a zero input", ps.max)
	}
}
