package avmtypes

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/rlp"
	nttypes "github.com/n42blockchain/N42/common/types"
)

func newLegacyTestTx() *Transaction {
	to := avmutil.BytesToAddress([]byte{0x01})
	return NewTx(&LegacyTx{
		Nonce:    1,
		GasPrice: big.NewInt(10),
		Gas:      21000,
		To:       &to,
		Value:    big.NewInt(100),
		Data:     []byte{0xAB},
		V:        big.NewInt(27),
		R:        big.NewInt(1),
		S:        big.NewInt(2),
	})
}

func newDynamicFeeTestTx() *Transaction {
	to := avmutil.BytesToAddress([]byte{0x02})
	return NewTx(&DynamicFeeTx{
		ChainID:   big.NewInt(1),
		Nonce:     2,
		GasTipCap: big.NewInt(1),
		GasFeeCap: big.NewInt(10),
		Gas:       50000,
		To:        &to,
		Value:     big.NewInt(200),
		Data:      []byte{0xCD},
		V:         big.NewInt(0),
		R:         big.NewInt(1),
		S:         big.NewInt(2),
	})
}

func TestTransactionBasicAccessors(t *testing.T) {
	tx := newLegacyTestTx()
	if tx.Type() != LegacyTxType {
		t.Errorf("Type() = %d, want %d", tx.Type(), LegacyTxType)
	}
	if tx.Nonce() != 1 {
		t.Errorf("Nonce() = %d", tx.Nonce())
	}
	if tx.Gas() != 21000 {
		t.Errorf("Gas() = %d", tx.Gas())
	}
	if tx.GasPrice().Cmp(big.NewInt(10)) != 0 {
		t.Errorf("GasPrice() = %v", tx.GasPrice())
	}
	if tx.GasTipCap().Cmp(big.NewInt(10)) != 0 {
		t.Errorf("GasTipCap() = %v", tx.GasTipCap())
	}
	if tx.GasFeeCap().Cmp(big.NewInt(10)) != 0 {
		t.Errorf("GasFeeCap() = %v", tx.GasFeeCap())
	}
	if tx.Value().Cmp(big.NewInt(100)) != 0 {
		t.Errorf("Value() = %v", tx.Value())
	}
	if string(tx.Data()) != string([]byte{0xAB}) {
		t.Errorf("Data() = %v", tx.Data())
	}
	if tx.AccessList() != nil {
		t.Errorf("AccessList() = %v, want nil for legacy tx", tx.AccessList())
	}
	if tx.To() == nil {
		t.Fatal("To() = nil")
	}
	v, r, s := tx.RawSignatureValues()
	if v.Cmp(big.NewInt(27)) != 0 || r.Cmp(big.NewInt(1)) != 0 || s.Cmp(big.NewInt(2)) != 0 {
		t.Errorf("RawSignatureValues() = %v, %v, %v", v, r, s)
	}

	// GasPrice/Value should return defensive copies.
	gp := tx.GasPrice()
	gp.SetInt64(999)
	if tx.GasPrice().Cmp(big.NewInt(10)) != 0 {
		t.Errorf("GasPrice() leaked internal big.Int: mutated external copy affected internal state")
	}
}

func TestTransactionToReturnsCopy(t *testing.T) {
	tx := newLegacyTestTx()
	to1 := tx.To()
	to2 := tx.To()
	if to1 == to2 {
		t.Fatal("To() should return a fresh pointer each call")
	}
	if *to1 != *to2 {
		t.Fatal("To() pointer values should be equal")
	}
}

func TestTransactionHashIsCachedAndTypeDependent(t *testing.T) {
	legacy := newLegacyTestTx()
	h1 := legacy.Hash()
	h2 := legacy.Hash()
	if h1 != h2 {
		t.Fatal("Hash() should be cached and stable across calls")
	}

	dyn := newDynamicFeeTestTx()
	dynHash := dyn.Hash()
	if dynHash == h1 {
		t.Fatal("different transactions should not share a hash")
	}
}

func TestTransactionProtected(t *testing.T) {
	// EIP-155 protected V values (not 27/28/0/1).
	protected := NewTx(&LegacyTx{Nonce: 1, GasPrice: big.NewInt(1), Gas: 1, V: big.NewInt(37), R: big.NewInt(1), S: big.NewInt(1)})
	if !protected.Protected() {
		t.Error("expected protected() == true for V=37")
	}

	unprotected := NewTx(&LegacyTx{Nonce: 1, GasPrice: big.NewInt(1), Gas: 1, V: big.NewInt(27), R: big.NewInt(1), S: big.NewInt(1)})
	if unprotected.Protected() {
		t.Error("expected protected() == false for V=27")
	}

	nilV := NewTx(&LegacyTx{Nonce: 1, GasPrice: big.NewInt(1), Gas: 1})
	if nilV.Protected() {
		t.Error("expected protected() == false when V is nil")
	}

	dyn := newDynamicFeeTestTx()
	if !dyn.Protected() {
		t.Error("non-legacy transactions should always report Protected() == true")
	}
}

func TestIsProtectedV(t *testing.T) {
	cases := map[int64]bool{27: false, 28: false, 0: false, 1: false, 37: true, 38: true}
	for v, want := range cases {
		if got := isProtectedV(big.NewInt(v)); got != want {
			t.Errorf("isProtectedV(%d) = %v, want %v", v, got, want)
		}
	}
	// A V value with more than 8 bits is always protected.
	big256 := new(big.Int).Lsh(big.NewInt(1), 9)
	if !isProtectedV(big256) {
		t.Error("isProtectedV() should be true for V with BitLen > 8")
	}
}

func TestTransactionSize(t *testing.T) {
	tx := newLegacyTestTx()
	size1 := tx.Size()
	if size1 == 0 {
		t.Fatal("Size() should be non-zero")
	}
	size2 := tx.Size()
	if size1 != size2 {
		t.Fatal("Size() should be cached and stable")
	}
}

func TestTransactionUnmarshalBinaryRoundTrip(t *testing.T) {
	legacy := newLegacyTestTx()
	encoded, err := rlp.EncodeToBytes(legacy.inner)
	if err != nil {
		t.Fatalf("EncodeToBytes error: %v", err)
	}
	got := new(Transaction)
	if err := got.UnmarshalBinary(encoded); err != nil {
		t.Fatalf("UnmarshalBinary error: %v", err)
	}
	if got.Type() != LegacyTxType || got.Nonce() != legacy.Nonce() {
		t.Errorf("round trip mismatch: got %#v", got.inner)
	}

	dyn := newDynamicFeeTestTx()
	innerEncoded, err := rlp.EncodeToBytes(dyn.inner)
	if err != nil {
		t.Fatalf("EncodeToBytes error: %v", err)
	}
	typedEncoded := append([]byte{DynamicFeeTxType}, innerEncoded...)
	got2 := new(Transaction)
	if err := got2.UnmarshalBinary(typedEncoded); err != nil {
		t.Fatalf("UnmarshalBinary (typed) error: %v", err)
	}
	if got2.Type() != DynamicFeeTxType || got2.Nonce() != dyn.Nonce() {
		t.Errorf("typed round trip mismatch: got %#v", got2.inner)
	}
}

func TestTransactionDecodeTypedErrors(t *testing.T) {
	tx := new(Transaction)
	if _, err := tx.decodeTyped([]byte{}); err == nil {
		t.Error("expected error for empty input")
	}
	if _, err := tx.decodeTyped([]byte{0x01}); err == nil {
		t.Error("expected error for single-byte input (too short)")
	}
	if _, err := tx.decodeTyped([]byte{0xFF, 0x00}); err == nil {
		t.Error("expected error for unknown tx type byte")
	}
}

func TestTransactionUnmarshalBinaryTypedTooShort(t *testing.T) {
	tx := new(Transaction)
	err := tx.UnmarshalBinary([]byte{0x01})
	if err == nil {
		t.Error("expected error decoding a too-short typed transaction")
	}
}

func TestToastAndFromastAddress(t *testing.T) {
	if ToastAddress(nil) != nil {
		t.Error("ToastAddress(nil) should be nil")
	}
	if FromastAddress(nil) != nil {
		t.Error("FromastAddress(nil) should be nil")
	}

	var zero avmutil.Address
	got := ToastAddress(&zero)
	if got == nil || *got != (nttypes.Address{0}) {
		t.Errorf("ToastAddress(zero) = %v", got)
	}

	addr := avmutil.BytesToAddress([]byte{0x01, 0x02})
	toasted := ToastAddress(&addr)
	back := FromastAddress(toasted)
	if back == nil || *back != addr {
		t.Errorf("round trip mismatch: got %v, want %v", back, addr)
	}
}

func TestToastAndFromastHash(t *testing.T) {
	h := avmutil.BytesToHash([]byte{0x01, 0x02, 0x03})
	toasted := ToastHash(h)
	back := FromastHash(toasted)
	if back != h {
		t.Errorf("round trip mismatch: got %v, want %v", back, h)
	}
}

func TestToastAndFromastAccessList(t *testing.T) {
	al := sampleAccessList()
	toasted := ToastAccessList(al)
	if len(toasted) != len(al) {
		t.Fatalf("ToastAccessList length = %d, want %d", len(toasted), len(al))
	}
	back := FromastAccessList(toasted)
	if len(back) != len(al) {
		t.Fatalf("FromastAccessList length = %d, want %d", len(back), len(al))
	}
	for i := range al {
		if back[i].Address != al[i].Address {
			t.Errorf("address mismatch at %d: got %v, want %v", i, back[i].Address, al[i].Address)
		}
		if len(back[i].StorageKeys) != len(al[i].StorageKeys) {
			t.Errorf("storage keys length mismatch at %d", i)
		}
	}

	// empty input
	if got := ToastAccessList(nil); len(got) != 0 {
		t.Errorf("ToastAccessList(nil) = %v, want empty", got)
	}
	if got := FromastAccessList(nil); len(got) != 0 {
		t.Errorf("FromastAccessList(nil) = %v, want empty", got)
	}
}

func TestToastAndFromastLog(t *testing.T) {
	if ToastLog(nil) != nil {
		t.Error("ToastLog(nil) should be nil")
	}
	if FromastLog(nil) != nil {
		t.Error("FromastLog(nil) should be nil")
	}

	l := sampleLog()
	toasted := ToastLog(&l)
	if toasted == nil {
		t.Fatal("ToastLog returned nil for non-nil input")
	}
	if toasted.Address != *ToastAddress(&l.Address) {
		t.Errorf("Address mismatch")
	}
	if len(toasted.Topics) != len(l.Topics) {
		t.Errorf("Topics length mismatch")
	}
	if toasted.BlockNumber.Uint64() != l.BlockNumber {
		t.Errorf("BlockNumber mismatch: got %d, want %d", toasted.BlockNumber.Uint64(), l.BlockNumber)
	}

	back := FromastLog(toasted)
	if back == nil {
		t.Fatal("FromastLog returned nil")
	}
	if back.BlockNumber != l.BlockNumber || back.TxIndex != l.TxIndex || back.Index != l.Index || back.Removed != l.Removed {
		t.Errorf("round trip mismatch: got %#v, want %#v", back, l)
	}
}

func TestToastAndFromastLogs(t *testing.T) {
	l1 := sampleLog()
	l2 := sampleLog()
	l2.Index = 9
	logs := []*Log{&l1, &l2}

	toasted := ToastLogs(logs)
	if len(toasted) != 2 {
		t.Fatalf("ToastLogs length = %d, want 2", len(toasted))
	}

	back := FromastLogs(toasted)
	if len(back) != 2 {
		t.Fatalf("FromastLogs length = %d, want 2", len(back))
	}
	if back[1].Index != 9 {
		t.Errorf("FromastLogs did not preserve Index: got %d", back[1].Index)
	}

	if len(ToastLogs(nil)) != 0 {
		t.Error("ToastLogs(nil) should be empty")
	}
	if len(FromastLogs(nil)) != 0 {
		t.Error("FromastLogs(nil) should be empty")
	}
}

func TestWriteCounter(t *testing.T) {
	var c writeCounter
	n, err := c.Write([]byte{1, 2, 3})
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != 3 {
		t.Errorf("Write returned n=%d, want 3", n)
	}
	if c != 3 {
		t.Errorf("writeCounter = %v, want 3", c)
	}
}

func TestFromN42HeaderNonBlockHeader(t *testing.T) {
	// FromN42Header should degrade gracefully for a non-*block.Header
	// implementation of IHeader, or a nil *block.Header.
	var nilHeader *block.Header
	got := FromN42Header(nilHeader)
	if got == nil {
		t.Fatal("FromN42Header(nil typed pointer) returned nil")
	}
}
