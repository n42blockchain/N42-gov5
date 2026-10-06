package jsonrpc

import (
	"context"
	"strings"
	"testing"
)

func TestEncodeID(t *testing.T) {
	id := encodeID([]byte{0x00, 0x00, 0x01})
	if id != ID("0x1") {
		t.Errorf("got %q want 0x1", id)
	}

	allZero := encodeID([]byte{0x00, 0x00})
	if allZero != ID("0x0") {
		t.Errorf("got %q want 0x0", allZero)
	}

	id2 := encodeID([]byte{0xab, 0xcd})
	if !strings.HasPrefix(string(id2), "0x") {
		t.Errorf("expected 0x prefix, got %q", id2)
	}
}

func TestNewIDUnique(t *testing.T) {
	a := NewID()
	b := NewID()
	if a == "" || b == "" {
		t.Fatal("expected non-empty IDs")
	}
	if !strings.HasPrefix(string(a), "0x") || !strings.HasPrefix(string(b), "0x") {
		t.Errorf("expected 0x prefix: %q %q", a, b)
	}
}

func TestNotifierFromContext(t *testing.T) {
	if _, ok := NotifierFromContext(context.Background()); ok {
		t.Error("expected no notifier in empty context")
	}
	n := &Notifier{}
	ctx := context.WithValue(context.Background(), notifierKey{}, n)
	got, ok := NotifierFromContext(ctx)
	if !ok || got != n {
		t.Errorf("got %v %v", got, ok)
	}
}
