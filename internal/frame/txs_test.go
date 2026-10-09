// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame_test

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
)

func TestDecodeFramesAndGas(t *testing.T) {
	keys := frametest.Keys(t, 8, 1)
	b := frametest.NewBuilder(keys[0], big.NewInt(94), keys[1:])
	raws, hashes, txs := b.Frames(t, 6, 50)
	for i, raw := range raws {
		h, err := frame.HashOf(raw)
		if err != nil || h != hashes[i] {
			t.Fatalf("HashOf %d: %v", i, err)
		}
		f, err := frame.Decode(raw)
		if err != nil {
			t.Fatal(err)
		}
		if g := frame.GasEstimate(f); g != 50*21000 {
			t.Fatalf("gas estimate %d", g)
		}
	}
	for _, workers := range []int{1, 4} {
		got, dropped, err := frame.DecodeFrames(raws, workers)
		if err != nil || dropped != 0 || len(got) != len(txs) {
			t.Fatalf("workers %d: %d txs dropped %d err %v", workers, len(got), dropped, err)
		}
		for i := range txs {
			if got[i].Hash() != txs[i].Hash() {
				t.Fatalf("workers %d: tx %d order differs", workers, i)
			}
		}
	}
	// An undecodable entry is dropped, not fatal.
	bad, _, err := frame.Encode(keys[0], 9, frame.TxTypeEthereum, [][]byte{{0x02, 0x01}, mustEnc(t, txs[0])})
	if err != nil {
		t.Fatal(err)
	}
	got, dropped, err := frame.DecodeTxs(bad)
	if err != nil || dropped != 1 || len(got) != 1 {
		t.Fatalf("bad entry: %d %d %v", len(got), dropped, err)
	}
}

func mustEnc(t *testing.T, tx interface{ EthEncoded() ([]byte, error) }) []byte {
	b, err := tx.EthEncoded()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
