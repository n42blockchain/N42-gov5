package stateless

import (
	"runtime"
	"sync"
)

// BlockResult is the per-block outcome of a batch verification.
type BlockResult struct {
	Number uint64
	Err    error // nil == verified against the trusted header chain
}

// VerifyBatch verifies many blocks' state transitions concurrently against a
// trusted HeaderChain. Because every block's pre/post stateRoot is already
// anchored by the header chain, block N's verification does NOT depend on block
// N-1's — so the whole window verifies in parallel. This is the minimal client's
// fast bootstrap: fetch a window of (witness, proof), fan out, and a block is
// zero-trust verified once its goroutine returns nil.
//
// workers<=0 uses GOMAXPROCS. Results are returned in the same order as proofs.
func VerifyBatch(hc *HeaderChain, proofs []*BlockProof, workers int) []BlockResult {
	results := make([]BlockResult, len(proofs))
	if len(proofs) == 0 {
		return results
	}
	if workers <= 0 {
		workers = runtime.GOMAXPROCS(0)
	}
	if workers > len(proofs) {
		workers = len(proofs)
	}

	var idx int
	var mu sync.Mutex
	next := func() int {
		mu.Lock()
		defer mu.Unlock()
		i := idx
		idx++
		return i
	}

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for {
				i := next()
				if i >= len(proofs) {
					return
				}
				bp := proofs[i]
				results[i] = BlockResult{Number: bp.Number, Err: VerifyAgainstChain(hc, bp)}
			}
		}()
	}
	wg.Wait()
	return results
}

// CountVerified returns how many results in the batch verified without error.
func CountVerified(results []BlockResult) int {
	n := 0
	for _, r := range results {
		if r.Err == nil {
			n++
		}
	}
	return n
}
