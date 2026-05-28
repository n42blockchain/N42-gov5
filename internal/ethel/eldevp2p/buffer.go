// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// buffer.go — persistent reorder buffer for staged catch-up download.
//
// reth's BodyStage buffers out-of-order bodies in a BinaryHeap and emits
// contiguous runs so gaps don't stall and nothing fetched is thrown away
// (net/downloaders/src/bodies/bodies.rs). The N42 v1 downloader instead
// discarded everything past the first gap each round and re-fetched it next
// round ("549 bodies fetched, 10 imported"). blockBuffer fixes that: fetched
// headers/bodies persist ACROSS rounds keyed by block number; each round only
// the still-missing numbers are requested; the executor is fed the contiguous
// prefix; executed entries are pruned. Bodies are validated by txRoot at insert
// time (see Downloader.ensureBodies) so a mis-ordered/garbage body is never
// cached (and is therefore re-requested), not cached-then-rejected-forever.

//go:build n42el

package eldevp2p

import (
	"sync"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/devp2p"
)

type blockBuffer struct {
	mu      sync.Mutex
	headers map[uint64]*block.Header
	bodies  map[uint64]devp2p.BlockBody
}

func newBlockBuffer() *blockBuffer {
	return &blockBuffer{
		headers: make(map[uint64]*block.Header),
		bodies:  make(map[uint64]devp2p.BlockBody),
	}
}

func (b *blockBuffer) hasHeader(n uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.headers[n]
	return ok
}

func (b *blockBuffer) hasBody(n uint64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.bodies[n]
	return ok
}

func (b *blockBuffer) headerAt(n uint64) (*block.Header, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	h, ok := b.headers[n]
	return h, ok
}

func (b *blockBuffer) putHeader(n uint64, h *block.Header) {
	b.mu.Lock()
	b.headers[n] = h
	b.mu.Unlock()
}

func (b *blockBuffer) putBody(n uint64, body devp2p.BlockBody) {
	b.mu.Lock()
	b.bodies[n] = body
	b.mu.Unlock()
}

// prune drops every header/body at or below throughEq (executed + committed).
func (b *blockBuffer) prune(throughEq uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for n := range b.headers {
		if n <= throughEq {
			delete(b.headers, n)
		}
	}
	for n := range b.bodies {
		if n <= throughEq {
			delete(b.bodies, n)
		}
	}
}

func (b *blockBuffer) size() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.bodies)
}

// assembleContiguous returns the longest contiguous run [from, k] (k <= to) for
// which BOTH a header and a body are buffered, as a header slice + body map ready
// for executeRange. Empty if from itself is missing.
func (b *blockBuffer) assembleContiguous(from, to uint64) ([]*block.Header, map[uint64]devp2p.BlockBody) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var headers []*block.Header
	bodies := make(map[uint64]devp2p.BlockBody)
	for n := from; n <= to; n++ {
		h, hok := b.headers[n]
		body, bok := b.bodies[n]
		if !hok || !bok {
			break
		}
		headers = append(headers, h)
		bodies[n] = body
	}
	return headers, bodies
}
