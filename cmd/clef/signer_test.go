package main

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/accounts"
	"github.com/n42blockchain/N42/accounts/keystore"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

func TestClefBigFromHexutil(t *testing.T) {
	if got := bigFromHexutil(nil); got.Sign() != 0 {
		t.Fatalf("bigFromHexutil(nil) = %v, want 0", got)
	}
	v := (*hexutil.Big)(big.NewInt(42))
	if got := bigFromHexutil(v); got.Int64() != 42 {
		t.Fatalf("bigFromHexutil(42) = %v, want 42", got)
	}
}

func TestClefToUint256(t *testing.T) {
	out, err := toUint256(nil)
	if err != nil || out.Sign() != 0 {
		t.Fatalf("toUint256(nil) = %v, %v, want 0, nil", out, err)
	}
	out, err = toUint256(big.NewInt(100))
	if err != nil || out.Uint64() != 100 {
		t.Fatalf("toUint256(100) = %v, %v", out, err)
	}
	huge := new(big.Int).Lsh(big.NewInt(1), 300) // overflows uint256
	if _, err := toUint256(huge); err == nil {
		t.Fatal("expected an overflow error for a 300-bit value")
	}
}

func TestClefMustUint256OverflowReturnsZero(t *testing.T) {
	huge := new(big.Int).Lsh(big.NewInt(1), 300)
	got := mustUint256(huge)
	if got.Sign() != 0 {
		t.Fatalf("mustUint256(overflow) = %v, want 0", got)
	}
	ok := mustUint256(big.NewInt(7))
	if ok.Uint64() != 7 {
		t.Fatalf("mustUint256(7) = %v, want 7", ok)
	}
}

// clefTestKeystore creates a fresh keystore with one unlocked account in a
// temp dir, suitable for exercising SignerService without touching any real
// account material.
func clefTestKeystore(t *testing.T) (*keystore.KeyStore, accounts.Account) {
	t.Helper()
	ks := keystore.NewKeyStore(t.TempDir(), keystore.LightScryptN, keystore.LightScryptP)
	acct, err := ks.NewAccount("test-password")
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}
	if err := ks.Unlock(acct, "test-password"); err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	return ks, acct
}

func TestClefSignerServiceVersion(t *testing.T) {
	ks, _ := clefTestKeystore(t)
	svc := NewSignerService(ks, big.NewInt(94), mustPermissiveRuleEngine(t), nil)
	v, err := svc.Version(context.Background())
	if err != nil || v != clefVersion {
		t.Fatalf("Version() = %q, %v, want %q", v, err, clefVersion)
	}
}

func TestClefSignerServiceListAccounts(t *testing.T) {
	ks, acct := clefTestKeystore(t)
	svc := NewSignerService(ks, big.NewInt(94), mustPermissiveRuleEngine(t), nil)
	addrs, err := svc.ListAccounts(context.Background())
	if err != nil {
		t.Fatalf("ListAccounts error: %v", err)
	}
	if len(addrs) != 1 || addrs[0] != acct.Address {
		t.Fatalf("ListAccounts = %v, want [%v]", addrs, acct.Address)
	}
}

func TestClefSignerServiceNewAccount(t *testing.T) {
	ks, _ := clefTestKeystore(t)
	svc := NewSignerService(ks, big.NewInt(94), mustPermissiveRuleEngine(t), nil)
	addr, err := svc.NewAccount(context.Background())
	if err != nil {
		t.Fatalf("NewAccount error: %v", err)
	}
	if addr == (types.Address{}) {
		t.Fatal("expected a non-zero address from NewAccount")
	}
	addrs, _ := svc.ListAccounts(context.Background())
	if len(addrs) != 2 {
		t.Fatalf("expected 2 accounts after NewAccount, got %d", len(addrs))
	}
}

func TestClefSignerServiceSignTransactionLegacy(t *testing.T) {
	ks, acct := clefTestKeystore(t)
	svc := NewSignerService(ks, big.NewInt(94), mustPermissiveRuleEngine(t), nil)

	to := types.Address{0x09}
	args := TransactionArgs{
		From:     acct.Address,
		To:       &to,
		Gas:      21000,
		GasPrice: (*hexutil.Big)(big.NewInt(1_000_000_000)),
		Value:    (*hexutil.Big)(big.NewInt(1)),
		Nonce:    0,
	}
	signed, err := svc.SignTransaction(context.Background(), args)
	if err != nil {
		t.Fatalf("SignTransaction error: %v", err)
	}
	if len(signed.Raw) == 0 {
		t.Fatal("expected a non-empty raw transaction")
	}
}

func TestClefSignerServiceSignTransactionUnknownAccount(t *testing.T) {
	ks, _ := clefTestKeystore(t)
	svc := NewSignerService(ks, big.NewInt(94), mustPermissiveRuleEngine(t), nil)

	args := TransactionArgs{From: types.Address{0xff}}
	if _, err := svc.SignTransaction(context.Background(), args); err == nil {
		t.Fatal("expected an error signing from an unknown account")
	}
}

func TestClefSignerServiceSignTransactionRejectedByRules(t *testing.T) {
	ks, acct := clefTestKeystore(t)
	denyAll, err := NewRuleEngine("")
	if err != nil {
		t.Fatalf("NewRuleEngine: %v", err)
	}
	// Force denial by requiring a To address on a contract-creation tx.
	denyAll.config.RequireTo = true
	svc := NewSignerService(ks, big.NewInt(94), denyAll, nil)

	args := TransactionArgs{From: acct.Address} // no To -> contract creation
	if _, err := svc.SignTransaction(context.Background(), args); err == nil {
		t.Fatal("expected the transaction to be rejected by rules")
	}
}

func TestClefSignerServiceSignDynamicFeeRequiresBothFeeFields(t *testing.T) {
	ks, acct := clefTestKeystore(t)
	svc := NewSignerService(ks, big.NewInt(94), mustPermissiveRuleEngine(t), nil)

	args := TransactionArgs{
		From:         acct.Address,
		MaxFeePerGas: (*hexutil.Big)(big.NewInt(100)),
		// MaxPriorityFeePerGas intentionally omitted
	}
	if _, err := svc.SignTransaction(context.Background(), args); err == nil {
		t.Fatal("expected an error when only one dynamic-fee field is set")
	}
}

func TestClefSignerServiceSignData(t *testing.T) {
	ks, acct := clefTestKeystore(t)
	svc := NewSignerService(ks, big.NewInt(94), mustPermissiveRuleEngine(t), nil)

	sig, err := svc.SignData(context.Background(), "text/plain", acct.Address, hexutil.Bytes("hello"))
	if err != nil {
		t.Fatalf("SignData error: %v", err)
	}
	if len(sig) == 0 {
		t.Fatal("expected a non-empty signature")
	}
}

func TestClefSignerServiceSignDataUnknownAccount(t *testing.T) {
	ks, _ := clefTestKeystore(t)
	svc := NewSignerService(ks, big.NewInt(94), mustPermissiveRuleEngine(t), nil)
	if _, err := svc.SignData(context.Background(), "text/plain", types.Address{0xaa}, hexutil.Bytes("hello")); err == nil {
		t.Fatal("expected an error for an unknown account")
	}
}

func TestClefSignerServiceSignTypedData(t *testing.T) {
	ks, acct := clefTestKeystore(t)
	svc := NewSignerService(ks, big.NewInt(94), mustPermissiveRuleEngine(t), nil)

	td := TypedData{
		PrimaryType: "Mail",
		Domain:      map[string]interface{}{"name": "test", "version": "1"},
		Message:     map[string]interface{}{"contents": "hello"},
	}
	sig, err := svc.SignTypedData(context.Background(), acct.Address, td)
	if err != nil {
		t.Fatalf("SignTypedData error: %v", err)
	}
	if len(sig) == 0 {
		t.Fatal("expected a non-empty signature")
	}
}

func TestClefHashTypedDataDeterministic(t *testing.T) {
	td := TypedData{
		PrimaryType: "Mail",
		Domain:      map[string]interface{}{"name": "test"},
		Message:     map[string]interface{}{"contents": "hi"},
	}
	h1 := hashTypedData(td)
	h2 := hashTypedData(td)
	if len(h1) != 32 {
		t.Fatalf("hashTypedData length = %d, want 32", len(h1))
	}
	if string(h1) != string(h2) {
		t.Fatal("hashTypedData is not deterministic for identical input")
	}

	other := td
	other.Message = map[string]interface{}{"contents": "bye"}
	if string(hashTypedData(other)) == string(h1) {
		t.Fatal("expected different messages to hash differently")
	}
}

func mustPermissiveRuleEngine(t *testing.T) *RuleEngine {
	t.Helper()
	re, err := NewRuleEngine("")
	if err != nil {
		t.Fatalf("NewRuleEngine: %v", err)
	}
	return re
}

var _ = uint256.NewInt // keep import if later tests need it
