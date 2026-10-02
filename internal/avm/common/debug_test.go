package common

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestPrintDeprecationWarning(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error: %v", err)
	}
	os.Stdout = w

	PrintDeprecationWarning("deprecated thing")

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read pipe error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "deprecated thing") {
		t.Errorf("PrintDeprecationWarning output missing message: %q", out)
	}
	if !strings.Contains(out, "#") {
		t.Errorf("PrintDeprecationWarning output missing border: %q", out)
	}
}

func TestReport(t *testing.T) {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error: %v", err)
	}
	os.Stderr = w

	Report("extra context", 42)

	w.Close()
	os.Stderr = old

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read pipe error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "BUG! PLEASE REPORT") {
		t.Errorf("Report output missing banner: %q", out)
	}
	if !strings.Contains(out, "extra context") {
		t.Errorf("Report output missing extra args: %q", out)
	}
}
