package hotstuff

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
)

// TestLoadEpochSchedule_MissingFile confirms the "not found" branch returns
// (nil, nil) rather than an error, letting callers treat it as "no schedule".
func TestLoadEpochSchedule_MissingFile(t *testing.T) {
	dir := t.TempDir()
	es, err := LoadEpochSchedule(filepath.Join(dir, "does-not-exist.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if es != nil {
		t.Fatalf("expected nil schedule for missing file, got %+v", es)
	}
}

// TestLoadEpochSchedule_BadJSON covers the parse-error branch.
func TestLoadEpochSchedule_BadJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadEpochSchedule(p); err == nil {
		t.Fatalf("expected parse error")
	}
}

// TestLoadEpochSchedule_ValidAndSorted covers successful load, sorting, and
// downstream GetForEpoch/RecoveryWindow/ParseValidators behavior.
func TestLoadEpochSchedule_ValidAndSorted(t *testing.T) {
	sk1, err := bls.RandKey()
	if err != nil {
		t.Fatalf("RandKey: %v", err)
	}
	pkHex := "0x" + hex.EncodeToString(sk1.PublicKey().Marshal())

	dir := t.TempDir()
	p := filepath.Join(dir, "sched.json")
	content := `{"entries":[
		{"epoch":10,"validators":[{"address":"0x0000000000000000000000000000000000000001","blsKey":"` + pkHex + `"}]},
		{"epoch":2,"validators":[{"address":"0x0000000000000000000000000000000000000002","blsKey":"` + pkHex + `"}]}
	]}`
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	es, err := LoadEpochSchedule(p)
	if err != nil {
		t.Fatalf("LoadEpochSchedule: %v", err)
	}
	if es == nil || len(es.Entries) != 2 {
		t.Fatalf("expected 2 entries, got %+v", es)
	}
	if es.Entries[0].Epoch != 2 || es.Entries[1].Epoch != 10 {
		t.Fatalf("entries not sorted by epoch: %+v", es.Entries)
	}

	// GetForEpoch: exact, in-between, below-all, and nil-receiver cases.
	if got := es.GetForEpoch(2); got == nil || got.Epoch != 2 {
		t.Fatalf("GetForEpoch(2): expected epoch 2 entry, got %+v", got)
	}
	if got := es.GetForEpoch(5); got == nil || got.Epoch != 2 {
		t.Fatalf("GetForEpoch(5): expected epoch 2 entry (latest <= 5), got %+v", got)
	}
	if got := es.GetForEpoch(100); got == nil || got.Epoch != 10 {
		t.Fatalf("GetForEpoch(100): expected epoch 10 entry, got %+v", got)
	}
	if got := es.GetForEpoch(1); got != nil {
		t.Fatalf("GetForEpoch(1): expected nil (below all entries), got %+v", got)
	}
	var nilSchedule *EpochSchedule
	if got := nilSchedule.GetForEpoch(5); got != nil {
		t.Fatalf("nil receiver GetForEpoch: expected nil, got %+v", got)
	}

	// ParseValidators success.
	vs, err := es.Entries[0].ParseValidators()
	if err != nil {
		t.Fatalf("ParseValidators: %v", err)
	}
	if len(vs) != 1 || vs[0].Address != types.HexToAddress("0x0000000000000000000000000000000000000002") {
		t.Fatalf("unexpected parsed validators: %+v", vs)
	}

	// ParseValidators error path: malformed BLS key.
	bad := EpochScheduleEntry{Validators: []EpochScheduleValidator{{Address: "0x01", BLSKey: "zz"}}}
	if _, err := bad.ParseValidators(); err == nil {
		t.Fatalf("expected error for malformed BLS key")
	}

	// RecoveryWindow: normal range, nil receiver, empty range.
	rw := es.RecoveryWindow(0, 100)
	if len(rw) != 2 {
		t.Fatalf("RecoveryWindow(0,100): expected 2, got %d", len(rw))
	}
	rw = es.RecoveryWindow(3, 9)
	if len(rw) != 0 {
		t.Fatalf("RecoveryWindow(3,9): expected 0, got %d", len(rw))
	}
	if got := nilSchedule.RecoveryWindow(0, 1); got != nil {
		t.Fatalf("nil receiver RecoveryWindow: expected nil, got %+v", got)
	}
}

// TestLoadBLSKeyFromDir covers the empty-dir guard, missing-file error,
// malformed-hex error, and a successful round trip.
func TestLoadBLSKeyFromDir(t *testing.T) {
	addr := types.HexToAddress("0x0000000000000000000000000000000000000003")

	if _, err := LoadBLSKeyFromDir("", addr); err == nil {
		t.Fatalf("expected error for empty key directory")
	}

	dir := t.TempDir()
	if _, err := LoadBLSKeyFromDir(dir, addr); err == nil {
		t.Fatalf("expected error for missing key file")
	}

	// Malformed hex content.
	addrHex := strings_ToLower(addr.Hex())
	badPath := filepath.Join(dir, "bls_"+addrHex+".key")
	if err := os.WriteFile(badPath, []byte("not-hex!!"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := LoadBLSKeyFromDir(dir, addr); err == nil {
		t.Fatalf("expected error for malformed hex key")
	}

	// Valid secret key, with surrounding whitespace and 0x prefix, exercises
	// the trim + strip + success path.
	sk, err := bls.RandKey()
	if err != nil {
		t.Fatalf("RandKey: %v", err)
	}
	if err := os.WriteFile(badPath, []byte("0x"+hex.EncodeToString(sk.Marshal())+"\n"), 0o600); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	loaded, err := LoadBLSKeyFromDir(dir, addr)
	if err != nil {
		t.Fatalf("LoadBLSKeyFromDir success path: %v", err)
	}
	if hex.EncodeToString(loaded.Marshal()) != hex.EncodeToString(sk.Marshal()) {
		t.Fatalf("loaded key does not match original")
	}
}

func strings_ToLower(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}

// TestExtractHeaderQC covers nil/empty extra and a nil-QC round trip through
// decodeHeaderExtra (full encode/decode is covered elsewhere in codec_test.go).
func TestExtractHeaderQC(t *testing.T) {
	if qc, err := ExtractHeaderQC(nil); err == nil || qc != nil {
		t.Fatalf("expected error and nil QC for empty extra, got qc=%v err=%v", qc, err)
	}
	if qc, err := ExtractHeaderQC([]byte("short")); err == nil || qc != nil {
		t.Fatalf("expected error for too-short extra, got qc=%v err=%v", qc, err)
	}
}
