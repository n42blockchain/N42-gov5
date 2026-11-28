// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package replay

import (
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
)

func TestP1AllocDeltaRoundTrip(t *testing.T) {
	d := NewP1AllocDelta()
	a := types.HexToAddress("0xaa")
	b := types.HexToAddress("0xbb")
	d.AddPrecreate(a, 7)
	d.AddTopup(a, uint256.NewInt(100), 9)
	d.AddTopup(b, uint256.NewInt(5), 3)
	d.AddTopup(b, uint256.NewInt(6), 4)

	path := filepath.Join(t.TempDir(), "delta.json")
	if err := WriteP1AllocDelta(path, d); err != nil {
		t.Fatal(err)
	}
	got, err := ReadP1AllocDelta(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(got.Entries))
	}
	ea := got.Entries[a]
	if ea.Wei.Uint64() != 101 || !ea.Precreate || ea.Topups != 1 || ea.FirstBlock != 7 {
		t.Fatalf("a = %+v", ea)
	}
	eb := got.Entries[b]
	if eb.Wei.Uint64() != 11 || eb.Precreate || eb.Topups != 2 || eb.FirstBlock != 3 {
		t.Fatalf("b = %+v", eb)
	}
	if got.Total().Uint64() != 112 {
		t.Fatalf("total = %s", got.Total().Dec())
	}
}

func TestP1RepairOpenResumeRules(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "delta.json")
	mk := func() *EngineV2 {
		return &EngineV2{cfg: ConfigV2{Profile1RepairOut: path, Profile1Genesis: &conf.Genesis{}}, log: log2.New()}
	}
	if err := (&EngineV2{cfg: ConfigV2{Profile1RepairOut: path}}).p1RepairOpen(0); err == nil {
		t.Fatal("without --p1-genesis: want an error")
	}
	if err := mk().p1RepairOpen(5); err == nil {
		t.Fatal("resumed target without the delta file: want an error")
	}
	e := mk()
	if err := e.p1RepairOpen(0); err != nil || e.p1Repair == nil {
		t.Fatalf("fresh target: %v", err)
	}
	e.p1Repair.AddTopup(types.HexToAddress("0x01"), uint256.NewInt(3), 1)
	if err := e.p1RepairFlush(); err != nil {
		t.Fatal(err)
	}
	if err := mk().p1RepairOpen(0); err == nil {
		t.Fatal("fresh target with an existing delta file: want an error")
	}
	r := mk()
	if err := r.p1RepairOpen(5); err != nil || len(r.p1Repair.Entries) != 1 {
		t.Fatalf("resume: %v", err)
	}
}
