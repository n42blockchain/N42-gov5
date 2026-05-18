package history

import (
	"bytes"
	"fmt"
	"math/rand"
	"os"
	"testing"
)

func TestMPHFWriterReaderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	prefix := "mphf-test"
	pageSize := 4
	const N = 50

	// Build deterministic keys + blobs.
	keys := make([][]byte, N)
	blobs := make(map[string][]byte, N)
	for i := 0; i < N; i++ {
		k := make([]byte, 20)
		k[0] = byte(i)
		k[19] = byte(0xaa ^ (i << 1))
		keys[i] = k
		blob := []byte(fmt.Sprintf("blob-%03d-payload-%d", i, i*7))
		blobs[string(k)] = blob
	}

	w, err := NewMPHFWriter(MPHFWriterOpts{
		BaseDir: dir, Prefix: prefix, PageSize: pageSize,
		TmpDir: dir + "/etl", KeyCount: N, EtlBufMB: 1,
	})
	if err != nil {
		t.Fatalf("NewMPHFWriter: %v", err)
	}
	for _, k := range keys {
		if err := w.Append(k, blobs[string(k)]); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	st := w.Stats()
	t.Logf("MPHF Stats: keys=%d pages=%d kv=%dB idx=%dB mphf=%dB", st.KeyCount, st.PageCount, st.KvSize, st.IdxSize, st.MphfSize)

	r, err := OpenMPHF(dir, prefix)
	if err != nil {
		t.Fatalf("OpenMPHF: %v", err)
	}
	defer r.Close()
	if r.KeyCount() != N {
		t.Errorf("KeyCount: got %d, want %d", r.KeyCount(), N)
	}

	// Every inserted key must round-trip.
	for _, k := range keys {
		got, ok, err := r.Get(k)
		if err != nil {
			t.Errorf("Get %x: %v", k, err)
			continue
		}
		if !ok {
			t.Errorf("Get %x: not found", k)
			continue
		}
		if !bytes.Equal(got, blobs[string(k)]) {
			t.Errorf("Get %x: got %q, want %q", k, got, blobs[string(k)])
		}
	}

	// Phantom keys: random 20B keys that aren't in the set. MPHF gives
	// them SOME ordinal; reader must reject via fp mismatch.
	rng := rand.New(rand.NewSource(42))
	var phantomCorrect, phantomBad int
	for i := 0; i < 200; i++ {
		var k [20]byte
		rng.Read(k[:])
		k[0] = 0xff // ensure not in our deterministic set
		if _, dup := blobs[string(k[:])]; dup {
			continue
		}
		got, ok, err := r.Get(k[:])
		if err != nil {
			t.Errorf("Get phantom %x: err %v", k, err)
			continue
		}
		if ok {
			phantomBad++
			t.Logf("phantom hit (acceptable at ~2^-32 rate): %x → %q", k, got)
		} else {
			phantomCorrect++
		}
		_ = got
	}
	t.Logf("phantom: %d correctly rejected, %d hits (fp collision)", phantomCorrect, phantomBad)
	if phantomBad > 2 {
		// Expected ~1 in 2^32 = vanishingly rare; >2 in 200 trials means our fp logic is broken.
		t.Errorf("too many phantom hits: %d/200 (expected ~0)", phantomBad)
	}
}

func TestMPHFMagicGuard(t *testing.T) {
	dir := t.TempDir()
	// Write a plain (non-MPHF) history file then try opening as MPHF.
	w, err := NewWriter(dir, "plain", 4, 4)
	if err != nil {
		t.Fatal(err)
	}
	w.Append([]byte{0, 0, 0, 1}, []byte{1})
	w.Close()
	// Plain mode produces .kv + .idx but no .mphf, so OpenMPHF will fail
	// either on "no .mphf" or on the kv magic mismatch (KVMagic != MPHFKVMagic).
	if _, err := OpenMPHF(dir, "plain"); err == nil {
		t.Errorf("expected magic-mismatch / missing-mphf error")
	} else {
		t.Logf("got expected error: %v", err)
	}
}

func TestMPHFSizeBudget(t *testing.T) {
	// Realistic: 10K storage keys (52B each), avg blob 30B.
	// Without MPHF: page entry = 52B key + 1B varint blobLen + 30B blob = 83B
	// With MPHF:    page entry = 4B fp + 1B varint blobLen + 30B blob = 35B
	// Savings: 48B/key on raw page bytes. Plus 1.7-3 bits/key MPHF.
	const N = 10_000
	dir := t.TempDir()
	w, err := NewMPHFWriter(MPHFWriterOpts{
		BaseDir: dir, Prefix: "bench", PageSize: 64,
		TmpDir: dir + "/etl", KeyCount: N, EtlBufMB: 32,
	})
	if err != nil {
		t.Fatal(err)
	}

	rng := rand.New(rand.NewSource(1))
	for i := 0; i < N; i++ {
		var k [52]byte
		rng.Read(k[:])
		blob := make([]byte, 20+rng.Intn(20))
		rng.Read(blob)
		if err := w.Append(k[:], blob); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	st := w.Stats()
	totalB := st.KvSize + st.IdxSize + st.MphfSize

	// Plain (non-MPHF) for comparison: key bytes alone = N × 52B = 520 KB
	// (uncompressed). Plain coldstore compresses this with zstd-page,
	// but random keys don't dedup well → expect ~500 KB.
	plainKeyOnly := uint64(N * 52)

	bpk := float64(totalB) / float64(N)
	t.Logf("MPHF total: %d B (kv=%d idx=%d mphf=%d) = %.2f B/key",
		totalB, st.KvSize, st.IdxSize, st.MphfSize, bpk)
	t.Logf("Plain key bytes alone (no blob, no zstd): %d (%.2f B/key)",
		plainKeyOnly, float64(plainKeyOnly)/float64(N))

	// Sanity: with 30B avg blob + 4B fp + 1B vlen = 35B/entry pre-zstd,
	// + tiny MPHF overhead, total should be well under (52 + 30) = 82 B/key.
	if bpk > 60 {
		t.Errorf("MPHF too large: %.2f B/key (random data, expected < 60)", bpk)
	}
}

func TestMPHFEmpty(t *testing.T) {
	dir := t.TempDir()
	// MPHF requires KeyCount > 0; verify single-key handling.
	w, err := NewMPHFWriter(MPHFWriterOpts{
		BaseDir: dir, Prefix: "single", PageSize: 16,
		TmpDir: dir + "/etl", KeyCount: 1, EtlBufMB: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	k := []byte{0x42, 0x42, 0x42, 0x42}
	v := []byte{0xff, 0xff}
	if err := w.Append(k, v); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenMPHF(dir, "single")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	got, ok, err := r.Get(k)
	if err != nil || !ok || !bytes.Equal(got, v) {
		t.Errorf("Get single: got %x ok=%v err=%v", got, ok, err)
	}
}

func TestMPHFLargeBlob(t *testing.T) {
	dir := t.TempDir()
	const N = 10
	w, err := NewMPHFWriter(MPHFWriterOpts{
		BaseDir: dir, Prefix: "big", PageSize: 4,
		TmpDir: dir + "/etl", KeyCount: N, EtlBufMB: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	keys := make([][]byte, N)
	blobs := make([][]byte, N)
	for i := 0; i < N; i++ {
		k := make([]byte, 20)
		k[0] = byte(i)
		keys[i] = k
		blob := make([]byte, 1000+i*100)
		for j := range blob {
			blob[j] = byte(i ^ (j & 0xff))
		}
		blobs[i] = blob
		if err := w.Append(k, blob); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := OpenMPHF(dir, "big")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for i, k := range keys {
		got, ok, err := r.Get(k)
		if err != nil || !ok {
			t.Errorf("Get %x: err=%v ok=%v", k, err, ok)
			continue
		}
		if !bytes.Equal(got, blobs[i]) {
			t.Errorf("Get %x: blob mismatch (len %d vs %d)", k, len(got), len(blobs[i]))
		}
	}
}

func TestMPHFFilesExist(t *testing.T) {
	dir := t.TempDir()
	w, err := NewMPHFWriter(MPHFWriterOpts{
		BaseDir: dir, Prefix: "files", PageSize: 8,
		TmpDir: dir + "/etl", KeyCount: 5, EtlBufMB: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		k := []byte{byte(i), 0, 0, 0}
		w.Append(k, []byte{byte(i)})
	}
	w.Close()

	for _, ext := range []string{".mphf", ".kv", ".idx"} {
		p := dir + "/files" + ext
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("missing %s: %v", ext, err)
			continue
		}
		t.Logf("%s: %d B", ext, info.Size())
		if info.Size() == 0 {
			t.Errorf("%s is empty", ext)
		}
	}
}
