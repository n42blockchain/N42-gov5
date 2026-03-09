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
	"encoding/binary"
	"fmt"
	"math/rand"
	"testing"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// --- DAG Construction Benchmarks ---

// BenchmarkBuildDAG measures the cost of building the dependency graph.
func BenchmarkBuildDAG(b *testing.B) {
	for _, numTxs := range []int{10, 50, 100, 200, 500} {
		b.Run(fmt.Sprintf("txs=%d", numTxs), func(b *testing.B) {
			txInfos := makeIndependentTxInfos(numTxs)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				BuildDAG(txInfos)
			}
		})
	}
}

// BenchmarkBuildDAG_WithConflicts measures DAG construction with many conflicts.
func BenchmarkBuildDAG_WithConflicts(b *testing.B) {
	for _, numTxs := range []int{10, 50, 100, 200} {
		b.Run(fmt.Sprintf("txs=%d", numTxs), func(b *testing.B) {
			txInfos := makeConflictingTxInfos(numTxs)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				BuildDAG(txInfos)
			}
		})
	}
}

// BenchmarkBuildDAG_WithStorage measures DAG construction with storage slot access lists.
func BenchmarkBuildDAG_WithStorage(b *testing.B) {
	for _, numTxs := range []int{50, 100, 200} {
		b.Run(fmt.Sprintf("txs=%d", numTxs), func(b *testing.B) {
			txInfos := makeStorageTxInfos(numTxs, 5) // 5 storage keys per tx
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				BuildDAG(txInfos)
			}
		})
	}
}

// --- DAGExecutor vs Block-STM Benchmarks ---

// BenchmarkDAGExecutor_VsBlockSTM_Independent compares DAG executor vs Block-STM
// for independent transactions (best case for both).
func BenchmarkDAGExecutor_VsBlockSTM_Independent(b *testing.B) {
	for _, numTxs := range []int{50, 100, 200} {
		fn := independentExecFn(10)
		txInfos := makeIndependentTxInfos(numTxs)

		b.Run(fmt.Sprintf("blockstm/txs=%d", numTxs), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewExecutor(numTxs, 0, fn)
				e.Run()
			}
		})

		b.Run(fmt.Sprintf("dag/txs=%d", numTxs), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewDAGExecutor(numTxs, 0, txInfos, fn)
				e.Run()
			}
		})
	}
}

// BenchmarkDAGExecutor_VsBlockSTM_HotSpot compares both executors under hot-spot
// contention where DAG should significantly outperform pure Block-STM.
func BenchmarkDAGExecutor_VsBlockSTM_HotSpot(b *testing.B) {
	numTxs := 100
	for _, ratio := range []float64{0.05, 0.10, 0.25, 0.50} {
		fn := hotSpotExecFn(numTxs, ratio, 10)
		txInfos := makeHotSpotTxInfos(numTxs, ratio)

		b.Run(fmt.Sprintf("blockstm/hot=%.0f%%", ratio*100), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewExecutor(numTxs, 0, fn)
				e.Run()
			}
		})

		b.Run(fmt.Sprintf("dag/hot=%.0f%%", ratio*100), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewDAGExecutor(numTxs, 0, txInfos, fn)
				e.Run()
			}
		})
	}
}

// BenchmarkDAGExecutor_VsBlockSTM_FullyConflicting compares both executors
// when all transactions conflict (worst case — should be sequential in both).
func BenchmarkDAGExecutor_VsBlockSTM_FullyConflicting(b *testing.B) {
	for _, numTxs := range []int{10, 50, 100} {
		fn := conflictingExecFn(10)
		txInfos := makeConflictingTxInfos(numTxs)

		b.Run(fmt.Sprintf("blockstm/txs=%d", numTxs), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewExecutor(numTxs, 0, fn)
				e.Run()
			}
		})

		b.Run(fmt.Sprintf("dag/txs=%d", numTxs), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewDAGExecutor(numTxs, 0, txInfos, fn)
				e.Run()
			}
		})
	}
}

// BenchmarkDAGExecutor_WaveConvergence reports execution and abort stats.
func BenchmarkDAGExecutor_WaveConvergence(b *testing.B) {
	patterns := []struct {
		name    string
		fn      TxExecuteFunc
		txInfos []TxAccessInfo
	}{
		{
			"independent",
			independentExecFn(1),
			makeIndependentTxInfos(100),
		},
		{
			"hot5%",
			hotSpotExecFn(100, 0.05, 1),
			makeHotSpotTxInfos(100, 0.05),
		},
		{
			"hot25%",
			hotSpotExecFn(100, 0.25, 1),
			makeHotSpotTxInfos(100, 0.25),
		},
		{
			"hot50%",
			hotSpotExecFn(100, 0.50, 1),
			makeHotSpotTxInfos(100, 0.50),
		},
		{
			"conflicting",
			conflictingExecFn(1),
			makeConflictingTxInfos(100),
		},
	}

	for _, p := range patterns {
		b.Run(p.name, func(b *testing.B) {
			var totalExecs, totalAborts int64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewDAGExecutor(len(p.txInfos), 0, p.txInfos, p.fn)
				e.Run()
				execs, aborts := e.Stats()
				totalExecs += execs
				totalAborts += aborts
			}
			b.ReportMetric(float64(totalExecs)/float64(b.N), "executions/op")
			b.ReportMetric(float64(totalAborts)/float64(b.N), "aborts/op")
			b.ReportMetric(float64(totalExecs)/float64(b.N)/float64(len(p.txInfos)), "exec-ratio")
		})
	}
}

// --- Benchmark Helpers ---

func benchTxAddr(i int) types.Address {
	var addr types.Address
	binary.BigEndian.PutUint64(addr[12:], uint64(i))
	return addr
}

func benchTxHash(i int) types.Hash {
	var h types.Hash
	binary.BigEndian.PutUint64(h[24:], uint64(i))
	return h
}

// makeIndependentTxInfos creates txInfos where each tx has a unique sender and recipient.
func makeIndependentTxInfos(numTxs int) []TxAccessInfo {
	infos := make([]TxAccessInfo, numTxs)
	for i := 0; i < numTxs; i++ {
		from := benchTxAddr(i)
		to := benchTxAddr(i + numTxs)
		infos[i] = TxAccessInfo{
			Index:         i,
			From:          from,
			To:            &to,
			HasAccessList: true,
		}
	}
	return infos
}

// makeConflictingTxInfos creates txInfos where all txs share the same recipient.
func makeConflictingTxInfos(numTxs int) []TxAccessInfo {
	sharedAddr := benchTxAddr(999999)
	infos := make([]TxAccessInfo, numTxs)
	for i := 0; i < numTxs; i++ {
		from := benchTxAddr(i)
		infos[i] = TxAccessInfo{
			Index:         i,
			From:          from,
			To:            &sharedAddr,
			HasAccessList: true,
		}
	}
	return infos
}

// makeHotSpotTxInfos creates txInfos where hotRatio fraction share a "hot" address.
func makeHotSpotTxInfos(numTxs int, hotRatio float64) []TxAccessInfo {
	rng := rand.New(rand.NewSource(42))
	hotCount := int(float64(numTxs) * hotRatio)
	if hotCount < 1 {
		hotCount = 1
	}
	isHot := make([]bool, numTxs)
	perm := rng.Perm(numTxs)
	for i := 0; i < hotCount; i++ {
		isHot[perm[i]] = true
	}

	hotAddr := benchTxAddr(999999)
	infos := make([]TxAccessInfo, numTxs)
	for i := 0; i < numTxs; i++ {
		from := benchTxAddr(i)
		if isHot[i] {
			infos[i] = TxAccessInfo{
				Index:         i,
				From:          from,
				To:            &hotAddr,
				HasAccessList: true,
				AccessList: transaction.AccessList{
					{Address: hotAddr, StorageKeys: nil},
				},
			}
		} else {
			to := benchTxAddr(i + numTxs)
			infos[i] = TxAccessInfo{
				Index:         i,
				From:          from,
				To:            &to,
				HasAccessList: true,
			}
		}
	}
	return infos
}

// makeStorageTxInfos creates txInfos with storage slot access lists.
func makeStorageTxInfos(numTxs, slotsPerTx int) []TxAccessInfo {
	contractAddr := benchTxAddr(0xDEAD)
	infos := make([]TxAccessInfo, numTxs)
	for i := 0; i < numTxs; i++ {
		from := benchTxAddr(i)
		to := benchTxAddr(i + numTxs)
		slots := make([]types.Hash, slotsPerTx)
		for j := 0; j < slotsPerTx; j++ {
			slots[j] = benchTxHash(i*slotsPerTx + j)
		}
		infos[i] = TxAccessInfo{
			Index:         i,
			From:          from,
			To:            &to,
			HasAccessList: true,
			AccessList: transaction.AccessList{
				{Address: contractAddr, StorageKeys: slots},
			},
		}
	}
	return infos
}
