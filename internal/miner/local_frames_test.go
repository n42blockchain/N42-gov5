// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/internal/txspool"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules/state"
)

// lfTransfer signs a 21000-gas transfer with the given tip and fee cap.
func lfTransfer(t testing.TB, signer transaction.Signer, key *ecdsa.PrivateKey, nonce uint64, tip, feeCap uint64) *transaction.Transaction {
	t.Helper()
	to := types.Address{0x77}
	tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1337),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(tip),
		GasFeeCap: uint256.NewInt(feeCap),
		Gas:       21000,
		To:        &to,
		Value:     uint256.NewInt(1),
	}), signer, key)
	require.NoError(t, err)
	return tx
}

func lfSeq() func() uint64 {
	var n uint64
	return func() uint64 { n++; return n }
}

// TestPackLocalFrames: pool transactions are framed in the pool fill's
// order (price, then nonce), 500 to a frame, signed by the node key; the
// gas budget stops the packing; an account priced out by the base fee is
// passed over.
func TestPackLocalFrames(t *testing.T) {
	signer := transaction.NewLondonSigner(big.NewInt(1337))
	nodeKey, _ := crypto.GenerateKey()
	s := frame.KeySigner(nodeKey)
	keys := make([]*ecdsa.PrivateKey, 4)
	addrs := make([]types.Address, 4)
	for i := range keys {
		keys[i], _ = crypto.GenerateKey()
		addrs[i] = crypto.PubkeyToAddress(keys[i].PublicKey)
	}
	// Sender i bids tip 30-i: the highest bidder's whole run comes first.
	// Sender 3's fee cap is under the base fee.
	const baseFee = 100
	mk := func() (map[types.Address][]*transaction.Transaction, []*transaction.Transaction) {
		pending := map[types.Address][]*transaction.Transaction{}
		var want []*transaction.Transaction
		counts := []int{600, 450, 150, 10}
		for i, n := range counts {
			feeCap := uint64(1000)
			if i == 3 {
				feeCap = baseFee - 1
			}
			for nonce := 0; nonce < n; nonce++ {
				tx := lfTransfer(t, signer, keys[i], uint64(nonce), uint64(30-i), feeCap)
				pending[addrs[i]] = append(pending[addrs[i]], tx)
				if i != 3 {
					want = append(want, tx)
				}
			}
		}
		return pending, want
	}

	pending, want := mk()
	frames, err := packLocalFrames(s, lfSeq(), pending, uint256.NewInt(baseFee), 1<<40)
	require.NoError(t, err)
	require.Len(t, frames, 3)
	var got []*transaction.Transaction
	for i, f := range frames {
		require.LessOrEqual(t, len(f.txs), frame.MaxTxs)
		dec, err := frame.Decode(f.raw)
		require.NoError(t, err)
		h, err := dec.Verify()
		require.NoError(t, err, "frame %d signature", i)
		require.Equal(t, f.hash, h)
		require.Equal(t, s.Address, dec.Gateway, "frame %d signed by the node key", i)
		require.Equal(t, uint64(len(f.txs))*21000, f.gas)
		require.Equal(t, frame.GasEstimate(dec), f.gas)
		txs, _, err := frame.DecodeTxs(f.raw)
		require.NoError(t, err)
		require.Len(t, txs, len(f.txs))
		for j := range txs {
			require.Equal(t, f.txs[j].Hash(), txs[j].Hash())
		}
		got = append(got, f.txs...)
	}
	require.Equal(t, []int{500, 500, 200}, []int{len(frames[0].txs), len(frames[1].txs), len(frames[2].txs)})
	require.Len(t, got, len(want))
	for i := range want {
		require.Equal(t, want[i].Hash(), got[i].Hash(), "position %d", i)
	}

	// Budget for 700 transfers and a bit: 700 packed, in the same order.
	pending, want = mk()
	frames, err = packLocalFrames(s, lfSeq(), pending, uint256.NewInt(baseFee), 700*21000+20999)
	require.NoError(t, err)
	require.Len(t, frames, 2)
	require.Equal(t, 500, len(frames[0].txs))
	require.Equal(t, 200, len(frames[1].txs))
	require.Equal(t, want[699].Hash(), frames[1].txs[199].Hash())

	// No signer, empty pool, budget under one transfer: nothing.
	pending, _ = mk()
	for _, c := range []struct {
		s      *frame.Signer
		p      map[types.Address][]*transaction.Transaction
		budget uint64
	}{{nil, pending, 1 << 40}, {s, nil, 1 << 40}, {s, pending, 20999}} {
		frames, err = packLocalFrames(c.s, lfSeq(), c.p, uint256.NewInt(baseFee), c.budget)
		require.NoError(t, err)
		require.Empty(t, frames)
	}
}

// TestPickFramesExcludesOwnSigner: the gateway pick passes over this
// node's own local frames (a discarded build's leftovers); they are framed
// afresh from the pool instead.
func TestPickFramesExcludesOwnSigner(t *testing.T) {
	st, hashes := frameStoreOf(t, 3, 2)
	nodeKey, _ := crypto.GenerateKey()
	s := frame.KeySigner(nodeKey)
	signer := transaction.NewLondonSigner(big.NewInt(1337))
	k, _ := crypto.GenerateKey()
	frames, err := packLocalFrames(s, lfSeq(), map[types.Address][]*transaction.Transaction{
		crypto.PubkeyToAddress(k.PublicKey): {lfTransfer(t, signer, k, 0, 1, 1000)},
	}, nil, 1<<30)
	require.NoError(t, err)
	require.Len(t, frames, 1)
	require.True(t, st.PutEntry(frames[0].hash, store.Entry{Raw: frames[0].raw, Gas: frames[0].gas, Txs: 1}))

	p := pickFrames(st, nil, s.Address, 1<<40, 1024)
	require.Equal(t, hashes, p.hashes)
	p = pickFrames(st, nil, types.Address{}, 1<<40, 1024)
	require.Len(t, p.hashes, 4)
}

// lfPoolPending counts the pool's pending transactions.
func lfPoolPending(p interface {
	Pending(bool) map[types.Address][]*transaction.Transaction
}) int {
	n := 0
	for _, l := range p.Pending(false) {
		n += len(l)
	}
	return n
}

// lfMineBlock builds, seals and writes one block through the worker's own
// production path (commitWork -> task -> handleSealed).
func lfMineBlock(t *testing.T, w *worker, ts int64) block.IBlock {
	t.Helper()
	require.NoError(t, w.commitWork(nil, false, ts, types.Hash{}, false, time.Time{}))
	var tk *task
	select {
	case tk = <-w.taskCh:
	default:
		t.Fatal("commitWork sent no task")
	}
	sealhash := w.engine.SealHash(tk.block.Header())
	w.mu.Lock()
	w.pendingTasks[sealhash] = tk
	w.mu.Unlock()
	before := w.chain.CurrentBlock().Number64().Uint64()
	w.handleSealed(tk.block)
	require.Equal(t, before+1, w.chain.CurrentBlock().Number64().Uint64(), "block not written")
	return tk.block
}

func lfStateNonce(t *testing.T, f *mnTChainFixture, a types.Address) uint64 {
	t.Helper()
	var n uint64
	require.NoError(t, f.DB.View(context.Background(), func(tx kv.Tx) error {
		acc, err := state.NewPlainStateReader(tx).ReadAccountData(a)
		if acc != nil {
			n = acc.Nonce
		}
		return err
	}))
	return n
}

// TestFrameBlocksMinePoolTxs is the S87 regression: with frame blocks on
// and an EMPTY frame store, transactions submitted through the pool (the
// JSON-RPC path, the benchmark's funding) are mined as the leader's local
// frames within a few blocks, and the pool's pending set drains through its
// ordinary chain-head reset (state nonces). Then gateway frames arrive and
// a block carries both kinds, the pool's local frames first.
func TestFrameBlocksMinePoolTxs(t *testing.T) {
	defer internal.SetFrameBlocksForTest(true)()
	f := mnTNewChainFixture(t)
	st := store.New(1 << 26)
	f.Chain.SetFrameStore(st)

	poolIface, err := txspool.NewTxsPool(context.Background(), f.Chain, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = poolIface.Stop() })

	w := mnTBareWorker(t, f, poolIface)
	w.coinbase = f.Coinbase
	w.resubmitAdjustCh = make(chan *intervalAdjust, 64)
	atomic.StoreInt32(&w.running, 1)
	nodeKey, _ := crypto.GenerateKey()
	w.SetLocalFrameSigner(frame.KeySigner(nodeKey))
	nodeAddr := crypto.PubkeyToAddress(nodeKey.PublicKey)

	var txs []*transaction.Transaction
	for i := 0; i < 3; i++ {
		txs = append(txs, f.mnTSignedTransfer(t, 0, f.Senders[3], uint256.NewInt(1)))
	}
	for i := 0; i < 2; i++ {
		txs = append(txs, f.mnTSignedTransfer(t, 1, f.Senders[3], uint256.NewInt(1)))
	}
	for i, err := range poolIface.AddLocals(txs) {
		require.NoError(t, err, "tx %d", i)
	}
	require.Eventually(t, func() bool { return lfPoolPending(poolIface) == len(txs) }, 5*time.Second, 10*time.Millisecond)

	ts := time.Now().Unix()
	mined := 0
	for b := 0; b < 3 && mined < len(txs); b++ {
		ts++
		blk := lfMineBlock(t, w, ts)
		body := blk.Body().(*block.Body)
		if len(blk.Transactions()) > 0 {
			require.True(t, body.IsFrameBody(), "a pool tx was mined outside a frame")
			for _, raw := range body.FrameData {
				gw, _ := frame.GatewayOf(raw)
				require.Equal(t, nodeAddr, gw)
			}
		}
		mined += len(blk.Transactions())
	}
	require.Equal(t, len(txs), mined, "pool transactions not mined in frame blocks")
	require.Equal(t, uint64(3), lfStateNonce(t, f, f.Senders[0]))
	require.Equal(t, uint64(2), lfStateNonce(t, f, f.Senders[1]))
	require.Eventually(t, func() bool { return lfPoolPending(poolIface) == 0 }, 5*time.Second, 10*time.Millisecond,
		"the pool did not drop the mined local-frame transactions")
	require.Zero(t, st.Len(), "written frames stay in the store")

	// Gateway frames arrive; new pool transactions too.
	gwKey, _ := crypto.GenerateKey()
	var gwTxs [][]byte
	for i := 0; i < 4; i++ {
		enc, err := f.mnTSignedTransfer(t, 2, f.Senders[3], uint256.NewInt(1)).EthEncoded()
		require.NoError(t, err)
		gwTxs = append(gwTxs, enc)
	}
	gwRaw, gwHash, err := frame.Encode(gwKey, 1, frame.TxTypeEthereum, gwTxs)
	require.NoError(t, err)
	dec, err := frame.Decode(gwRaw)
	require.NoError(t, err)
	require.True(t, st.PutEntry(gwHash, store.Entry{Raw: gwRaw, Gas: frame.GasEstimate(dec), Txs: int(dec.TxCount)}))
	poolTx := f.mnTSignedTransfer(t, 0, f.Senders[3], uint256.NewInt(1))
	require.NoError(t, poolIface.AddLocals([]*transaction.Transaction{poolTx})[0])
	require.Eventually(t, func() bool { return lfPoolPending(poolIface) == 1 }, 5*time.Second, 10*time.Millisecond)

	ts++
	blk := lfMineBlock(t, w, ts)
	body := blk.Body().(*block.Body)
	require.Len(t, body.FrameHashes, 2)
	first, _ := frame.GatewayOf(body.FrameData[0])
	require.Equal(t, nodeAddr, first, "the pool's local frame comes first")
	require.Equal(t, gwHash, body.FrameHashes[1])
	got := blk.Transactions()
	require.Len(t, got, 5)
	require.Equal(t, poolTx.Hash(), got[0].Hash())
	require.Equal(t, uint64(4), lfStateNonce(t, f, f.Senders[0]))
	require.Equal(t, uint64(4), lfStateNonce(t, f, f.Senders[2]))
	require.Eventually(t, func() bool { return lfPoolPending(poolIface) == 0 }, 5*time.Second, 10*time.Millisecond)
}
