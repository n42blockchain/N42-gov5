package bodyf2

import (
	"errors"
	"testing"

	"github.com/holiman/uint256"
)

func TestStoreWriteReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	to := addr(9)
	u := uint256.NewInt
	// Two segments' worth of blocks (kept tiny: a few blocks each).
	mk := func(base byte) []F2Block {
		return []F2Block{
			{Txs: []F2Tx{
				{Type: 0, From: addr(base), To: &to, Nonce: 1, Gas: 21000, Value: u(1000), GasFeeCap: u(7)},
				{Type: 2, From: addr(base + 1), To: nil, Nonce: 2, Gas: 50000, Value: u(0), GasFeeCap: u(9), GasTipCap: u(2), Data: []byte{1, 2, 3}},
			}},
		}
	}

	dict := NewAddrDict()
	w, err := NewWriter(dir, dict)
	if err != nil {
		t.Fatal(err)
	}
	// Write segment 0 and 2 (leave 1 as a gap → absent sentinel).
	if err := w.AppendSegment(0, mk(10)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendSegment(2, mk(20)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := OpenReader(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Segment 0, block 0 (global block 0).
	b0, err := r.ReadBlock(0)
	if err != nil {
		t.Fatalf("read block 0: %v", err)
	}
	if len(b0.Txs) != 2 || b0.Txs[0].From != addr(10) || b0.Txs[0].Value.Uint64() != 1000 {
		t.Errorf("block 0 mismatch: %+v", b0.Txs[0])
	}
	if b0.Txs[1].To != nil || b0.Txs[1].GasTipCap.Uint64() != 2 {
		t.Errorf("block 0 tx1 mismatch")
	}
	// Segment 2 lives at global block 2*SegSize.
	b2, err := r.ReadBlock(2 * SegSize)
	if err != nil {
		t.Fatalf("read block 2*SegSize: %v", err)
	}
	if b2.Txs[0].From != addr(20) {
		t.Errorf("seg2 from mismatch: %x", b2.Txs[0].From)
	}
	// Segment 1 was a gap → ErrF2Absent.
	if _, err := r.ReadBlock(1 * SegSize); !errors.Is(err, ErrF2Absent) {
		t.Errorf("gap segment: want ErrF2Absent, got %v", err)
	}
}
