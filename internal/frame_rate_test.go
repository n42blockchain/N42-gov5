// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
)

// TestFrameDecodeRecoverRate measures the follower's per-block frame work
// on 8 cores: decode 100 frames x 500 transfers, then batch sender
// recovery. Opt-in (N42_FRAME_RATE=1): it signs 50k transactions.
func TestFrameDecodeRecoverRate(t *testing.T) {
	if os.Getenv("N42_FRAME_RATE") != "1" {
		t.Skip("set N42_FRAME_RATE=1")
	}
	prev := runtime.GOMAXPROCS(8)
	defer runtime.GOMAXPROCS(prev)
	keys := frametest.Keys(t, 1001, 11)
	fb := frametest.NewBuilder(keys[0], senderRecoveryChainID, keys[1:])
	raws, _, _ := fb.Frames(t, 100, 500)

	t0 := time.Now()
	txs, _, err := frame.DecodeFrames(raws, 8)
	if err != nil {
		t.Fatal(err)
	}
	dDecode := time.Since(t0)
	signer := transaction.LatestSignerForChainID(senderRecoveryChainID)
	t1 := time.Now()
	recoverBlockSenders(signer, txs)
	dRecover := time.Since(t1)
	for _, tx := range txs[:10] {
		if _, err := transaction.Sender(signer, tx); err != nil {
			t.Fatal(err)
		}
	}
	n := float64(len(txs))
	t.Logf("8 cores, %d txs: decode %v (%.0f tx/s), recover %v (%.0f tx/s), decode+recover %.0f tx/s",
		len(txs), dDecode, n/dDecode.Seconds(), dRecover, n/dRecover.Seconds(), n/(dDecode+dRecover).Seconds())
}
