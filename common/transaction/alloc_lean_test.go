package transaction

import (
	"bytes"
	"math/big"
	"reflect"
	"testing"
	"unsafe"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// leanTransferBatch is a frame-like batch of signed legacy transfers: one
// gas price, values repeating in runs, EIP-155 and pre-EIP-155 V, a few
// contract creations and calls with data.
func leanTransferBatch(t testing.TB, n int) [][]byte {
	key, _ := crypto.GenerateKey()
	s155 := NewEIP155Signer(big.NewInt(94))
	home := HomesteadSigner{}
	out := make([][]byte, n)
	for i := range out {
		to := types.Address{byte(i), byte(i >> 8), 0xee}
		inner := &LegacyTx{Nonce: uint64(i), GasPrice: uint256.NewInt(1e9), Gas: 21000, To: &to, Value: uint256.NewInt(uint64(1 + i/7))}
		switch i % 11 {
		case 3:
			inner.To = nil
			inner.Data = []byte{0x60, 0x00, byte(i)}
		case 5:
			inner.Data = make([]byte, 40+i%9)
		case 8:
			inner.GasPrice = uint256.NewInt(uint64(2e9 + i))
		}
		signer := Signer(s155)
		if i%13 == 4 {
			signer = home
		}
		tx, err := SignNewTx(key, signer, inner)
		if err != nil {
			t.Fatal(err)
		}
		if out[i], err = EncodeEthereumTransaction(tx); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func decodeBatchWith(t *testing.T, lean bool, d *EthBatchDecoder, encs [][]byte) []*Transaction {
	t.Helper()
	defer SetAllocLeanForTest(lean)()
	d.Reset(len(encs))
	out := make([]*Transaction, len(encs))
	for i, e := range encs {
		tx, err := d.DecodeAliased(e)
		if err != nil {
			t.Fatalf("lean=%v rejected %x: %v", lean, e, err)
		}
		out[i] = tx
	}
	return out
}

// sameTx compares every observable of two decodes of enc.
func sameTx(t *testing.T, enc []byte, got, want *Transaction) {
	t.Helper()
	sameDecoded(t, enc, got, want)
	if got.Type() != want.Type() || got.Nonce() != want.Nonce() || got.Gas() != want.Gas() ||
		got.GasPrice().Cmp(want.GasPrice()) != 0 || got.Value().Cmp(want.Value()) != 0 ||
		!bytes.Equal(got.Data(), want.Data()) || !reflect.DeepEqual(got.To(), want.To()) {
		t.Fatalf("fields differ on %x", enc)
	}
	gv, gr, gs := got.RawSignatureValues()
	wv, wr, ws := want.RawSignatureValues()
	if gv.Cmp(wv) != 0 || gr.Cmp(wr) != 0 || gs.Cmp(ws) != 0 {
		t.Fatalf("signature differs on %x", enc)
	}
	for _, s := range []Signer{NewEIP155Signer(big.NewInt(94)), HomesteadSigner{}} {
		ga, gerr := Sender(s, got)
		wa, werr := Sender(s, want)
		if ga != wa || (gerr == nil) != (werr == nil) {
			t.Fatalf("sender differs on %x: %s/%v vs %s/%v", enc, ga, gerr, wa, werr)
		}
	}
}

// TestAllocLeanDecodeDifferential: N42_ALLOC_LEAN decodes a batch into
// transactions equal, field by field and in hash, encoding and sender, to
// the default batch decode and to DecodeEthereumTransaction; they keep
// their values after the rest of the batch (and a second batch on another
// decoder) is decoded, an undersized decoder grows correctly, and a Reset
// decoder decodes as a fresh one.
func TestAllocLeanDecodeDifferential(t *testing.T) {
	encs := append(leanTransferBatch(t, 300), batchSeeds(t)...)
	for _, size := range []int{len(encs), 7, 0} {
		dOff, dOn := NewEthBatchDecoder(size), NewEthBatchDecoder(size)
		for round := 0; round < 2; round++ {
			off := decodeBatchWith(t, false, dOff, encs)
			on := decodeBatchWith(t, true, dOn, encs)
			for i, e := range encs {
				want, err := DecodeEthereumTransaction(e)
				if err != nil {
					t.Fatal(err)
				}
				sameTx(t, e, on[i], off[i])
				sameTx(t, e, on[i], want)
			}
		}
	}
	// The lean path is taken: a recipient aliases the encoding, equal gas
	// prices share one slot and V mostly does.
	d := NewEthBatchDecoder(len(encs))
	on := decodeBatchWith(t, true, d, encs)
	l0, l1 := on[0].inner.(*LegacyTx), on[1].inner.(*LegacyTx)
	if l0.GasPrice != l1.GasPrice {
		t.Fatal("equal gas prices not shared under N42_ALLOC_LEAN")
	}
	vs := make(map[*uint256.Int]bool)
	for _, tx := range on[:300] {
		vs[tx.inner.(*LegacyTx).V] = true
	}
	if len(vs) > 100 {
		t.Fatalf("%d distinct V slots for 300 transactions", len(vs))
	}
	p := uintptr(unsafe.Pointer(l0.To))
	b := uintptr(unsafe.Pointer(&encs[0][0]))
	if p < b || p >= b+uintptr(len(encs[0])) {
		t.Fatal("recipient does not alias the encoding under N42_ALLOC_LEAN")
	}
}

// TestAllocLeanDecodeRejections: under N42_ALLOC_LEAN every single-byte
// mutation of a few legacy encodings is accepted or rejected as
// DecodeEthereumTransaction does, and a declined fast decode leaves no
// residue (slab slots, shared integers) for the next transaction.
func TestAllocLeanDecodeRejections(t *testing.T) {
	defer SetAllocLeanForTest(true)()
	encs := leanTransferBatch(t, 12)
	d := NewEthBatchDecoder(4)
	var held []*Transaction
	var heldEnc [][]byte
	for _, enc := range encs {
		for i := range enc {
			for _, x := range []byte{0x41, 0x80, 0xff} {
				m := append([]byte(nil), enc...)
				m[i] ^= x
				_, werr := DecodeEthereumTransaction(m)
				got, gerr := d.DecodeAliased(m)
				if (werr == nil) != (gerr == nil) {
					t.Fatalf("accept differs on %x: %v vs %v", m, gerr, werr)
				}
				if gerr == nil {
					want, _ := DecodeEthereumTransaction(m)
					sameDecoded(t, m, got, want)
					held, heldEnc = append(held, got), append(heldEnc, m)
				}
			}
			got, err := d.DecodeAliased(enc)
			if err != nil {
				t.Fatal(err)
			}
			held, heldEnc = append(held, got), append(heldEnc, enc)
		}
	}
	for i, tx := range held {
		want, _ := DecodeEthereumTransaction(heldEnc[i])
		sameDecoded(t, heldEnc[i], tx, want)
	}
}

func BenchmarkDecodeLegacyTransferBatch(b *testing.B) {
	key, _ := crypto.GenerateKey()
	signer := NewEIP155Signer(big.NewInt(94))
	encs := make([][]byte, 500)
	for i := range encs {
		to := types.Address{byte(i), byte(i >> 8), 0xaa}
		tx, _ := SignNewTx(key, signer, &LegacyTx{Nonce: uint64(i), GasPrice: uint256.NewInt(1e9), Gas: 21000, To: &to, Value: uint256.NewInt(1)})
		encs[i], _ = EncodeEthereumTransaction(tx)
	}
	for _, hash := range []bool{false, true} {
		for _, lean := range []bool{false, true} {
			name := "default"
			if lean {
				name = "lean"
			}
			if hash {
				name += "+hash"
			}
			b.Run(name, func(b *testing.B) {
				defer SetAllocLeanForTest(lean)()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					d := NewEthBatchDecoder(len(encs))
					for _, e := range encs {
						tx, _ := d.DecodeAliased(e)
						if hash {
							tx.Hash()
						}
					}
				}
			})
		}
	}
}
