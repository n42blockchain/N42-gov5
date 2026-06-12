// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Decode pipeline. The build loop is single-threaded and ~12% of its CPU was
// changeset blob decode + address/slot keccaks — work with no dependency on
// the MDBX tx or the trie state. A small worker pool now pre-decodes blocks
// ahead of the main loop into ready-to-apply form (dirty maps + key hashes);
// the main thread merges the hashes into its caches and applies. Freezer
// Retrieve is mutex-guarded, so workers share the tables safely.
package main

import (
	"fmt"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
)

// decodedBlock is one block's changesets in apply-ready form.
type decodedBlock struct {
	n      uint64
	dirtyA map[types.Address]*account.StateAccount
	dirtyS map[types.Address]map[types.Hash]*uint256.Int
	ahash  map[types.Address][32]byte
	shash  map[types.Hash][32]byte
	err    error
}

// decodePipeline pre-decodes [next, end) on `workers` goroutines, delivering
// blocks IN ORDER through Next().
type decodePipeline struct {
	jobs    chan uint64
	results chan *decodedBlock
	reorder map[uint64]*decodedBlock
	next    uint64
	done    chan struct{}
	wg      sync.WaitGroup
}

const pipelineDepth = 64

func startDecodePipeline(b *builder, start, end uint64, workers int) *decodePipeline {
	p := &decodePipeline{
		jobs:    make(chan uint64, pipelineDepth),
		results: make(chan *decodedBlock, pipelineDepth),
		reorder: make(map[uint64]*decodedBlock, pipelineDepth+1),
		next:    start,
		done:    make(chan struct{}),
	}
	for w := 0; w < workers; w++ {
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			// Per-worker key-hash caches: miners and hot contracts repeat
			// constantly; caching here keeps the keccaks off the main thread
			// AND deduplicated within a worker.
			ac := make(map[types.Address][32]byte, 1<<14)
			sc := make(map[types.Hash][32]byte, 1<<14)
			for n := range p.jobs {
				p.results <- decodeOne(b, n, ac, sc)
				if len(ac) > 1_000_000 {
					ac = make(map[types.Address][32]byte, 1<<14)
				}
				if len(sc) > 1_000_000 {
					sc = make(map[types.Hash][32]byte, 1<<14)
				}
			}
		}()
	}
	go func() {
		defer close(p.jobs)
		for n := start; n < end; n++ {
			select {
			case p.jobs <- n:
			case <-p.done:
				return
			}
		}
	}()
	return p
}

// Next returns block n's decoded form (must be called with consecutive n
// starting at the pipeline's start).
func (p *decodePipeline) Next(n uint64) (*decodedBlock, error) {
	for {
		if d, ok := p.reorder[n]; ok {
			delete(p.reorder, n)
			return d, d.err
		}
		d := <-p.results
		p.reorder[d.n] = d
	}
}

// Stop terminates the workers (used on error paths).
func (p *decodePipeline) Stop() {
	close(p.done)
	go func() { // drain so workers can exit
		for range p.results {
		}
	}()
	p.wg.Wait()
	close(p.results)
}

func decodeOne(b *builder, n uint64, ac map[types.Address][32]byte, sc map[types.Hash][32]byte) *decodedBlock {
	d := &decodedBlock{
		n:      n,
		dirtyA: make(map[types.Address]*account.StateAccount),
		dirtyS: make(map[types.Address]map[types.Hash]*uint256.Int),
		ahash:  make(map[types.Address][32]byte, 8),
		shash:  make(map[types.Hash][32]byte, 8),
	}
	hashAddr := func(addr types.Address) {
		if _, ok := d.ahash[addr]; ok {
			return
		}
		h, ok := ac[addr]
		if !ok {
			copy(h[:], crypto.Keccak256(addr[:]))
			ac[addr] = h
		}
		d.ahash[addr] = h
	}
	hashSlot := func(slot types.Hash) {
		if _, ok := d.shash[slot]; ok {
			return
		}
		h, ok := sc[slot]
		if !ok {
			copy(h[:], crypto.Keccak256(slot[:]))
			sc[slot] = h
		}
		d.shash[slot] = h
	}

	accBlob, err := b.acctTbl.Retrieve(n)
	if err != nil {
		d.err = fmt.Errorf("acctcs: %w", err)
		return d
	}
	stoBlob, err := b.storTbl.Retrieve(n)
	if err != nil {
		d.err = fmt.Errorf("storcs: %w", err)
		return d
	}
	if len(accBlob) > 0 {
		entries, err := ethel.DecodeAccountChanges(accBlob)
		if err != nil {
			d.err = fmt.Errorf("decode acctcs: %w", err)
			return d
		}
		for _, e := range entries {
			hashAddr(e.Address)
			if len(e.NewValue) == 0 {
				d.dirtyA[e.Address] = nil
				continue
			}
			var acct account.StateAccount
			if err := acct.DecodeForStorage(e.NewValue); err != nil {
				d.err = fmt.Errorf("decode account %x: %w", e.Address, err)
				return d
			}
			d.dirtyA[e.Address] = &acct
		}
	}
	if len(stoBlob) > 0 {
		entries, err := ethel.DecodeStorageChanges(stoBlob)
		if err != nil {
			d.err = fmt.Errorf("decode storcs: %w", err)
			return d
		}
		for _, e := range entries {
			var addr types.Address
			var slot types.Hash
			copy(addr[:], e.CompositeKey[:20])
			copy(slot[:], e.CompositeKey[20:])
			// Account deleted this block: drop its WRITES, keep its wipes —
			// mirrors addSlot in block().
			if a, ok := d.dirtyA[addr]; ok && a == nil && len(e.NewValue) != 0 {
				continue
			}
			hashAddr(addr)
			hashSlot(slot)
			inner, ok := d.dirtyS[addr]
			if !ok {
				inner = make(map[types.Hash]*uint256.Int, 8)
				d.dirtyS[addr] = inner
			}
			if len(e.NewValue) == 0 {
				inner[slot] = nil
			} else {
				inner[slot] = new(uint256.Int).SetBytes(e.NewValue)
			}
		}
	}
	return d
}