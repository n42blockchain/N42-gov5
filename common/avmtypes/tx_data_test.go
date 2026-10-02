package avmtypes

import (
	"math/big"
	"testing"

	"github.com/n42blockchain/N42/common/avmutil"
)

func sampleAccessList() AccessList {
	return AccessList{
		{
			Address:     avmutil.BytesToAddress([]byte{0x01}),
			StorageKeys: []avmutil.Hash{avmutil.BytesToHash([]byte{0x02}), avmutil.BytesToHash([]byte{0x03})},
		},
		{
			Address:     avmutil.BytesToAddress([]byte{0x04}),
			StorageKeys: []avmutil.Hash{avmutil.BytesToHash([]byte{0x05})},
		},
	}
}

func TestAccessListStorageKeys(t *testing.T) {
	al := sampleAccessList()
	if got := al.StorageKeys(); got != 3 {
		t.Fatalf("StorageKeys() = %d, want 3", got)
	}
	if got := AccessList(nil).StorageKeys(); got != 0 {
		t.Fatalf("StorageKeys() on nil list = %d, want 0", got)
	}
}

func TestAccessListTxAccessorsAndCopy(t *testing.T) {
	to := avmutil.BytesToAddress([]byte{0xAA})
	tx := &AccessListTx{
		ChainID:    big.NewInt(5),
		Nonce:      7,
		GasPrice:   big.NewInt(100),
		Gas:        21000,
		To:         &to,
		Value:      big.NewInt(1000),
		Data:       []byte{0x01, 0x02},
		AccessList: sampleAccessList(),
		V:          big.NewInt(1),
		R:          big.NewInt(2),
		S:          big.NewInt(3),
	}

	if tx.txType() != AccessListTxType {
		t.Errorf("txType() = %d, want %d", tx.txType(), AccessListTxType)
	}
	if tx.chainID().Cmp(big.NewInt(5)) != 0 {
		t.Errorf("chainID() = %v", tx.chainID())
	}
	if len(tx.accessList()) != 2 {
		t.Errorf("accessList() length = %d, want 2", len(tx.accessList()))
	}
	if string(tx.data()) != string([]byte{0x01, 0x02}) {
		t.Errorf("data() = %v", tx.data())
	}
	if tx.gas() != 21000 {
		t.Errorf("gas() = %d", tx.gas())
	}
	if tx.gasPrice().Cmp(big.NewInt(100)) != 0 || tx.gasTipCap().Cmp(big.NewInt(100)) != 0 || tx.gasFeeCap().Cmp(big.NewInt(100)) != 0 {
		t.Errorf("gas price accessors mismatch: price=%v tip=%v fee=%v", tx.gasPrice(), tx.gasTipCap(), tx.gasFeeCap())
	}
	if tx.value().Cmp(big.NewInt(1000)) != 0 {
		t.Errorf("value() = %v", tx.value())
	}
	if tx.nonce() != 7 {
		t.Errorf("nonce() = %d", tx.nonce())
	}
	if tx.to() == nil || *tx.to() != to {
		t.Errorf("to() = %v, want %v", tx.to(), to)
	}

	v, r, s := tx.rawSignatureValues()
	if v.Cmp(big.NewInt(1)) != 0 || r.Cmp(big.NewInt(2)) != 0 || s.Cmp(big.NewInt(3)) != 0 {
		t.Errorf("rawSignatureValues() = %v, %v, %v", v, r, s)
	}

	tx.setSignatureValues(big.NewInt(9), big.NewInt(10), big.NewInt(11), big.NewInt(12))
	if tx.ChainID.Cmp(big.NewInt(9)) != 0 || tx.V.Cmp(big.NewInt(10)) != 0 || tx.R.Cmp(big.NewInt(11)) != 0 || tx.S.Cmp(big.NewInt(12)) != 0 {
		t.Errorf("setSignatureValues did not update fields: %#v", tx)
	}

	cpy := tx.copy().(*AccessListTx)
	if cpy == tx {
		t.Fatal("copy() returned the same pointer")
	}
	if cpy.Nonce != tx.Nonce || cpy.Gas != tx.Gas {
		t.Errorf("copy() scalar fields mismatch")
	}
	if cpy.Value.Cmp(tx.Value) != 0 || cpy.ChainID.Cmp(tx.ChainID) != 0 || cpy.GasPrice.Cmp(tx.GasPrice) != 0 {
		t.Errorf("copy() big.Int fields mismatch")
	}
	if cpy.To == tx.To {
		t.Errorf("copy() To should be a distinct pointer")
	}
	if len(cpy.AccessList) != len(tx.AccessList) {
		t.Errorf("copy() AccessList length mismatch")
	}
	cpy.AccessList[0].StorageKeys[0] = avmutil.BytesToHash([]byte{0xFF})
	if tx.AccessList[0].StorageKeys[0] == cpy.AccessList[0].StorageKeys[0] {
		t.Errorf("copy() AccessList storage keys should not alias the original")
	}
}

func TestAccessListTxCopyNilBigInts(t *testing.T) {
	tx := &AccessListTx{Nonce: 1}
	cpy := tx.copy().(*AccessListTx)
	if cpy.Value.Sign() != 0 || cpy.ChainID.Sign() != 0 || cpy.GasPrice.Sign() != 0 {
		t.Errorf("expected zero-valued big.Ints when source fields are nil")
	}
}

func TestCopyAddressPtr(t *testing.T) {
	if copyAddressPtr(nil) != nil {
		t.Fatal("copyAddressPtr(nil) should return nil")
	}
	addr := avmutil.BytesToAddress([]byte{0x01})
	cpy := copyAddressPtr(&addr)
	if cpy == &addr {
		t.Fatal("copyAddressPtr should return a distinct pointer")
	}
	if *cpy != addr {
		t.Fatal("copyAddressPtr should preserve the value")
	}
}

func TestDynamicFeeTxAccessorsAndCopy(t *testing.T) {
	to := avmutil.BytesToAddress([]byte{0xBB})
	tx := &DynamicFeeTx{
		ChainID:    big.NewInt(5),
		Nonce:      3,
		GasTipCap:  big.NewInt(2),
		GasFeeCap:  big.NewInt(20),
		Gas:        50000,
		To:         &to,
		Value:      big.NewInt(77),
		Data:       []byte{0x09},
		AccessList: sampleAccessList(),
		V:          big.NewInt(1),
		R:          big.NewInt(2),
		S:          big.NewInt(3),
	}

	if tx.txType() != DynamicFeeTxType {
		t.Errorf("txType() = %d", tx.txType())
	}
	if tx.chainID().Cmp(big.NewInt(5)) != 0 {
		t.Errorf("chainID() = %v", tx.chainID())
	}
	if len(tx.accessList()) != 2 {
		t.Errorf("accessList() length mismatch")
	}
	if string(tx.data()) != string([]byte{0x09}) {
		t.Errorf("data() mismatch")
	}
	if tx.gas() != 50000 {
		t.Errorf("gas() mismatch")
	}
	if tx.gasFeeCap().Cmp(big.NewInt(20)) != 0 || tx.gasTipCap().Cmp(big.NewInt(2)) != 0 || tx.gasPrice().Cmp(big.NewInt(20)) != 0 {
		t.Errorf("fee accessors mismatch: feeCap=%v tipCap=%v price=%v", tx.gasFeeCap(), tx.gasTipCap(), tx.gasPrice())
	}
	if tx.value().Cmp(big.NewInt(77)) != 0 {
		t.Errorf("value() mismatch")
	}
	if tx.nonce() != 3 {
		t.Errorf("nonce() mismatch")
	}
	if tx.to() == nil || *tx.to() != to {
		t.Errorf("to() mismatch")
	}

	v, r, s := tx.rawSignatureValues()
	if v.Sign() == 0 || r.Sign() == 0 || s.Sign() == 0 {
		t.Errorf("rawSignatureValues unexpected zero values")
	}
	tx.setSignatureValues(big.NewInt(1), big.NewInt(2), big.NewInt(3), big.NewInt(4))
	if tx.ChainID.Cmp(big.NewInt(1)) != 0 {
		t.Errorf("setSignatureValues did not set chainID")
	}

	cpy := tx.copy().(*DynamicFeeTx)
	if cpy == tx {
		t.Fatal("copy() returned the same pointer")
	}
	if cpy.GasTipCap.Cmp(tx.GasTipCap) != 0 || cpy.GasFeeCap.Cmp(tx.GasFeeCap) != 0 {
		t.Errorf("copy() fee fields mismatch")
	}
}

func TestDynamicFeeTxCopyNilBigInts(t *testing.T) {
	tx := &DynamicFeeTx{Nonce: 1}
	cpy := tx.copy().(*DynamicFeeTx)
	if cpy.Value.Sign() != 0 || cpy.GasTipCap.Sign() != 0 || cpy.GasFeeCap.Sign() != 0 || cpy.ChainID.Sign() != 0 {
		t.Errorf("expected zero-valued big.Ints when source fields are nil")
	}
}

func TestLegacyTxAccessorsAndCopy(t *testing.T) {
	to := avmutil.BytesToAddress([]byte{0xCC})
	tx := &LegacyTx{
		Nonce:    4,
		GasPrice: big.NewInt(42),
		Gas:      30000,
		To:       &to,
		Value:    big.NewInt(500),
		Data:     []byte{0x07},
		V:        big.NewInt(27),
		R:        big.NewInt(1),
		S:        big.NewInt(2),
	}

	if tx.txType() != LegacyTxType {
		t.Errorf("txType() = %d", tx.txType())
	}
	if tx.accessList() != nil {
		t.Errorf("accessList() should be nil for LegacyTx")
	}
	if string(tx.data()) != string([]byte{0x07}) {
		t.Errorf("data() mismatch")
	}
	if tx.gas() != 30000 {
		t.Errorf("gas() mismatch")
	}
	if tx.gasPrice().Cmp(big.NewInt(42)) != 0 || tx.gasTipCap().Cmp(big.NewInt(42)) != 0 || tx.gasFeeCap().Cmp(big.NewInt(42)) != 0 {
		t.Errorf("gas price accessors mismatch")
	}
	if tx.value().Cmp(big.NewInt(500)) != 0 {
		t.Errorf("value() mismatch")
	}
	if tx.nonce() != 4 {
		t.Errorf("nonce() mismatch")
	}
	if tx.to() == nil || *tx.to() != to {
		t.Errorf("to() mismatch")
	}

	v, r, s := tx.rawSignatureValues()
	if v.Cmp(big.NewInt(27)) != 0 || r.Cmp(big.NewInt(1)) != 0 || s.Cmp(big.NewInt(2)) != 0 {
		t.Errorf("rawSignatureValues mismatch")
	}
	tx.setSignatureValues(nil, big.NewInt(100), big.NewInt(101), big.NewInt(102))
	if tx.V.Cmp(big.NewInt(100)) != 0 || tx.R.Cmp(big.NewInt(101)) != 0 || tx.S.Cmp(big.NewInt(102)) != 0 {
		t.Errorf("setSignatureValues did not update V/R/S")
	}

	cpy := tx.copy().(*LegacyTx)
	if cpy == tx {
		t.Fatal("copy() returned the same pointer")
	}
	if cpy.GasPrice.Cmp(tx.GasPrice) != 0 || cpy.Value.Cmp(tx.Value) != 0 {
		t.Errorf("copy() big.Int fields mismatch")
	}
	if cpy.To == tx.To {
		t.Errorf("copy() To should not alias original pointer")
	}
}

func TestLegacyTxCopyNilBigInts(t *testing.T) {
	tx := &LegacyTx{Nonce: 1}
	cpy := tx.copy().(*LegacyTx)
	if cpy.Value.Sign() != 0 || cpy.GasPrice.Sign() != 0 {
		t.Errorf("expected zero-valued big.Ints when source fields are nil")
	}
}

func TestNewTransactionAndNewContractCreation(t *testing.T) {
	to := avmutil.BytesToAddress([]byte{0x01})
	tx := NewTransaction(1, to, big.NewInt(10), 21000, big.NewInt(1), nil)
	if tx == nil {
		t.Fatal("NewTransaction returned nil")
	}

	creation := NewContractCreation(1, big.NewInt(0), 100000, big.NewInt(1), []byte{0x60, 0x60})
	if creation == nil {
		t.Fatal("NewContractCreation returned nil")
	}
}
