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

// TestAllocLeanFrameDifferential: frames of legacy transfers decode
// (DecodeTxs, DecodeFramesEachWith) and recover (RecoverSenders) to the
// same transactions, hashes, encodings and senders with N42_ALLOC_LEAN
// off and on.
func TestAllocLeanFrameDifferential(t *testing.T) {
	keys := frametest.Keys(t, 5, 7)
	signer := transaction.LatestSignerForChainID(big.NewInt(94))
	var raws [][]byte
	for f := 0; f < 3; f++ {
		var encs [][]byte
		for i := 0; i < 120+f*37; i++ {
			to := types.Address{byte(i), byte(f), 0x42}
			inner := &transaction.LegacyTx{Nonce: uint64(i), GasPrice: uint256.NewInt(1e9 + uint64(i/50)), Gas: 21000,
				To: &to, Value: uint256.NewInt(uint64(1 + i%3))}
			tx, err := transaction.SignNewTx(keys[1+i%4], signer, inner)
			if err != nil {
				t.Fatal(err)
			}
			encs = append(encs, mustEnc(t, tx))
		}
		if f == 2 {
			encs = append(encs, []byte{0x02, 0x01}) // undecodable, dropped
		}
		raw, _, err := frame.Encode(keys[0], uint64(f+1), frame.TxTypeEthereum, encs)
		if err != nil {
			t.Fatal(err)
		}
		raws = append(raws, raw)
	}
	type out struct {
		hashes  []types.Hash
		encs    [][]byte
		inners  []string
		senders []types.Address
		dropped int
	}
	run := func(lean bool) []out {
		defer transaction.SetAllocLeanForTest(lean)()
		per, dropped, err := frame.DecodeFramesEachWith(raws, 2, func(_ int, txs []*transaction.Transaction) { frame.HashTxs(txs) })
		if err != nil || dropped != 1 {
			t.Fatalf("lean %v: dropped %d err %v", lean, dropped, err)
		}
		var res []out
		for i, txs := range per {
			o := out{}
			for _, tx := range txs {
				o.hashes = append(o.hashes, tx.Hash())
				o.encs = append(o.encs, mustEnc(t, tx))
				v, r, s := tx.RawSignatureValues()
				o.inners = append(o.inners, fmt.Sprint(tx.Type(), tx.Nonce(), tx.Gas(), tx.GasPrice(), tx.Value(),
					*tx.To(), tx.Data(), v, r, s))
			}
			ss, failed, err := frame.RecoverSenders(signer, raws[i])
			if err != nil || failed != 0 {
				t.Fatalf("lean %v: recover %v failed %d", lean, err, failed)
			}
			o.senders = ss.Addrs
			_, o.dropped, _ = frame.DecodeTxs(raws[i])
			res = append(res, o)
		}
		return res
	}
	off, on := run(false), run(true)
	if !reflect.DeepEqual(off, on) {
		t.Fatal("N42_ALLOC_LEAN frame decode differs")
	}
}
