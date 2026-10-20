// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame_test

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
)

// concatHashes is what DecodeFramesEachWith's callers build: every frame's
// transactions in frame order.
func concatHashes(per [][]*transaction.Transaction) []types.Hash {
	var out []types.Hash
	for _, p := range per {
		for _, tx := range p {
			out = append(out, tx.Hash())
		}
	}
	return out
}

// TestDecodeFramesFlatMatchesEach: DecodeFramesFlatWith yields exactly the
// per-frame decode and its concatenation (N42_INGEST_RECYCLE on and off),
// in any worker count; flat is per's windows, each capped so an append to
// one frame cannot overwrite the next; a frame with a dropped entry gives
// no flat slice but the same per and dropped count; a bad layout is the
// same error.
func TestDecodeFramesFlatMatchesEach(t *testing.T) {
	keys := frametest.Keys(t, 6, 7)
	b := frametest.NewBuilder(keys[0], big.NewInt(94), keys[1:])
	raws, _, txs := b.Frames(t, 5, 30)
	empty, _, err := frame.Encode(keys[0], 77, frame.TxTypeEthereum, nil)
	if err != nil {
		t.Fatal(err)
	}
	bad, _, err := frame.Encode(keys[0], 78, frame.TxTypeEthereum, [][]byte{mustEnc(t, txs[1]), {0x02, 0x01}, mustEnc(t, txs[0])})
	if err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{1, 3} {
		for _, set := range [][][]byte{raws, append(append([][]byte(nil), raws[:2]...), append([][]byte{empty}, raws[2:]...)...), {raws[0], bad, raws[1]}} {
			seen := make([]int, len(set))
			wantPer, wantDropped, err := frame.DecodeFramesEachWith(set, workers, nil)
			if err != nil {
				t.Fatal(err)
			}
			flat, per, dropped, err := frame.DecodeFramesFlatWith(set, workers, func(i int, got []*transaction.Transaction) {
				seen[i]++
				frame.HashTxs(got)
			})
			if err != nil || dropped != wantDropped || len(per) != len(wantPer) {
				t.Fatalf("workers %d: %v dropped %d/%d frames %d/%d", workers, err, dropped, wantDropped, len(per), len(wantPer))
			}
			want := concatHashes(wantPer)
			if got := concatHashes(per); !reflect.DeepEqual(got, want) {
				t.Fatalf("workers %d: per differs", workers)
			}
			for i := range set {
				if seen[i] != 1 {
					t.Fatalf("workers %d: frame %d seen %d times", workers, i, seen[i])
				}
			}
			if wantDropped != 0 {
				if flat != nil {
					t.Fatalf("workers %d: flat slice despite a dropped entry", workers)
				}
				continue
			}
			if flat == nil || len(flat) != len(want) || cap(flat) != len(want) {
				t.Fatalf("workers %d: flat %d/%d, want %d", workers, len(flat), cap(flat), len(want))
			}
			off := 0
			for i, p := range per {
				if cap(p) != len(p) {
					t.Fatalf("workers %d: frame %d window cap %d len %d", workers, i, cap(p), len(p))
				}
				for j := range p {
					if p[j] != flat[off+j] {
						t.Fatalf("workers %d: frame %d is not flat's window", workers, i)
					}
				}
				off += len(p)
			}
			for i, tx := range flat {
				if tx.Hash() != want[i] {
					t.Fatalf("workers %d: flat tx %d differs", workers, i)
				}
			}
		}
	}
	// A bad layout fails the same way, after is called only for good frames.
	broken := append([]byte(nil), raws[1]...)
	broken[30]++
	calls := 0
	if flat, per, _, err := frame.DecodeFramesFlatWith([][]byte{raws[0], broken}, 1, func(int, []*transaction.Transaction) { calls++ }); err != frame.ErrPayloadLayout || flat != nil || per != nil || calls != 1 {
		t.Fatalf("broken layout: %v flat %v per %v calls %d", err, flat != nil, per != nil, calls)
	}
	if _, _, _, err := frame.DecodeFramesFlatWith([][]byte{raws[0], broken[:10]}, 1, nil); err == nil {
		t.Fatal("short header decoded")
	}
}

// BenchmarkDecodeBlockFrames: a 16-frame block of MaxTxs transfers each,
// decoded into the block's transaction list as ResolveFrames does, with
// N42_INGEST_RECYCLE off (per frame, then concatenated) and on (flat).
func BenchmarkDecodeBlockFrames(b *testing.B) {
	keys := frametest.Keys(b, 33, 92)
	fb := frametest.NewBuilder(keys[0], big.NewInt(1337), keys[1:])
	raws, _, _ := fb.Frames(b, 16, frame.MaxTxs)
	for _, on := range []bool{false, true} {
		name := "off"
		if on {
			name = "on"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				var txs []*transaction.Transaction
				if on {
					flat, _, _, err := frame.DecodeFramesFlatWith(raws, 1, nil)
					if err != nil || flat == nil {
						b.Fatal(err)
					}
					txs = flat
				} else {
					per, _, err := frame.DecodeFramesEachWith(raws, 1, nil)
					if err != nil {
						b.Fatal(err)
					}
					total := 0
					for _, p := range per {
						total += len(p)
					}
					txs = make([]*transaction.Transaction, 0, total)
					for _, p := range per {
						txs = append(txs, p...)
					}
				}
				if len(txs) != 16*frame.MaxTxs {
					b.Fatal(len(txs))
				}
			}
		})
	}
}
