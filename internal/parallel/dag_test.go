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
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// --- Helper functions ---

func makeAddr(b byte) types.Address {
	var addr types.Address
	addr[0] = b
	return addr
}

func makeAddrPtr(b byte) *types.Address {
	addr := makeAddr(b)
	return &addr
}

func makeHash(b byte) types.Hash {
	var h types.Hash
	h[0] = b
	return h
}

// makeTxInfo creates a TxAccessInfo with given access list.
func makeTxInfo(index int, from byte, to *byte, accessList transaction.AccessList) TxAccessInfo {
	info := TxAccessInfo{
		Index:         index,
		From:          makeAddr(from),
		HasAccessList: true,
		AccessList:    accessList,
	}
	if to != nil {
		addr := makeAddr(*to)
		info.To = &addr
	}
	return info
}

// makeTxInfoNoAL creates a TxAccessInfo without an access list.
func makeTxInfoNoAL(index int, from byte, to *byte) TxAccessInfo {
	info := TxAccessInfo{
		Index:         index,
		From:          makeAddr(from),
		HasAccessList: false,
	}
	if to != nil {
		addr := makeAddr(*to)
		info.To = &addr
	}
	return info
}

func bytePtr(b byte) *byte {
	return &b
}

// --- BuildDAG Tests ---

func TestBuildDAG_NoDependencies(t *testing.T) {
	// 4 transactions, each with unique sender and recipient, no overlapping access lists.
	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, bytePtr(0x11), nil),
		makeTxInfo(1, 0x02, bytePtr(0x12), nil),
		makeTxInfo(2, 0x03, bytePtr(0x13), nil),
		makeTxInfo(3, 0x04, bytePtr(0x14), nil),
	}

	dag := BuildDAG(txInfos)

	if dag.NumTxs != 4 {
		t.Fatalf("expected 4 txs, got %d", dag.NumTxs)
	}
	if len(dag.Dependencies) != 0 {
		t.Fatalf("expected 0 dependencies, got %d: %+v", len(dag.Dependencies), dag.Dependencies)
	}
	if len(dag.Independent) != 4 {
		t.Fatalf("expected 4 independent txs, got %d", len(dag.Independent))
	}
}

func TestBuildDAG_LinearChain(t *testing.T) {
	// TX0, TX1, TX2 all send to the same address — forms a chain: TX0 -> TX1 -> TX2.
	sharedAddr := bytePtr(0xAA)
	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, sharedAddr, nil),
		makeTxInfo(1, 0x02, sharedAddr, nil),
		makeTxInfo(2, 0x03, sharedAddr, nil),
	}

	dag := BuildDAG(txInfos)

	if dag.NumTxs != 3 {
		t.Fatalf("expected 3 txs, got %d", dag.NumTxs)
	}

	// TX0 should be independent, TX1 depends on TX0, TX2 depends on TX1.
	if !containsInt(dag.Independent, 0) {
		t.Fatal("TX0 should be independent")
	}
	if !dag.HasDependency(1, 0) {
		t.Fatal("TX1 should depend on TX0")
	}
	if !dag.HasDependency(2, 1) {
		t.Fatal("TX2 should depend on TX1")
	}
}

func TestBuildDAG_SharedStorageSlot(t *testing.T) {
	// TX0 and TX1 access the same storage slot on the same contract.
	contractAddr := makeAddr(0xCC)
	slot := makeHash(0x42)

	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, bytePtr(0x10), transaction.AccessList{
			{Address: contractAddr, StorageKeys: []types.Hash{slot}},
		}),
		makeTxInfo(1, 0x02, bytePtr(0x20), transaction.AccessList{
			{Address: contractAddr, StorageKeys: []types.Hash{slot}},
		}),
		makeTxInfo(2, 0x03, bytePtr(0x30), nil), // independent
	}

	dag := BuildDAG(txInfos)

	if !dag.HasDependency(1, 0) {
		t.Fatal("TX1 should depend on TX0 (shared storage slot)")
	}
	if dag.HasDependency(2, 0) || dag.HasDependency(2, 1) {
		t.Fatal("TX2 should be independent")
	}
}

func TestBuildDAG_DiamondDependency(t *testing.T) {
	// Diamond pattern:
	//   TX0 -> TX1 (share address A)
	//   TX0 -> TX2 (share address B)
	//   TX1 -> TX3 (share address C)
	//   TX2 -> TX3 (share address D)
	addrA := makeAddr(0xA0)
	addrB := makeAddr(0xB0)
	addrC := makeAddr(0xC0)
	addrD := makeAddr(0xD0)

	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, nil, transaction.AccessList{
			{Address: addrA, StorageKeys: nil},
			{Address: addrB, StorageKeys: nil},
		}),
		makeTxInfo(1, 0x02, nil, transaction.AccessList{
			{Address: addrA, StorageKeys: nil},
			{Address: addrC, StorageKeys: nil},
		}),
		makeTxInfo(2, 0x03, nil, transaction.AccessList{
			{Address: addrB, StorageKeys: nil},
			{Address: addrD, StorageKeys: nil},
		}),
		makeTxInfo(3, 0x04, nil, transaction.AccessList{
			{Address: addrC, StorageKeys: nil},
			{Address: addrD, StorageKeys: nil},
		}),
	}

	dag := BuildDAG(txInfos)

	// TX0 independent.
	if !containsInt(dag.Independent, 0) {
		t.Fatal("TX0 should be independent")
	}
	// TX1 depends on TX0 (via addrA).
	if !dag.HasDependency(1, 0) {
		t.Fatal("TX1 should depend on TX0")
	}
	// TX2 depends on TX0 (via addrB).
	if !dag.HasDependency(2, 0) {
		t.Fatal("TX2 should depend on TX0")
	}
	// TX3 depends on TX1 (via addrC) and TX2 (via addrD).
	if !dag.HasDependency(3, 1) {
		t.Fatal("TX3 should depend on TX1")
	}
	if !dag.HasDependency(3, 2) {
		t.Fatal("TX3 should depend on TX2")
	}
}

func TestBuildDAG_NoAccessList(t *testing.T) {
	// TX1 has no access list — it becomes a barrier.
	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, bytePtr(0x10), nil),
		makeTxInfoNoAL(1, 0x02, bytePtr(0x20)),
		makeTxInfo(2, 0x03, bytePtr(0x30), nil),
	}

	dag := BuildDAG(txInfos)

	if len(dag.Unknown) != 1 || dag.Unknown[0] != 1 {
		t.Fatalf("expected TX1 to be unknown, got %v", dag.Unknown)
	}

	// TX1 should depend on TX0 (barrier: depends on all earlier).
	if !dag.HasDependency(1, 0) {
		t.Fatal("TX1 (unknown) should depend on TX0")
	}
	// TX2 should depend on TX1 (barrier: all later depend on unknown).
	if !dag.HasDependency(2, 1) {
		t.Fatal("TX2 should depend on TX1 (unknown barrier)")
	}
}

func TestBuildDAG_SameSender(t *testing.T) {
	// Two transactions from the same sender: they must be ordered.
	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0xAA, bytePtr(0x10), nil),
		makeTxInfo(1, 0xAA, bytePtr(0x20), nil),
	}

	dag := BuildDAG(txInfos)

	if !dag.HasDependency(1, 0) {
		t.Fatal("TX1 should depend on TX0 (same sender)")
	}
}

func TestBuildDAG_Empty(t *testing.T) {
	dag := BuildDAG(nil)
	if dag.NumTxs != 0 {
		t.Fatalf("expected 0 txs, got %d", dag.NumTxs)
	}
}

// --- Schedule Tests ---

func TestDAGSchedule_AllIndependent(t *testing.T) {
	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, bytePtr(0x11), nil),
		makeTxInfo(1, 0x02, bytePtr(0x12), nil),
		makeTxInfo(2, 0x03, bytePtr(0x13), nil),
		makeTxInfo(3, 0x04, bytePtr(0x14), nil),
	}

	dag := BuildDAG(txInfos)
	batches := dag.Schedule(4)

	// All independent txs should be in a single batch.
	if len(batches) != 1 {
		t.Fatalf("expected 1 batch for fully independent txs, got %d", len(batches))
	}
	if len(batches[0]) != 4 {
		t.Fatalf("expected 4 txs in first batch, got %d", len(batches[0]))
	}
}

func TestDAGSchedule_LinearChain(t *testing.T) {
	sharedAddr := bytePtr(0xAA)
	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, sharedAddr, nil),
		makeTxInfo(1, 0x02, sharedAddr, nil),
		makeTxInfo(2, 0x03, sharedAddr, nil),
	}

	dag := BuildDAG(txInfos)
	batches := dag.Schedule(4)

	// Linear chain should produce 3 batches of 1 tx each.
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches for linear chain, got %d", len(batches))
	}
	for i, batch := range batches {
		if len(batch) != 1 {
			t.Fatalf("batch %d: expected 1 tx, got %d", i, len(batch))
		}
	}
}

func TestDAGSchedule_MaxParallelism(t *testing.T) {
	// Diamond: TX0, then TX1 and TX2 in parallel, then TX3.
	addrA := makeAddr(0xA0)
	addrB := makeAddr(0xB0)
	addrC := makeAddr(0xC0)
	addrD := makeAddr(0xD0)

	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, nil, transaction.AccessList{
			{Address: addrA}, {Address: addrB},
		}),
		makeTxInfo(1, 0x02, nil, transaction.AccessList{
			{Address: addrA}, {Address: addrC},
		}),
		makeTxInfo(2, 0x03, nil, transaction.AccessList{
			{Address: addrB}, {Address: addrD},
		}),
		makeTxInfo(3, 0x04, nil, transaction.AccessList{
			{Address: addrC}, {Address: addrD},
		}),
	}

	dag := BuildDAG(txInfos)
	batches := dag.Schedule(4)

	// Expected: batch0=[TX0], batch1=[TX1,TX2], batch2=[TX3]
	if len(batches) != 3 {
		t.Fatalf("expected 3 batches for diamond, got %d: %v", len(batches), batches)
	}
	if len(batches[0]) != 1 || batches[0][0] != 0 {
		t.Fatalf("batch 0: expected [TX0], got %v", batches[0])
	}
	if len(batches[1]) != 2 {
		t.Fatalf("batch 1: expected 2 txs (TX1, TX2), got %v", batches[1])
	}
	if len(batches[2]) != 1 || batches[2][0] != 3 {
		t.Fatalf("batch 2: expected [TX3], got %v", batches[2])
	}
}

func TestDAGSchedule_Empty(t *testing.T) {
	dag := BuildDAG(nil)
	batches := dag.Schedule(4)
	if batches != nil {
		t.Fatalf("expected nil batches for empty DAG, got %v", batches)
	}
}

// --- DAGExecutor Tests ---

func TestDAGExecutor_ZeroTxs(t *testing.T) {
	exec := NewDAGExecutor(0, 4, nil, func(txIndex int, rw *ReadWriteSet) error {
		return nil
	})
	results := exec.Run()
	if results != nil {
		t.Fatalf("expected nil for 0 txs, got %v", results)
	}
}

func TestDAGExecutor_Independent(t *testing.T) {
	numTxs := 10
	txInfos := make([]TxAccessInfo, numTxs)
	for i := 0; i < numTxs; i++ {
		txInfos[i] = makeTxInfo(i, byte(i+1), bytePtr(byte(i+0x80)), nil)
	}

	var exec *DAGExecutor
	exec = NewDAGExecutor(numTxs, 4, txInfos, func(txIndex int, rw *ReadWriteSet) error {
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
		t.Fatalf("expected 0 aborts, got %d", aborts)
	}
	if executions != int64(numTxs) {
		t.Fatalf("expected %d executions, got %d", numTxs, executions)
	}
}

func TestDAGExecutor_WithDependencies(t *testing.T) {
	// TX0 -> TX1 -> TX2: all share the same key (sequential chain).
	numTxs := 3
	sharedKey := balanceKey(0xAA)

	// All txs send to the same address to create dependency.
	sharedTo := bytePtr(0xAA)
	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, sharedTo, nil),
		makeTxInfo(1, 0x02, sharedTo, nil),
		makeTxInfo(2, 0x03, sharedTo, nil),
	}

	var exec *DAGExecutor
	exec = NewDAGExecutor(numTxs, 4, txInfos, func(txIndex int, rw *ReadWriteSet) error {
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

	// Final value should be 3 (0 + 1 + 1 + 1).
	val, _, _, found := exec.MVS().Read(sharedKey, numTxs)
	if !found {
		t.Fatal("final value not found")
	}
	if val[0] != byte(numTxs) {
		t.Fatalf("expected final value %d, got %d", numTxs, val[0])
	}
}

func TestDAGExecutor_MixedAccessLists(t *testing.T) {
	// TX0: has access list (independent)
	// TX1: NO access list (barrier)
	// TX2: has access list (independent of TX0, but depends on TX1)
	numTxs := 3
	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, bytePtr(0x10), nil),
		makeTxInfoNoAL(1, 0x02, bytePtr(0x20)),
		makeTxInfo(2, 0x03, bytePtr(0x30), nil),
	}

	exec := NewDAGExecutor(numTxs, 4, txInfos, func(txIndex int, rw *ReadWriteSet) error {
		key := balanceKey(byte(txIndex))
		rw.RecordWrite(key, []byte{byte(txIndex * 10)})
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

	// Verify DAG structure: TX1 should be barrier.
	dag := exec.DAG()
	if !dag.HasDependency(1, 0) {
		t.Fatal("TX1 (unknown) should depend on TX0")
	}
	if !dag.HasDependency(2, 1) {
		t.Fatal("TX2 should depend on TX1 (unknown barrier)")
	}
}

func TestDAGExecutor_ErrorPropagation(t *testing.T) {
	numTxs := 5
	txInfos := make([]TxAccessInfo, numTxs)
	for i := 0; i < numTxs; i++ {
		txInfos[i] = makeTxInfo(i, byte(i+1), bytePtr(byte(i+0x80)), nil)
	}

	exec := NewDAGExecutor(numTxs, 2, txInfos, func(txIndex int, rw *ReadWriteSet) error {
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

func TestDAGExecutor_SequentialConsistency(t *testing.T) {
	// All txs increment a shared counter — must produce same result as sequential.
	numTxs := 20
	sharedKey := balanceKey(0)

	// All txs share address to create DAG dependencies.
	sharedTo := bytePtr(0x00)
	txInfos := make([]TxAccessInfo, numTxs)
	for i := 0; i < numTxs; i++ {
		txInfos[i] = makeTxInfo(i, byte(i+1), sharedTo, nil)
	}

	var exec *DAGExecutor
	exec = NewDAGExecutor(numTxs, 4, txInfos, func(txIndex int, rw *ReadWriteSet) error {
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
		t.Fatalf("expected %d, got %d", numTxs, val[0])
	}
}

func TestDAGExecutor_LargeBlock(t *testing.T) {
	numTxs := 100
	txInfos := make([]TxAccessInfo, numTxs)
	for i := 0; i < numTxs; i++ {
		txInfos[i] = makeTxInfo(i, byte(i%200+1), bytePtr(byte((i+100)%200+1)), nil)
	}

	exec := NewDAGExecutor(numTxs, 8, txInfos, func(txIndex int, rw *ReadWriteSet) error {
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

func TestDAGExecutor_ValidationCatchesMissedDeps(t *testing.T) {
	// Simulate a case where access lists don't capture all dependencies.
	// TX0 and TX1 have independent access lists, but at runtime they
	// both read/write the same key (missed by static analysis).
	numTxs := 4
	sharedKey := balanceKey(0xFF)

	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, bytePtr(0x10), nil),
		makeTxInfo(1, 0x02, bytePtr(0x20), nil),
		makeTxInfo(2, 0x03, bytePtr(0x30), nil),
		makeTxInfo(3, 0x04, bytePtr(0x40), nil),
	}

	var exec *DAGExecutor
	exec = NewDAGExecutor(numTxs, 4, txInfos, func(txIndex int, rw *ReadWriteSet) error {
		// All txs access the same key at runtime (not in access list).
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
	for i, r := range results {
		if r.Err != nil {
			t.Fatalf("tx %d error: %v", i, r.Err)
		}
	}

	// Final value should still be correct thanks to validation+re-execution.
	val, _, _, found := exec.MVS().Read(sharedKey, numTxs)
	if !found {
		t.Fatal("final value not found")
	}
	if !bytes.Equal(val, []byte{byte(numTxs)}) {
		t.Fatalf("expected final value %d, got %d (validation should catch missed deps)", numTxs, val[0])
	}
}

// --- Deps helper test ---

func TestDAG_Deps(t *testing.T) {
	sharedAddr := bytePtr(0xAA)
	txInfos := []TxAccessInfo{
		makeTxInfo(0, 0x01, sharedAddr, nil),
		makeTxInfo(1, 0x02, sharedAddr, nil),
		makeTxInfo(2, 0x03, sharedAddr, nil),
	}

	dag := BuildDAG(txInfos)

	deps0 := dag.Deps(0)
	if len(deps0) != 0 {
		t.Fatalf("TX0 should have no deps, got %v", deps0)
	}

	deps1 := dag.Deps(1)
	if len(deps1) != 1 || deps1[0] != 0 {
		t.Fatalf("TX1 deps: expected [0], got %v", deps1)
	}

	deps2 := dag.Deps(2)
	if len(deps2) != 1 || deps2[0] != 1 {
		t.Fatalf("TX2 deps: expected [1], got %v", deps2)
	}

	// Out of bounds.
	if dag.Deps(-1) != nil {
		t.Fatal("expected nil for negative index")
	}
	if dag.Deps(100) != nil {
		t.Fatal("expected nil for out-of-range index")
	}
}

// --- Utility ---

func containsInt(slice []int, val int) bool {
	for _, v := range slice {
		if v == val {
			return true
		}
	}
	return false
}
