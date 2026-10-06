package datc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func hexOf(b byte, n int) string {
	s := ""
	for i := 0; i < n; i++ {
		s += string("0123456789abcdef"[b>>4]) + string("0123456789abcdef"[b&0xf])
	}
	return s
}

func TestParseDeriveContracts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "contracts.txt")
	h1 := hexOf(0x11, stoDomainLen)
	h2 := hexOf(0x22, stoDomainLen)
	content := "# header\n\n" + h1 + " 2\n" + h2 + " 3\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	out, err := parseDeriveContracts(path)
	if err != nil {
		t.Fatalf("parseDeriveContracts: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].depth != 2 || out[1].depth != 3 {
		t.Fatalf("unexpected depths: %+v", out)
	}
}

func TestParseDeriveContractsMissingFile(t *testing.T) {
	if _, err := parseDeriveContracts("/nonexistent/contracts.txt"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestParseDeriveContractsErrors(t *testing.T) {
	h1 := hexOf(0x11, stoDomainLen)
	cases := map[string]string{
		"single field":     h1 + "\n",
		"bad hex":          "zz 2\n",
		"wrong hash length": "aabb 2\n",
		"depth too low":    h1 + " 0\n",
		"depth too high":   h1 + " " + "99\n",
		"duplicate":        h1 + " 1\n" + h1 + " 2\n",
	}
	for name, content := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "c.txt")
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("setup %s: %v", name, err)
		}
		if _, err := parseDeriveContracts(path); err == nil {
			t.Errorf("%s: expected error, content=%q", name, strings.TrimSpace(content))
		}
	}
}
