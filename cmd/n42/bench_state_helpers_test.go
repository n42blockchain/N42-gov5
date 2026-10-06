package main

import (
	"math"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/replay"
	"github.com/n42blockchain/N42/lib/bmt"
)

func TestBenchStateSafeDiv(t *testing.T) {
	if got := safeDiv(10, 2); got != 5 {
		t.Fatalf("safeDiv(10,2) = %v, want 5", got)
	}
	if got := safeDiv(10, 0); got != 0 {
		t.Fatalf("safeDiv(10,0) = %v, want 0", got)
	}
}

func TestBenchStateAvg(t *testing.T) {
	if got := avg(nil); got != 0 {
		t.Fatalf("avg(nil) = %v, want 0", got)
	}
	if got := avg([]int{1, 2, 3}); got != 2 {
		t.Fatalf("avg([1,2,3]) = %v, want 2", got)
	}
}

func TestBenchStateEntryBlake3Key(t *testing.T) {
	addr := types.Address{0x01, 0x02}
	accEntry := replay.LeafEntry{Tag: replay.TagAccount, Address: addr}
	accKey := entryBlake3Key(accEntry)

	slot := types.Hash{0x03}
	stoEntry := replay.LeafEntry{Tag: replay.TagStorage, Address: addr, Slot: slot}
	stoKey := entryBlake3Key(stoEntry)

	if accKey == stoKey {
		t.Fatal("account and storage keys for the same address must differ")
	}
	// Deterministic: same input, same output.
	if entryBlake3Key(accEntry) != accKey {
		t.Fatal("entryBlake3Key is not deterministic")
	}
}

func TestBenchStateToVerkleKey(t *testing.T) {
	var k1, k2 [32]byte
	k1[0] = 1
	k2[0] = 2
	v1 := toVerkleKey(k1)
	v2 := toVerkleKey(k2)
	if v1 == v2 {
		t.Fatal("distinct inputs produced the same verkle key")
	}
	if toVerkleKey(k1) != v1 {
		t.Fatal("toVerkleKey is not deterministic")
	}
}

func TestBenchStateVerkleDepthEst(t *testing.T) {
	if got := verkleDepthEst(100); got != 1 {
		t.Fatalf("verkleDepthEst(100) = %v, want 1", got)
	}
	if got := verkleDepthEst(256); got != 1 {
		t.Fatalf("verkleDepthEst(256) = %v, want 1", got)
	}
	got := verkleDepthEst(256 * 256)
	want := math.Ceil(math.Log(float64(256*256)) / math.Log(256))
	if got != want {
		t.Fatalf("verkleDepthEst(65536) = %v, want %v", got, want)
	}
}

func TestBenchStateVerkleProofSizeEst(t *testing.T) {
	got := verkleProofSizeEst(100)
	if got != 544+1*32+1 {
		t.Fatalf("verkleProofSizeEst(100) = %d, want %d", got, 544+1*32+1)
	}
}

func TestBenchStatePathTree16(t *testing.T) {
	tr := newPathTree16()
	entries := []bmt.BatchEntry{
		{Key: [32]byte{1}, Value: []byte("a")},
		{Key: [32]byte{2}, Value: []byte("bb")},
	}
	tr.putBatch(entries)
	if tr.depth() < 1 {
		t.Fatalf("depth = %d, want >= 1", tr.depth())
	}
	if tr.proofSizeEst() != tr.depth()*15*32 {
		t.Fatalf("proofSizeEst mismatch")
	}
	if tr.totalNodes == 0 {
		t.Fatal("expected totalNodes to accumulate")
	}
}

func TestBenchStatePathTree2(t *testing.T) {
	tr := newPathTree2()
	entries := []bmt.BatchEntry{
		{Key: [32]byte{1}, Value: []byte("a")},
	}
	tr.putBatch(entries)
	if tr.depth() != 1 {
		t.Fatalf("depth with a single leaf = %d, want 1", tr.depth())
	}
	if tr.proofSizeEst() != 32 {
		t.Fatalf("proofSizeEst = %d, want 32", tr.proofSizeEst())
	}
}

func TestBenchStateKZGTree(t *testing.T) {
	tr := newKZGTree()
	entries := []bmt.BatchEntry{
		{Key: [32]byte{1}, Value: []byte("a")},
	}
	tr.putBatch(entries)
	if tr.depth() != 1 {
		t.Fatalf("depth with a single leaf = %d, want 1", tr.depth())
	}
	if tr.proofSizeEst() != 96 {
		t.Fatalf("proofSizeEst = %d, want 96", tr.proofSizeEst())
	}
	if tr.cryptoTimeEstUs() != 400.0 {
		t.Fatalf("cryptoTimeEstUs = %v, want 400", tr.cryptoTimeEstUs())
	}
}

func TestBenchStateBranchStoreAndContext(t *testing.T) {
	bs := &benchBranchStore{data: make(map[string][]byte)}
	bs.Put([]byte("k1"), []byte("v1"))
	got, err := bs.Get([]byte("k1"))
	if err != nil || string(got) != "v1" {
		t.Fatalf("Get after Put = %q, %v", got, err)
	}
	if missing, _ := bs.Get([]byte("missing")); missing != nil {
		t.Fatalf("Get(missing) = %v, want nil", missing)
	}
	if bs.CurrentBytes() != int64(len("k1")+len("v1")) {
		t.Fatalf("CurrentBytes = %d, want %d", bs.CurrentBytes(), len("k1")+len("v1"))
	}
	if bs.histNodes != 1 {
		t.Fatalf("histNodes = %d, want 1", bs.histNodes)
	}

	ctx := &benchContext{branches: bs, state: map[string][]byte{"acct": {0x01}}}
	d, ver, err := ctx.Branch([]byte("k1"))
	if err != nil || string(d) != "v1" || ver != 0 {
		t.Fatalf("Branch = %q, %d, %v", d, ver, err)
	}
	if err := ctx.PutBranch([]byte("k2"), []byte("v2"), nil); err != nil {
		t.Fatalf("PutBranch error: %v", err)
	}
	if got2, _ := bs.Get([]byte("k2")); string(got2) != "v2" {
		t.Fatalf("PutBranch did not persist, got %q", got2)
	}

	u, err := ctx.Account([]byte("acct"))
	if err != nil {
		t.Fatalf("Account error: %v", err)
	}
	if u.Nonce != 1 {
		t.Fatalf("Account nonce = %d, want 1", u.Nonce)
	}

	delU, err := ctx.Account([]byte("absent"))
	if err != nil {
		t.Fatalf("Account(absent) error: %v", err)
	}
	if delU.Flags == 0 {
		t.Fatal("expected DeleteUpdate flag for absent account")
	}

	stoU, err := ctx.Storage([]byte("anything"))
	if err != nil || stoU.Flags == 0 {
		t.Fatalf("Storage() = %v, %v, want DeleteUpdate", stoU, err)
	}

	if ctx.TxNum() != 0 {
		t.Fatalf("TxNum = %d, want 0", ctx.TxNum())
	}
}
