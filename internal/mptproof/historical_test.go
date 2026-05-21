package mptproof

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHistoricalLeafSource_FallbackToBase(t *testing.T) {
	// With nil history reader, ALL reads fall through to base.
	base := &MapLeafSource{
		Accounts: map[[20]byte][]byte{
			mkAddrN(1): {0x01, 0x02},
		},
	}
	h := NewHistoricalLeafSource(base, nil, 100)

	v, ok, err := h.AccountValue(mkAddrN(1))
	if err != nil || !ok {
		t.Fatalf("AccountValue: ok=%v err=%v", ok, err)
	}
	if !bytes.Equal(v, []byte{0x01, 0x02}) {
		t.Errorf("value: got %x want 0102", v)
	}

	// Absent key
	_, ok, err = h.AccountValue(mkAddrN(99))
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected absent")
	}
}

func TestHistoricalLeafSource_ScanOverlaysBase(t *testing.T) {
	// Without history, Scan should iterate base unchanged.
	base := &MapLeafSource{
		Accounts: map[[20]byte][]byte{
			mkAddrN(1): {0x01},
			mkAddrN(2): {0x02},
		},
	}
	h := NewHistoricalLeafSource(base, nil, 100)

	count := 0
	err := h.ScanAccounts(func(addr [20]byte, value []byte) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("scan count: got %d want 2", count)
	}
}

// =========================================================================
// Production: D:\n42-history-full integration
// =========================================================================

// TestHistoricalProof_Production_USDC takes ~15-20 min on real reth
// data due to 4 inline-sibling rebuilds on USDC's depth-6 branch.
// Run with -timeout 30m to be safe. See cmd/n42-proof-debug for an
// interactive runner.
func TestHistoricalProof_Production_USDC(t *testing.T) {
	if testing.Short() {
		t.Skip("--short: skipping full reth scan (~15 min)")
	}
	if deadline, ok := t.Deadline(); ok && time.Until(deadline) < 30*time.Minute {
		t.Skip("test deadline too short — pass -timeout 30m or longer")
	}
	if _, err := os.Stat(filepath.Join(productionHistoryDir, "account.mphf")); err != nil {
		t.Skipf("%s not present", productionHistoryDir)
	}
	if _, err := os.Stat(filepath.Join(productionRethDB, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB)
	}
	if _, err := os.Stat(filepath.Join(productionAccountsDir, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionAccountsDir)
	}

	leaves, err := NewRethLeafSource(productionRethDB, "PlainAccountState", "PlainStorageState", 4096)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{
		AccountsTrieDir: productionAccountsDir,
		StorageTrieDir:  productionStorageDir,
		HistoryDir:      productionHistoryDir,
		Leaves:          leaves,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)

	// USDC deployment: block 6,082,465. Below that → historicalstate
	// has no record (account didn't exist yet); above → has record.

	// Block 5M: pre-deployment, historicalstate should not find a record.
	resPre, err := g.HistoricalProof(HistoricalProofRequest{
		Address: usdc,
		BlockN:  5_000_000,
	})
	if err != nil {
		t.Fatalf("HistoricalProof @ 5M: %v", err)
	}
	t.Logf("USDC @ 5M:  latest_root=0x%x  latest_proof=%d nodes  asof_value=%d bytes",
		resPre.LatestStateRoot[:8],
		len(resPre.LatestAccountProof),
		len(resPre.AccountValueAtBlockN))
	if len(resPre.AccountValueLatest) == 0 {
		t.Error("USDC latest value should be non-empty (deployed contract)")
	}

	// Block 25M: post-deployment, history record should exist.
	resPost, err := g.HistoricalProof(HistoricalProofRequest{
		Address: usdc,
		BlockN:  25_000_000,
	})
	if err != nil {
		t.Fatalf("HistoricalProof @ 25M: %v", err)
	}
	t.Logf("USDC @ 25M: latest_root=0x%x  latest_proof=%d nodes  asof_value=%d bytes",
		resPost.LatestStateRoot[:8],
		len(resPost.LatestAccountProof),
		len(resPost.AccountValueAtBlockN))

	// Same latest root in both responses.
	if resPre.LatestStateRoot != resPost.LatestStateRoot {
		t.Errorf("latest root differs across queries: %x vs %x",
			resPre.LatestStateRoot, resPost.LatestStateRoot)
	}

	// Notes field documents the verification scope.
	if resPost.Notes == "" {
		t.Error("Notes should explain verification scope")
	}
	t.Logf("Notes: %s", resPost.Notes)
}

func TestHistoricalProof_WithStorageSlots(t *testing.T) {
	if _, err := os.Stat(filepath.Join(productionHistoryDir, "account.mphf")); err != nil {
		t.Skipf("%s not present", productionHistoryDir)
	}
	if _, err := os.Stat(filepath.Join(productionRethDB, "mdbx.dat")); err != nil {
		t.Skip()
	}
	if _, err := os.Stat(filepath.Join(productionAccountsDir, "mdbx.dat")); err != nil {
		t.Skip()
	}

	leaves, err := NewRethLeafSource(productionRethDB, "PlainAccountState", "PlainStorageState", 4096)
	if err != nil {
		t.Fatal(err)
	}
	g, err := New(Config{
		AccountsTrieDir: productionAccountsDir,
		StorageTrieDir:  productionStorageDir,
		HistoryDir:      productionHistoryDir,
		Leaves:          leaves,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Close()

	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)
	var slot0 [32]byte
	var slotB [32]byte
	slotB[31] = 0x0b // commonly-used ERC-20 slot

	res, err := g.HistoricalProof(HistoricalProofRequest{
		Address: usdc,
		Slots:   [][32]byte{slot0, slotB},
		BlockN:  20_000_000,
	})
	if err != nil {
		t.Fatalf("HistoricalProof: %v", err)
	}
	if len(res.StorageProofs) != 2 {
		t.Fatalf("expected 2 storage proofs, got %d", len(res.StorageProofs))
	}
	for i, sp := range res.StorageProofs {
		t.Logf("slot %d: asof=%d bytes latest=%d bytes proof=%d nodes",
			i, len(sp.ValueAtBlockN), len(sp.ValueLatest),
			len(sp.LatestStorageProof))
	}
}
