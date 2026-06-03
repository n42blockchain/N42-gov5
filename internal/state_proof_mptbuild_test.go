package internal

import (
	"context"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/mptbuild"
	"github.com/n42blockchain/N42/internal/mptproof"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// buildTinyGeneratorForProvider creates a synthetic 50-acct, 30-stor
// MDBX env via mptbuild + wraps it in an mptproof.Generator suitable
// for MPTProofProvider tests.
func buildTinyGeneratorForProvider(t *testing.T) (*mptproof.Generator, [][20]byte, map[[20]byte][]byte) {
	t.Helper()
	tmp := t.TempDir()
	acctDir := filepath.Join(tmp, "acct-mptcache")
	storDir := filepath.Join(tmp, "stor-mptcache")

	addrs := make([][20]byte, 50)
	values := make(map[[20]byte][]byte, 50)
	acctEntries := make([][2][]byte, 50)
	for i := 0; i < 50; i++ {
		var a [20]byte
		a[0] = byte(uint32(i) >> 24)
		a[1] = byte(uint32(i) >> 16)
		a[2] = byte(uint32(i) >> 8)
		a[3] = byte(uint32(i))
		addrs[i] = a
		v := make([]byte, 64)
		for k := range v {
			v[k] = byte((i+k)*7 ^ 0x55)
		}
		values[a] = v
		acctEntries[i] = [2][]byte{a[:], v}
	}

	acctTgt := &mptbuild.MDBXTarget{
		DBPath: acctDir, Table: "AccountsTrie", MapSizeGB: 1,
	}
	if _, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: acctEntries},
		Target:    acctTgt,
		Extractor: mptbuild.NewAccountExtractor(),
		TmpDir:    filepath.Join(tmp, "etl-acct"),
		BufMB:     1,
	}); err != nil {
		acctTgt.Close()
		t.Fatalf("build accounts: %v", err)
	}
	acctTgt.Close()

	// Build a real storage trie + matching MapLeafSource so the
	// storage-proof test exercises a non-empty walk.
	storEntries := make([][2][]byte, 0, 30)
	storMap := make(map[[20]byte]map[[32]byte][]byte)
	for i := 0; i < 30; i++ {
		ai := i % 10 // distribute slots across first 10 addrs
		addr := addrs[ai]
		var slot [32]byte
		slot[31] = byte(i + 1) // unique per slot
		// Storage value: 64 bytes so the leaf is hashed (not inlined)
		val := make([]byte, 64)
		for k := range val {
			val[k] = byte(i*17 + k)
		}
		// PlainStorageState DupSort: key=addr, value=slot32 || val
		v := append(append([]byte{}, slot[:]...), val...)
		storEntries = append(storEntries, [2][]byte{addr[:], v})

		if storMap[addr] == nil {
			storMap[addr] = make(map[[32]byte][]byte)
		}
		storMap[addr][slot] = val
	}
	storTgt := &mptbuild.MDBXTarget{
		DBPath: storDir, Table: "StoragesTrie", MapSizeGB: 1,
	}
	if _, err := mptbuild.Build(context.Background(), mptbuild.Opts{
		Source:    &mptbuild.MapSource{Entries: storEntries},
		Target:    storTgt,
		Extractor: mptbuild.NewStorageExtractor(),
		TmpDir:    filepath.Join(tmp, "etl-stor"),
		BufMB:     1,
	}); err != nil {
		storTgt.Close()
		t.Fatalf("build storage: %v", err)
	}
	storTgt.Close()

	leaves := &mptproof.MapLeafSource{Accounts: values, Storage: storMap}
	gen, err := mptproof.New(mptproof.Config{
		AccountsTrieDir: acctDir,
		StorageTrieDir:  storDir,
		Leaves:          leaves,
	})
	if err != nil {
		t.Fatalf("New mptproof.Generator: %v", err)
	}
	t.Cleanup(func() { gen.Close() })
	return gen, addrs, values
}

func TestMPTProofProvider_Descriptor(t *testing.T) {
	gen, _, _ := buildTinyGeneratorForProvider(t)
	p := NewMPTProofProvider(gen)
	desc := p.Descriptor()
	if desc.Backend != StateProofBackendEthereumMPT {
		t.Errorf("backend: got %v want EthereumMPT", desc.Backend)
	}
	if desc.Semantics != StateProofSemanticsCanonicalEIP1186 {
		t.Errorf("semantics: got %v want canonical-eip1186", desc.Semantics)
	}
	if desc.StorageHash != StorageHashSemanticsCanonicalTrieRoot {
		t.Errorf("storage hash: got %v want canonical-trie-root", desc.StorageHash)
	}
	if !desc.SupportsHistorical {
		t.Error("MPT provider should advertise historical state-as-of support")
	}
}

func TestMPTProofProvider_AccountProof_Latest(t *testing.T) {
	gen, addrs, _ := buildTinyGeneratorForProvider(t)
	p := NewMPTProofProvider(gen)

	addr20 := addrs[7]
	var addr types.Address
	copy(addr[:], addr20[:])

	pb, err := p.AccountProof(nil, addr,
		jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber))
	if err != nil {
		t.Fatalf("AccountProof: %v", err)
	}
	if len(pb) == 0 {
		t.Fatal("AccountProof returned 0 nodes")
	}
	t.Logf("proof: %d nodes", len(pb))
	for i, s := range pb {
		if !strings.HasPrefix(s, "0x") {
			t.Errorf("node %d missing 0x prefix: %q", i, s[:min(16, len(s))])
		}
		raw, err := hex.DecodeString(strings.TrimPrefix(s, "0x"))
		if err != nil {
			t.Errorf("node %d not valid hex: %v", i, err)
			continue
		}
		t.Logf("  node %d: %d bytes", i, len(raw))
	}
}

func TestMPTProofProvider_HistoricalAcceptsBlock(t *testing.T) {
	// Phase D.4: AccountProof for historical blocks now returns the
	// LATEST trie's proof (verifies against current stateRoot) rather
	// than failing — caller cross-references with HistoricalAccountValue
	// for the as-of value. True block-N Merkle proof requires Phase D.4.1
	// full-rebuild work.
	gen, addrs, _ := buildTinyGeneratorForProvider(t)
	p := NewMPTProofProvider(gen)

	var addr types.Address
	copy(addr[:], addrs[0][:])

	pb, err := p.AccountProof(nil, addr,
		jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.BlockNumber(100)))
	if err != nil {
		t.Fatalf("historical AccountProof: %v", err)
	}
	if len(pb) == 0 {
		t.Error("expected non-empty proof")
	}
	t.Logf("historical block-100 returns latest proof (%d nodes); cross-ref via HistoricalAccountValue", len(pb))
}

func TestMPTProofProvider_StorageProof_Latest(t *testing.T) {
	gen, addrs, _ := buildTinyGeneratorForProvider(t)
	p := NewMPTProofProvider(gen)

	// addrs[0] gets slots at i=0, 10, 20 (per buildTiny's distribution).
	// Use slot[31]=1 which is the i=0 case for addrs[0].
	var addr types.Address
	copy(addr[:], addrs[0][:])
	var slot types.Hash
	slot[31] = 0x01

	pb, err := p.StorageProof(nil, addr, slot,
		jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber))
	if err != nil {
		t.Fatalf("StorageProof: %v", err)
	}
	if len(pb) == 0 {
		t.Fatal("StorageProof returned 0 nodes")
	}
	t.Logf("storage proof: %d nodes, total %d hex chars",
		len(pb), totalLen(pb))
}

func totalLen(pb []string) int {
	total := 0
	for _, s := range pb {
		total += len(s)
	}
	return total
}

func TestMPTProofProvider_StorageHash_ReturnsTrieRoot(t *testing.T) {
	gen, addrs, _ := buildTinyGeneratorForProvider(t)
	p := NewMPTProofProvider(gen)

	var addr types.Address
	copy(addr[:], addrs[0][:])

	root, err := p.StorageHash(nil, addr, nil, nil,
		jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber))
	if err != nil {
		t.Fatalf("StorageHash: %v", err)
	}
	if root == (types.Hash{}) {
		t.Error("StorageHash returned zero root")
	}
	t.Logf("storage trie root: 0x%x", root[:8])

	// Should match Generator's StorageTrieRoot directly.
	want, err := gen.StorageTrieRoot()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 32; i++ {
		if root[i] != want[i] {
			t.Errorf("byte %d: got %02x want %02x", i, root[i], want[i])
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
