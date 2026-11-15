// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame_test

import (
	"fmt"
	"math/big"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
)

func encTB(tb testing.TB, tx interface{ EthEncoded() ([]byte, error) }) []byte {
	tb.Helper()
	b, err := tx.EthEncoded()
	if err != nil {
		tb.Fatal(err)
	}
	return b
}

// slabFrames builds frames of legacy and dynamic-fee transfers, one with an
// undecodable entry.
func slabFrames(t testing.TB, nFrames, perFrame int) [][]byte {
	keys := frametest.Keys(t, 5, 9)
	signer := transaction.LatestSignerForChainID(big.NewInt(94))
	var encs [][]byte
	for i := 0; i < perFrame; i++ {
		to := types.Address{byte(i), 0x42}
		var inner transaction.TxData = &transaction.LegacyTx{Nonce: uint64(i), GasPrice: uint256.NewInt(1e9 + uint64(i/50)),
			Gas: 21000, To: &to, Value: uint256.NewInt(uint64(1 + i%3))}
		if i%5 == 4 {
			inner = &transaction.DynamicFeeTx{ChainID: uint256.NewInt(94), Nonce: uint64(i), GasTipCap: uint256.NewInt(1),
				GasFeeCap: uint256.NewInt(2e9), Gas: 21000, To: &to, Value: uint256.NewInt(7)}
		}
		tx, err := transaction.SignNewTx(keys[1+i%4], signer, inner)
		if err != nil {
			t.Fatal(err)
		}
		encs = append(encs, encTB(t, tx))
	}
	var raws [][]byte
	for f := 0; f < nFrames; f++ {
		e := encs[:perFrame-f%3] // frames of different sizes
		if f == 3 {
			e = append(append([][]byte(nil), e...), []byte{0x02, 0x01})
		}
		raw, _, err := frame.Encode(keys[0], uint64(f+1), frame.TxTypeEthereum, e)
		if err != nil {
			t.Fatal(err)
		}
		raws = append(raws, raw)
	}
	return raws
}

func describeTxs(t testing.TB, per [][]*transaction.Transaction) []string {
	var out []string
	for _, txs := range per {
		for _, tx := range txs {
			v, r, s := tx.RawSignatureValues()
			out = append(out, fmt.Sprint(tx.Hash(), encTB(t, tx), tx.Type(), tx.Nonce(), tx.Gas(), tx.GasPrice(),
				tx.GasFeeCap(), tx.Value(), *tx.To(), v, r, s))
		}
		out = append(out, "|")
	}
	return out
}

// TestFrameDecodeSlabDifferential: with N42_FRAME_DECODE_SLAB a block's
// frames decode to the same transactions (values, hashes, encodings) as per
// frame, for lean and default slabs and any worker count; every frame's
// transactions, read after the whole block decoded, are still intact (a
// later frame never overwrote an earlier one's slab entries); and the
// callers' appends to a frame's slice never write into another frame's.
func TestFrameDecodeSlabDifferential(t *testing.T) {
	raws := slabFrames(t, 40, 60)
	for _, lean := range []bool{false, true} {
		for _, workers := range []int{1, 3, 16} {
			t.Run(fmt.Sprintf("lean%v/w%d", lean, workers), func(t *testing.T) {
				defer transaction.SetAllocLeanForTest(lean)()
				decode := func(slab bool) ([][]*transaction.Transaction, int) {
					defer frame.SetFrameDecodeSlabForTest(slab)()
					per, dropped, err := frame.DecodeFramesEachWith(raws, workers, nil)
					if err != nil {
						t.Fatal(err)
					}
					return per, dropped
				}
				want, wd := decode(false)
				got, gd := decode(true)
				if wd != 1 || gd != 1 {
					t.Fatalf("dropped %d / %d", wd, gd)
				}
				if !reflect.DeepEqual(describeTxs(t, want), describeTxs(t, got)) {
					t.Fatal("slab decode differs")
				}
				// Appending to one frame's slice leaves its neighbour alone.
				before := describeTxs(t, got)
				for i := range got {
					got[i] = append(got[i], nil)
					got[i] = got[i][:len(got[i])-1]
				}
				if !reflect.DeepEqual(before, describeTxs(t, got)) {
					t.Fatal("append wrote into another frame's transactions")
				}
			})
		}
	}
}

// BenchmarkDecodeFramesBlock: one block of 620 frames x 500 transfers on
// 48 workers, per-frame slabs vs per-worker slabs (N42_FRAME_DECODE_SLAB),
// both with N42_ALLOC_LEAN on.
func BenchmarkDecodeFramesBlock(b *testing.B) {
	keys := frametest.Keys(b, 33, 91)
	fb := frametest.NewBuilder(keys[0], big.NewInt(1337), keys[1:])
	base, _, txs := fb.Frames(b, 1, frame.MaxTxs)
	encs := make([][]byte, len(txs))
	for i, tx := range txs {
		encs[i] = encTB(b, tx)
	}
	raws := make([][]byte, 620)
	raws[0] = base[0]
	for i := 1; i < len(raws); i++ {
		raw, _, err := frame.Encode(keys[0], uint64(i+1), frame.TxTypeEthereum, encs)
		if err != nil {
			b.Fatal(err)
		}
		raws[i] = raw
	}
	defer transaction.SetAllocLeanForTest(true)()
	for _, slab := range []bool{false, true} {
		b.Run(fmt.Sprintf("slab=%v", slab), func(b *testing.B) {
			defer frame.SetFrameDecodeSlabForTest(slab)()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, _, err := frame.DecodeFramesEachWith(raws, 48, nil); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
