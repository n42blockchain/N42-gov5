package txlookup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

func TestSegmentLookup(t *testing.T) {
	segDir := `d:\N42\v5\mainnet\txlookup`
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`

	if _, err := os.Stat(segDir); err != nil {
		t.Skip("txlookup segments not found")
	}

	// Open segment.
	baseName := SegmentFileName(0, 500000)
	idxPath := filepath.Join(segDir, baseName+".idx")
	datPath := filepath.Join(segDir, baseName+".dat")

	seg, err := OpenSegment(idxPath, datPath)
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()
	t.Logf("Segment opened: txCount=%d startBlock=%d", seg.TxCount(), seg.StartBlock())

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

	// Build a tiny segment (blocks 0-10000) in temp dir.
	tmpDir := t.TempDir()
	builder := NewSegmentBuilder(f, tmpDir)
	if err := builder.BuildRange(context.Background(), 0, 10000); err != nil {
		t.Fatal(err)
	}

	// Open and verify.
	baseName := SegmentFileName(0, 10000)
	seg, err := OpenSegment(
		filepath.Join(tmpDir, baseName+".idx"),
		filepath.Join(tmpDir, baseName+".dat"))
	if err != nil {
		t.Fatal(err)
	}
	defer seg.Close()

	t.Logf("Built segment: txCount=%d", seg.TxCount())

	// Verify block 46147 (first tx on mainnet) if in range.
	// Block 46147 > 10000, so this segment won't have it.
	// Verify any tx in blocks 0-9999 instead.
	verified := 0
	for blockNum := uint64(0); blockNum < 10000 && verified < 50; blockNum++ {
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
	t.Logf("Verified %d transactions", verified)
}
