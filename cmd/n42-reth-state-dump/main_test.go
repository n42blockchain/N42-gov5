package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestQuantity(t *testing.T) {
	for _, tc := range []struct {
		input []byte
		want  string
	}{
		{nil, "0x0"},
		{[]byte{0, 0}, "0x0"},
		{[]byte{0, 1}, "0x1"},
		{[]byte{0x12, 0x34}, "0x1234"},
	} {
		if got := quantity(tc.input); got != tc.want {
			t.Fatalf("quantity(%x) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestWriteAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	want := []byte("complete\n")
	if err := writeAtomic(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestDumpAccountUsesRethInitStateFields(t *testing.T) {
	item := dumpAccount{
		Address: "0x0000000000000000000000000000000000000001",
		Balance: "0x2a",
		Nonce:   "0x3",
		Code:    "0x6000",
		Storage: map[string]string{
			"0x0000000000000000000000000000000000000000000000000000000000000004": "0x0000000000000000000000000000000000000000000000000000000000000005",
		},
	}
	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(item); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"address", "balance", "nonce", "code", "storage"} {
		if _, ok := decoded[field]; !ok {
			t.Fatalf("missing %q in %s", field, out.String())
		}
	}
}
