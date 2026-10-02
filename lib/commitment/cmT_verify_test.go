package commitment

// cmT_verify_test.go drives VerifyBranchHashes against real branch data
// produced by a small HPH.Process run (captured via RecordingContext), then
// checks both the match and mismatch (tampered account value) paths.

import (
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/lib/common/length"
)

// cmTRunAndRecordBranches runs a tiny HPH.Process over the given addresses
// and returns the recorded branch writes plus the plain-key account values
// fed into the trie (for VerifyBranchHashes' accountValues map).
func cmTRunAndRecordBranches(t *testing.T, addrs [][]byte) (map[string]BranchWrite, map[string][]byte) {
	t.Helper()

	// Pre-populate the account reader with the SAME nonce/balance each
	// address will be given below, matching the HA3a round-trip test's
	// pattern (persistent_context_test.go): fold-time context reads for
	// neighboring cells must resolve to a real, non-nil Update rather than
	// the nil a bare stub would return.
	reader := &inMemAccountReader{store: map[string]*Update{}}
	accountValues := make(map[string][]byte, len(addrs))
	ub := NewUpdateBuilder()
	for _, a := range addrs {
		hexAddr := bytesToHexString(a)
		nonce := uint64(a[0])
		balance := uint64(a[0]) * 1000
		ub.Balance(hexAddr, balance).Nonce(hexAddr, nonce)

		// VerifyBranchHashes decodes accountValues with
		// account.StateAccount.DecodeForStorage ("V3 format"), which is a
		// different wire format from the Update encoding RecordingContext
		// captures — build it directly, the same way
		// persistent_context_test.go's mkAccount does.
		acc := account.StateAccount{Nonce: nonce}
		acc.Balance.SetUint64(balance)
		buf := make([]byte, acc.EncodingLengthForStorage())
		acc.EncodeForStorage(buf)
		accountValues[string(a)] = buf

		u := &Update{Flags: BalanceUpdate | NonceUpdate, Nonce: nonce}
		u.Balance.SetUint64(balance)
		reader.store[string(a)] = u
	}

	db := cmTOpenWarmupEnv(t)
	t.Cleanup(db.Close)
	tx, err := db.BeginRw(t.Context())
	if err != nil {
		t.Fatalf("BeginRw: %v", err)
	}
	t.Cleanup(tx.Rollback)

	pctx := NewPersistentPatriciaContext(reader, nil)
	pctx.SetWriteTx(tx)
	rc := NewRecordingContext(pctx)
	hph := NewHexPatriciaHashed(length.Addr, rc)

	plainKeys, builderUpdates := ub.Build()
	updates := WrapKeyUpdates(t, ModeDirect, KeyToHexNibbleHash, plainKeys, builderUpdates)
	defer updates.Close()

	if _, err := hph.Process(t.Context(), updates, "verify-branch", nil, WarmupConfig{}); err != nil {
		t.Fatalf("Process: %v", err)
	}

	return rc.putBranches, accountValues
}

func TestVerifyBranchHashes_Match(t *testing.T) {
	addrs := [][]byte{
		{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2},
		{3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3, 3},
	}
	putBranches, accountValues := cmTRunAndRecordBranches(t, addrs)
	if len(putBranches) == 0 {
		t.Fatal("expected at least one recorded branch write")
	}

	var verified int
	for k, bw := range putBranches {
		if err := VerifyBranchHashes([]byte(k), BranchData(bw.NewData), accountValues, nil); err != nil {
			t.Errorf("VerifyBranchHashes(%x): %v", k, err)
		} else {
			verified++
		}
	}
	if verified == 0 {
		t.Fatal("no branch was successfully verified")
	}
}

func TestVerifyBranchHashes_Mismatch(t *testing.T) {
	addrs := [][]byte{
		{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
		{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2},
	}
	putBranches, accountValues := cmTRunAndRecordBranches(t, addrs)
	if len(putBranches) == 0 {
		t.Fatal("expected at least one recorded branch write")
	}

	// Tamper with every account value's nonce/balance byte so recomputed
	// hashes no longer match the stored stateHash.
	tampered := make(map[string][]byte, len(accountValues))
	for k, v := range accountValues {
		cp := append([]byte(nil), v...)
		if len(cp) > 1 {
			cp[1] ^= 0xff
		}
		tampered[k] = cp
	}

	var mismatchSeen bool
	for k, bw := range putBranches {
		if err := VerifyBranchHashes([]byte(k), BranchData(bw.NewData), tampered, nil); err != nil {
			mismatchSeen = true
		}
	}
	if !mismatchSeen {
		t.Fatal("expected at least one mismatch after tampering with account values")
	}
}
