package txlookup

import (
	"testing"

	"github.com/n42blockchain/N42/internal/cscompact"
)

func TestTxNumRange(t *testing.T) {
	// Each entry marks the first tx number of a new block.
	entries := []cscompact.TxBlockEntry{
		{TxNum: 0, BlockNum: 0},
		{TxNum: 5, BlockNum: 1},
		{TxNum: 9, BlockNum: 2},
		{TxNum: 20, BlockNum: 5},
	}

	min, max := txNumRange(entries, 1, 2)
	if min != 5 || max != 9 {
		t.Fatalf("txNumRange(1,2) = (%d,%d), want (5,9)", min, max)
	}

	min, max = txNumRange(entries, 0, 1)
	if min != 0 || max != 5 {
		t.Fatalf("txNumRange(0,1) = (%d,%d), want (0,5)", min, max)
	}
}

func TestTxNumRange_EndBeyondLastEntry(t *testing.T) {
	entries := []cscompact.TxBlockEntry{
		{TxNum: 0, BlockNum: 0},
		{TxNum: 5, BlockNum: 1},
	}
	// endBlock beyond the last recorded block: maxTxNum should fall back to
	// last TxNum + 100_000_000 (an intentionally generous upper bound).
	min, max := txNumRange(entries, 1, 1000)
	if min != 5 {
		t.Fatalf("txNumRange min = %d, want 5", min)
	}
	if max != 5+100_000_000 {
		t.Fatalf("txNumRange max = %d, want %d", max, uint64(5+100_000_000))
	}
}

func TestTxNumRange_Empty(t *testing.T) {
	min, max := txNumRange(nil, 0, 10)
	if min != 0 || max != 0 {
		t.Fatalf("txNumRange(empty) = (%d,%d), want (0,0)", min, max)
	}
}

func TestNewRethBuilder(t *testing.T) {
	b := NewRethBuilder(nil, t.TempDir())
	if b == nil {
		t.Fatal("NewRethBuilder returned nil")
	}
	if b.outputDir == "" {
		t.Fatal("expected outputDir to be set")
	}
}

func TestDatBlockCount(t *testing.T) {
	if got := datBlockCount(nil); got != 0 {
		t.Fatalf("datBlockCount(nil) = %d, want 0", got)
	}
	if got := datBlockCount([]byte{1, 2, 3}); got != 0 {
		t.Fatalf("datBlockCount(short) = %d, want 0", got)
	}

	// Not V2 (wrong magic).
	notV2 := make([]byte, 20)
	copy(notV2, "XXXX")
	if got := datBlockCount(notV2); got != 0 {
		t.Fatalf("datBlockCount(bad magic) = %d, want 0", got)
	}

	// Build real V2 bytes via buildDatV2Bytes-compatible layout using the
	// public SegmentBuilder path indirectly is overkill here — hand-encode
	// the known V2 header: magic + blockCount(u32 LE) + txCount(u64 LE).
	v2 := make([]byte, 16)
	copy(v2[:4], "EFD2")
	v2[4], v2[5], v2[6], v2[7] = 7, 0, 0, 0 // blockCount = 7
	if got := datBlockCount(v2); got != 7 {
		t.Fatalf("datBlockCount(v2) = %d, want 7", got)
	}
}
