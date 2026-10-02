/*
   Tests for the pure helper functions in helpers.go and the table-config
   wiring in tables.go that do not require a real MDBX environment.
*/

package kv

import (
	"testing"
)

func TestG35NextSubtree(t *testing.T) {
	cases := []struct {
		in   []byte
		want []byte
		ok   bool
	}{
		{[]byte{0x01, 0x02}, []byte{0x01, 0x03}, true},
		{[]byte{0x01, 0xff}, []byte{0x02}, true},
		{[]byte{0xff, 0xff}, nil, false},
		{[]byte{}, nil, false},
	}
	for i, c := range cases {
		got, ok := NextSubtree(c.in)
		if ok != c.ok {
			t.Fatalf("case %d: expected ok=%v got %v", i, c.ok, ok)
		}
		if ok && string(got) != string(c.want) {
			t.Fatalf("case %d: expected %x got %x", i, c.want, got)
		}
	}
}

func TestG35DefaultPageSize(t *testing.T) {
	ps := DefaultPageSize()
	if ps == 0 {
		t.Fatalf("expected non-zero page size")
	}
	if ps%4096 != 0 {
		t.Fatalf("expected page size to be a multiple of 4096, got %d", ps)
	}
}

// fakeGetPut is a minimal in-memory Getter+Putter for testing helpers that
// only touch GetOne/Put (EnsureNotChangedBool, GetBool).
type fakeGetPut struct {
	store map[string][]byte
}

func newFakeGetPut() *fakeGetPut {
	return &fakeGetPut{store: map[string][]byte{}}
}

func (f *fakeGetPut) key(bucket string, k []byte) string { return bucket + "/" + string(k) }

func (f *fakeGetPut) Has(table string, key []byte) (bool, error) {
	_, ok := f.store[f.key(table, key)]
	return ok, nil
}

func (f *fakeGetPut) GetOne(table string, key []byte) ([]byte, error) {
	return f.store[f.key(table, key)], nil
}

func (f *fakeGetPut) ForEach(table string, fromPrefix []byte, walker func(k, v []byte) error) error {
	return nil
}
func (f *fakeGetPut) ForPrefix(table string, prefix []byte, walker func(k, v []byte) error) error {
	return nil
}
func (f *fakeGetPut) ForAmount(table string, prefix []byte, amount uint32, walker func(k, v []byte) error) error {
	return nil
}

func (f *fakeGetPut) Put(table string, k, v []byte) error {
	f.store[f.key(table, k)] = v
	return nil
}

func TestG35GetBool(t *testing.T) {
	g := newFakeGetPut()
	ok, err := GetBool(g, "bucket", []byte("k"))
	if err != nil {
		t.Fatalf("GetBool missing key: %v", err)
	}
	if ok {
		t.Fatalf("expected false for missing key")
	}

	_ = g.Put("bucket", []byte("k"), []byte{1})
	ok, err = GetBool(g, "bucket", []byte("k"))
	if err != nil || !ok {
		t.Fatalf("expected true after storing 1 byte, got ok=%v err=%v", ok, err)
	}

	_ = g.Put("bucket", []byte("k2"), []byte{0})
	ok, err = GetBool(g, "bucket", []byte("k2"))
	if err != nil || ok {
		t.Fatalf("expected false for 0 byte, got ok=%v err=%v", ok, err)
	}
}

func TestG35EnsureNotChangedBool(t *testing.T) {
	g := newFakeGetPut()

	// first call with value=true stores true and reports ok (no prior mismatch).
	ok, enabled, err := EnsureNotChangedBool(g, "b", []byte("flag"), true)
	if err != nil {
		t.Fatalf("EnsureNotChangedBool: %v", err)
	}
	if !ok || !enabled {
		t.Fatalf("expected ok=true enabled=true on first write, got ok=%v enabled=%v", ok, enabled)
	}

	// second call with the same value should still be ok.
	ok, enabled, err = EnsureNotChangedBool(g, "b", []byte("flag"), true)
	if err != nil || !ok || !enabled {
		t.Fatalf("expected consistent read, got ok=%v enabled=%v err=%v", ok, enabled, err)
	}

	// calling with a different value should report ok=false but return the
	// persisted (unchanged) value.
	ok, enabled, err = EnsureNotChangedBool(g, "b", []byte("flag"), false)
	if err != nil {
		t.Fatalf("EnsureNotChangedBool mismatch: %v", err)
	}
	if ok {
		t.Fatalf("expected ok=false when requested value differs from stored value")
	}
	if !enabled {
		t.Fatalf("expected stored value (true) to be returned unchanged")
	}

	// first call with value=false on a fresh key stores false.
	ok, enabled, err = EnsureNotChangedBool(g, "b", []byte("flag2"), false)
	if err != nil || !ok || enabled {
		t.Fatalf("expected ok=true enabled=false storing false, got ok=%v enabled=%v err=%v", ok, enabled, err)
	}
}

func TestG35TablesCfgByLabel(t *testing.T) {
	cases := []struct {
		label Label
		want  TableCfg
	}{
		{ChainDB, ChaindataTablesCfg},
		{TxPoolDB, TxpoolTablesCfg},
		{SentryDB, SentryTablesCfg},
		{DownloaderDB, DownloaderTablesCfg},
		{DiagnosticsDB, DiagnosticsTablesCfg},
	}
	for _, c := range cases {
		got := TablesCfgByLabel(c.label)
		if len(got) != len(c.want) {
			t.Fatalf("label %v: expected cfg of len %d, got %d", c.label, len(c.want), len(got))
		}
	}
}

func TestG35TablesCfgByLabelPanicsOnUnknown(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for unknown label")
		}
	}()
	_ = TablesCfgByLabel(Label(99))
}

func TestG35EnsureTableDefaultsAddsMissing(t *testing.T) {
	cfg := TableCfg{}
	ensureTableDefaults(cfg, []string{"a", "b"})
	if len(cfg) != 2 {
		t.Fatalf("expected 2 default entries, got %d", len(cfg))
	}
	if _, ok := cfg["a"]; !ok {
		t.Fatalf("expected default entry for 'a'")
	}

	// existing entries must not be overwritten.
	cfg2 := TableCfg{"a": {Flags: DupSort}}
	ensureTableDefaults(cfg2, []string{"a", "c"})
	if cfg2["a"].Flags != DupSort {
		t.Fatalf("expected existing entry to be preserved")
	}
	if _, ok := cfg2["c"]; !ok {
		t.Fatalf("expected default entry for 'c' to be added")
	}
}

func TestG35LabelStringAndUnmarshal(t *testing.T) {
	cases := []struct {
		l Label
		s string
	}{
		{ChainDB, "chaindata"},
		{TxPoolDB, "txpool"},
		{SentryDB, "sentry"},
		{ConsensusDB, "consensus"},
		{DownloaderDB, "downloader"},
		{InMem, "inMem"},
		{DiagnosticsDB, "diagnostics"},
		{CaplinDB, "caplin"},
	}
	for _, c := range cases {
		if got := c.l.String(); got != c.s {
			t.Fatalf("Label(%d).String() = %q, want %q", c.l, got, c.s)
		}
		if got := UnmarshalLabel(c.s); got != c.l {
			t.Fatalf("UnmarshalLabel(%q) = %v, want %v", c.s, got, c.l)
		}
	}

	if got := Label(200).String(); got != "unknown" {
		t.Fatalf("expected 'unknown' for unrecognized label, got %q", got)
	}
}

func TestG35UnmarshalLabelPanicsOnUnknown(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("expected panic for unknown label string")
		}
	}()
	UnmarshalLabel("nope")
}

func TestG35ReinitIsIdempotent(t *testing.T) {
	// reinit() runs at init() time; calling it again must not panic or
	// corrupt the schema (sorts ChaindataTables, re-applies defaults and
	// deprecated-table flags).
	before := len(ChaindataTablesCfg)
	reinit()
	after := len(ChaindataTablesCfg)
	if before != after {
		t.Fatalf("expected stable table count across reinit, got %d then %d", before, after)
	}
}
