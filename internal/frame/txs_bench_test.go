// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame_test

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
)

// BenchmarkDecodeTxs: one frame of MaxTxs signed transfers decoded as the
// import does (layout check + transaction decode). Reported per frame.
func BenchmarkDecodeTxs(b *testing.B) {
	keys := frametest.Keys(b, 33, 91)
	fb := frametest.NewBuilder(keys[0], big.NewInt(1337), keys[1:])
	raws, _, _ := fb.Frames(b, 1, frame.MaxTxs)
	b.ReportAllocs()
	b.SetBytes(int64(len(raws[0])))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		txs, dropped, err := frame.DecodeTxs(raws[0])
		if err != nil || dropped != 0 || len(txs) != frame.MaxTxs {
			b.Fatal(err, dropped, len(txs))
		}
	}
}
