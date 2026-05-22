package mptproof

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/internal/mptbuild"
)

const (
	productionAccountsDir = `D:\n42-mpt\accounts-mptcache`
	productionStorageDir  = `D:\n42-mpt\storage-mptcache`
	productionHistoryDir  = `D:\n42-history-full`
	productionRethDB      = `D:\reth2k\db`
)

func mkAddrN(seed uint32) [20]byte {
	var a [20]byte
	a[0] = byte(seed >> 24)
	a[1] = byte(seed >> 16)
	a[2] = byte(seed >> 8)
	a[3] = byte(seed)
	return a
}

// buildSyntheticGenerator stands up a Generator backed by an MDBX
// AccountsTrie built from N synthetic accounts + a MapLeafSource
// containing the same accounts. Storage trie left empty for these
// tests.
func buildSyntheticGenerator(t *testing.T, n int) (*Generator, [][20]byte, map[[20]byte][]byte) {
	t.Helper()
	tmp := t.TempDir()
	accDir := filepath.Join(tmp, "acc-mptcache")
	storDir := filepath.Join(tmp, "stor-mptcache")

	addrs := make([][20]byte, n)
	values := make(map[[20]byte][]byte, n)
	entries := make([][2][]byte, n)
	for i := 0; i < n; i++ {
		addrs[i] = mkAddrN(uint32(i))
		// Use 64-byte leaf values so the leaf RLP node is well above
		// 32 bytes — guarantees HashBuilder will store the LEAF HASH
		// in the parent branch's hash_mask (vs inlining the bytes,
		// which BranchNodeCompact cannot represent).
		v := make([]byte, 64)
		for k := range v {
			v[k] = byte((i + k) * 7 ^ 0x55)
		}
		values[addrs[i]] = v
		entries[i] = [2][]byte{addrs[i][:], v}
	}

	tgt := &mptbuild.MDBXTarget{
		DBPath:    accDir,
		Table:     "AccountsTrie",
		MapSizeGB: 1,
	}
	if _, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: entries},
		Target:    tgt,
		Extractor: mptbuild.NewAccountExtractor(),
		TmpDir:    filepath.Join(tmp, "etl-acc"),
		BufMB:     1,
	}); err != nil {
		tgt.Close()
		t.Fatalf("build accounts: %v", err)
	}
	tgt.Close()

	// Empty storage trie — Build needs at least one entry, so insert
	// a stub.
	stub := [][2][]byte{{addrs[0][:], append(make([]byte, 32), 0xFF)}}
	stgt := &mptbuild.MDBXTarget{DBPath: storDir, Table: "StoragesTrie", MapSizeGB: 1}
	if _, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: stub},
		Target:    stgt,
		Extractor: mptbuild.NewStorageExtractor(),
		TmpDir:    filepath.Join(tmp, "etl-stor"),
		BufMB:     1,
	}); err != nil {
		stgt.Close()
		t.Fatalf("build storage stub: %v", err)
	}
	stgt.Close()

	leaves := &MapLeafSource{Accounts: values}
	g, err := New(Config{
		AccountsTrieDir: accDir,
		StorageTrieDir:  storDir,
		Leaves:          leaves,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { g.Close() })
	return g, addrs, values
}

func TestGenerator_LatestAccountProof_KnownAddr(t *testing.T) {
	g, addrs, values := buildSyntheticGenerator(t, 200)

	addr := addrs[42]
	proof, err := g.LatestAccountProof(addr)
	if err != nil {
		t.Fatalf("LatestAccountProof: %v", err)
	}
	if proof.Address != addr {
		t.Errorf("addr roundtrip: got %x want %x", proof.Address, addr)
	}
	if !proof.LeafFound {
		t.Error("expected leaf found for known addr")
	}
	if string(proof.LeafValue) != string(values[addr]) {
		t.Errorf("leaf value mismatch: got %x want %x",
			proof.LeafValue, values[addr])
	}
	if proof.Walk == nil {
		t.Fatal("walk is nil")
	}
	t.Logf("acct proof: hashed=%x walk-outcome=%v hops=%d siblings=%d",
		proof.HashedAddr[:6], proof.Walk.Outcome,
		len(proof.Walk.Hops), len(proof.Walk.CollectSiblings()))

	var zero [32]byte
	if proof.StateRoot == zero {
		t.Error("StateRoot is zero")
	}
}

func TestGenerator_LatestAccountProof_AbsentAddr(t *testing.T) {
	g, _, _ := buildSyntheticGenerator(t, 100)

	// Pick a seed far beyond N to ensure not in test set.
	absent := mkAddrN(0xDEADBEEF)
	proof, err := g.LatestAccountProof(absent)
	if err != nil {
		t.Fatalf("LatestAccountProof: %v", err)
	}
	if proof.LeafFound {
		t.Error("expected !LeafFound for never-inserted addr")
	}
	if len(proof.LeafValue) != 0 {
		t.Errorf("expected empty leaf value, got %x", proof.LeafValue)
	}
	// Walk should still produce some hops (proof of non-existence).
	if len(proof.Walk.Hops) == 0 {
		t.Error("expected non-empty walk for absent addr")
	}
}

func TestGenerator_RethLeafSource_Account(t *testing.T) {
	if _, err := os.Stat(filepath.Join(productionRethDB, "mdbx.dat")); err != nil {
		t.Skipf("%s not present; skip", productionRethDB)
	}
	src, err := NewRethLeafSource(productionRethDB, "PlainAccountState", "PlainStorageState", 4096)
	if err != nil {
		t.Fatalf("NewRethLeafSource: %v", err)
	}
	defer src.Close()

	// USDC contract address (well-known, exists on mainnet).
	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)

	v, found, err := src.AccountValue(usdc)
	if err != nil {
		t.Fatalf("AccountValue USDC: %v", err)
	}
	if !found {
		t.Fatal("USDC not found in PlainAccountState — reth DB stale?")
	}
	t.Logf("USDC account value: %d bytes  prefix=%x", len(v), v[:min(8, len(v))])
}


// =========================================================================
// Integration tests against D:\n42-mpt + reth source
// =========================================================================

func openProductionGenerator(t *testing.T) *Generator {
	t.Helper()
	if _, err := os.Stat(filepath.Join(productionAccountsDir, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionAccountsDir)
	}
	if _, err := os.Stat(filepath.Join(productionStorageDir, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionStorageDir)
	}
	if _, err := os.Stat(filepath.Join(productionRethDB, "mdbx.dat")); err != nil {
		t.Skipf("%s not present", productionRethDB)
	}
	leaves, err := NewRethLeafSource(productionRethDB, "PlainAccountState", "PlainStorageState", 4096)
	if err != nil {
		t.Fatalf("NewRethLeafSource: %v", err)
	}
	g, err := New(Config{
		AccountsTrieDir: productionAccountsDir,
		StorageTrieDir:  productionStorageDir,
		HistoryDir:      productionHistoryDir,
		Leaves:          leaves,
	})
	if err != nil {
		leaves.Close()
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { g.Close() })
	return g
}

func TestProduction_LatestAccountProof_USDC(t *testing.T) {
	g := openProductionGenerator(t)

	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)

	proof, err := g.LatestAccountProof(usdc)
	if err != nil {
		t.Fatalf("LatestAccountProof USDC: %v", err)
	}
	t.Logf("USDC proof: root=%x leaf=%d bytes hops=%d siblings=%d",
		proof.StateRoot[:6], len(proof.LeafValue),
		len(proof.Walk.Hops), len(proof.Walk.CollectSiblings()))
	if !proof.LeafFound {
		t.Error("USDC leaf not found in reth source")
	}
	// LandedOnLeaf is the iota value 1 in mpttrie.
	if int(proof.Walk.Outcome) != 1 {
		t.Errorf("walk did not land on leaf (LandedOnLeaf=1): got %v", proof.Walk.Outcome)
	}
	// Root should match what we recorded at build time.
	expectedRoot := "7812409463082683ff0a0b5cef75d87f8dfe313f006ea70cf8246f4521700033"
	if hex.EncodeToString(proof.StateRoot[:]) != expectedRoot {
		t.Errorf("state root mismatch:\n  got  %s\n  want %s",
			hex.EncodeToString(proof.StateRoot[:]), expectedRoot)
	}
}

func TestProduction_LatestStorageProofs_USDC(t *testing.T) {
	g := openProductionGenerator(t)

	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)

	// Try a few well-known slots on USDC. We don't know exact values
	// but slot 0 (proxy implementation per EIP-1967) often exists.
	var slot0 [32]byte
	var slotB [32]byte
	slotB[31] = 0x0b // commonly used in ERC-20 storage layouts

	proofs, err := g.LatestStorageProofs(usdc, [][32]byte{slot0, slotB})
	if err != nil {
		t.Fatalf("LatestStorageProofs: %v", err)
	}
	if len(proofs) != 2 {
		t.Fatalf("expected 2 proofs, got %d", len(proofs))
	}
	for i, p := range proofs {
		t.Logf("slot %d: hashedKey=%x found=%v leaf=%d bytes hops=%d siblings=%d",
			i, p.HashedKey[:6], p.LeafFound, len(p.LeafValue),
			len(p.Walk.Hops), len(p.Walk.CollectSiblings()))
	}
	// Storage trie root should match what we recorded at build time.
	expectedStorRoot := "c44e11b0c6fc1032dcb7a96b3150dd93d6d3d9c4681b0ff962ece29ff306dfc8"
	if hex.EncodeToString(proofs[0].StateRoot[:]) != expectedStorRoot {
		t.Errorf("storage root mismatch:\n  got  %s\n  want %s",
			hex.EncodeToString(proofs[0].StateRoot[:]), expectedStorRoot)
	}
}

func TestProduction_LatestProof_Bundle(t *testing.T) {
	g := openProductionGenerator(t)

	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)
	var slot0 [32]byte

	bundle, err := g.LatestProof(usdc, [][32]byte{slot0})
	if err != nil {
		t.Fatalf("LatestProof: %v", err)
	}
	if bundle.Account == nil || bundle.Account.Address != usdc {
		t.Fatal("account section missing or wrong addr")
	}
	if len(bundle.Storages) != 1 {
		t.Fatalf("expected 1 storage proof, got %d", len(bundle.Storages))
	}
	t.Logf("bundle: account-hops=%d storage-hops=%d",
		len(bundle.Account.Walk.Hops), len(bundle.Storages[0].Walk.Hops))
}

func TestProduction_HistoricalAccountValue_USDC(t *testing.T) {
	g := openProductionGenerator(t)
	if g.history == nil {
		t.Skip("history not configured")
	}

	var usdc [20]byte
	b, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	copy(usdc[:], b)

	// USDC was deployed at block 6082465. Before that, should be absent.
	v, found, err := g.HistoricalAccountValue(usdc, 5_000_000)
	if err != nil {
		t.Fatalf("Historical at 5M: %v", err)
	}
	t.Logf("USDC @ 5M:  found=%v len=%d", found, len(v))
	if found {
		t.Logf("  (note: present at 5M means OldValue at first modification ≤ 5M)")
	}

	// At 25M should definitely have history (account-state changed at
	// deployment at minimum).
	v, found, err = g.HistoricalAccountValue(usdc, 25_000_000)
	if err != nil {
		t.Fatalf("Historical at 25M: %v", err)
	}
	t.Logf("USDC @ 25M: found=%v len=%d", found, len(v))
	if !found {
		t.Error("USDC should have history at block 25M")
	}
}
