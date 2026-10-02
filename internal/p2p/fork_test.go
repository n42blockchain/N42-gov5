// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package p2p

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/p2p/enode"
	"github.com/n42blockchain/N42/internal/p2p/enr"
)

func TestCurrentForkDigestIsDeterministic(t *testing.T) {
	// The fork digest is the first 4 bytes of the genesis hash, and
	// types.BytesToHash right-aligns short input, so the differing bytes
	// must be in the leading 4 bytes to actually change the digest.
	var h1, h3 types.Hash
	h1[0], h1[1], h1[2] = 1, 2, 3
	h3[0], h3[1], h3[2] = 4, 5, 6

	s1 := &Service{genesisHash: h1}
	s2 := &Service{genesisHash: h1}
	s3 := &Service{genesisHash: h3}

	d1, err := s1.currentForkDigest()
	if err != nil {
		t.Fatalf("currentForkDigest: %v", err)
	}
	d2, err := s2.currentForkDigest()
	if err != nil {
		t.Fatalf("currentForkDigest: %v", err)
	}
	d3, err := s3.currentForkDigest()
	if err != nil {
		t.Fatalf("currentForkDigest: %v", err)
	}
	if d1 != d2 {
		t.Fatal("same genesis hash should produce the same fork digest")
	}
	if d1 == d3 {
		t.Fatal("different genesis hashes should produce different fork digests")
	}
}

func newLocalNode(t *testing.T) *enode.LocalNode {
	t.Helper()
	db, err := enode.OpenDB(context.Background(), "", t.TempDir())
	if err != nil {
		t.Fatalf("enode.OpenDB: %v", err)
	}
	t.Cleanup(db.Close)
	priv, err := privKey(&conf.P2PConfig{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("privKey: %v", err)
	}
	return enode.NewLocalNode(db, priv)
}

func TestAddForkEntryAndCompareForkENR(t *testing.T) {
	var genesis types.Hash
	genesis[0], genesis[1], genesis[2] = 9, 9, 9
	s := &Service{genesisHash: genesis}

	ln := newLocalNode(t)
	if _, err := addForkEntry(ln, genesis); err != nil {
		t.Fatalf("addForkEntry: %v", err)
	}

	if err := s.compareForkENR(ln.Node().Record()); err != nil {
		t.Fatalf("compareForkENR should accept a matching fork digest: %v", err)
	}

	// A different local genesis hash should now mismatch.
	var otherGenesis types.Hash
	otherGenesis[0], otherGenesis[1], otherGenesis[2] = 1, 1, 1
	other := &Service{genesisHash: otherGenesis}
	if err := other.compareForkENR(ln.Node().Record()); err == nil {
		t.Fatal("compareForkENR should reject a mismatching fork digest")
	}
}

func TestCompareForkENRMissingEntry(t *testing.T) {
	s := &Service{genesisHash: types.BytesToHash([]byte{1})}
	record := &enr.Record{}
	if err := s.compareForkENR(record); err == nil {
		t.Fatal("compareForkENR should fail when the ENR has no fork entry")
	}
}
