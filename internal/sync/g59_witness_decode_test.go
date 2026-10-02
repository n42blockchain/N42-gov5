package sync

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state/witness"
)

// TestDecodeWitnessResponseRoundTrip covers DecodeWitnessResponse's success
// path against a real encoded BlockWitness.
func TestDecodeWitnessResponseRoundTrip(t *testing.T) {
	w := &witness.BlockWitness{ParentRoot: types.Hash{0x01}}
	data, err := w.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	got, err := DecodeWitnessResponse(&GetBlockWitnessResponse{Found: true, Witness: data})
	if err != nil {
		t.Fatalf("DecodeWitnessResponse: %v", err)
	}
	if got.ParentRoot != w.ParentRoot {
		t.Fatalf("ParentRoot = %x, want %x", got.ParentRoot, w.ParentRoot)
	}
}

// TestDecodeWitnessResponseNotFound covers the !Found guard.
func TestDecodeWitnessResponseNotFound(t *testing.T) {
	if _, err := DecodeWitnessResponse(&GetBlockWitnessResponse{Found: false}); err == nil {
		t.Fatal("expected an error when Found is false")
	}
}

// TestDecodeWitnessResponseEmptyWitness covers the empty-witness-bytes guard.
func TestDecodeWitnessResponseEmptyWitness(t *testing.T) {
	if _, err := DecodeWitnessResponse(&GetBlockWitnessResponse{Found: true, Witness: nil}); err == nil {
		t.Fatal("expected an error for an empty witness payload")
	}
}
