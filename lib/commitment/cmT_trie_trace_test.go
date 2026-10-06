package commitment

// cmT_trie_trace_test.go covers BuildTrieTrace/Save/LoadTrieTrace/
// DecodeTrieState/containsKey/ErrorTracePath over a small hand-built
// RecordingContext (same package, so its unexported maps can be populated
// directly without driving a full HPH run).

import (
	"path/filepath"
	"testing"
)

func cmTMakeUpdateBytes(flags UpdateFlags) []byte {
	// Minimal encoding: Update's first byte is its Flags, per BuildTrieTrace's
	// own UpdateFlags(v[0]) decoding.
	return []byte{byte(flags), 0x01, 0x02}
}

func TestBuildTrieTrace_AndRoundTrip(t *testing.T) {
	rc := NewRecordingContext(nil)
	rc.branches["\x01\x02"] = []byte{0xaa, 0xbb}
	rc.accounts["acct-live"] = cmTMakeUpdateBytes(BalanceUpdate)
	rc.accounts["acct-deleted"] = cmTMakeUpdateBytes(DeleteUpdate)
	rc.storages["stor-live"] = cmTMakeUpdateBytes(StorageUpdate)
	rc.storages["stor-deleted"] = cmTMakeUpdateBytes(DeleteUpdate)
	rc.putBranches["\x03\x04"] = BranchWrite{PrevData: []byte{0x01}, NewData: []byte{0x02, 0x03}}

	inputKeys := map[string]struct{}{"acct-live": {}, "stor-deleted": {}}
	trieState := []byte{0xde, 0xad, 0xbe, 0xef}

	tt, err := BuildTrieTrace(rc, inputKeys, trieState)
	if err != nil {
		t.Fatalf("BuildTrieTrace: %v", err)
	}

	// Live accounts/storages appear in their state maps; deleted ones don't.
	if len(tt.Accounts) != 1 {
		t.Errorf("Accounts: got %d entries, want 1 (deleted excluded)", len(tt.Accounts))
	}
	if len(tt.Storages) != 1 {
		t.Errorf("Storages: got %d entries, want 1 (deleted excluded)", len(tt.Storages))
	}
	if len(tt.Branches) != 1 {
		t.Errorf("Branches: got %d, want 1", len(tt.Branches))
	}
	if len(tt.PutBranches) != 1 {
		t.Errorf("PutBranches: got %d, want 1", len(tt.PutBranches))
	}

	// Updates is filtered by inputKeys: only acct-live and stor-deleted
	// (deleted entries still appear in Updates, just not in the state map).
	if len(tt.Updates) != 2 {
		t.Fatalf("Updates: got %d, want 2", len(tt.Updates))
	}
	// Sorted by PlainKey (hex-encoded).
	if tt.Updates[0].PlainKey > tt.Updates[1].PlainKey {
		t.Error("Updates not sorted by PlainKey")
	}

	gotState, derr := tt.DecodeTrieState()
	if derr != nil {
		t.Fatalf("DecodeTrieState: %v", derr)
	}
	if string(gotState) != string(trieState) {
		t.Errorf("DecodeTrieState: got %x, want %x", gotState, trieState)
	}

	// Save/Load round trip.
	path := filepath.Join(t.TempDir(), "trace.toml")
	if err := tt.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, lerr := LoadTrieTrace(path)
	if lerr != nil {
		t.Fatalf("LoadTrieTrace: %v", lerr)
	}
	if len(loaded.Updates) != len(tt.Updates) {
		t.Errorf("round-trip Updates length mismatch: got %d, want %d", len(loaded.Updates), len(tt.Updates))
	}
	gotState2, derr2 := loaded.DecodeTrieState()
	if derr2 != nil {
		t.Fatalf("round-trip DecodeTrieState: %v", derr2)
	}
	if string(gotState2) != string(trieState) {
		t.Errorf("round-trip TrieState mismatch: got %x, want %x", gotState2, trieState)
	}
}

// TestBuildTrieTrace_NilInputKeys exercises the "inputKeys == nil" path: every
// recorded account/storage becomes an Update, and no TrieState / PutBranches
// are set when none were recorded.
func TestBuildTrieTrace_NilInputKeys(t *testing.T) {
	rc := NewRecordingContext(nil)
	rc.accounts["a1"] = cmTMakeUpdateBytes(BalanceUpdate)
	rc.storages["s1"] = cmTMakeUpdateBytes(StorageUpdate)

	tt, err := BuildTrieTrace(rc, nil, nil)
	if err != nil {
		t.Fatalf("BuildTrieTrace: %v", err)
	}
	if len(tt.Updates) != 2 {
		t.Errorf("Updates: got %d, want 2 (nil inputKeys includes all)", len(tt.Updates))
	}
	if tt.TrieState != "" {
		t.Errorf("TrieState: got %q, want empty", tt.TrieState)
	}
	if tt.PutBranches != nil {
		t.Errorf("PutBranches: got %v, want nil (none recorded)", tt.PutBranches)
	}

	state, derr := tt.DecodeTrieState()
	if derr != nil || state != nil {
		t.Errorf("DecodeTrieState on empty state: got (%v, %v), want (nil, nil)", state, derr)
	}
}

func TestErrorTracePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"trace.toml", "trace.error.toml"},
		{"/tmp/dir/trace.toml", "/tmp/dir/trace.error.toml"},
		{"trace.txt", "trace.txt" + ErrorTraceSuffix},
		{"noext", "noext" + ErrorTraceSuffix},
	}
	for _, c := range cases {
		if got := ErrorTracePath(c.in); got != c.want {
			t.Errorf("ErrorTracePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestLoadTrieTrace_MissingFile(t *testing.T) {
	_, err := LoadTrieTrace(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}
