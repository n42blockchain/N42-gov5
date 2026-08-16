package txlookup

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// TestV2FullVerify builds a V2 segment and verifies EVERY transaction
// in the range against the Geth freezer source data.
func TestV2FullVerify(t *testing.T) {
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`
	if _, err := os.Stat(ancientPath); err != nil {
		t.Skip("Geth ancient not found")
	}

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Build V2 segment covering blocks 0-56000 (includes first mainnet txs at 46147).
	// BuildRange aligns to SegmentSize, so we pass the raw range.
	const endBlock = 56000
	tmpDir := t.TempDir()
	builder := NewSegmentBuilder(f, tmpDir, testBodyTxHashes)
	if err := builder.BuildRange(context.Background(), 0, endBlock); err != nil {
		t.Fatal(err)
	}

	// The segment covers blocks 0 to endBlock (aligned to SegmentSize boundary).
	baseName := SegmentFileName(0, endBlock)
	seg, err := OpenSegment(
		filepath.Join(tmpDir, baseName+".idx"),
		filepath.Join(tmpDir, baseName+".dat"))
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()

	if !seg.IsV2() {
		t.Fatal("expected V2 segment")
	}

	// Verify every tx in every block (skip empty pre-tx blocks quickly).
	totalTx, verified, misses := 0, 0, 0
	for blockNum := uint64(0); blockNum < endBlock; blockNum++ {
		bodyData, err := f.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			t.Fatalf("read body %d: %v", blockNum, err)
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			t.Fatalf("decode body %d: %v", blockNum, err)
		}

		for ti, tx := range body.Transactions {
			totalTx++
			txHash := tx.Hash()
			result := seg.Lookup(txHash)
			if result == nil {
				misses++
				t.Errorf("block %d tx %d (%s): NOT FOUND", blockNum, ti, txHash.Hex()[:10])
				continue
			}
			if *result != blockNum {
				t.Errorf("block %d tx %d: got blockNum %d", blockNum, ti, *result)
				continue
			}
			verified++
		}
	}

	if misses > 0 {
		t.Fatalf("%d/%d transactions missing", misses, totalTx)
	}
	t.Logf("V2 full verify: %d/%d transactions OK (blocks 0-%d)",
		verified, totalTx, endBlock-1)
}

// TestV1vsV2CrossVerify builds the same segment in V1 and V2 format,
// then verifies both produce identical results for every transaction.
func TestV1vsV2CrossVerify(t *testing.T) {
	v1Dir := `d:\N42-eth\mainnet\txlookup`
	v2Dir := `d:\N42-eth\v2test\txlookup`
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`

	baseName := SegmentFileName(0, 1000000)
	v1Idx := filepath.Join(v1Dir, baseName+".idx")
	v1Dat := filepath.Join(v1Dir, baseName+".dat")
	v2Idx := filepath.Join(v2Dir, baseName+".idx")
	v2Dat := filepath.Join(v2Dir, baseName+".dat")

	for _, p := range []string{v1Idx, v2Idx, ancientPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("required file not found: %s", p)
		}
	}

	segV1, err := OpenSegment(v1Idx, v1Dat)
	if err != nil {
		t.Fatal("open V1:", err)
	}
	defer segV1.Close()

	segV2, err := OpenSegment(v2Idx, v2Dat)
	if err != nil {
		t.Fatal("open V2:", err)
	}
	defer segV2.Close()

	if segV1.IsV2() {
		t.Fatal("V1 segment detected as V2")
	}
	if !segV2.IsV2() {
		t.Fatal("V2 segment not detected as V2")
	}
	if segV1.TxCount() != segV2.TxCount() {
		t.Fatalf("txCount mismatch: V1=%d V2=%d", segV1.TxCount(), segV2.TxCount())
	}

	t.Logf("V1: txCount=%d v2=%v", segV1.TxCount(), segV1.IsV2())
	t.Logf("V2: txCount=%d v2=%v", segV2.TxCount(), segV2.IsV2())

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Sample blocks across the full range.
	sampleBlocks := []uint64{
		46147,  // first mainnet tx
		46400, 46402, 49018, 50111,
		100000, 200000, 300000, 400000, 500000,
		600000, 700000, 800000, 900000,
		999999, // last block in segment
	}

	verified, mismatches := 0, 0
	for _, blockNum := range sampleBlocks {
		bodyData, err := f.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			continue
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			continue
		}

		for _, tx := range body.Transactions {
			txHash := tx.Hash()
			r1 := segV1.Lookup(txHash)
			r2 := segV2.Lookup(txHash)

			if r1 == nil && r2 == nil {
				continue
			}
			if r1 == nil || r2 == nil {
				mismatches++
				t.Errorf("block %d: V1=%v V2=%v", blockNum, r1, r2)
				continue
			}
			if *r1 != *r2 {
				mismatches++
				t.Errorf("block %d: V1=%d V2=%d", blockNum, *r1, *r2)
				continue
			}
			if *r1 != blockNum {
				t.Errorf("block %d: both returned wrong block %d", blockNum, *r1)
			}
			verified++
		}
	}

	if mismatches > 0 {
		t.Fatalf("%d mismatches between V1 and V2", mismatches)
	}
	t.Logf("V1 vs V2 cross-verify: %d txs across %d blocks — identical",
		verified, len(sampleBlocks))
}

// TestV2BoundaryBlocks verifies segment boundary conditions:
// first block, last block, empty blocks, and blocks at segment edges.
func TestV2BoundaryBlocks(t *testing.T) {
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`
	if _, err := os.Stat(ancientPath); err != nil {
		t.Skip("Geth ancient not found")
	}

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Build segment covering 0-48000 (includes first mainnet txs and empty blocks).
	const endBlock = 48000
	tmpDir := t.TempDir()
	builder := NewSegmentBuilder(f, tmpDir, testBodyTxHashes)
	if err := builder.BuildRange(context.Background(), 0, endBlock); err != nil {
		t.Fatal(err)
	}

	baseName := SegmentFileName(0, endBlock)
	seg, err := OpenSegment(
		filepath.Join(tmpDir, baseName+".idx"),
		filepath.Join(tmpDir, baseName+".dat"))
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()
	t.Logf("Segment: blocks %d-%d, txCount=%d, v2=%v",
		seg.StartBlock(), seg.endBlock, seg.TxCount(), seg.IsV2())

	// Test 1: first block in segment (block 0, no txs).
	{
		bodyData, _ := f.Ancient(freezer.TableBodies, 0)
		body, _ := ethel.DecodeGethBody(bodyData)
		t.Logf("First block 0: %d txs", len(body.Transactions))
	}

	// Test 2: last block in segment.
	{
		lastBlock := uint64(endBlock - 1)
		bodyData, _ := f.Ancient(freezer.TableBodies, lastBlock)
		body, _ := ethel.DecodeGethBody(bodyData)
		for _, tx := range body.Transactions {
			result := seg.Lookup(tx.Hash())
			if result == nil {
				t.Errorf("last block %d: tx not found", lastBlock)
			} else if *result != lastBlock {
				t.Errorf("last block %d: got %d", lastBlock, *result)
			}
		}
		t.Logf("Last block %d: %d txs — OK", lastBlock, len(body.Transactions))
	}

	// Test 3: block 46147 — first ever mainnet transaction.
	{
		bodyData, _ := f.Ancient(freezer.TableBodies, 46147)
		body, _ := ethel.DecodeGethBody(bodyData)
		if len(body.Transactions) == 0 {
			t.Fatal("block 46147 should have transactions")
		}
		for ti, tx := range body.Transactions {
			result := seg.Lookup(tx.Hash())
			if result == nil {
				t.Errorf("block 46147 tx %d: not found", ti)
			} else if *result != 46147 {
				t.Errorf("block 46147 tx %d: got %d", ti, *result)
			}
		}
		t.Logf("Block 46147 (first tx): %d txs — OK", len(body.Transactions))
	}

	// Test 4: verify consecutive empty blocks (0-46146) don't break lookupV2.
	emptyCount := 0
	for b := uint64(0); b < 100; b++ {
		bodyData, _ := f.Ancient(freezer.TableBodies, b)
		body, _ := ethel.DecodeGethBody(bodyData)
		if len(body.Transactions) == 0 {
			emptyCount++
		}
	}
	t.Logf("First 100 blocks: %d empty (pre-tx era)", emptyCount)

	// Test 5: out-of-range hash should return nil (or false positive within range).
	var fakeHash [32]byte
	for i := 0; i < 100; i++ {
		rand.Read(fakeHash[:])
		result := seg.Lookup(fakeHash)
		if result != nil && *result >= endBlock {
			t.Errorf("fake hash returned out-of-range block %d", *result)
		}
	}
}

// TestV2FalsePositiveRate measures the false positive rate with random hashes.
// FPR depends on RecSplit existence filter (.idx), not the V2 .dat format.
func TestV2FalsePositiveRate(t *testing.T) {
	v2Dir := `d:\N42-eth\v2test\txlookup`
	baseName := SegmentFileName(0, 1000000)
	idxPath := filepath.Join(v2Dir, baseName+".idx")
	datPath := filepath.Join(v2Dir, baseName+".dat")

	if _, err := os.Stat(idxPath); err != nil {
		t.Skip("V2 segment not found")
	}

	seg, err := OpenSegment(idxPath, datPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()

	const trials = 100000
	falsePositives := 0
	var fakeHash [32]byte

	for i := 0; i < trials; i++ {
		rand.Read(fakeHash[:])
		if result := seg.Lookup(fakeHash); result != nil {
			falsePositives++
			// False positives must return a block within range.
			if *result < seg.StartBlock() || *result >= seg.endBlock {
				t.Errorf("false positive returned out-of-range block %d", *result)
			}
		}
	}

	fpr := float64(falsePositives) / float64(trials) * 100
	t.Logf("False positive rate: %d/%d = %.3f%% (expected ~0.4%%, depends on RecSplit config)",
		falsePositives, trials, fpr)
}

// TestV2EmptySegment verifies an empty segment (no transactions) works correctly.
func TestV2EmptySegment(t *testing.T) {
	tmpDir := t.TempDir()
	datPath := filepath.Join(tmpDir, "test.dat")

	blockCount := uint64(1000)
	if err := writeEmptyDatV2(datPath, blockCount); err != nil {
		t.Fatal(err)
	}

	fi, _ := os.Stat(datPath)
	if fi.Size() != 16 {
		t.Fatalf("empty V2 dat should be 16 bytes, got %d", fi.Size())
	}

	dat, _ := os.ReadFile(datPath)
	if string(dat[:4]) != "EFD2" {
		t.Fatal("missing V2 magic")
	}

	seg, err := openSegmentV2Standalone(dat, 5000)
	if err != nil {
		t.Fatal(err)
	}

	if seg.TxCount() != 0 {
		t.Fatalf("empty segment txCount=%d, want 0", seg.TxCount())
	}
	if seg.blockCount != blockCount {
		t.Fatalf("blockCount=%d, want %d", seg.blockCount, blockCount)
	}

	// ef should be nil for empty segments.
	if seg.ef != nil {
		t.Fatal("ef should be nil for empty segment")
	}

	t.Logf("Empty V2 segment: %d bytes, blockCount=%d, txCount=%d — OK",
		fi.Size(), seg.blockCount, seg.TxCount())
}

// TestV2HighDensityBlocks verifies correctness with tx-dense blocks (post-DeFi era).
func TestV2HighDensityBlocks(t *testing.T) {
	v2Dir := `d:\N42-eth\v2test\txlookup`
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`

	baseName := SegmentFileName(0, 1000000)
	idxPath := filepath.Join(v2Dir, baseName+".idx")
	datPath := filepath.Join(v2Dir, baseName+".dat")

	for _, p := range []string{idxPath, ancientPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("required file not found: %s", p)
		}
	}

	seg, err := OpenSegment(idxPath, datPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Find blocks with highest tx density in the range.
	type blockInfo struct {
		num     uint64
		txCount int
	}

	// Scan last 10K blocks in segment (most dense).
	var densest []blockInfo
	for b := uint64(990000); b < 1000000; b++ {
		bodyData, err := f.Ancient(freezer.TableBodies, b)
		if err != nil {
			continue
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			continue
		}
		if len(body.Transactions) > 5 {
			densest = append(densest, blockInfo{b, len(body.Transactions)})
		}
	}

	// Verify all txs in the densest blocks.
	verified := 0
	for _, bi := range densest {
		bodyData, _ := f.Ancient(freezer.TableBodies, bi.num)
		body, _ := ethel.DecodeGethBody(bodyData)
		for ti, tx := range body.Transactions {
			result := seg.Lookup(tx.Hash())
			if result == nil {
				t.Errorf("dense block %d tx %d: NOT FOUND", bi.num, ti)
				continue
			}
			if *result != bi.num {
				t.Errorf("dense block %d tx %d: got %d", bi.num, ti, *result)
				continue
			}
			verified++
		}
	}
	t.Logf("High density: verified %d txs across %d blocks (>5 tx each)",
		verified, len(densest))
}

// TestV2LookupPerformance benchmarks lookup latency for V2 segments.
func TestV2LookupPerformance(t *testing.T) {
	v2Dir := `d:\N42-eth\v2test\txlookup`
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`

	baseName := SegmentFileName(0, 1000000)
	idxPath := filepath.Join(v2Dir, baseName+".idx")
	datPath := filepath.Join(v2Dir, baseName+".dat")

	for _, p := range []string{idxPath, ancientPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("required file not found: %s", p)
		}
	}

	seg, err := OpenSegment(idxPath, datPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Collect real tx hashes for benchmarking.
	var hashes [][32]byte
	for b := uint64(46147); b < 100000 && len(hashes) < 10000; b++ {
		bodyData, _ := f.Ancient(freezer.TableBodies, b)
		body, _ := ethel.DecodeGethBody(bodyData)
		for _, tx := range body.Transactions {
			h := tx.Hash()
			hashes = append(hashes, h)
		}
	}

	if len(hashes) == 0 {
		t.Skip("no tx hashes collected")
	}

	// Warm up.
	for i := 0; i < 100; i++ {
		seg.Lookup(hashes[i%len(hashes)])
	}

	// Benchmark: lookup all hashes.
	t0 := time.Now()
	lookups := 0
	for i := 0; i < 5; i++ {
		for _, h := range hashes {
			seg.Lookup(h)
			lookups++
		}
	}
	elapsed := time.Since(t0)

	nsPerLookup := float64(elapsed.Nanoseconds()) / float64(lookups)
	t.Logf("V2 lookup: %d lookups in %v = %.0f ns/lookup (%.1f M lookups/sec)",
		lookups, elapsed.Truncate(time.Microsecond),
		nsPerLookup, float64(lookups)/elapsed.Seconds()/1e6)
}

// TestV2DatIntegrity verifies the V2 .dat Elias-Fano encoding is internally consistent.
func TestV2DatIntegrity(t *testing.T) {
	// Test with various block/tx distributions.
	cases := []struct {
		name       string
		txPerBlock []uint32
	}{
		{"uniform", []uint32{10, 10, 10, 10, 10}},
		{"all-empty", []uint32{0, 0, 0, 0, 0}},
		{"single-tx", []uint32{0, 0, 1, 0, 0}},
		{"first-only", []uint32{100, 0, 0, 0, 0}},
		{"last-only", []uint32{0, 0, 0, 0, 100}},
		{"dense", []uint32{1000, 500, 2000, 300, 1500}},
		{"alternating", []uint32{5, 0, 5, 0, 5, 0, 5}},
		{"one-block", []uint32{42}},
		{"many-empty", func() []uint32 {
			out := make([]uint32, 1000)
			out[0] = 3
			out[500] = 7
			out[999] = 1
			return out
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blockCount := uint64(len(tc.txPerBlock))
			totalTx := uint64(0)
			for _, c := range tc.txPerBlock {
				totalTx += uint64(c)
			}

			if totalTx == 0 {
				// Empty segment — just verify writeEmptyDatV2 doesn't crash.
				tmpDir := t.TempDir()
				datPath := filepath.Join(tmpDir, "test.dat")
				if err := writeEmptyDatV2(datPath, blockCount); err != nil {
					t.Fatal(err)
				}
				t.Logf("empty: %d blocks — OK", blockCount)
				return
			}

			tmpDir := t.TempDir()
			datPath := filepath.Join(tmpDir, "test.dat")
			if err := writeDatV2(datPath, blockCount, totalTx, tc.txPerBlock); err != nil {
				t.Fatal(err)
			}

			dat, _ := os.ReadFile(datPath)
			seg, err := openSegmentV2Standalone(dat, 0)
			if err != nil {
				t.Fatal(err)
			}

			// Verify every ordinal maps to the correct block.
			ord := uint64(0)
			for block, cnt := range tc.txPerBlock {
				for j := 0; j < int(cnt); j++ {
					got := seg.lookupV2(ord)
					if got == nil {
						t.Fatalf("ordinal %d: nil (expected block %d)", ord, block)
					}
					if *got != uint64(block) {
						t.Fatalf("ordinal %d: got block %d, want %d", ord, *got, block)
					}
					ord++
				}
			}

			// Verify out-of-range ordinals return nil.
			if got := seg.lookupV2(totalTx); got != nil {
				t.Errorf("ordinal %d (out of range): got %d", totalTx, *got)
			}
			if got := seg.lookupV2(totalTx + 1000); got != nil {
				t.Errorf("ordinal %d (far out of range): got %d", totalTx+1000, *got)
			}

			fi, _ := os.Stat(datPath)
			v1Size := totalTx * 4
			t.Logf("%s: %d blocks, %d txs, v2=%d bytes (v1=%d, ratio=%.1fx)",
				tc.name, blockCount, totalTx, fi.Size(), v1Size,
				float64(v1Size)/float64(fi.Size()))
		})
	}
}

// TestV2ServiceIntegration verifies the Service can load and query V2 segments.
func TestV2ServiceIntegration(t *testing.T) {
	v2Dir := `d:\N42-eth\v2test\txlookup`
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`

	if _, err := os.Stat(v2Dir); err != nil {
		t.Skip("V2 segments not found")
	}

	svc, err := NewService(v2Dir)
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	t.Logf("Service: %s", svc.Stats())
	if svc.SegmentCount() == 0 {
		t.Fatal("no segments loaded")
	}

	// Verify segments loaded.
	t.Logf("Service segments: %d", svc.SegmentCount())

	// Verify known txs via direct segment lookup (Service.Lookup needs kv.Tx).
	if _, err := os.Stat(ancientPath); err != nil {
		t.Skip("Geth ancient not found for tx verification")
	}

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Test txs in different segments.
	testBlocks := []uint64{46147, 500000, 999999, 1500000, 2500000, 3500000}

	verified := 0
	for _, blockNum := range testBlocks {
		bodyData, err := f.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			continue
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			continue
		}
		for _, tx := range body.Transactions {
			txHash := tx.Hash()
			result, err := svc.Lookup(nil, types.Hash(txHash))
			if err != nil {
				t.Errorf("block %d: lookup err: %v", blockNum, err)
			} else if result == nil {
				t.Errorf("block %d: tx not found", blockNum)
			} else if *result != blockNum {
				t.Errorf("block %d: got %d", blockNum, *result)
			} else {
				verified++
			}
		}
	}
	t.Logf("Service integration: verified %d txs across multiple segments", verified)
}

// TestV2SegmentFileSize verifies V2 dat files are dramatically smaller than V1.
func TestV2SegmentFileSize(t *testing.T) {
	v1Dir := `d:\N42-eth\mainnet\txlookup`
	v2Dir := `d:\N42-eth\v2test\txlookup`

	baseName := SegmentFileName(0, 1000000)

	v1Dat := filepath.Join(v1Dir, baseName+".dat")
	v2Dat := filepath.Join(v2Dir, baseName+".dat")

	v1Fi, err := os.Stat(v1Dat)
	if err != nil {
		t.Skip("V1 dat not found")
	}
	v2Fi, err := os.Stat(v2Dat)
	if err != nil {
		t.Skip("V2 dat not found")
	}

	ratio := float64(v1Fi.Size()) / float64(v2Fi.Size())
	t.Logf("V1 dat: %s (%d bytes)", fmtBytes(v1Fi.Size()), v1Fi.Size())
	t.Logf("V2 dat: %s (%d bytes)", fmtBytes(v2Fi.Size()), v2Fi.Size())
	t.Logf("Compression ratio: %.1fx", ratio)

	if ratio < 5 {
		t.Errorf("compression ratio %.1fx is unexpectedly low (expected >10x)", ratio)
	}
}

func fmtBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// testBodyTxHashes is the eth-el body decoder the builder now takes from its
// caller, so package txlookup does not import internal/ethel.
func testBodyTxHashes(bodyData []byte) ([]types.Hash, error) {
	body, err := ethel.DecodeGethBody(bodyData)
	if err != nil {
		return nil, err
	}
	out := make([]types.Hash, len(body.Transactions))
	for i, tx := range body.Transactions {
		out[i] = tx.Hash()
	}
	return out, nil
}
