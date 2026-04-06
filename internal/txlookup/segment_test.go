package txlookup

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func TestSegmentLookup(t *testing.T) {
	segDir := `d:\N42-eth\mainnet\txlookup`
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`

	if _, err := os.Stat(segDir); err != nil {
		t.Skip("txlookup segments not found")
	}

	// Open segment.
	baseName := SegmentFileName(0, 1000000)
	idxPath := filepath.Join(segDir, baseName+".idx")
	datPath := filepath.Join(segDir, baseName+".dat")

	seg, err := OpenSegment(idxPath, datPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()
	t.Logf("Segment opened: txCount=%d startBlock=%d v2=%v", seg.TxCount(), seg.StartBlock(), seg.IsV2())

	// Open Geth freezer to get real tx hashes.
	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Test blocks with known transactions.
	testBlocks := []uint64{46147, 46400, 46402, 49018, 50111, 100009, 200006, 499999}

	verified := 0
	for _, blockNum := range testBlocks {
		bodyData, err := f.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			t.Fatalf("read body %d: %v", blockNum, err)
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil {
			t.Fatalf("decode body %d: %v", blockNum, err)
		}

		for ti, tx := range body.Transactions {
			txHash := tx.Hash()
			result := seg.Lookup(txHash)
			if result == nil {
				t.Errorf("block %d tx %d (%s): not found", blockNum, ti, txHash.Hex()[:10])
				continue
			}
			if *result != blockNum {
				t.Errorf("block %d tx %d: got blockNum %d, want %d",
					blockNum, ti, *result, blockNum)
				continue
			}
			verified++
		}
	}

	// Test non-existent tx hash.
	var fakeHash [32]byte
	fakeHash[0] = 0xFF
	fakeHash[1] = 0xEE
	if result := seg.Lookup(fakeHash); result != nil {
		t.Logf("False positive for fake hash: blockNum=%d (expected for ~0.4%% FPR)", *result)
	}

	t.Logf("Verified %d transactions across %d blocks", verified, len(testBlocks))
}

func TestSegmentBuildAndVerify(t *testing.T) {
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`
	if _, err := os.Stat(ancientPath); err != nil {
		t.Skip("Geth ancient not found")
	}

	f, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	// Use V2 segment from d:\N42-eth\v2test if available.
	v2Dir := `d:\N42-eth\v2test\txlookup`
	baseName := SegmentFileName(0, 1000000)
	idxPath := filepath.Join(v2Dir, baseName+".idx")
	datPath := filepath.Join(v2Dir, baseName+".dat")

	if _, err := os.Stat(idxPath); err != nil {
		t.Skip("V2 test segment not found at", v2Dir)
	}

	seg, err := OpenSegment(idxPath, datPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()

	t.Logf("Built segment: txCount=%d v2=%v", seg.TxCount(), seg.IsV2())

	// Verify known mainnet blocks with transactions.
	verified := 0
	for blockNum := uint64(46147); blockNum < 100000 && verified < 200; blockNum++ {
		bodyData, _ := f.Ancient(freezer.TableBodies, blockNum)
		body, _ := ethel.DecodeGethBody(bodyData)
		for _, tx := range body.Transactions {
			txHash := tx.Hash()
			result := seg.Lookup(txHash)
			if result == nil {
				t.Errorf("block %d: tx %s not found", blockNum, txHash.Hex()[:10])
				continue
			}
			if *result != blockNum {
				t.Errorf("block %d: got %d", blockNum, *result)
			}
			verified++
		}
	}
	t.Logf("Verified %d transactions, v2=%v", verified, seg.IsV2())
}

func TestEliasFanoDatV2(t *testing.T) {
	// Unit test: write V2 .dat with known block boundaries, verify lookupV2.
	tmpDir := t.TempDir()
	datPath := filepath.Join(tmpDir, "test.dat")

	// 10 blocks: tx counts [3, 0, 5, 1, 0, 2, 4, 0, 1, 6] = 22 txs total.
	txPerBlock := []uint32{3, 0, 5, 1, 0, 2, 4, 0, 1, 6}
	blockCount := uint64(len(txPerBlock))
	totalTx := uint64(0)
	for _, c := range txPerBlock {
		totalTx += uint64(c)
	}

	if err := writeDatV2(datPath, blockCount, totalTx, txPerBlock); err != nil {
		t.Fatal(err)
	}

	fi, _ := os.Stat(datPath)
	t.Logf("V2 dat: %d bytes for %d blocks, %d txs (v1 would be %d bytes)",
		fi.Size(), blockCount, totalTx, totalTx*4)

	// Read back and verify.
	dat, err := os.ReadFile(datPath)
	if err != nil {
		t.Fatal(err)
	}

	if string(dat[:4]) != "EFD2" {
		t.Fatal("missing V2 magic")
	}

	// Build expected ordinal → block mapping.
	expected := make([]int, totalTx)
	ord := 0
	for block, cnt := range txPerBlock {
		for j := 0; j < int(cnt); j++ {
			expected[ord] = block
			ord++
		}
	}

	// Create a minimal TxSegment to test lookupV2.
	seg, _ := openSegmentV2Standalone(dat, 100) // startBlock=100
	for i, wantBlock := range expected {
		got := seg.lookupV2(uint64(i))
		if got == nil {
			t.Fatalf("ordinal %d: got nil, want block %d", i, 100+wantBlock)
		}
		if *got != uint64(100+wantBlock) {
			t.Fatalf("ordinal %d: got block %d, want %d", i, *got, 100+wantBlock)
		}
	}

	// Out of range should return nil.
	if got := seg.lookupV2(totalTx); got != nil {
		t.Fatalf("ordinal %d (out of range): got %d, want nil", totalTx, *got)
	}
	t.Logf("All %d ordinals verified correctly", totalTx)
}

// openSegmentV2Standalone creates a TxSegment from raw V2 dat bytes for testing.
func openSegmentV2Standalone(dat []byte, startBlock uint64) (*TxSegment, error) {
	blockCount := uint64(binary.LittleEndian.Uint32(dat[4:8]))
	txCount := binary.LittleEndian.Uint64(dat[8:16])

	ef, _ := eliasfano32.ReadEliasFano(dat[16:])

	return &TxSegment{
		startBlock: startBlock,
		endBlock:   startBlock + blockCount,
		ef:         ef,
		blockCount: blockCount,
		txCount:    txCount,
	}, nil
}
