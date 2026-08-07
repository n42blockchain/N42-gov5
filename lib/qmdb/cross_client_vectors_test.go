// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package qmdb

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type crossClientQMDBVector struct {
	Version  string `json:"version"`
	Workload struct {
		InsertCount uint64      `json:"insert_count"`
		Updates     [][2]uint64 `json:"updates"`
		Deletes     []uint64    `json:"deletes"`
	} `json:"workload"`
	Checkpoints struct {
		InsertRoot      string `json:"insert_root"`
		UpdateRoot      string `json:"update_root"`
		DeleteRoot      string `json:"delete_root"`
		NextSlot        uint64 `json:"next_slot"`
		LiveCount       int    `json:"live_count"`
		SnapshotEntries int    `json:"snapshot_entries"`
	} `json:"checkpoints"`
	Proof struct {
		Key uint64 `json:"key"`
		Hex string `json:"hex"`
	} `json:"proof"`
	Portable struct {
		Hex string `json:"hex"`
	} `json:"portable"`
}

// interopKey and interopValue deliberately avoid implementation-specific
// hashing or serialization. The same deterministic workload is replayed by
// the Rust compatibility engine.
func interopKey(i uint64) Hash {
	var key Hash
	binary.LittleEndian.PutUint64(key[:8], i)
	return key
}

func interopValue(i uint64) []byte {
	var value [8]byte
	binary.LittleEndian.PutUint64(value[:], i)
	return value[:]
}

func TestCrossClientQMDBV1Vectors(t *testing.T) {
	encoded, err := os.ReadFile("testdata/cross_client_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector crossClientQMDBVector
	if err := json.Unmarshal(encoded, &vector); err != nil {
		t.Fatal(err)
	}
	if vector.Version != "n42-qmdb-interop-v1" {
		t.Fatalf("unsupported vector version %q", vector.Version)
	}

	tree := New()
	for i := uint64(0); i < vector.Workload.InsertCount; i++ {
		tree.Set(interopKey(i), interopValue(i))
	}
	insertRoot := tree.Root()
	if got := hex.EncodeToString(insertRoot[:]); got != vector.Checkpoints.InsertRoot {
		t.Fatalf("insert root = %s, want %s", got, vector.Checkpoints.InsertRoot)
	}

	for _, update := range vector.Workload.Updates {
		tree.Set(interopKey(update[0]), interopValue(update[1]))
	}
	updateRoot := tree.Root()
	if got := hex.EncodeToString(updateRoot[:]); got != vector.Checkpoints.UpdateRoot {
		t.Fatalf("update root = %s, want %s", got, vector.Checkpoints.UpdateRoot)
	}

	for _, key := range vector.Workload.Deletes {
		tree.Delete(interopKey(key))
	}
	deleteRoot := tree.Root()
	if got := hex.EncodeToString(deleteRoot[:]); got != vector.Checkpoints.DeleteRoot {
		t.Fatalf("delete root = %s, want %s", got, vector.Checkpoints.DeleteRoot)
	}
	if tree.NextSlot() != vector.Checkpoints.NextSlot || tree.LiveCount() != vector.Checkpoints.LiveCount {
		t.Fatalf("tree counters = (%d, %d), want (%d, %d)", tree.NextSlot(), tree.LiveCount(), vector.Checkpoints.NextSlot, vector.Checkpoints.LiveCount)
	}

	snapshot := tree.SnapshotLog()
	if len(snapshot) != vector.Checkpoints.SnapshotEntries {
		t.Fatalf("snapshot entries = %d, want %d", len(snapshot), vector.Checkpoints.SnapshotEntries)
	}
	restored := FromSnapshotLog(snapshot)
	if got := restored.Root(); got != deleteRoot {
		t.Fatalf("snapshot root = %x, want %x", got, deleteRoot)
	}
	proof, ok := tree.GetProof(interopKey(vector.Proof.Key))
	if !ok {
		t.Fatal("updated cross-twig key has no proof")
	}
	proofBytes := proof.Marshal()
	if got := hex.EncodeToString(proofBytes); got != vector.Proof.Hex {
		t.Fatalf("proof bytes changed:\n%s", got)
	}
	if !VerifyEncodedProof(deleteRoot, proofBytes) {
		t.Fatal("generated cross-client proof does not verify")
	}
}

func TestCrossClientPortableV1Fixture(t *testing.T) {
	encodedVector, err := os.ReadFile("testdata/cross_client_v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector crossClientQMDBVector
	if err := json.Unmarshal(encodedVector, &vector); err != nil {
		t.Fatal(err)
	}
	tree := New()
	tree.Set(interopKey(1), interopValue(11))
	tree.Set(interopKey(2), interopValue(22))
	tree.Set(interopKey(1), interopValue(33))
	tree.Delete(interopKey(2))
	snapshot := &PortableSnapshot{
		ChainID:     1143,
		GenesisHash: interopKey(0x11),
		BlockNumber: 42,
		BlockHash:   interopKey(0x22),
		Root:        tree.Root(),
		NextSlot:    tree.NextSlot(),
		Entries:     tree.SnapshotLog(),
	}
	encoded, err := MarshalPortableSnapshot(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(encoded); got != vector.Portable.Hex {
		t.Fatalf("portable bytes changed:\n%s", got)
	}
}
