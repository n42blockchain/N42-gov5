// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package block

import (
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// receiptsChunk is how many receipts one parallel work item covers.
const receiptsChunk = 4096

type receiptsChunkOut struct {
	buf   []byte
	bloom Bloom
	ready chan struct{}
}

var chunkBufPool = sync.Pool{New: func() any {
	b := make([]byte, 0, 1<<20)
	return &b
}}

// ReceiptsRootBloomParallel returns the native receipts root and the block
// bloom of rs, equal to ReceiptsDigest(rs, true, false) and CreateBloom(rs).
// Workers encode contiguous chunks and compute their blooms (a bloom is a
// bitwise OR, so chunk blooms combine in any order); the calling goroutine
// feeds the encodings to the Keccak sponge strictly in chunk order, so the
// root is the hash of the same byte stream as the serial pass.
func ReceiptsRootBloomParallel(rs Receipts, workers int) (root types.Hash, bloom Bloom) {
	if len(rs) == 0 {
		return hash.NilHash, bloom
	}
	nChunks := (len(rs) + receiptsChunk - 1) / receiptsChunk
	if workers < 1 {
		workers = 1
	}
	if workers > nChunks {
		workers = nChunks
	}
	outs := make([]receiptsChunkOut, nChunks)
	for i := range outs {
		outs[i].ready = make(chan struct{})
	}
	var next atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := bloomHasherPool.Get().(*bloomHasher)
			defer bloomHasherPool.Put(h)
			for {
				c := int(next.Add(1)) - 1
				if c >= nChunks {
					return
				}
				lo, hi := c*receiptsChunk, (c+1)*receiptsChunk
				if hi > len(rs) {
					hi = len(rs)
				}
				bp := chunkBufPool.Get().(*[]byte)
				buf := (*bp)[:0]
				o := &outs[c]
				for _, r := range rs[lo:hi] {
					if len(r.Logs) != 0 {
						o.bloom.addLogs(r.Logs, h)
					}
					buf = appendNativeReceipt(buf, r)
				}
				*bp = buf
				o.buf = buf
				close(o.ready)
			}
		}()
	}
	sha := hash.HasherPool.Get().(crypto.KeccakState)
	sha.Reset()
	for i := range outs {
		<-outs[i].ready
		sha.Write(outs[i].buf) //nolint:errcheck
		for j := range bloom {
			bloom[j] |= outs[i].bloom[j]
		}
		b := outs[i].buf[:0]
		chunkBufPool.Put(&b)
		outs[i].buf = nil
	}
	sha.Read(root[:])
	hash.HasherPool.Put(sha)
	wg.Wait()
	return root, bloom
}
