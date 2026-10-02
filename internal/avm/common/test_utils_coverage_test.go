package common

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadJSON(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "valid.json")
	if err := os.WriteFile(file, []byte(`{"a":1,"b":"two"}`), 0o600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	var out struct {
		A int    `json:"a"`
		B string `json:"b"`
	}
	if err := LoadJSON(file, &out); err != nil {
		t.Fatalf("LoadJSON error: %v", err)
	}
	if out.A != 1 || out.B != "two" {
		t.Errorf("LoadJSON() = %#v", out)
	}
}

func TestLoadJSONMissingFile(t *testing.T) {
	var out map[string]interface{}
	if err := LoadJSON(filepath.Join(t.TempDir(), "missing.json"), &out); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadJSONSyntaxError(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(file, []byte("{\n  \"a\": ,\n}"), 0o600); err != nil {
		t.Fatalf("WriteFile error: %v", err)
	}

	var out map[string]interface{}
	err := LoadJSON(file, &out)
	if err == nil {
		t.Fatal("expected a JSON syntax error")
	}
}

func TestFindLine(t *testing.T) {
	data := []byte("line1\nline2\nline3")
	if got := findLine(data, 0); got != 1 {
		t.Errorf("findLine(0) = %d, want 1", got)
	}
	if got := findLine(data, 6); got != 2 {
		t.Errorf("findLine(6) = %d, want 2", got)
	}
	if got := findLine(data, int64(len(data))); got != 3 {
		t.Errorf("findLine(end) = %d, want 3", got)
	}
}
