package commitment

import (
	"testing"

	"github.com/n42blockchain/N42/lib/common/empty"
)

func TestParseTrieVariant(t *testing.T) {
	cases := map[string]TrieVariant{
		"bin":          VariantBinPatriciaTrie,
		"hex-parallel": VariantConcurrentHexPatricia,
		"hex":          VariantHexPatriciaTrie,
		"":             VariantHexPatriciaTrie,
		"unknown":      VariantHexPatriciaTrie,
	}
	for in, want := range cases {
		if got := ParseTrieVariant(in); got != want {
			t.Fatalf("ParseTrieVariant(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestUpdatesSetModeAndPlainKeys(t *testing.T) {
	u := NewUpdates(ModeUpdate, t.TempDir(), keyHasherNoop)
	if u.Mode() != ModeUpdate {
		t.Fatalf("expected ModeUpdate, got %v", u.Mode())
	}
	// PlainKeys is meaningless outside ModeDirect.
	if pk := u.PlainKeys(); pk != nil {
		t.Fatalf("expected nil PlainKeys in ModeUpdate, got %v", pk)
	}

	u.SetMode(ModeDirect)
	if u.Mode() != ModeDirect {
		t.Fatalf("expected ModeDirect after SetMode, got %v", u.Mode())
	}
	defer u.Close()

	u.TouchPlainKey("abc", []byte{1}, u.TouchAccount)
	u.TouchPlainKey("def", []byte{2}, u.TouchAccount)

	pk := u.PlainKeys()
	if len(pk) != 2 {
		t.Fatalf("expected 2 plain keys, got %d: %v", len(pk), pk)
	}
	if _, ok := pk["abc"]; !ok {
		t.Fatalf("expected key 'abc' present")
	}
	// Returned map must be a copy: mutating it must not affect internal state.
	delete(pk, "abc")
	pk2 := u.PlainKeys()
	if len(pk2) != 2 {
		t.Fatalf("PlainKeys() must return a defensive copy, internal state mutated")
	}
}

func TestUpdatesTouchCode(t *testing.T) {
	u := NewUpdates(ModeUpdate, t.TempDir(), keyHasherNoop)
	defer u.Close()

	// Non-empty code sets CodeHash to keccak256(code) and the CodeUpdate flag.
	ku := &KeyUpdate{plainKey: "k1", update: new(Update)}
	u.TouchCode(ku, []byte("some-bytecode"))
	if ku.update.Flags&CodeUpdate == 0 {
		t.Fatalf("expected CodeUpdate flag set")
	}
	if ku.update.CodeHash == empty.CodeHash {
		t.Fatalf("expected non-empty code hash for non-empty code")
	}

	// NOTE (observed defect, not fixed here): TouchCode unconditionally
	// ORs in CodeUpdate before checking "Flags == 0" to decide whether to
	// mark the entry deleted, so that branch is unreachable in practice —
	// Flags always carries CodeUpdate at the check. Empty code therefore
	// always ends up flagged CodeUpdate (never DeleteUpdate) regardless of
	// prior state. This test pins the actual observed behavior.
	ku2 := &KeyUpdate{plainKey: "k2", update: new(Update)}
	u.TouchCode(ku2, nil)
	if ku2.update.Flags != CodeUpdate {
		t.Fatalf("expected CodeUpdate flag (DeleteUpdate branch is unreachable), got %v", ku2.update.Flags)
	}
	if ku2.update.CodeHash != empty.CodeHash {
		t.Fatalf("expected empty code hash for empty code")
	}
}
