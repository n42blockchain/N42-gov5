package ethel

import "testing"

// TestBodyWireRoundTrip: EncodeBodyBlock → DecodeBodyBlock reconstructs each
// block faithfully (tx count + per-tx Hash, which covers all tx fields). Reuses
// the makeTestBlocks fixtures (legacy/dynfee/setcode/blob mixes), so it certifies
// the single-block serve-RPC body wire against the same cases as the segment codec.
func TestBodyWireRoundTrip(t *testing.T) {
	for bi, orig := range makeTestBlocks() {
		wire, err := EncodeBodyBlock(orig, 1)
		if err != nil {
			t.Fatalf("block %d: encode: %v", bi, err)
		}
		got, err := DecodeBodyBlock(wire)
		if err != nil {
			t.Fatalf("block %d: decode: %v", bi, err)
		}
		if len(got.Txs) != len(orig.Txs) {
			t.Fatalf("block %d: tx count %d != %d", bi, len(got.Txs), len(orig.Txs))
		}
		for ti := range orig.Txs {
			if got.Txs[ti].Hash() != orig.Txs[ti].Hash() {
				t.Errorf("block %d tx %d: hash %x != %x", bi, ti,
					got.Txs[ti].Hash(), orig.Txs[ti].Hash())
			}
		}
		if len(got.Withdrawals) != len(orig.Withdrawals) {
			t.Errorf("block %d: withdrawal count %d != %d", bi, len(got.Withdrawals), len(orig.Withdrawals))
		}
		// GethBodyFromDecoded must surface the same txs for replay.
		gb := GethBodyFromDecoded(got)
		if len(gb.Transactions) != len(orig.Txs) {
			t.Errorf("block %d: GethBody tx count %d != %d", bi, len(gb.Transactions), len(orig.Txs))
		}
	}
}
