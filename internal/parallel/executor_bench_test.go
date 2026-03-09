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
	"runtime"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// --- Helpers ---

func benchAddr(i int) types.Address {
	var addr types.Address
	binary.BigEndian.PutUint64(addr[12:], uint64(i))
	return addr
}

func benchVal(v uint64) []byte {
	val := make([]byte, 32)
	binary.BigEndian.PutUint64(val[24:], v)
	return val
}

// simulateWork burns CPU to simulate EVM execution.
func simulateWork(microseconds int) {
	start := time.Now()
	for time.Since(start) < time.Duration(microseconds)*time.Microsecond {
	}
}

// independentExecFn: each tx writes only to its own address (zero conflicts).
func independentExecFn(workUS int) TxExecuteFunc {
	return func(txIndex int, rw *ReadWriteSet) error {
		addr := benchAddr(txIndex)
		key := LocationKey{Address: addr, Field: FieldBalance}
		rw.RecordRead(key, -1, 0, true) // read from base
		rw.RecordWrite(key, benchVal(uint64(txIndex*1000)))
		simulateWork(workUS)
		return nil
	}
}

// conflictingExecFn: all txs write to the same address (100% conflict).
func conflictingExecFn(workUS int) TxExecuteFunc {
	sharedAddr := benchAddr(0)
	return func(txIndex int, rw *ReadWriteSet) error {
		key := LocationKey{Address: sharedAddr, Field: FieldBalance}
		if txIndex == 0 {
			rw.RecordRead(key, -1, 0, true)
		} else {
			rw.RecordRead(key, txIndex-1, 0, false)
		}
		rw.RecordWrite(key, benchVal(uint64(txIndex)))
		simulateWork(workUS)
		return nil
	}
}

// hotSpotExecFn: hotRatio fraction of txs access a shared account, rest independent.
func hotSpotExecFn(numTxs int, hotRatio float64, workUS int) TxExecuteFunc {
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
	hotAddr := benchAddr(999999)

	return func(txIndex int, rw *ReadWriteSet) error {
		if isHot[txIndex] {
			key := LocationKey{Address: hotAddr, Field: FieldBalance}
			rw.RecordRead(key, -1, 0, true)
			rw.RecordWrite(key, benchVal(uint64(txIndex)))
		} else {
			addr := benchAddr(txIndex)
			key := LocationKey{Address: addr, Field: FieldBalance}
			rw.RecordRead(key, -1, 0, true)
			rw.RecordWrite(key, benchVal(uint64(txIndex)))
		}
		simulateWork(workUS)
		return nil
	}
}

// serialExecute runs transactions sequentially for comparison baseline.
func serialExecute(numTxs int, execFn TxExecuteFunc) {
	for i := 0; i < numTxs; i++ {
		rw := NewReadWriteSet(i)
		execFn(i, rw)
	}
}

// --- Executor Throughput Benchmarks ---

// BenchmarkExecutor_Independent: best-case for Block-STM (zero conflicts).
func BenchmarkExecutor_Independent(b *testing.B) {
	for _, numTxs := range []int{10, 50, 100, 200, 500} {
		b.Run(fmt.Sprintf("txs=%d", numTxs), func(b *testing.B) {
			fn := independentExecFn(10)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewExecutor(numTxs, 0, fn)
				e.Run()
			}
		})
	}
}

// BenchmarkExecutor_Conflicting: worst-case (100% conflicts).
func BenchmarkExecutor_Conflicting(b *testing.B) {
	for _, numTxs := range []int{10, 50, 100} {
		b.Run(fmt.Sprintf("txs=%d", numTxs), func(b *testing.B) {
			fn := conflictingExecFn(10)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewExecutor(numTxs, 0, fn)
				e.Run()
			}
		})
	}
}

// BenchmarkExecutor_HotSpot: realistic Zipfian-like access patterns.
func BenchmarkExecutor_HotSpot(b *testing.B) {
	numTxs := 100
	for _, ratio := range []float64{0.05, 0.10, 0.25, 0.50} {
		b.Run(fmt.Sprintf("hot=%.0f%%", ratio*100), func(b *testing.B) {
			fn := hotSpotExecFn(numTxs, ratio, 10)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewExecutor(numTxs, 0, fn)
				e.Run()
			}
		})
	}
}

// BenchmarkExecutor_VsSerial: parallel vs sequential comparison.
func BenchmarkExecutor_VsSerial(b *testing.B) {
	for _, numTxs := range []int{50, 100, 200} {
		fn := independentExecFn(50) // 50μs per tx

		b.Run(fmt.Sprintf("serial/txs=%d", numTxs), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				serialExecute(numTxs, fn)
			}
		})

		b.Run(fmt.Sprintf("parallel/txs=%d", numTxs), func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewExecutor(numTxs, 0, fn)
				e.Run()
			}
		})
	}
}

// BenchmarkExecutor_WorkerScaling: performance with different worker counts.
func BenchmarkExecutor_WorkerScaling(b *testing.B) {
	numTxs := 100
	maxCPU := runtime.NumCPU()
	workers := []int{1, 2, 4}
	if maxCPU >= 8 {
		workers = append(workers, 8)
	}
	if maxCPU >= 16 {
		workers = append(workers, 16)
	}

	for _, w := range workers {
		b.Run(fmt.Sprintf("workers=%d", w), func(b *testing.B) {
			fn := independentExecFn(20)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewExecutor(numTxs, w, fn)
				e.Run()
			}
		})
	}
}

// BenchmarkExecutor_WaveConvergence reports execution stats per conflict pattern.
func BenchmarkExecutor_WaveConvergence(b *testing.B) {
	patterns := []struct {
		name string
		fn   TxExecuteFunc
	}{
		{"independent", independentExecFn(1)},
		{"hot5%", hotSpotExecFn(100, 0.05, 1)},
		{"hot25%", hotSpotExecFn(100, 0.25, 1)},
		{"hot50%", hotSpotExecFn(100, 0.50, 1)},
		{"conflicting", conflictingExecFn(1)},
	}

	for _, p := range patterns {
		b.Run(p.name, func(b *testing.B) {
			var totalExecs, totalAborts int64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				e := NewExecutor(100, 0, p.fn)
				e.Run()
				execs, aborts := e.Stats()
				totalExecs += execs
				totalAborts += aborts
			}
			b.ReportMetric(float64(totalExecs)/float64(b.N), "executions/op")
			b.ReportMetric(float64(totalAborts)/float64(b.N), "aborts/op")
			b.ReportMetric(float64(totalExecs)/float64(b.N)/100.0, "exec-ratio")
		})
	}
}

// --- MVS Benchmarks ---

// BenchmarkMVS_Write measures concurrent write performance.
func BenchmarkMVS_Write(b *testing.B) {
	mvs := NewMVS()
	keys := make([]LocationKey, 1000)
	for i := range keys {
		keys[i] = LocationKey{Address: benchAddr(i), Field: FieldBalance}
	}
	val := make([]byte, 32)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			mvs.Write(keys[i%len(keys)], i%100, 0, val)
			i++
		}
	})
}

// BenchmarkMVS_Read measures read performance with populated data.
func BenchmarkMVS_Read(b *testing.B) {
	mvs := NewMVS()
	numKeys := 1000
	keys := make([]LocationKey, numKeys)
	val := make([]byte, 32)

	for i := range keys {
		keys[i] = LocationKey{Address: benchAddr(i), Field: FieldBalance}
		for tx := 0; tx < 10; tx++ {
			mvs.Write(keys[i], tx, 0, val)
		}
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			mvs.Read(keys[i%numKeys], 5)
			i++
		}
	})
}

// BenchmarkValidator_ReadSet measures validation speed.
func BenchmarkValidator_ReadSet(b *testing.B) {
	mvs := NewMVS()
	numReads := 100
	val := make([]byte, 32)

	keys := make([]LocationKey, numReads)
	for i := range keys {
		keys[i] = LocationKey{Address: benchAddr(i), Field: FieldBalance}
		mvs.Write(keys[i], 0, 0, val)
	}

	rw := NewReadWriteSet(1)
	for _, key := range keys {
		rw.RecordRead(key, 0, 0, false) // read from tx 0's write
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Validate(mvs, rw)
	}
}
