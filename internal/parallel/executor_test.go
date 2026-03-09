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
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// balanceKey returns a LocationKey for balance of address idx.
func balanceKey(idx byte) LocationKey {
	var a types.Address
	a[0] = idx
	return LocationKey{Address: a, Field: FieldBalance}
}

func TestExecutor_ZeroTxs(t *testing.T) {
	exec := NewExecutor(0, 4, func(txIndex int, rw *ReadWriteSet) error {
		return nil
	})
	results := exec.Run()
	if results != nil {
		t.Fatalf("expected nil for 0 txs, got %v", results)
	}
}

func TestExecutor_SequentialFallback(t *testing.T) {
	var execOrder []int
	exec := NewExecutor(2, 4, func(txIndex int, rw *ReadWriteSet) error {
		execOrder = append(execOrder, txIndex)
		key := balanceKey(byte(txIndex))
		rw.RecordWrite(key, []byte{byte(txIndex * 10)})
		return nil
	})
	results := exec.Run()
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("tx %d had error: %v", i, r.Err)
		}
	}
}

func TestExecutor_IndependentTxs(t *testing.T) {
	numTxs := 10
	exec := NewExecutor(numTxs, 4, func(txIndex int, rw *ReadWriteSet) error {
		key := balanceKey(byte(txIndex))
		rw.RecordWrite(key, []byte{byte(txIndex)})
		return nil
	})

	results := exec.Run()
	if len(results) != numTxs {
		t.Fatalf("expected %d results, got %d", numTxs, len(results))
	}
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("tx %d error: %v", i, r.Err)
		}
	}

	executions, aborts := exec.Stats()
	if aborts != 0 {
		t.Fatalf("expected 0 aborts for independent txs, got %d", aborts)
	}
	if executions != int64(numTxs) {
		t.Fatalf("expected %d executions, got %d", numTxs, executions)
	}
}

func TestExecutor_ConflictingTxs(t *testing.T) {
	numTxs := 5
	sharedKey := balanceKey(0)

	var exec *Executor
	exec = NewExecutor(numTxs, 2, func(txIndex int, rw *ReadWriteSet) error {
		val, writerTx, writerInc, found := exec.MVS().Read(sharedKey, txIndex)
		if found {
			rw.RecordRead(sharedKey, writerTx, writerInc, false)
		} else {
			rw.RecordRead(sharedKey, -1, 0, true)
			val = []byte{0}
		}
		newVal := []byte{val[0] + 1}
		rw.RecordWrite(sharedKey, newVal)
		return nil
	})

	results := exec.Run()
	if len(results) != numTxs {
		t.Fatalf("expected %d results, got %d", numTxs, len(results))
	}
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("tx %d error: %v", i, r.Err)
		}
	}

	val, _, _, found := exec.MVS().Read(sharedKey, numTxs)
	if !found {
		t.Fatal("final value not found")
	}
	if val[0] != byte(numTxs) {
		t.Fatalf("expected final value %d, got %d", numTxs, val[0])
	}
}

func TestExecutor_ErrorPropagation(t *testing.T) {
	exec := NewExecutor(5, 2, func(txIndex int, rw *ReadWriteSet) error {
		if txIndex == 2 {
			return fmt.Errorf("tx %d failed", txIndex)
		}
		key := balanceKey(byte(txIndex))
		rw.RecordWrite(key, []byte{byte(txIndex)})
		return nil
	})

	results := exec.Run()
	for i, r := range results {
		if i == 2 {
			if r.Err == nil {
				t.Fatal("expected error for tx 2")
			}
		} else {
			if r.Err != nil {
				t.Fatalf("tx %d unexpected error: %v", i, r.Err)
			}
		}
	}
}

func TestExecutor_LargeBlock(t *testing.T) {
	numTxs := 100
	exec := NewExecutor(numTxs, 8, func(txIndex int, rw *ReadWriteSet) error {
		var addr types.Address
		addr[0] = byte(txIndex % 256)
		addr[1] = byte(txIndex / 256)
		key := LocationKey{Address: addr, Field: FieldBalance}
		rw.RecordWrite(key, []byte{byte(txIndex)})
		return nil
	})

	results := exec.Run()
	if len(results) != numTxs {
		t.Fatalf("expected %d results, got %d", numTxs, len(results))
	}
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("tx %d error: %v", i, r.Err)
		}
	}
}

func TestExecutor_SequentialConsistency(t *testing.T) {
	numTxs := 20
	sharedKey := balanceKey(0)

	var parExec *Executor
	parExec = NewExecutor(numTxs, 4, func(txIndex int, rw *ReadWriteSet) error {
		val, writerTx, writerInc, found := parExec.MVS().Read(sharedKey, txIndex)
		if found {
			rw.RecordRead(sharedKey, writerTx, writerInc, false)
		} else {
			rw.RecordRead(sharedKey, -1, 0, true)
			val = []byte{0}
		}
		newVal := []byte{val[0] + byte(1)}
		rw.RecordWrite(sharedKey, newVal)
		return nil
	})

	results := parExec.Run()
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("tx %d error: %v", i, r.Err)
		}
	}

	val, _, _, found := parExec.MVS().Read(sharedKey, numTxs)
	if !found {
		t.Fatal("final value not found")
	}
	if val[0] != byte(numTxs) {
		t.Fatalf("expected %d, got %d", numTxs, val[0])
	}
}

func TestExecutor_MixedReadWrite(t *testing.T) {
	numTxs := 10
	keyA := balanceKey(0xAA)
	keyB := balanceKey(0xBB)

	var execCount atomic.Int64
	var exec *Executor
	exec = NewExecutor(numTxs, 4, func(txIndex int, rw *ReadWriteSet) error {
		execCount.Add(1)
		if txIndex%2 == 0 {
			rw.RecordWrite(keyA, []byte{byte(txIndex)})
		} else {
			val, writerTx, writerInc, found := exec.MVS().Read(keyA, txIndex)
			if found {
				rw.RecordRead(keyA, writerTx, writerInc, false)
				rw.RecordWrite(keyB, val)
			} else {
				rw.RecordRead(keyA, -1, 0, true)
				rw.RecordWrite(keyB, []byte{0xFF})
			}
		}
		return nil
	})

	results := exec.Run()
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("tx %d error: %v", i, r.Err)
		}
	}

	val, writer, _, found := exec.MVS().Read(keyA, numTxs)
	if !found {
		t.Fatal("keyA not found")
	}
	if writer != 8 {
		t.Fatalf("keyA writer: expected 8, got %d", writer)
	}
	if !bytes.Equal(val, []byte{8}) {
		t.Fatalf("keyA value: expected [8], got %v", val)
	}
}

func TestValidator_ValidReadSet(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(1, FieldBalance)

	mvs.Write(key, 0, 0, []byte{10})

	rw := NewReadWriteSet(1)
	rw.RecordRead(key, 0, 0, false)

	if !Validate(mvs, rw) {
		t.Fatal("expected valid read set")
	}
}

func TestValidator_StaleRead_DifferentWriter(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(1, FieldBalance)

	mvs.Write(key, 0, 0, []byte{10})
	rw := NewReadWriteSet(2)
	rw.RecordRead(key, 0, 0, false)

	mvs.Write(key, 1, 0, []byte{20})

	if Validate(mvs, rw) {
		t.Fatal("expected invalid: writer changed")
	}
}

func TestValidator_StaleRead_FromBase(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(1, FieldBalance)

	rw := NewReadWriteSet(2)
	rw.RecordRead(key, -1, 0, true)

	if !Validate(mvs, rw) {
		t.Fatal("expected valid: no MVS write exists")
	}

	mvs.Write(key, 0, 0, []byte{10})

	if Validate(mvs, rw) {
		t.Fatal("expected invalid: MVS write appeared for base read")
	}
}

func TestValidator_StaleRead_WriterRemoved(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(1, FieldBalance)

	mvs.Write(key, 0, 0, []byte{10})
	rw := NewReadWriteSet(2)
	rw.RecordRead(key, 0, 0, false)

	mvs.Delete(key, 0)

	if Validate(mvs, rw) {
		t.Fatal("expected invalid: writer removed")
	}
}

func TestValidator_StaleRead_IncarnationChanged(t *testing.T) {
	mvs := NewMVS()
	key := makeKey(1, FieldBalance)

	// tx0 writes at incarnation 0.
	mvs.Write(key, 0, 0, []byte{10})
	rw := NewReadWriteSet(1)
	rw.RecordRead(key, 0, 0, false)

	// Validate: should pass (writer=0, inc=0).
	if !Validate(mvs, rw) {
		t.Fatal("expected valid")
	}

	// tx0 re-executes at incarnation 1 (writes different value).
	mvs.Write(key, 0, 1, []byte{20})

	// Validate: should fail (writer=0 same, but incarnation changed 0→1).
	if Validate(mvs, rw) {
		t.Fatal("expected invalid: incarnation changed")
	}
}
