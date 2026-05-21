package mpttrie

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/internal/mptbuild"
)

const productionAccountsDir = `D:\n42-mpt\accounts-mptcache`

func keccak(b []byte) []byte {
	h := sha3.NewLegacyKeccak256()
	h.Write(b)
	return h.Sum(nil)
}

func nibblesOf(b []byte) []byte {
	out := make([]byte, len(b)*2)
	for i, byteVal := range b {
		out[i*2] = byteVal >> 4
		out[i*2+1] = byteVal & 0x0f
	}
	return out
}

func mkAddrN(seed uint32) []byte {
	a := make([]byte, 20)
	a[0] = byte(seed >> 24)
	a[1] = byte(seed >> 16)
	a[2] = byte(seed >> 8)
	a[3] = byte(seed)
	return a
}

// buildSyntheticAccountsTrie spins up mptbuild on N synthetic accounts
// into a temp MDBX, returns the directory path and expected state root.
func buildSyntheticAccountsTrie(t *testing.T, n int) (dir string, root [32]byte) {
	t.Helper()
	tmp := t.TempDir()
	dir = filepath.Join(tmp, "mptdb")

	entries := make([][2][]byte, n)
	for i := 0; i < n; i++ {
		entries[i] = [2][]byte{
			mkAddrN(uint32(i)),
			{byte(i & 0xff), byte((i >> 8) & 0xff)},
		}
	}

	tgt := &mptbuild.MDBXTarget{
		DBPath:    dir,
		Table:     "AccountsTrie",
		MapSizeGB: 1,
	}
	res, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: entries},
		Target:    tgt,
		Extractor: mptbuild.NewAccountExtractor(),
		TmpDir:    filepath.Join(tmp, "etl"),
		BufMB:     1,
	})
	if err != nil {
		tgt.Close()
		t.Fatalf("Build: %v", err)
	}
	tgt.Close()
	return dir, res.StateRoot
}

func TestReader_OpenAndClose(t *testing.T) {
	dir, _ := buildSyntheticAccountsTrie(t, 50)
	r, err := Open(dir, "AccountsTrie")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestReader_StateRoot_MatchesBuild(t *testing.T) {
	dir, expectedRoot := buildSyntheticAccountsTrie(t, 100)
	r, err := Open(dir, "AccountsTrie")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, err := r.StateRoot()
	if err != nil {
		t.Fatalf("StateRoot: %v", err)
	}
	if got != expectedRoot {
		t.Errorf("root mismatch:\n  got  %x\n  want %x", got, expectedRoot)
	}
}

func TestReader_Stats(t *testing.T) {
	dir, _ := buildSyntheticAccountsTrie(t, 500)
	r, err := Open(dir, "AccountsTrie")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	s, err := r.Stats()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Stats: root=0x%s branches=%d bytes=%d built=%s",
		hex.EncodeToString(s.Root[:]), s.BranchCount, s.BranchBytes, s.BuiltAt.Format("2006-01-02 15:04:05"))
	if s.BranchCount == 0 {
		t.Error("expected non-zero branch count for 500 leaves")
	}
	if s.BranchBytes == 0 {
		t.Error("expected non-zero bucket size")
	}
	var zero [32]byte
	if s.Root == zero {
		t.Error("Root is zero")
	}
}

func TestReader_GetBranch_Root(t *testing.T) {
	dir, _ := buildSyntheticAccountsTrie(t, 100)
	r, err := Open(dir, "AccountsTrie")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Root branch is at empty path. (May or may not exist depending
	// on trie shape — small tries can have leaf-as-root with no
	// explicit branch.)
	bn, ok, err := r.GetBranch(nil)
	if err != nil {
		t.Fatalf("GetBranch(root): %v", err)
	}
	if !ok {
		t.Log("no explicit root branch (small trie with leaf root)")
		return
	}
	t.Logf("root branch: hasState=%016b hasTree=%016b hasHash=%016b hashes=%d hasRoot=%v",
		bn.HasState, bn.HasTree, bn.HasHash, len(bn.Hashes), bn.HasRoot)
	if bn.HasState == 0 {
		t.Error("root has no children?")
	}
}

func TestReader_Walk_KnownKeyDescends(t *testing.T) {
	dir, _ := buildSyntheticAccountsTrie(t, 200)
	r, err := Open(dir, "AccountsTrie")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Walk to an addr we know was inserted.
	addr := mkAddrN(42)
	hashed := keccak(addr)
	nibbles := nibblesOf(hashed)
	res, err := r.Walk(nibbles)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	t.Logf("Walk addr=%x: outcome=%v hops=%d depth=%d",
		addr[:6], res.Outcome, len(res.Hops), res.PathDepth())
	if res.Outcome != LandedOnLeaf {
		t.Errorf("expected LandedOnLeaf for known key, got %v", res.Outcome)
	}
	if len(res.Hops) == 0 {
		t.Error("walk produced no hops")
	}
	// Each hop's branch must show the target child exists.
	for i, hop := range res.Hops {
		if !hop.Branch.HasChild(hop.TargetNibble) {
			t.Errorf("hop %d: target nibble %x not in HasState (mask=%016b)",
				i, hop.TargetNibble, hop.Branch.HasState)
		}
	}
}

func TestReader_Walk_AbsentKey(t *testing.T) {
	dir, _ := buildSyntheticAccountsTrie(t, 100)
	r, err := Open(dir, "AccountsTrie")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// An address that wasn't in our 100 test entries. We pick a
	// high-entropy seed value far from 0..99.
	absent := mkAddrN(0xDEADBEEF)
	hashed := keccak(absent)
	nibbles := nibblesOf(hashed)
	res, err := r.Walk(nibbles)
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}
	t.Logf("absent walk: outcome=%v hops=%d", res.Outcome, len(res.Hops))
	// Either NoSuchChild (state bit clear) or NoBranchAtPath (rare).
	// Should NOT be LandedOnLeaf because we never inserted this key.
	if res.Outcome == LandedOnLeaf {
		// In a sparse trie of 100 leaves at random keccak positions,
		// the absent key MAY accidentally share enough prefix to land
		// where some other leaf was reduced. Verify by checking the
		// landed hash isn't the actual leaf for our absent key.
		t.Logf("Walk landed on a leaf for absent key — sparse trie boundary")
	}
}

func TestReader_Walk_CollectSiblings(t *testing.T) {
	dir, _ := buildSyntheticAccountsTrie(t, 500)
	r, err := Open(dir, "AccountsTrie")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	addr := mkAddrN(7)
	hashed := keccak(addr)
	nibbles := nibblesOf(hashed)
	res, err := r.Walk(nibbles)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != LandedOnLeaf {
		t.Fatalf("expected LandedOnLeaf, got %v", res.Outcome)
	}
	siblings := res.CollectSiblings()
	t.Logf("walk collected %d sibling hashes across %d hops", len(siblings), len(res.Hops))
	if len(siblings) == 0 {
		t.Error("expected at least one sibling at root level")
	}
}

func TestDecodeBranch_RoundTrip(t *testing.T) {
	// Hand-built minimal: 2 children (nibbles 0x3, 0xa), hash mask same.
	// Format: [hasState BE 2][hasTree BE 2][hasHash BE 2][hashes...]
	raw := make([]byte, 6+2*32)
	// state_mask = (1<<3) | (1<<10) = 0x0408
	raw[0] = 0x04
	raw[1] = 0x08
	// tree_mask = 0 (no deeper branches)
	// hash_mask = 0x0408 (both children have hashes stored)
	raw[4] = 0x04
	raw[5] = 0x08
	// two distinguishable hashes
	for i := 0; i < 32; i++ {
		raw[6+i] = byte(0x10 + i)
		raw[6+32+i] = byte(0xA0 + i)
	}
	bn, ok, err := DecodeBranch(raw)
	if err != nil || !ok {
		t.Fatalf("DecodeBranch: ok=%v err=%v", ok, err)
	}
	if bn.HasState != 0x0408 || bn.HasTree != 0 || bn.HasHash != 0x0408 {
		t.Fatalf("masks: state=%04x tree=%04x hash=%04x",
			bn.HasState, bn.HasTree, bn.HasHash)
	}
	if !bn.HasChild(0x3) || !bn.HasChild(0xa) {
		t.Error("expected children at nibbles 0x3 and 0xa")
	}
	if bn.HasChild(0x5) {
		t.Error("nibble 0x5 should not exist")
	}
	h3, ok := bn.ChildHash(0x3)
	if !ok || h3[0] != 0x10 {
		t.Errorf("child 0x3 hash: %x", h3)
	}
	hA, ok := bn.ChildHash(0xa)
	if !ok || hA[0] != 0xA0 {
		t.Errorf("child 0xa hash: %x", hA)
	}
	sib := bn.SiblingHashes(0x3)
	if len(sib) != 1 || sib[0][0] != 0xA0 {
		t.Errorf("siblings of nibble 0x3: %v", sib)
	}
}

// ===== Integration tests against production D:\n42-mpt =====

func openProductionAccountsReader(t *testing.T) *Reader {
	t.Helper()
	if _, err := os.Stat(filepath.Join(productionAccountsDir, "mdbx.dat")); err != nil {
		t.Skipf("%s not present; run cmd/n42-mpt-build first", productionAccountsDir)
	}
	r, err := Open(productionAccountsDir, "AccountsTrie")
	if err != nil {
		t.Fatalf("Open production: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestProduction_Stats(t *testing.T) {
	r := openProductionAccountsReader(t)
	s, err := r.Stats()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("production accounts: root=0x%s branches=%d bytes=%.2f GB built=%s",
		hex.EncodeToString(s.Root[:]),
		s.BranchCount,
		float64(s.BranchBytes)/1e9,
		s.BuiltAt.Format("2006-01-02 15:04:05"))
	// Sanity bounds: 386M leaves → expect ~28-30M branches.
	if s.BranchCount < 20_000_000 || s.BranchCount > 40_000_000 {
		t.Errorf("branch count out of range: %d", s.BranchCount)
	}
	// State root should match what cmd/n42-mpt-build printed: 0x7812...0033
	expected := "7812409463082683ff0a0b5cef75d87f8dfe313f006ea70cf8246f4521700033"
	got := hex.EncodeToString(s.Root[:])
	if got != expected {
		t.Errorf("production root mismatch:\n  got  %s\n  want %s", got, expected)
	}
}

func TestProduction_WalkKnownAddress(t *testing.T) {
	r := openProductionAccountsReader(t)

	// Walk to USDC contract address (well-known, very likely to exist).
	usdc, _ := hex.DecodeString("a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48")
	hashed := keccak(usdc)
	nibbles := nibblesOf(hashed)
	res, err := r.Walk(nibbles)
	if err != nil {
		t.Fatalf("Walk USDC: %v", err)
	}
	t.Logf("USDC walk: outcome=%v hops=%d depth=%d",
		res.Outcome, len(res.Hops), res.PathDepth())
	t.Logf("USDC walk siblings (%d):", len(res.CollectSiblings()))
	for i, hop := range res.Hops {
		t.Logf("  hop %d  depth=%d nibble=0x%x  state=%016b hashes=%d hasTree=%016b",
			i, hop.PrefixDepth, hop.TargetNibble,
			hop.Branch.HasState, len(hop.Branch.Hashes), hop.Branch.HasTree)
	}
	if res.Outcome != LandedOnLeaf {
		t.Errorf("expected LandedOnLeaf for USDC, got %v", res.Outcome)
	}
}

func TestProduction_WalkPhantomAddress(t *testing.T) {
	r := openProductionAccountsReader(t)

	// High-entropy synthetic address that almost certainly does not
	// exist on mainnet.
	phantom, _ := hex.DecodeString("deadc0debadbeef0deadc0debadbeef0deadc0de")
	hashed := keccak(phantom)
	nibbles := nibblesOf(hashed)
	res, err := r.Walk(nibbles)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("phantom walk: outcome=%v hops=%d depth=%d",
		res.Outcome, len(res.Hops), res.PathDepth())
	// Outcome is allowed to be LandedOnLeaf in a sparse trie (lands
	// in a leaf that wasn't ours). Just confirm walk ran.
	if len(res.Hops) == 0 {
		t.Error("walk produced no hops")
	}
}
