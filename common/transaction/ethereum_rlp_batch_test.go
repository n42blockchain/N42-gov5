package transaction

import (
	"bytes"
	"math/big"
	"reflect"
	"sync"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// batchSeeds adds encodings the fast path declines (access-list types, a
// non-empty access list) to the fast-path seeds.
func batchSeeds(t testing.TB) [][]byte {
	out := fastDecodeSeeds(t)
	key, _ := crypto.GenerateKey()
	to := types.Address{0xcc}
	for _, inner := range []TxData{
		&AccessListTx{ChainID: uint256.NewInt(94), Nonce: 3, GasPrice: uint256.NewInt(2), Gas: 21000, To: &to, Value: uint256.NewInt(5)},
		&DynamicFeeTx{ChainID: uint256.NewInt(94), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(1), Gas: 30000, To: &to,
			Value: uint256.NewInt(1), AccessList: AccessList{{Address: types.Address{1}}}},
	} {
		tx, err := SignNewTx(key, NewLondonSigner(big.NewInt(94)), inner)
		if err != nil {
			t.Fatal(err)
		}
		enc, err := EncodeEthereumTransaction(tx)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, enc)
	}
	return out
}

func sameDecoded(t *testing.T, enc []byte, got, want *Transaction) {
	t.Helper()
	if !reflect.DeepEqual(got.inner, want.inner) {
		t.Fatalf("batch decode differs on %x:\n got  %#v\n want %#v", enc, got.inner, want.inner)
	}
	if got.Hash() != want.Hash() {
		t.Fatalf("hash differs on %x", enc)
	}
	ge, _ := got.EthEncoded()
	we, _ := want.EthEncoded()
	if !bytes.Equal(ge, we) || !bytes.Equal(ge, enc) {
		t.Fatalf("encoding differs on %x", enc)
	}
	gs, _ := got.EncodedSize()
	if gs != len(enc) {
		t.Fatalf("size %d, want %d", gs, len(enc))
	}
}

// TestEthBatchDecoderMatchesDecode: a batch decoder yields exactly what
// DecodeEthereumTransaction yields, accepts and rejects the same inputs,
// survives more transactions than it was sized for, and a Reset decoder
// decodes as a fresh one.
func TestEthBatchDecoderMatchesDecode(t *testing.T) {
	seeds := batchSeeds(t)
	for _, size := range []int{len(seeds), 1, 0} {
		d := NewEthBatchDecoder(size)
		for round := 0; round < 2; round++ {
			var held []*Transaction
			for _, enc := range seeds {
				want, err := DecodeEthereumTransaction(enc)
				if err != nil {
					t.Fatal(err)
				}
				got, err := d.DecodeAliased(enc)
				if err != nil {
					t.Fatalf("batch rejected %x: %v", enc, err)
				}
				sameDecoded(t, enc, got, want)
				held = append(held, got)
			}
			// Every transaction still holds its own values after the
			// later ones were carved from the same slab.
			for i, tx := range held {
				want, _ := DecodeEthereumTransaction(seeds[i])
				sameDecoded(t, seeds[i], tx, want)
			}
			d.Reset(size)
		}
	}
	// Rejections match, and a declined fast decode leaves no residue in the
	// next entry.
	d := NewEthBatchDecoder(4)
	for _, enc := range seeds[:6] {
		for i := range enc {
			m := append([]byte(nil), enc...)
			m[i] ^= 0x41
			_, werr := DecodeEthereumTransaction(m)
			got, gerr := d.DecodeAliased(m)
			if (werr == nil) != (gerr == nil) {
				t.Fatalf("accept differs on %x: %v vs %v", m, gerr, werr)
			}
			if gerr == nil {
				want, _ := DecodeEthereumTransaction(m)
				sameDecoded(t, m, got, want)
			}
		}
		got, err := d.DecodeAliased(enc)
		if err != nil {
			t.Fatal(err)
		}
		want, _ := DecodeEthereumTransaction(enc)
		sameDecoded(t, enc, got, want)
	}
}

// TestHashConcurrentOnce: concurrent first calls agree with a sequential
// hash (run under -race for the publication).
func TestHashConcurrentOnce(t *testing.T) {
	seeds := batchSeeds(t)
	for _, enc := range seeds {
		ref, _ := DecodeEthereumTransaction(enc)
		want := ref.Hash()
		tx, _ := DecodeEthereumTransaction(enc)
		var wg sync.WaitGroup
		got := make([]types.Hash, 16)
		for i := range got {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				got[i] = tx.Hash()
			}(i)
		}
		wg.Wait()
		for _, h := range got {
			if h != want {
				t.Fatalf("concurrent hash %s, want %s", h, want)
			}
		}
	}
}

// TestHashStuckClaim: a claimed computation that never publishes (its
// claimant panicked) does not stop others from getting the hash.
func TestHashStuckClaim(t *testing.T) {
	enc := batchSeeds(t)[0]
	ref, _ := DecodeEthereumTransaction(enc)
	tx, _ := DecodeEthereumTransaction(enc)
	tx.hashState.Store(hashWriting)
	if tx.Hash() != ref.Hash() {
		t.Fatal("hash under a stuck claim differs")
	}
}

// TestSenderMemoSlot: the first memo lives in the transaction's slot, a
// second signer's memo replaces it without disturbing the first's readers,
// and Sender answers under each.
func TestSenderMemoSlot(t *testing.T) {
	key, _ := crypto.GenerateKey()
	to := types.Address{0xdd}
	s94 := NewLondonSigner(big.NewInt(94))
	tx, err := SignNewTx(key, s94, &DynamicFeeTx{ChainID: uint256.NewInt(94), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(1),
		Gas: 21000, To: &to, Value: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	want := crypto.PubkeyToAddress(key.PublicKey)
	a := types.Address{0x01}
	tx.SetSenderMemo(s94, a)
	first := tx.from.Load()
	if first != &tx.fromSlot || !tx.HasSenderMemo(s94) {
		t.Fatal("first memo not in the slot")
	}
	if got, _ := Sender(s94, tx); got != a {
		t.Fatalf("memo %s, want %s", got, a)
	}
	other := NewLondonSigner(big.NewInt(95))
	b := types.Address{0x02}
	tx.SetSenderMemo(other, b)
	if tx.from.Load() == &tx.fromSlot || first.from != a || !tx.HasSenderMemo(other) || tx.HasSenderMemo(s94) {
		t.Fatal("second memo overwrote the slot")
	}
	fresh, _ := DecodeEthereumTransaction(mustEnc(t, tx))
	if got, err := Sender(s94, fresh); err != nil || got != want {
		t.Fatalf("recovered %s (%v), want %s", got, err, want)
	}
}

func mustEnc(t *testing.T, tx *Transaction) []byte {
	t.Helper()
	enc, err := EncodeEthereumTransaction(tx)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}

func BenchmarkDecodeTransferBatch(b *testing.B) {
	key, _ := crypto.GenerateKey()
	to := types.Address{0xaa}
	signer := NewLondonSigner(big.NewInt(94))
	encs := make([][]byte, 500)
	for i := range encs {
		tx, _ := SignNewTx(key, signer, &DynamicFeeTx{ChainID: uint256.NewInt(94), Nonce: uint64(i), GasTipCap: uint256.NewInt(1),
			GasFeeCap: uint256.NewInt(1e10), Gas: 21000, To: &to, Value: uint256.NewInt(1)})
		encs[i], _ = EncodeEthereumTransaction(tx)
	}
	b.Run("single", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, e := range encs {
				tx, _ := DecodeEthereumTransaction(e)
				tx.Hash()
			}
		}
	})
	b.Run("batch", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			d := NewEthBatchDecoder(len(encs))
			for _, e := range encs {
				tx, _ := d.DecodeAliased(e)
				tx.Hash()
			}
		}
	})
}
