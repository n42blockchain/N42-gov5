package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestClefNewAuditLoggerEmptyPath(t *testing.T) {
	if _, err := NewAuditLogger(""); err == nil {
		t.Fatal("expected an error for an empty audit log path")
	}
}

func TestClefAuditLoggerWritesEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	al, err := NewAuditLogger(path)
	if err != nil {
		t.Fatalf("NewAuditLogger error: %v", err)
	}

	addr := types.Address{0x01, 0x02}
	al.LogSignRequest("eth_sign", addr, true, "user approved")
	al.LogSignRequest("eth_sign", addr, false, "user denied")
	al.LogAccountAccess("list_accounts", addr)
	al.LogInfo("startup", "clef started")

	if err := al.Close(); err != nil {
		t.Fatalf("Close error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	content := string(data)
	for _, want := range []string{"SIGN", "APPROVED", "DENIED", "ACCT", "INFO", "clef started"} {
		if !strings.Contains(content, want) {
			t.Errorf("audit log missing %q, got:\n%s", want, content)
		}
	}
}

func TestClefAuditLoggerAppendsAcrossOpens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	al1, err := NewAuditLogger(path)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	al1.LogInfo("one", "first entry")
	al1.Close()

	al2, err := NewAuditLogger(path)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	al2.LogInfo("two", "second entry")
	al2.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "first entry") || !strings.Contains(content, "second entry") {
		t.Fatalf("expected both entries to be present, got:\n%s", content)
	}
}
