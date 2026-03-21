// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package wallet

import (
	"errors"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// ---------- Account ----------

func TestNewAgentAccount(t *testing.T) {
	ownerKey := []byte("test-owner-key-32-bytes-long!!!")
	did := "did:n42:0xTestAgent"

	acct, err := NewAccount(ownerKey, did)
	if err != nil {
		t.Fatalf("NewAccount: %v", err)
	}

	var zeroAddr types.Address
	if acct.Address == zeroAddr {
		t.Fatal("agent address must be non-zero")
	}
	if acct.Owner == zeroAddr {
		t.Fatal("owner address must be non-zero")
	}
	if acct.AgentDID != did {
		t.Fatalf("AgentDID = %q, want %q", acct.AgentDID, did)
	}
	if len(acct.SessionKeys) != 0 {
		t.Fatalf("new account should have 0 session keys, got %d", len(acct.SessionKeys))
	}
}

func TestNewAgentAccountErrors(t *testing.T) {
	if _, err := NewAccount(nil, "did:n42:0x1"); !errors.Is(err, ErrNilOwnerKey) {
		t.Fatalf("expected ErrNilOwnerKey, got %v", err)
	}
	if _, err := NewAccount([]byte("key"), ""); !errors.Is(err, ErrEmptyDID) {
		t.Fatalf("expected ErrEmptyDID, got %v", err)
	}
}

func TestCreateSessionKey(t *testing.T) {
	acct, _ := NewAccount([]byte("owner-key"), "did:n42:0x1")

	perms := SessionPermissions{
		AllowedContracts: []types.Address{types.HexToAddress("0x01")},
		MaxGasPerTx:      100000,
	}
	expiry := time.Now().Add(1 * time.Hour)
	limit := uint256.NewInt(1000)

	sk, err := acct.CreateSessionKey(perms, expiry, limit)
	if err != nil {
		t.Fatalf("CreateSessionKey: %v", err)
	}
	if sk.Revoked {
		t.Fatal("new session key should not be revoked")
	}
	if sk.SpendLimit.Cmp(limit) != 0 {
		t.Fatalf("spend limit = %s, want %s", sk.SpendLimit.String(), limit.String())
	}
	if len(sk.Permissions.AllowedContracts) != 1 {
		t.Fatalf("allowed contracts = %d, want 1", len(sk.Permissions.AllowedContracts))
	}
}

func TestCreateSessionKeyMaxLimit(t *testing.T) {
	acct, _ := NewAccount([]byte("owner-key"), "did:n42:0x2")

	expiry := time.Now().Add(1 * time.Hour)
	for i := 0; i < MaxSessionKeys; i++ {
		_, err := acct.CreateSessionKey(SessionPermissions{}, expiry, uint256.NewInt(100))
		if err != nil {
			t.Fatalf("CreateSessionKey %d: %v", i, err)
		}
	}

	// The next one should fail.
	_, err := acct.CreateSessionKey(SessionPermissions{}, expiry, uint256.NewInt(100))
	if !errors.Is(err, ErrMaxSessionKeys) {
		t.Fatalf("expected ErrMaxSessionKeys, got %v", err)
	}
}

func TestValidateSessionKey(t *testing.T) {
	acct, _ := NewAccount([]byte("owner-key"), "did:n42:0x3")
	target := types.HexToAddress("0xAA")
	perms := SessionPermissions{
		AllowedContracts: []types.Address{target},
		MaxGasPerTx:      50000,
	}
	expiry := time.Now().Add(1 * time.Hour)
	limit := uint256.NewInt(500)

	sk, _ := acct.CreateSessionKey(perms, expiry, limit)

	// Valid call.
	if err := acct.ValidateSessionKey(sk.ID, target, nil, uint256.NewInt(100), 30000); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}

	// Wrong contract.
	if err := acct.ValidateSessionKey(sk.ID, types.HexToAddress("0xBB"), nil, uint256.NewInt(10), 30000); !errors.Is(err, ErrContractDenied) {
		t.Fatalf("expected ErrContractDenied, got %v", err)
	}

	// Spend limit exceeded.
	if err := acct.ValidateSessionKey(sk.ID, target, nil, uint256.NewInt(501), 30000); !errors.Is(err, ErrSpendLimitExceed) {
		t.Fatalf("expected ErrSpendLimitExceed, got %v", err)
	}

	// Gas limit exceeded.
	if err := acct.ValidateSessionKey(sk.ID, target, nil, uint256.NewInt(10), 60000); !errors.Is(err, ErrGasLimitExceed) {
		t.Fatalf("expected ErrGasLimitExceed, got %v", err)
	}
}

func TestValidateSessionKeyExpired(t *testing.T) {
	acct, _ := NewAccount([]byte("owner-key"), "did:n42:0x4")
	expiry := time.Now().Add(-1 * time.Second) // already expired
	sk, _ := acct.CreateSessionKey(SessionPermissions{}, expiry, uint256.NewInt(1000))

	err := acct.ValidateSessionKey(sk.ID, types.Address{}, nil, nil, 21000)
	if !errors.Is(err, ErrSessionKeyExpired) {
		t.Fatalf("expected ErrSessionKeyExpired, got %v", err)
	}
}

func TestRevokeSessionKey(t *testing.T) {
	acct, _ := NewAccount([]byte("owner-key"), "did:n42:0x5")
	expiry := time.Now().Add(1 * time.Hour)
	sk, _ := acct.CreateSessionKey(SessionPermissions{}, expiry, uint256.NewInt(1000))

	if err := acct.RevokeSessionKey(sk.ID); err != nil {
		t.Fatalf("RevokeSessionKey: %v", err)
	}

	err := acct.ValidateSessionKey(sk.ID, types.Address{}, nil, nil, 21000)
	if !errors.Is(err, ErrSessionKeyRevoked) {
		t.Fatalf("expected ErrSessionKeyRevoked after revoke, got %v", err)
	}
}

// ---------- Policies ----------

func TestRatePolicy(t *testing.T) {
	rp := NewRatePolicy(3, 1*time.Minute)

	now := time.Now()
	for i := 0; i < 3; i++ {
		op := &Operation{Timestamp: now.Add(time.Duration(i) * time.Second)}
		allow, _ := rp.Evaluate(op)
		if !allow {
			t.Fatalf("operation %d should be allowed within rate limit", i)
		}
	}

	// Fourth should be rejected.
	op := &Operation{Timestamp: now.Add(4 * time.Second)}
	allow, reason := rp.Evaluate(op)
	if allow {
		t.Fatal("fourth operation should be rejected by rate limit")
	}
	if reason == "" {
		t.Fatal("rejection reason should not be empty")
	}
}

func TestCapPolicy(t *testing.T) {
	txCap := uint256.NewInt(100)
	dailyCap := uint256.NewInt(250)
	cp := NewCapPolicy(txCap, dailyCap)

	now := time.Now()

	// Within per-tx cap.
	allow, _ := cp.Evaluate(&Operation{Value: uint256.NewInt(80), Timestamp: now})
	if !allow {
		t.Fatal("80 should be within per-tx cap of 100")
	}

	// Exceeds per-tx cap.
	allow, _ = cp.Evaluate(&Operation{Value: uint256.NewInt(101), Timestamp: now})
	if allow {
		t.Fatal("101 should exceed per-tx cap of 100")
	}

	// Within daily cap (80 already used, 80 more = 160 < 250).
	allow, _ = cp.Evaluate(&Operation{Value: uint256.NewInt(80), Timestamp: now})
	if !allow {
		t.Fatal("second 80 should be within daily cap")
	}

	// Exceed daily cap (80+80=160 already, 100 more = 260 > 250).
	allow, _ = cp.Evaluate(&Operation{Value: uint256.NewInt(100), Timestamp: now})
	if allow {
		t.Fatal("260 total should exceed daily cap of 250")
	}
}

func TestAllowlistPolicy(t *testing.T) {
	allowed := types.HexToAddress("0xAA")
	denied := types.HexToAddress("0xBB")
	ap := NewAllowlistPolicy([]types.Address{allowed})

	ok, _ := ap.Evaluate(&Operation{To: allowed})
	if !ok {
		t.Fatal("allowed address should pass allowlist")
	}

	ok, _ = ap.Evaluate(&Operation{To: denied})
	if ok {
		t.Fatal("denied address should fail allowlist")
	}
}

func TestCompositePolicy(t *testing.T) {
	allowed := types.HexToAddress("0xAA")
	denied := types.HexToAddress("0xBB")
	ap := NewAllowlistPolicy([]types.Address{allowed})
	rp := NewRatePolicy(100, 1*time.Hour)

	// AND mode: both must pass.
	andPolicy := NewCompositePolicy("and", []SpendingPolicy{ap, rp})
	ok, _ := andPolicy.Evaluate(&Operation{To: allowed, Timestamp: time.Now()})
	if !ok {
		t.Fatal("AND: allowed address + within rate should pass")
	}
	ok, _ = andPolicy.Evaluate(&Operation{To: denied, Timestamp: time.Now()})
	if ok {
		t.Fatal("AND: denied address should fail even if rate OK")
	}

	// OR mode: at least one must pass.
	orPolicy := NewCompositePolicy("or", []SpendingPolicy{
		NewAllowlistPolicy([]types.Address{allowed}),
		NewAllowlistPolicy([]types.Address{denied}),
	})
	ok, _ = orPolicy.Evaluate(&Operation{To: denied})
	if !ok {
		t.Fatal("OR: denied in first but allowed in second should pass")
	}

	// OR mode: none pass.
	orPolicy2 := NewCompositePolicy("or", []SpendingPolicy{
		NewAllowlistPolicy([]types.Address{allowed}),
	})
	ok, _ = orPolicy2.Evaluate(&Operation{To: denied})
	if ok {
		t.Fatal("OR: denied in all policies should fail")
	}
}

// ---------- Paymaster ----------

func TestPaymasterService(t *testing.T) {
	pm := NewPaymasterService()
	owner := types.HexToAddress("0x01")

	// Deposit.
	if err := pm.Deposit(owner, uint256.NewInt(1000)); err != nil {
		t.Fatalf("Deposit: %v", err)
	}
	if pm.Balance(owner).Cmp(uint256.NewInt(1000)) != 0 {
		t.Fatalf("balance = %s, want 1000", pm.Balance(owner).String())
	}

	// Sponsor.
	data, err := pm.SponsorOperation(owner, uint256.NewInt(100))
	if err != nil {
		t.Fatalf("SponsorOperation: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("sponsor data should not be empty")
	}
	if pm.Balance(owner).Cmp(uint256.NewInt(900)) != 0 {
		t.Fatalf("balance after sponsor = %s, want 900", pm.Balance(owner).String())
	}
	if pm.TotalSponsored() != 1 {
		t.Fatalf("total sponsored = %d, want 1", pm.TotalSponsored())
	}

	// Withdraw.
	if err := pm.WithdrawDeposit(owner, uint256.NewInt(900)); err != nil {
		t.Fatalf("WithdrawDeposit: %v", err)
	}
	if pm.Balance(owner).Cmp(uint256.NewInt(0)) != 0 {
		t.Fatalf("balance after withdraw = %s, want 0", pm.Balance(owner).String())
	}

	// Withdraw from empty.
	if err := pm.WithdrawDeposit(owner, uint256.NewInt(1)); !errors.Is(err, ErrNoDeposit) {
		t.Fatalf("expected ErrNoDeposit, got %v", err)
	}
}

// ---------- AgentService ----------

func TestAgentService(t *testing.T) {
	svc := NewService(8, true)

	acct, err := svc.CreateAccount([]byte("key1"), "did:n42:0xA")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if svc.AccountCount() != 1 {
		t.Fatalf("count = %d, want 1", svc.AccountCount())
	}

	got, err := svc.GetAccount(acct.Address)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if got.AgentDID != "did:n42:0xA" {
		t.Fatalf("DID = %q, want %q", got.AgentDID, "did:n42:0xA")
	}

	// Duplicate should fail.
	_, err = svc.CreateAccount([]byte("key1"), "did:n42:0xA")
	if !errors.Is(err, ErrAccountExists) {
		t.Fatalf("expected ErrAccountExists, got %v", err)
	}

	if svc.Paymaster() == nil {
		t.Fatal("paymaster should be enabled")
	}
}

// ---------- Additional coverage tests ----------

func TestGetSessionKey(t *testing.T) {
	acct, _ := NewAccount([]byte("owner-key"), "did:n42:0xGetSK")
	expiry := time.Now().Add(1 * time.Hour)
	perms := SessionPermissions{
		AllowedContracts: []types.Address{types.HexToAddress("0xCC")},
		MaxGasPerTx:      42000,
	}
	limit := uint256.NewInt(777)

	sk, err := acct.CreateSessionKey(perms, expiry, limit)
	if err != nil {
		t.Fatalf("CreateSessionKey: %v", err)
	}

	t.Run("existing key", func(t *testing.T) {
		got, err := acct.GetSessionKey(sk.ID)
		if err != nil {
			t.Fatalf("GetSessionKey: %v", err)
		}
		if got.ID != sk.ID {
			t.Fatalf("ID mismatch: got %s, want %s", got.ID.Hex(), sk.ID.Hex())
		}
		if got.SpendLimit.Cmp(limit) != 0 {
			t.Fatalf("SpendLimit = %s, want %s", got.SpendLimit.String(), limit.String())
		}
		if got.Permissions.MaxGasPerTx != 42000 {
			t.Fatalf("MaxGasPerTx = %d, want 42000", got.Permissions.MaxGasPerTx)
		}
		if len(got.Permissions.AllowedContracts) != 1 {
			t.Fatalf("AllowedContracts len = %d, want 1", len(got.Permissions.AllowedContracts))
		}
		if got.Revoked {
			t.Fatal("key should not be revoked")
		}
	})

	t.Run("non-existent key", func(t *testing.T) {
		_, err := acct.GetSessionKey(types.HexToHash("0xdeadbeef"))
		if !errors.Is(err, ErrSessionKeyUnknown) {
			t.Fatalf("expected ErrSessionKeyUnknown, got %v", err)
		}
	})
}

func TestActiveSessionKeys(t *testing.T) {
	acct, _ := NewAccount([]byte("owner-key"), "did:n42:0xActiveSK")

	// Key 1: valid, long expiry.
	sk1, err := acct.CreateSessionKey(SessionPermissions{}, time.Now().Add(1*time.Hour), uint256.NewInt(100))
	if err != nil {
		t.Fatalf("CreateSessionKey 1: %v", err)
	}

	// Key 2: will be revoked.
	sk2, err := acct.CreateSessionKey(SessionPermissions{}, time.Now().Add(1*time.Hour), uint256.NewInt(100))
	if err != nil {
		t.Fatalf("CreateSessionKey 2: %v", err)
	}

	// Key 3: already expired (expiry in the past).
	_, err = acct.CreateSessionKey(SessionPermissions{}, time.Now().Add(-1*time.Millisecond), uint256.NewInt(100))
	if err != nil {
		t.Fatalf("CreateSessionKey 3: %v", err)
	}

	// Revoke key 2.
	if err := acct.RevokeSessionKey(sk2.ID); err != nil {
		t.Fatalf("RevokeSessionKey: %v", err)
	}

	// Small sleep to ensure the expired key's expiry is in the past.
	time.Sleep(2 * time.Millisecond)

	active := acct.ActiveSessionKeys()
	if len(active) != 1 {
		t.Fatalf("ActiveSessionKeys returned %d keys, want 1", len(active))
	}
	if active[0].ID != sk1.ID {
		t.Fatalf("active key ID = %s, want %s", active[0].ID.Hex(), sk1.ID.Hex())
	}
}

func TestValidateSessionKey_MethodSelectorRequired(t *testing.T) {
	acct, _ := NewAccount([]byte("owner-key"), "did:n42:0xMethodSel")
	target := types.HexToAddress("0xAA")
	perms := SessionPermissions{
		AllowedMethods: [][]byte{{0x12, 0x34, 0x56, 0x78}},
	}
	expiry := time.Now().Add(1 * time.Hour)
	sk, _ := acct.CreateSessionKey(perms, expiry, uint256.NewInt(1000))

	// nil methodSig when AllowedMethods is non-empty should error.
	err := acct.ValidateSessionKey(sk.ID, target, nil, uint256.NewInt(10), 21000)
	if err == nil {
		t.Fatal("expected error when methodSig is nil with AllowedMethods set")
	}
	if err.Error() != "wallet: method selector required when method restrictions are set" {
		t.Fatalf("unexpected error message: %v", err)
	}

	// Short methodSig (< 4 bytes) should also error.
	err = acct.ValidateSessionKey(sk.ID, target, []byte{0x12, 0x34}, uint256.NewInt(10), 21000)
	if err == nil {
		t.Fatal("expected error for short methodSig")
	}
}

func TestEvaluateAll(t *testing.T) {
	allowed := types.HexToAddress("0xAA")
	denied := types.HexToAddress("0xBB")

	t.Run("all pass", func(t *testing.T) {
		policies := []SpendingPolicy{
			NewAllowlistPolicy([]types.Address{allowed}),
			NewRatePolicy(100, 1*time.Hour),
		}
		ok, reason := EvaluateAll(policies, &Operation{To: allowed, Timestamp: time.Now()})
		if !ok {
			t.Fatalf("expected allow, got deny: %s", reason)
		}
	})

	t.Run("first failure returned", func(t *testing.T) {
		policies := []SpendingPolicy{
			NewAllowlistPolicy([]types.Address{allowed}),
			NewRatePolicy(0, 1*time.Hour), // rate limit 0 = always deny
		}
		ok, reason := EvaluateAll(policies, &Operation{To: allowed, Timestamp: time.Now()})
		if ok {
			t.Fatal("expected deny from rate policy")
		}
		if reason == "" {
			t.Fatal("expected non-empty reason")
		}
		// The reason should cite the "rate" policy name.
		if !containsSubstring(reason, "rate") {
			t.Fatalf("expected reason to mention rate policy, got: %s", reason)
		}
	})

	t.Run("first policy denies", func(t *testing.T) {
		policies := []SpendingPolicy{
			NewAllowlistPolicy([]types.Address{allowed}),
			NewRatePolicy(100, 1*time.Hour),
		}
		ok, reason := EvaluateAll(policies, &Operation{To: denied, Timestamp: time.Now()})
		if ok {
			t.Fatal("expected deny from allowlist policy")
		}
		if !containsSubstring(reason, "allowlist") {
			t.Fatalf("expected reason to mention allowlist policy, got: %s", reason)
		}
	})

	t.Run("empty policies allow", func(t *testing.T) {
		ok, _ := EvaluateAll(nil, &Operation{To: denied, Timestamp: time.Now()})
		if !ok {
			t.Fatal("empty policy list should allow")
		}
	})
}

// containsSubstring is a test helper that checks if s contains substr.
func containsSubstring(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestPaymasterWithdrawExact(t *testing.T) {
	pm := NewPaymasterService()
	owner := types.HexToAddress("0x10")

	if err := pm.Deposit(owner, uint256.NewInt(500)); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	// Withdraw exactly the full balance.
	if err := pm.WithdrawDeposit(owner, uint256.NewInt(500)); err != nil {
		t.Fatalf("WithdrawDeposit: %v", err)
	}

	bal := pm.Balance(owner)
	if !bal.IsZero() {
		t.Fatalf("balance = %s, want 0", bal.String())
	}

	// Deposit count should be 0 since entry is removed on zero balance.
	if pm.DepositCount() != 0 {
		t.Fatalf("deposit count = %d, want 0", pm.DepositCount())
	}
}

func TestPaymasterMultipleOps(t *testing.T) {
	pm := NewPaymasterService()
	owner := types.HexToAddress("0x20")

	if err := pm.Deposit(owner, uint256.NewInt(1000)); err != nil {
		t.Fatalf("Deposit: %v", err)
	}

	// Sponsor 3 operations of 200 each.
	for i := 0; i < 3; i++ {
		data, err := pm.SponsorOperation(owner, uint256.NewInt(200))
		if err != nil {
			t.Fatalf("SponsorOperation %d: %v", i, err)
		}
		if len(data) == 0 {
			t.Fatalf("sponsor data %d should not be empty", i)
		}
	}

	// Balance should be 1000 - 3*200 = 400.
	bal := pm.Balance(owner)
	if bal.Cmp(uint256.NewInt(400)) != 0 {
		t.Fatalf("balance = %s, want 400", bal.String())
	}
	if pm.TotalSponsored() != 3 {
		t.Fatalf("total sponsored = %d, want 3", pm.TotalSponsored())
	}

	// Fourth sponsor of 500 should fail (only 400 available).
	_, err := pm.SponsorOperation(owner, uint256.NewInt(500))
	if !errors.Is(err, ErrInsufficientDeposit) {
		t.Fatalf("expected ErrInsufficientDeposit, got %v", err)
	}
}

func TestServiceRemoveAccount(t *testing.T) {
	svc := NewService(8, false)

	acct, err := svc.CreateAccount([]byte("remove-key"), "did:n42:0xRemove")
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if svc.AccountCount() != 1 {
		t.Fatalf("count = %d, want 1", svc.AccountCount())
	}

	// Remove the account.
	if err := svc.RemoveAccount(acct.Address); err != nil {
		t.Fatalf("RemoveAccount: %v", err)
	}
	if svc.AccountCount() != 0 {
		t.Fatalf("count after remove = %d, want 0", svc.AccountCount())
	}

	// GetAccount should return error.
	_, err = svc.GetAccount(acct.Address)
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("expected ErrAccountNotFound, got %v", err)
	}

	// Removing again should fail.
	err = svc.RemoveAccount(acct.Address)
	if !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("expected ErrAccountNotFound on double remove, got %v", err)
	}
}

func TestCompositePolicy_EmptyPolicies(t *testing.T) {
	op := &Operation{To: types.HexToAddress("0xFF"), Timestamp: time.Now()}

	t.Run("AND with empty policies allows", func(t *testing.T) {
		cp := NewCompositePolicy("and", []SpendingPolicy{})
		ok, _ := cp.Evaluate(op)
		if !ok {
			t.Fatal("AND composite with no policies should allow")
		}
	})

	t.Run("OR with empty policies allows", func(t *testing.T) {
		// Empty Policies slice triggers early return (true) before mode switch.
		cp := NewCompositePolicy("or", []SpendingPolicy{})
		ok, _ := cp.Evaluate(op)
		if !ok {
			t.Fatal("OR composite with no policies should allow (early return)")
		}
	})

	t.Run("nil policies allows", func(t *testing.T) {
		cp := NewCompositePolicy("and", nil)
		ok, _ := cp.Evaluate(op)
		if !ok {
			t.Fatal("AND composite with nil policies should allow")
		}
	})
}

func TestCapPolicyDailyCrossing(t *testing.T) {
	txCap := uint256.NewInt(100)
	dailyCap := uint256.NewInt(200)
	cp := NewCapPolicy(txCap, dailyCap)

	// Use up the daily cap on "day 1".
	day1 := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	ok, _ := cp.Evaluate(&Operation{Value: uint256.NewInt(100), Timestamp: day1})
	if !ok {
		t.Fatal("first 100 on day 1 should pass")
	}
	ok, _ = cp.Evaluate(&Operation{Value: uint256.NewInt(100), Timestamp: day1})
	if !ok {
		t.Fatal("second 100 on day 1 should pass (total 200 = daily cap)")
	}

	// Daily cap should now be exhausted for day 1.
	ok, _ = cp.Evaluate(&Operation{Value: uint256.NewInt(1), Timestamp: day1})
	if ok {
		t.Fatal("day 1 cap should be exhausted")
	}

	// On "day 2", the daily cap resets.
	day2 := time.Date(2026, 3, 21, 12, 0, 0, 0, time.UTC)
	ok, _ = cp.Evaluate(&Operation{Value: uint256.NewInt(100), Timestamp: day2})
	if !ok {
		t.Fatal("day 2 should have fresh daily cap")
	}
	ok, _ = cp.Evaluate(&Operation{Value: uint256.NewInt(100), Timestamp: day2})
	if !ok {
		t.Fatal("second 100 on day 2 should pass")
	}

	// Day 2 cap should also be exhausted now.
	ok, _ = cp.Evaluate(&Operation{Value: uint256.NewInt(1), Timestamp: day2})
	if ok {
		t.Fatal("day 2 cap should be exhausted after 200")
	}
}
