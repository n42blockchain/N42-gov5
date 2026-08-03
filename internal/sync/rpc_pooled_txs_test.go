package sync

import (
	"bytes"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

func hashN(i byte) types.Hash {
	var h types.Hash
	h[0], h[31] = i, i
	return h
}

func TestPooledTxsRequestRoundTrip(t *testing.T) {
	want := []types.Hash{hashN(1), hashN(2), hashN(255)}
	got, err := readPooledTxsRequest(bytes.NewReader(encodePooledTxsRequest(want)))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("count = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hash %d = %s, want %s", i, got[i], want[i])
		}
	}
}

// TestPooledTxsRequestRejectsOversize pins the bound on the responder's side.
// The count is peer-supplied, so without it a peer sizes this node's
// allocation and its lookup work.
func TestPooledTxsRequestRejectsOversize(t *testing.T) {
	over := make([]types.Hash, maxPooledTxRequest+1)
	if _, err := readPooledTxsRequest(bytes.NewReader(encodePooledTxsRequest(over))); err == nil {
		t.Fatal("expected a request over the limit to be rejected")
	}
	if _, err := readPooledTxsRequest(bytes.NewReader(encodePooledTxsRequest(nil))); err == nil {
		t.Fatal("expected an empty request to be rejected")
	}
}

// TestPooledTxsRequestRejectsShortPayload covers a peer that declares more
// hashes than it sends: the reader must fail rather than return whatever
// happened to be in the buffer.
func TestPooledTxsRequestRejectsShortPayload(t *testing.T) {
	full := encodePooledTxsRequest([]types.Hash{hashN(1), hashN(2)})
	if _, err := readPooledTxsRequest(bytes.NewReader(full[:len(full)-8])); err == nil {
		t.Fatal("expected a truncated request to be rejected")
	}
}

// TestInFlightClaimSuppressesDuplicates is the property that makes announcing
// cheaper than broadcasting: every mesh peer announces the same hash at about
// the same time, so without suppression a node opens one fetch per peer per
// transaction and re-creates the traffic the announcement was meant to avoid.
func TestInFlightClaimSuppressesDuplicates(t *testing.T) {
	tr := newInFlightTracker()
	batch := []types.Hash{hashN(1), hashN(2), hashN(3)}

	first := tr.claim(append([]types.Hash(nil), batch...))
	if len(first) != 3 {
		t.Fatalf("first claim took %d hashes, want 3", len(first))
	}
	second := tr.claim(append([]types.Hash(nil), batch...))
	if len(second) != 0 {
		t.Fatalf("second claim took %d hashes, want 0", len(second))
	}

	// A partially-overlapping batch must still yield the new hash.
	mixed := tr.claim([]types.Hash{hashN(2), hashN(9)})
	if len(mixed) != 1 || mixed[0] != hashN(9) {
		t.Fatalf("mixed claim = %v, want just hash 9", mixed)
	}
}

// TestInFlightClaimRetriesAfterTTL: a fetch that failed must be retried, or a
// transaction dropped once is dropped for as long as the entry survives in the
// cache.
func TestInFlightClaimRetriesAfterTTL(t *testing.T) {
	tr := newInFlightTracker()
	h := hashN(7)
	if len(tr.claim([]types.Hash{h})) != 1 {
		t.Fatal("first claim should take the hash")
	}
	tr.cache.Add(h, time.Now().Add(-inFlightTTL-time.Second))
	if len(tr.claim([]types.Hash{h})) != 1 {
		t.Fatal("claim should be retried once the TTL has passed")
	}
}

// TestClaimDoesNotAliasInput guards the slice reuse in claim: it builds its
// result with a zero-capacity reslice of the input precisely so it cannot
// write over hashes it has not examined yet.
func TestClaimDoesNotAliasInput(t *testing.T) {
	tr := newInFlightTracker()
	tr.cache.Add(hashN(1), time.Now())
	in := []types.Hash{hashN(1), hashN(2), hashN(3)}
	out := tr.claim(in)
	if len(out) != 2 || out[0] != hashN(2) || out[1] != hashN(3) {
		t.Fatalf("claim = %v, want hashes 2 and 3", out)
	}
	if in[0] != hashN(1) || in[1] != hashN(2) || in[2] != hashN(3) {
		t.Fatalf("claim modified its input: %v", in)
	}
}
