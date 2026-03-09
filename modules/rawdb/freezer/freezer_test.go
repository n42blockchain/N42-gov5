// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package freezer

import (
	"bytes"
	"fmt"
	"testing"
)

func TestFreezerTableBasic(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	// Append items.
	for i := uint64(0); i < 100; i++ {
		data := []byte(fmt.Sprintf("item-%d", i))
		if err := tbl.Append(i, data); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	if tbl.Items() != 100 {
		t.Fatalf("items: got %d, want 100", tbl.Items())
	}

	// Retrieve items.
	for i := uint64(0); i < 100; i++ {
		data, err := tbl.Retrieve(i)
		if err != nil {
			t.Fatalf("retrieve %d: %v", i, err)
		}
		want := []byte(fmt.Sprintf("item-%d", i))
		if !bytes.Equal(data, want) {
			t.Fatalf("item %d: got %q, want %q", i, data, want)
		}
	}

	// Out of bounds.
	if _, err := tbl.Retrieve(100); err != ErrOutOfBounds {
		t.Fatalf("expected ErrOutOfBounds, got %v", err)
	}
}

func TestFreezerTableBatch(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "batch")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	items := make([][]byte, 50)
	for i := range items {
		items[i] = []byte(fmt.Sprintf("batch-%d", i))
	}
	if err := tbl.AppendBatch(0, items); err != nil {
		t.Fatal(err)
	}

	if tbl.Items() != 50 {
		t.Fatalf("items: got %d, want 50", tbl.Items())
	}

	data, err := tbl.Retrieve(25)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("batch-25")) {
		t.Fatalf("got %q, want %q", data, "batch-25")
	}
}

func TestFreezerTableTruncate(t *testing.T) {
	dir := t.TempDir()
	tbl, err := NewFreezerTable(dir, "trunc")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl.Close()

	for i := uint64(0); i < 20; i++ {
		if err := tbl.Append(i, []byte(fmt.Sprintf("t-%d", i))); err != nil {
			t.Fatal(err)
		}
	}

	if err := tbl.TruncateHead(10); err != nil {
		t.Fatal(err)
	}
	if tbl.Items() != 10 {
		t.Fatalf("after truncate: got %d, want 10", tbl.Items())
	}

	// Can still read items before truncation point.
	data, err := tbl.Retrieve(5)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("t-5")) {
		t.Fatalf("got %q, want %q", data, "t-5")
	}

	// Cannot read truncated items.
	if _, err := tbl.Retrieve(10); err != ErrOutOfBounds {
		t.Fatalf("expected ErrOutOfBounds, got %v", err)
	}

	// Can append again after truncation.
	if err := tbl.Append(10, []byte("new-10")); err != nil {
		t.Fatal(err)
	}
	data, err = tbl.Retrieve(10)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("new-10")) {
		t.Fatalf("got %q, want %q", data, "new-10")
	}
}

func TestFreezerTableReopen(t *testing.T) {
	dir := t.TempDir()

	// Write some data.
	tbl, err := NewFreezerTable(dir, "reopen")
	if err != nil {
		t.Fatal(err)
	}
	for i := uint64(0); i < 10; i++ {
		if err := tbl.Append(i, []byte(fmt.Sprintf("r-%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	tbl.Close()

	// Reopen and verify.
	tbl2, err := NewFreezerTable(dir, "reopen")
	if err != nil {
		t.Fatal(err)
	}
	defer tbl2.Close()

	if tbl2.Items() != 10 {
		t.Fatalf("after reopen: got %d items, want 10", tbl2.Items())
	}

	data, err := tbl2.Retrieve(7)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, []byte("r-7")) {
		t.Fatalf("got %q, want %q", data, "r-7")
	}
}

func TestFreezerFull(t *testing.T) {
	dir := t.TempDir()
	f, err := New(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if f.Frozen() != 0 {
		t.Fatalf("initial frozen: got %d, want 0", f.Frozen())
	}

	// Freeze a batch of blocks.
	data := &FreezeData{
		Headers:    make([][]byte, 5),
		Bodies:     make([][]byte, 5),
		Receipts:   make([][]byte, 5),
		Hashes:     make([][]byte, 5),
		Difficulty: make([][]byte, 5),
	}
	for i := 0; i < 5; i++ {
		data.Headers[i] = []byte(fmt.Sprintf("header-%d", i))
		data.Bodies[i] = []byte(fmt.Sprintf("body-%d", i))
		data.Receipts[i] = []byte(fmt.Sprintf("receipt-%d", i))
		data.Hashes[i] = []byte(fmt.Sprintf("hash-%032d", i))
		data.Difficulty[i] = []byte(fmt.Sprintf("td-%d", i))
	}

	if err := f.Freeze(0, data); err != nil {
		t.Fatal(err)
	}
	if f.Frozen() != 5 {
		t.Fatalf("frozen: got %d, want 5", f.Frozen())
	}

	// Retrieve.
	hdr, err := f.Ancient(TableHeaders, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hdr, []byte("header-3")) {
		t.Fatalf("got %q, want %q", hdr, "header-3")
	}

	if !f.HasAncient(4) {
		t.Fatal("expected HasAncient(4) = true")
	}
	if f.HasAncient(5) {
		t.Fatal("expected HasAncient(5) = false")
	}

	// Truncate.
	if err := f.TruncateHead(3); err != nil {
		t.Fatal(err)
	}
	if f.Frozen() != 3 {
		t.Fatalf("after truncate: frozen=%d, want 3", f.Frozen())
	}

	// Reopen and verify persistence.
	f.Close()

	f2, err := New(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()

	if f2.Frozen() != 3 {
		t.Fatalf("after reopen: frozen=%d, want 3", f2.Frozen())
	}
	body, err := f2.Ancient(TableBodies, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, []byte("body-1")) {
		t.Fatalf("got %q, want %q", body, "body-1")
	}
}
