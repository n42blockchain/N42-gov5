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

package parallel

import (
	"bytes"
	"sync"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func makeKey(addr byte, field LocationField) LocationKey {
	var a types.Address
	a[0] = addr
	return LocationKey{Address: a, Field: field}
}

func makeStorageKey(addr byte, slot byte) LocationKey {
	var a types.Address
	a[0] = addr
	var h types.Hash
	h[0] = slot
	return LocationKey{Address: a, Field: FieldStorage, Slot: h}
}

func TestMVS_WriteAndRead(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(1, FieldBalance)

	_, _, _, found := mvs.Read(key, 5)
	if found {
		t.Fatal("expected not found for empty MVS")
	}

	mvs.Write(key, 0, 0, []byte{10})

	val, writer, _, found := mvs.Read(key, 5)
	if !found {
		t.Fatal("expected found")
	}
	if writer != 0 {
		t.Fatalf("expected writer 0, got %d", writer)
	}
	if !bytes.Equal(val, []byte{10}) {
		t.Fatalf("expected [10], got %v", val)
	}

	_, _, _, found = mvs.Read(key, 0)
	if found {
		t.Fatal("tx0 should not see its own write")
	}
}

func TestMVS_MultipleWriters(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(1, FieldNonce)

	mvs.Write(key, 0, 0, []byte{1})
	mvs.Write(key, 3, 0, []byte{3})
	mvs.Write(key, 7, 0, []byte{7})

	val, writer, _, found := mvs.Read(key, 5)
	if !found || writer != 3 || !bytes.Equal(val, []byte{3}) {
		t.Fatalf("tx5 read: found=%v, writer=%d, val=%v", found, writer, val)
	}

	val, writer, _, found = mvs.Read(key, 1)
	if !found || writer != 0 || !bytes.Equal(val, []byte{1}) {
		t.Fatalf("tx1 read: found=%v, writer=%d, val=%v", found, writer, val)
	}

	val, writer, _, found = mvs.Read(key, 10)
	if !found || writer != 7 || !bytes.Equal(val, []byte{7}) {
		t.Fatalf("tx10 read: found=%v, writer=%d, val=%v", found, writer, val)
	}
}

func TestMVS_OverwriteOnReExecution(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(2, FieldBalance)

	mvs.Write(key, 3, 0, []byte{100})
	mvs.Write(key, 3, 1, []byte{200})

	val, writer, inc, found := mvs.Read(key, 5)
	if !found || writer != 3 || inc != 1 || !bytes.Equal(val, []byte{200}) {
		t.Fatalf("expected overwritten value 200 inc=1, got %v (writer=%d, inc=%d, found=%v)", val, writer, inc, found)
	}
}

func TestMVS_Delete(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(3, FieldCode)

	mvs.Write(key, 2, 0, []byte{0xAB})
	mvs.Write(key, 5, 0, []byte{0xCD})

	mvs.Delete(key, 2)

	_, _, _, found := mvs.Read(key, 4)
	if found {
		t.Fatal("expected not found after delete")
	}

	val, writer, _, found := mvs.Read(key, 6)
	if !found || writer != 5 || !bytes.Equal(val, []byte{0xCD}) {
		t.Fatalf("tx6 read after delete: found=%v, writer=%d, val=%v", found, writer, val)
	}
}

func TestMVS_DeleteAll(t *testing.T) {
	mvs := NewMVS()
	key1 := makeKey(1, FieldBalance)
	key2 := makeKey(2, FieldNonce)

	mvs.Write(key1, 3, 0, []byte{10})
	mvs.Write(key2, 3, 0, []byte{20})
	mvs.Write(key1, 5, 0, []byte{50})

	mvs.DeleteAll(3)

	_, _, _, found := mvs.Read(key1, 4)
	if found {
		t.Fatal("key1 should not be found after DeleteAll")
	}
	_, _, _, found = mvs.Read(key2, 4)
	if found {
		t.Fatal("key2 should not be found after DeleteAll")
	}

	val, writer, _, found := mvs.Read(key1, 6)
	if !found || writer != 5 || !bytes.Equal(val, []byte{50}) {
		t.Fatalf("tx6 key1 after DeleteAll: found=%v, writer=%d, val=%v", found, writer, val)
	}
}

func TestMVS_NilValue(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(4, FieldSuicide)

	mvs.Write(key, 1, 0, nil)

	val, writer, _, found := mvs.Read(key, 5)
	if !found || writer != 1 || val != nil {
		t.Fatalf("nil value read: found=%v, writer=%d, val=%v", found, writer, val)
	}
}

func TestMVS_StorageSlot(t *testing.T) {
	mvs := NewMVS()
	key1 := makeStorageKey(1, 0xAA)
	key2 := makeStorageKey(1, 0xBB)

	mvs.Write(key1, 0, 0, []byte{1})
	mvs.Write(key2, 0, 0, []byte{2})

	val, _, _, _ := mvs.Read(key1, 1)
	if !bytes.Equal(val, []byte{1}) {
		t.Fatalf("slot AA: expected [1], got %v", val)
	}
	val, _, _, _ = mvs.Read(key2, 1)
	if !bytes.Equal(val, []byte{2}) {
		t.Fatalf("slot BB: expected [2], got %v", val)
	}
}

func TestMVS_ConcurrentAccess(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(5, FieldBalance)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(txIdx int) {
			defer wg.Done()
			mvs.Write(key, txIdx, 0, []byte{byte(txIdx)})
		}(i)
	}
	wg.Wait()

	val, writer, _, found := mvs.Read(key, 100)
	if !found || writer != 99 || !bytes.Equal(val, []byte{99}) {
		t.Fatalf("concurrent read: found=%v, writer=%d, val=%v", found, writer, val)
	}
}

func TestMVS_ValueCopyIsolation(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(6, FieldBalance)

	original := []byte{1, 2, 3}
	mvs.Write(key, 0, 0, original)

	original[0] = 99

	val, _, _, found := mvs.Read(key, 1)
	if !found || val[0] != 1 {
		t.Fatal("stored value was mutated by external change")
	}
}

func TestMVS_IncarnationTracking(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(7, FieldBalance)

	mvs.Write(key, 0, 0, []byte{10})
	_, _, inc, found := mvs.Read(key, 1)
	if !found || inc != 0 {
		t.Fatalf("expected inc=0, got %d", inc)
	}

	// Re-execute tx0 at incarnation 2.
	mvs.Write(key, 0, 2, []byte{20})
	_, _, inc, found = mvs.Read(key, 1)
	if !found || inc != 2 {
		t.Fatalf("expected inc=2 after overwrite, got %d", inc)
	}
}
