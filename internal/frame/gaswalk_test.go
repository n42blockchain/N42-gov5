// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package frame_test

import (
	"crypto/ecdsa"
	"fmt"
	"math"
	"math/big"
	"math/rand"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/lib/rlp"
)

// gasCases are gas limits around the interesting edges: zero, the usual
// transfer, every byte-length boundary of the RLP integer, and the top of
// uint64 (whose sums saturate).
var gasCases = []uint64{0, 1, 0x7f, 0x80, 0xff, 0x100, 21000, 0xffff, 1 << 24, 1<<32 - 1, 1 << 40, 1<<56 + 3,
	math.MaxUint64 / 2, math.MaxUint64 - 1, math.MaxUint64}

func randGas(r *rand.Rand) uint64 {
	if r.Intn(3) == 0 {
		return gasCases[r.Intn(len(gasCases))]
	}
	return r.Uint64() >> uint(r.Intn(64))
}

func randU256(r *rand.Rand) *uint256.Int {
	return new(uint256.Int).SetUint64(r.Uint64() >> uint(r.Intn(64)))
}

// randTx returns a random Ethereum transaction of every type the codec
// knows, signed (the decoders' fast paths) or carrying raw signature values.
func randTx(t *testing.T, r *rand.Rand, key *ecdsa.PrivateKey, signer transaction.Signer, chain *uint256.Int) *transaction.Transaction {
	t.Helper()
	to := types.BytesToAddress([]byte{byte(r.Intn(256)), 0xee})
	var data []byte
	if r.Intn(2) == 0 {
		data = make([]byte, r.Intn(200))
		r.Read(data)
	}
	var al transaction.AccessList
	if r.Intn(3) == 0 {
		al = transaction.AccessList{{Address: to, StorageKeys: []types.Hash{types.BytesToHash([]byte{1})}}}
	}
	var inner transaction.TxData
	switch r.Intn(5) {
	case 0:
		inner = &transaction.LegacyTx{Nonce: r.Uint64() >> 40, GasPrice: randU256(r), Gas: randGas(r), To: &to, Value: randU256(r), Data: data}
	case 1:
		inner = &transaction.AccessListTx{ChainID: chain, Nonce: r.Uint64() >> 40, GasPrice: randU256(r), Gas: randGas(r), To: &to,
			Value: randU256(r), Data: data, AccessList: al}
	case 2:
		inner = &transaction.DynamicFeeTx{ChainID: chain, Nonce: r.Uint64() >> 40, GasTipCap: randU256(r), GasFeeCap: randU256(r),
			Gas: randGas(r), To: &to, Value: randU256(r), Data: data, AccessList: al}
	case 3:
		inner = &transaction.BlobTx{ChainID: chain, Nonce: r.Uint64() >> 40, GasTipCap: randU256(r), GasFeeCap: randU256(r),
			Gas: randGas(r), To: to, Value: randU256(r), Data: data, AccessList: al, BlobFeeCap: randU256(r),
			BlobHashes: []types.Hash{types.BytesToHash([]byte{1, 2})}}
	default:
		inner = &transaction.SetCodeTx{ChainID: chain, Nonce: r.Uint64() >> 40, GasTipCap: randU256(r), GasFeeCap: randU256(r),
			Gas: randGas(r), To: &to, Value: randU256(r), Data: data, AccessList: al}
	}
	if r.Intn(4) != 0 {
		if tx, err := transaction.SignNewTx(key, signer, inner); err == nil {
			return tx
		}
	}
	return transaction.NewTx(inner)
}

// malformed returns an entry DecodeTxs may drop: garbage, truncations,
// trailing bytes, unknown type bytes, short lists, non-canonical and
// oversized gas fields.
func malformed(r *rand.Rand, valid []byte) []byte {
	switch r.Intn(9) {
	case 0:
		b := make([]byte, 1+r.Intn(120))
		r.Read(b)
		return b
	case 1:
		if len(valid) > 2 {
			return append([]byte(nil), valid[:1+r.Intn(len(valid)-1)]...)
		}
		return []byte{0xc1}
	case 2:
		return append(append([]byte(nil), valid...), byte(r.Intn(256)))
	case 3:
		return []byte{[]byte{0x05, 0x7f, 0x80, 0xbf}[r.Intn(4)], 0xc3, 0x01, 0x02, 0x03}
	case 4:
		return []byte{0xc0}
	case 5:
		// A legacy list whose gas has a leading zero byte.
		b, _ := rlp.EncodeToBytes([]interface{}{uint64(1), uint64(2), []byte{0x00, 0x52, 0x08}, make([]byte, 20), uint64(1), []byte{},
			uint64(27), uint64(1), uint64(1)})
		return b
	case 6:
		// Gas of nine bytes.
		b, _ := rlp.EncodeToBytes([]interface{}{uint64(1), uint64(2), []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}, make([]byte, 20), uint64(1),
			[]byte{}, uint64(27), uint64(1), uint64(1)})
		return b
	case 7:
		// A dynamic-fee envelope with a list where the gas belongs.
		b, _ := rlp.EncodeToBytes([]interface{}{uint64(1), uint64(2), uint64(3), uint64(4), []interface{}{uint64(5)}, make([]byte, 20)})
		return append([]byte{0x02}, b...)
	default:
		// One byte of a valid encoding flipped.
		b := append([]byte(nil), valid...)
		if len(b) > 0 {
			b[r.Intn(len(b))] ^= byte(1 + r.Intn(255))
		}
		return b
	}
}

func satSum(txs []*transaction.Transaction) uint64 {
	var g uint64
	for _, tx := range txs {
		if s := g + tx.Gas(); s >= g {
			g = s
		} else {
			g = math.MaxUint64
		}
	}
	return g
}

// checkWalk asserts GasWalk's contract against DecodeTxs on raw.
func checkWalk(t *testing.T, raw []byte, what string) (dropped int) {
	t.Helper()
	walked, n, werr := frame.GasWalk(raw)
	txs, dropped, derr := frame.DecodeTxs(raw)
	if werr != derr {
		t.Fatalf("%s: walk error %v, decode error %v", what, werr, derr)
	}
	if derr != nil {
		return 0
	}
	if n != len(txs)+dropped {
		t.Fatalf("%s: walk counts %d transactions, decode %d + %d dropped", what, n, len(txs), dropped)
	}
	decoded := satSum(txs)
	if dropped == 0 && walked != decoded {
		t.Fatalf("%s: walked gas %d, decoded %d (no entry dropped)", what, walked, decoded)
	}
	if walked < decoded {
		t.Fatalf("%s: walked gas %d below decoded %d", what, walked, decoded)
	}
	return dropped
}

// TestGasWalkMatchesDecode: for random frames of every transaction type,
// gas limits at every edge, and malformed entries, GasWalk equals the
// decoded transactions' saturating gas sum whenever no entry is dropped,
// and is never below it; per entry (one-entry frames) a decodable entry
// walks to exactly its decoded gas. Under every decode mode.
func TestGasWalkMatchesDecode(t *testing.T) {
	key, _ := crypto.GenerateKey()
	gw, _ := crypto.GenerateKey()
	chainID := big.NewInt(94)
	signer := transaction.LatestSignerForChainID(chainID)
	chain := uint256.MustFromBig(chainID)
	for _, pool := range []bool{false, true} {
		for _, lean := range []bool{false, true} {
			t.Run(fmt.Sprintf("pool=%v/lean=%v", pool, lean), func(t *testing.T) {
				defer frame.SetTxDecodePoolForTest(pool)()
				defer transaction.SetAllocLeanForTest(lean)()
				r := rand.New(rand.NewSource(int64(7 + 2*btoi(pool) + btoi(lean))))
				var seq uint64
				enc := func(txType uint8, entries [][]byte) []byte {
					seq++
					raw, _, err := frame.Encode(gw, seq, txType, entries)
					if err != nil {
						t.Fatal(err)
					}
					return raw
				}
				droppedFrames, single := 0, 0
				var all [][]byte
				for round := 0; round < 300; round++ {
					n := 1 + r.Intn(40)
					entries := make([][]byte, 0, n)
					bad := r.Intn(3) == 0
					for i := 0; i < n; i++ {
						tx := randTx(t, r, key, signer, chain)
						e, err := transaction.EncodeEthereumTransaction(tx)
						if err != nil {
							t.Fatal(err)
						}
						if bad && r.Intn(4) == 0 {
							e = malformed(r, e)
							if len(e) == 0 {
								e = []byte{0x01}
							}
						}
						// Per entry: a one-entry frame.
						one := enc(frame.TxTypeEthereum, [][]byte{e})
						if checkWalk(t, one, fmt.Sprintf("round %d entry %d", round, i)) == 0 {
							single++
						}
						entries = append(entries, e)
					}
					raw := enc(frame.TxTypeEthereum, entries)
					if checkWalk(t, raw, fmt.Sprintf("round %d", round)) > 0 {
						droppedFrames++
					}
					all = append(all, raw)
				}
				if droppedFrames == 0 || single == 0 {
					t.Fatalf("degenerate corpus: %d frames with drops, %d decodable entries", droppedFrames, single)
				}
				// Native frames go through the full decode per entry.
				native := make([][]byte, 0, 20)
				for i := 0; i < 20; i++ {
					b, err := randTx(t, r, key, signer, chain).Marshal()
					if err != nil {
						t.Fatal(err)
					}
					native = append(native, b)
				}
				native = append(native, []byte{0x0a, 0xff})
				all = append(all, enc(frame.TxTypeNative, native))
				checkWalk(t, all[len(all)-1], "native")

				// Many frames at once, across workers, equal the per-frame sums.
				var want uint64
				wantN := 0
				for _, raw := range all {
					g, n, err := frame.GasWalk(raw)
					if err != nil {
						t.Fatal(err)
					}
					if s := want + g; s >= want {
						want = s
					} else {
						want = math.MaxUint64
					}
					wantN += n
				}
				for _, workers := range []int{0, 1, 4} {
					g, n, err := frame.GasWalkFrames(all, workers)
					if err != nil || g != want || n != wantN {
						t.Fatalf("workers %d: %d %d %v, want %d %d", workers, g, n, err, want, wantN)
					}
				}
			})
		}
	}
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

// TestGasWalkEdges: saturation at the top of uint64, the layout errors
// DecodeTxs reports (the count one short or long, an envelope with trailing
// bytes) with the same error, and the lowest bad frame's error from
// GasWalkFrames.
func TestGasWalkEdges(t *testing.T) {
	key, _ := crypto.GenerateKey()
	gw, _ := crypto.GenerateKey()
	chainID := big.NewInt(94)
	signer := transaction.LatestSignerForChainID(chainID)
	to := types.HexToAddress("0xee")
	mk := func(gas uint64) []byte {
		tx, err := transaction.SignNewTx(key, signer, &transaction.DynamicFeeTx{ChainID: uint256.MustFromBig(chainID), GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(2), Gas: gas, To: &to, Value: uint256.NewInt(1)})
		if err != nil {
			t.Fatal(err)
		}
		e, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	raw, _, err := frame.Encode(gw, 1, frame.TxTypeEthereum, [][]byte{mk(math.MaxUint64 - 5), mk(10), mk(3)})
	if err != nil {
		t.Fatal(err)
	}
	if g, n, err := frame.GasWalk(raw); err != nil || g != math.MaxUint64 || n != 3 {
		t.Fatalf("saturation: %d %d %v", g, n, err)
	}
	checkWalk(t, raw, "saturation")
	ok, _, err := frame.Encode(gw, 2, frame.TxTypeEthereum, [][]byte{mk(21000), mk(5)})
	if err != nil {
		t.Fatal(err)
	}
	if g, n, err := frame.GasWalk(ok); err != nil || g != 21005 || n != 2 {
		t.Fatalf("plain: %d %d %v", g, n, err)
	}
	for _, delta := range []int{-1, 1} {
		bad := append([]byte(nil), ok...)
		bad[30] = byte(int(bad[30]) + delta) // tx count, low byte
		if _, _, err := frame.GasWalk(bad); err != frame.ErrPayloadLayout {
			t.Fatalf("delta %d: %v", delta, err)
		}
		checkWalk(t, bad, fmt.Sprintf("delta %d", delta))
		if _, _, err := frame.GasWalkFrames([][]byte{ok, bad, append(append([]byte(nil), ok...), 0)}, 2); err != frame.ErrPayloadLayout {
			t.Fatalf("frames delta %d: %v", delta, err)
		}
	}
	trailing := append(append([]byte(nil), ok...), 0)
	checkWalk(t, trailing, "trailing")
	if _, _, err := frame.GasWalk(trailing); err == nil {
		t.Fatal("trailing bytes accepted")
	}
	if _, _, err := frame.GasWalk(nil); err == nil {
		t.Fatal("empty frame accepted")
	}
	if g, n, err := frame.GasWalkFrames(nil, 0); err != nil || g != 0 || n != 0 {
		t.Fatalf("no frames: %d %d %v", g, n, err)
	}
}

// BenchmarkGasWalkVsDecode: the walk against the decode it replaces on the
// availability path, over frames of signed transfers.
func BenchmarkGasWalkVsDecode(b *testing.B) {
	keys := frametest.Keys(b, 9, 4)
	fb := frametest.NewBuilder(keys[0], big.NewInt(94), keys[1:])
	raws, _, _ := fb.Frames(b, 20, 400)
	b.Run("walk", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, _, err := frame.GasWalkFrames(raws, 1); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("decode", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			per, _, err := frame.DecodeFramesEachWith(raws, 1, func(_ int, txs []*transaction.Transaction) { frame.HashTxs(txs) })
			if err != nil || len(per) != len(raws) {
				b.Fatal(err)
			}
		}
	})
}
