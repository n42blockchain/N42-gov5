package main

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
)

func TestClefNewRuleEnginePermissiveWhenEmpty(t *testing.T) {
	re, err := NewRuleEngine("")
	if err != nil {
		t.Fatalf("NewRuleEngine(\"\") error: %v", err)
	}
	ok, reason := re.ApproveTransaction(&TransactionArgs{})
	if !ok {
		t.Fatalf("expected permissive engine to approve, got denied: %s", reason)
	}
}

func TestClefNewRuleEngineMissingFile(t *testing.T) {
	if _, err := NewRuleEngine("/no/such/rules.json"); err == nil {
		t.Fatal("expected an error for a missing rules file")
	}
}

func TestClefNewRuleEngineInvalidJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write rules file: %v", err)
	}
	if _, err := NewRuleEngine(path); err == nil {
		t.Fatal("expected an error for malformed JSON")
	}
}

func TestClefNewRuleEngineInvalidAllowAddress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"allow_addresses":["not-an-address"]}`), 0o600); err != nil {
		t.Fatalf("write rules file: %v", err)
	}
	if _, err := NewRuleEngine(path); err == nil {
		t.Fatal("expected an error for an invalid allow_address")
	}
}

func TestClefNewRuleEngineInvalidMaxValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"max_value_wei":"not-a-number"}`), 0o600); err != nil {
		t.Fatalf("write rules file: %v", err)
	}
	if _, err := NewRuleEngine(path); err == nil {
		t.Fatal("expected an error for an invalid max_value_wei")
	}
}

func TestClefNewRuleEngineNegativeMaxValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"max_value_wei":"-5"}`), 0o600); err != nil {
		t.Fatalf("write rules file: %v", err)
	}
	if _, err := NewRuleEngine(path); err == nil {
		t.Fatal("expected an error for a negative max_value_wei")
	}
}

func TestClefApproveTransactionAllowList(t *testing.T) {
	allowed := types.Address{0x01}
	other := types.Address{0x02}
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"allow_addresses":["`+allowed.Hex()+`"]}`), 0o600); err != nil {
		t.Fatalf("write rules file: %v", err)
	}
	re, err := NewRuleEngine(path)
	if err != nil {
		t.Fatalf("NewRuleEngine error: %v", err)
	}

	if ok, _ := re.ApproveTransaction(&TransactionArgs{From: allowed}); !ok {
		t.Fatal("expected allowed address to be approved")
	}
	ok, reason := re.ApproveTransaction(&TransactionArgs{From: other})
	if ok {
		t.Fatal("expected address not on the allow-list to be denied")
	}
	if reason == "" {
		t.Fatal("expected a denial reason")
	}
}

func TestClefApproveTransactionRequireTo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"require_to":true}`), 0o600); err != nil {
		t.Fatalf("write rules file: %v", err)
	}
	re, err := NewRuleEngine(path)
	if err != nil {
		t.Fatalf("NewRuleEngine error: %v", err)
	}

	if ok, _ := re.ApproveTransaction(&TransactionArgs{}); ok {
		t.Fatal("expected contract creation (nil To) to be denied under require_to")
	}
	to := types.Address{0x03}
	if ok, _ := re.ApproveTransaction(&TransactionArgs{To: &to}); !ok {
		t.Fatal("expected a transaction with To set to be approved")
	}
}

func TestClefApproveTransactionMaxValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"max_value_wei":"1000"}`), 0o600); err != nil {
		t.Fatalf("write rules file: %v", err)
	}
	re, err := NewRuleEngine(path)
	if err != nil {
		t.Fatalf("NewRuleEngine error: %v", err)
	}

	small := (*hexutil.Big)(big.NewInt(500))
	if ok, _ := re.ApproveTransaction(&TransactionArgs{Value: small}); !ok {
		t.Fatal("expected a value under the cap to be approved")
	}
	large := (*hexutil.Big)(big.NewInt(5000))
	ok, reason := re.ApproveTransaction(&TransactionArgs{Value: large})
	if ok {
		t.Fatal("expected a value over the cap to be denied")
	}
	if reason == "" {
		t.Fatal("expected a denial reason for over-cap value")
	}
}

func TestClefApproveTransactionNil(t *testing.T) {
	re, _ := NewRuleEngine("")
	if ok, reason := re.ApproveTransaction(nil); ok || reason == "" {
		t.Fatalf("expected nil transaction to be denied with a reason, got ok=%v reason=%q", ok, reason)
	}
}

func TestClefApproveSignData(t *testing.T) {
	allowed := types.Address{0x01}
	path := filepath.Join(t.TempDir(), "rules.json")
	if err := os.WriteFile(path, []byte(`{"allow_addresses":["`+allowed.Hex()+`"]}`), 0o600); err != nil {
		t.Fatalf("write rules file: %v", err)
	}
	re, err := NewRuleEngine(path)
	if err != nil {
		t.Fatalf("NewRuleEngine error: %v", err)
	}
	if ok, _ := re.ApproveSignData(allowed, []byte("data")); !ok {
		t.Fatal("expected allowed address to be approved for sign-data")
	}
	if ok, reason := re.ApproveSignData(types.Address{0x99}, []byte("data")); ok || reason == "" {
		t.Fatal("expected disallowed address to be denied with a reason")
	}
}
