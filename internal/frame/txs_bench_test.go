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

// BenchmarkAdmitAndHashTxs is one frame's whole hashing cost at a node:
// admission (Decode, Verify) and the transaction hashes its later decode
// needs (DecodeTxs + HashTxs; under v2 the decode takes the entry hashes
// admission kept). The decode is the same in both and is reported apart in
// BenchmarkDecodeTxs.
func BenchmarkAdmitAndHashTxsV1(b *testing.B) { benchAdmitAndHashTxs(b, false) }
func BenchmarkAdmitAndHashTxsV2(b *testing.B) { benchAdmitAndHashTxs(b, true) }

func benchAdmitAndHashTxs(b *testing.B, v2 bool) {
	defer frame.SetHashV2ForTest(v2)()
	keys := frametest.Keys(b, 33, 91)
	fb := frametest.NewBuilder(keys[0], big.NewInt(1337), keys[1:])
	raws, _, _ := fb.Frames(b, 1, frame.MaxTxs)
	raw := raws[0]
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		f, err := frame.Decode(raw)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := f.Verify(); err != nil {
			b.Fatal(err)
		}
		txs, _, err := frame.DecodeTxsSeeded(raw, f.TxHashes)
		if err != nil || len(txs) != frame.MaxTxs {
			b.Fatal(err)
		}
		frame.HashTxs(txs)
	}
}
