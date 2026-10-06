// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package hash

import (
	"bytes"
	"runtime"
	"sync"

	"lukechampine.com/blake3"

	"github.com/n42blockchain/N42/common/types"
)

// Blake3BinaryRoot is the N42 native transactions root: a binary Merkle tree
// of BLAKE3 hashes over the list's encodings, in list order.
//
//	leaf_i  = blake3(0x00 || enc_i)
//	node    = blake3(0x01 || left || right)
//	an odd node at the end of a level is carried up unchanged (RFC 6962)
//	root    = the last node; a one-entry list's root is its leaf
//	empty   = EmptyBlake3Root = blake3("")
//
// It replaces the Ethereum keccak Merkle-Patricia root (DeriveShaErigon)
// from the chain's txRootBlake3Time: the state is a BLAKE3 binary forest
// (QMDB) and the body root follows the same design. The tree is
// O(n) hashes with no trie structure; leaves and every level hash across
// the cores.
func Blake3BinaryRoot(list DerivableList) types.Hash {
	n := list.Len()
	if n == 0 {
		return EmptyBlake3Root
	}
	level := make([]types.Hash, n)
	workers := runtime.GOMAXPROCS(0)
	if workers > 16 {
		workers = 16
	}
	if n < 1024 || workers < 2 {
		workers = 1
	}
	parallelFor(n, workers, func(lo, hi int) {
		var buf bytes.Buffer
		for i := lo; i < hi; i++ {
			buf.Reset()
			buf.WriteByte(0x00)
			list.EncodeIndex(i, &buf)
			level[i] = blake3.Sum256(buf.Bytes())
		}
	})
	for len(level) > 1 {
		pairs := len(level) / 2
		next := make([]types.Hash, pairs+len(level)%2)
		w := workers
		if pairs < 2048 {
			w = 1
		}
		parallelFor(pairs, w, func(lo, hi int) {
			var m [65]byte
			m[0] = 0x01
			for k := lo; k < hi; k++ {
				copy(m[1:33], level[2*k][:])
				copy(m[33:], level[2*k+1][:])
				next[k] = blake3.Sum256(m[:])
			}
		})
		if len(level)%2 == 1 {
			next[pairs] = level[len(level)-1]
		}
		level = next
	}
	return level[0]
}

// EmptyBlake3Root is Blake3BinaryRoot of an empty list: blake3 of no bytes.
var EmptyBlake3Root = types.Hash(blake3.Sum256(nil))

// parallelFor runs f over [0, n) in worker-sized chunks. A panic in a chunk
// (a list entry that cannot be encoded) is re-raised on the caller's
// goroutine, where the import's recovery sees it, instead of killing the
// process from a worker.
func parallelFor(n, workers int, f func(lo, hi int)) {
	if workers <= 1 || n < workers {
		f(0, n)
		return
	}
	chunk := (n + workers - 1) / workers
	var wg sync.WaitGroup
	var panicMu sync.Mutex
	var panicked interface{}
	for w := 0; w < workers; w++ {
		lo, hi := w*chunk, (w+1)*chunk
		if hi > n {
			hi = n
		}
		if lo >= hi {
			break
		}
		wg.Add(1)
		go func(lo, hi int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panicMu.Lock()
					if panicked == nil {
						panicked = r
					}
					panicMu.Unlock()
				}
			}()
			f(lo, hi)
		}(lo, hi)
	}
	wg.Wait()
	if panicked != nil {
		panic(panicked)
	}
}
