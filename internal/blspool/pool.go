// Package blspool holds the deterministic mobile-voter pool derivation and
// per-block committee sampling shared by the replay-v2 re-sealer (which signs)
// and the JSON-RPC consensus API (which resolves committee membership for block
// explorers). Both MUST sample identically, so the logic lives here once.
package blspool

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"runtime"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
)

// IKM returns the BLS key-derivation input material for pool index i:
// sha256(seed || uint64_be(i)). Matches cmd/n42-blspool.
func IKM(seed [32]byte, i int) [32]byte {
	var idx [8]byte
	binary.BigEndian.PutUint64(idx[:], uint64(i))
	return sha256.Sum256(append(append([]byte{}, seed[:]...), idx[:]...))
}

// DeriveKeys derives the pool of `size` keypairs from the master seed in
// parallel. When withSecret is false only public keys are returned (sks is nil)
// — the explorer path needs only public keys.
func DeriveKeys(seed [32]byte, size int, withSecret bool) (sks []common.SecretKey, pks []common.PublicKey, err error) {
	if size <= 0 {
		return nil, nil, fmt.Errorf("blspool: invalid size %d", size)
	}
	pks = make([]common.PublicKey, size)
	if withSecret {
		sks = make([]common.SecretKey, size)
	}
	npar := runtime.GOMAXPROCS(0)
	var wg sync.WaitGroup
	var derErr error
	var once sync.Once
	chunk := (size + npar - 1) / npar
	for w := 0; w < npar; w++ {
		lo := w * chunk
		hi := lo + chunk
		if hi > size {
			hi = size
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			for i := lo; i < hi; i++ {
				ikm := IKM(seed, i)
				sk, e := bls.SecretKeyFromRandom32Byte(ikm)
				if e != nil {
					once.Do(func() { derErr = e })
					return
				}
				pks[i] = sk.PublicKey()
				if withSecret {
					sks[i] = sk
				}
			}
		}(lo, hi)
	}
	wg.Wait()
	if derErr != nil {
		return nil, nil, fmt.Errorf("blspool: derive: %w", derErr)
	}
	return sks, pks, nil
}

// ActivePool returns the number of voters active at blockNum: a linear ramp
// from committeeSize at genesis up to poolSize over rampBlocks, then poolSize.
func ActivePool(blockNum uint64, poolSize, committeeSize int, rampBlocks uint64) int {
	if rampBlocks == 0 || blockNum >= rampBlocks {
		return poolSize
	}
	grow := uint64(poolSize-committeeSize) * blockNum / rampBlocks
	active := committeeSize + int(grow)
	if active > poolSize {
		active = poolSize
	}
	return active
}

// Committee deterministically samples committeeSize distinct indices from
// [0, active) via a partial Fisher-Yates shuffle seeded by (view, blockHash).
// The returned slice is the ordered validator set for that view.
func Committee(view uint64, blockHash types.Hash, active, committeeSize int) []int {
	return NewCommitteeScratch().Committee(view, blockHash, active, committeeSize)
}

// CommitteeScratch holds the reusable buffers for committee sampling. The
// one-shot Committee allocated a fresh swap map (+ a 40-byte append per
// iteration) on EVERY call — at one call per block that was 49% of all process
// allocations in the reseal conversion (369 GB). A caller-owned scratch keeps
// the map buckets and output slice alive across blocks; clear() empties the map
// without releasing its storage, so the steady state allocates nothing.
// Outputs are bit-identical to the one-shot form (same algorithm, asserted by
// tests). Not safe for concurrent use.
type CommitteeScratch struct {
	swaps map[int]int
	out   []int
}

// NewCommitteeScratch creates a reusable sampler scratch.
func NewCommitteeScratch() *CommitteeScratch {
	return &CommitteeScratch{swaps: make(map[int]int, 1024)}
}

// Committee deterministically samples committeeSize distinct indices from
// [0, active) via a partial Fisher-Yates shuffle seeded by (view, blockHash).
// The returned slice is owned by the scratch and overwritten by the next call.
func (cs *CommitteeScratch) Committee(view uint64, blockHash types.Hash, active, committeeSize int) []int {
	k := committeeSize
	if k > active {
		k = active
	}
	var seedBuf [40]byte
	binary.LittleEndian.PutUint64(seedBuf[:8], view)
	copy(seedBuf[8:], blockHash[:])
	base := sha256.Sum256(seedBuf[:])

	clear(cs.swaps)
	get := func(i int) int {
		if v, ok := cs.swaps[i]; ok {
			return v
		}
		return i
	}
	if cap(cs.out) < k {
		cs.out = make([]int, k)
	}
	out := cs.out[:k]
	var hashBuf [40]byte // base(32) || ctr(8) — fixed, no per-iteration append
	copy(hashBuf[:32], base[:])
	for i := 0; i < k; i++ {
		binary.LittleEndian.PutUint64(hashBuf[32:], uint64(i))
		h := sha256.Sum256(hashBuf[:])
		rnd := binary.LittleEndian.Uint64(h[:8])
		j := i + int(rnd%uint64(active-i))
		vj := get(j)
		out[i] = vj
		cs.swaps[j] = get(i)
		cs.swaps[i] = vj
	}
	return out
}
