package api

import (
	"fmt"
	"testing"
)

// BenchmarkBatchRawTransactionIngest measures processBatchEntries on a
// 200-entry batch of real signed transfers -- the same decode + ECDSA
// sender-recovery work BatchRawTransaction's own processOne closure does --
// at worker counts 1 (serial), 8, and 32. Run pinned to a fixed CPU set on
// the shared box, e.g.:
//
//	taskset -c 200-207 go test -tags nosqlite,noboltdb -run NONE \
//	  -bench BenchmarkBatchRawTransactionIngest -benchtime 2s ./internal/api/
//
// b.ReportMetric adds a tx/s figure (200*b.N transactions per Elapsed) next
// to the standard ns/op and -benchmem's allocs/op.
func BenchmarkBatchRawTransactionIngest(b *testing.B) {
	const batchSize = 200
	inputs, _, signer := signedTransferBatch(b, batchSize)
	processOne := decodeAndRecover(signer)

	for _, workers := range []int{1, 8, 32} {
		workers := workers
		b.Run(benchWorkersName(workers), func(b *testing.B) {
			var jobs chan func()
			if workers > 1 {
				jobs = newIngestPool(workers)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				results := processBatchEntries(inputs, workers, jobs, processOne)
				if len(results) != batchSize {
					b.Fatalf("got %d results, want %d", len(results), batchSize)
				}
			}
			b.StopTimer()
			txPerSec := float64(batchSize) * float64(b.N) / b.Elapsed().Seconds()
			b.ReportMetric(txPerSec, "tx/s")
		})
	}
}

func benchWorkersName(n int) string {
	if n == 1 {
		return "workers=1(serial)"
	}
	return fmt.Sprintf("workers=%d", n)
}
