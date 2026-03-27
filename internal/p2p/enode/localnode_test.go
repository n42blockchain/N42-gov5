package enode

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/n42blockchain/N42/crypto"
)

type failingLocalEntry struct{}

func (failingLocalEntry) ENRKey() string { return "broken" }

func (failingLocalEntry) EncodeRLP(io.Writer) error {
	return errors.New("encode failed")
}

func TestLocalNodeSetSkipsInvalidEntry(t *testing.T) {
	db, err := OpenDB(context.Background(), "", t.TempDir())
	if err != nil {
		t.Fatalf("OpenDB() error = %v", err)
	}
	defer db.Close()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	ln := NewLocalNode(db, key)

	ln.Set(failingLocalEntry{})

	if node := ln.Node(); node == nil {
		t.Fatal("Node() returned nil after invalid entry")
	}
}

func TestLocalNodeSetSkipsNilEntry(t *testing.T) {
	db, err := OpenDB(context.Background(), "", t.TempDir())
	if err != nil {
		t.Fatalf("OpenDB() error = %v", err)
	}
	defer db.Close()

	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	ln := NewLocalNode(db, key)

	ln.Set(nil)

	if node := ln.Node(); node == nil {
		t.Fatal("Node() returned nil after nil entry")
	}
}
