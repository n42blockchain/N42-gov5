package transaction

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// reflectDecode decodes data with the reflection decoder only.
func reflectDecode(data []byte) (*Transaction, error) {
	reflectDecodeOnly = true
	defer func() { reflectDecodeOnly = false }()
	return decodeEthereumTransaction(data)
}

// checkFastDecode requires the fast path, where it accepts, to agree with
// the reflection decoder exactly.
func checkFastDecode(t testing.TB, data []byte) (handled bool) {
	var fast *Transaction
	var ok bool
	if len(data) > 0 && data[0] >= 0xc0 {
		fast, ok = fastDecodeLegacy(data)
	} else if len(data) > 0 && data[0] == DynamicFeeTxType {
		fast, ok = fastDecodeDynamicFee(data[1:])
	}
	if !ok {
		return false
	}
	ref, err := reflectDecode(data)
	if err != nil {
		t.Fatalf("fast path accepted %x, reflection rejects: %v", data, err)
	}
	if !reflect.DeepEqual(fast.inner, ref.inner) {
		t.Fatalf("fast path differs on %x:\n fast %#v\n refl %#v", data, fast.inner, ref.inner)
	}
	return true
}

func fastDecodeSeeds(t testing.TB) [][]byte {
	key, _ := crypto.GenerateKey()
	to := types.Address{0xaa, 0xbb}
	var out [][]byte
	signerL := NewEIP155Signer(big.NewInt(94))
	signerD := NewLondonSigner(big.NewInt(94))
	for _, v := range []uint64{0, 1, 127, 128, 255, 1 << 20, 1<<64 - 1} {
		for _, data := range [][]byte{nil, {0x01}, {0x80}, make([]byte, 60)} {
			legacy := &LegacyTx{Nonce: v, GasPrice: uint256.NewInt(v), Gas: v, To: &to, Value: uint256.NewInt(v), Data: data}
			dyn := &DynamicFeeTx{ChainID: uint256.NewInt(94), Nonce: v, GasTipCap: uint256.NewInt(v), GasFeeCap: uint256.NewInt(v + 1), Gas: v, To: &to, Value: uint256.NewInt(v), Data: data}
			create := &LegacyTx{Nonce: v, GasPrice: uint256.NewInt(1), Gas: 21000, Value: uint256.NewInt(0), Data: data}
			for _, inner := range []TxData{legacy, dyn, create} {
				signer := Signer(signerL)
				if _, isDyn := inner.(*DynamicFeeTx); isDyn {
					signer = signerD
				}
				tx, err := SignNewTx(key, signer, inner)
				if err != nil {
					t.Fatal(err)
				}
				enc, err := EncodeEthereumTransaction(tx)
				if err != nil {
					t.Fatal(err)
				}
				out = append(out, enc)
			}
		}
	}
	return out
}

// TestFastDecodeMatchesReflection decodes signed legacy, EIP-1559 and
// contract-creation encodings and every single-byte mutation and truncation
// of a few of them.
func TestFastDecodeMatchesReflection(t *testing.T) {
	seeds := fastDecodeSeeds(t)
	handled := 0
	for _, enc := range seeds {
		if !checkFastDecode(t, enc) {
			t.Fatalf("fast path declined a canonical encoding %x", enc)
		}
		handled++
	}
	for _, enc := range seeds[:9] {
		for i := range enc {
			for _, d := range []byte{1, 0x7f, 0x80, 0xff} {
				m := append([]byte(nil), enc...)
				m[i] += d
				checkFastDecode(t, m)
			}
			checkFastDecode(t, enc[:i])
			checkFastDecode(t, append(append([]byte(nil), enc...), 0x00))
		}
	}
	// A non-empty access list goes to the reflection decoder.
	dyn := &DynamicFeeTx{ChainID: uint256.NewInt(94), GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(1), Value: uint256.NewInt(1),
		AccessList: AccessList{{Address: types.Address{1}}}}
	key, _ := crypto.GenerateKey()
	tx, _ := SignNewTx(key, NewLondonSigner(big.NewInt(94)), dyn)
	enc, _ := EncodeEthereumTransaction(tx)
	if _, ok := fastDecodeDynamicFee(enc[1:]); ok {
		t.Fatal("fast path took a non-empty access list")
	}
	if got, err := DecodeEthereumTransaction(enc); err != nil || got.Hash() != tx.Hash() {
		t.Fatalf("fallback decode: %v", err)
	}
	t.Logf("%d canonical encodings matched", handled)
}

func FuzzFastDecode(f *testing.F) {
	for _, s := range fastDecodeSeeds(f) {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		checkFastDecode(t, data)
	})
}

func BenchmarkDecodeLegacyTransfer(b *testing.B) {
	key, _ := crypto.GenerateKey()
	to := types.Address{0xaa}
	tx, _ := SignNewTx(key, NewEIP155Signer(big.NewInt(94)), &LegacyTx{Nonce: 77, GasPrice: uint256.NewInt(1000000008), Gas: 21000, To: &to, Value: uint256.NewInt(1)})
	enc, _ := EncodeEthereumTransaction(tx)
	for _, mode := range []struct {
		name    string
		reflect bool
	}{{"reflect", true}, {"fast", false}} {
		b.Run(mode.name, func(b *testing.B) {
			reflectDecodeOnly = mode.reflect
			defer func() { reflectDecodeOnly = false }()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if _, err := DecodeEthereumTransaction(enc); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
