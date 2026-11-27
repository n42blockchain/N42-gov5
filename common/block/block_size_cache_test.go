package block

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/rlp"
)

// TestBlockEncodedSizeCached: EncodedSize equals the RLP length, is cached,
// and WithSeal drops the cached value.
func TestBlockEncodedSizeCached(t *testing.T) {
	h := &Header{Number: uint256.NewInt(7), Difficulty: uint256.NewInt(1), GasLimit: 30_000_000}
	b := NewBlock(h, nil).(*Block)
	enc, err := rlp.EncodeToBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	size, err := b.EncodedSize()
	if err != nil || size != uint64(len(enc)) {
		t.Fatalf("EncodedSize = %d, %v; want %d", size, err, len(enc))
	}
	if cached, ok := b.size.Load().(uint64); !ok || cached != size {
		t.Fatal("size not cached")
	}
	h2 := CopyHeader(h)
	h2.Extra = make([]byte, 32)
	b.WithSeal(h2)
	enc2, _ := rlp.EncodeToBytes(b)
	size2, _ := b.EncodedSize()
	if size2 != uint64(len(enc2)) || size2 == size {
		t.Fatalf("after WithSeal: %d, want %d (was %d)", size2, len(enc2), size)
	}
}
