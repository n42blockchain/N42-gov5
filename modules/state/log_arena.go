// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// log_arena.go — per-block bump allocation for EVM logs.
//
// A LOG opcode used to cost four heap allocations: the Log struct, its topics
// slice, a copy of the event data, and a uint256 for the block number. On the
// dense-replay profile that was 36 GiB and 290 million objects per 200k
// blocks, and at 256 workers every one of them queued on the runtime
// allocator's central lock.
//
// Witness replay consumes a block's logs exactly once, when the receipt root
// is verified, and then discards the whole block. That lifetime is what the
// arena exploits: logs, topics and data are carved out of chunks the arena
// owns, the block number is one shared value, and Reset truncates everything
// for the next block while keeping the chunks. The arena is OPT-IN
// (IntraBlockState.SetLogArena): a node keeps logs alive after the block for
// RPC and filters, and must keep allocating them individually.

package state

import (
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

const (
	logArenaLogChunk   = 256       // Log structs per chunk
	logArenaTopicChunk = 4096      // topics per chunk
	logArenaDataChunk  = 256 << 10 // bytes per data chunk
)

type logArena struct {
	logChunks [][]block.Log // each chunk is allocated once and never grows, so pointers stay valid
	logUsed   int           // logs used in the last chunk
	topics    []types.Hash  // current topic chunk; a full one is simply replaced
	data      []byte        // current data chunk; same
	blockNum  uint256.Int   // shared BlockNumber for every log in the block
}

// reset forgets every log of the finished block but keeps the chunks.
func (a *logArena) reset() {
	if len(a.logChunks) > 1 {
		// Keep one chunk; a block that needed many is the exception.
		a.logChunks = a.logChunks[:1]
	}
	a.logUsed = 0
	a.topics = a.topics[:0]
	a.data = a.data[:0]
}

// newLog hands out a Log whose Topics and Data slices have the requested
// lengths and are ready to be filled. Data larger than a chunk gets its own
// allocation rather than forcing a huge chunk to be retained.
func (a *logArena) newLog(addr types.Address, ntopics, dataLen int, blockNum uint64) *block.Log {
	if len(a.logChunks) == 0 || a.logUsed == logArenaLogChunk {
		a.logChunks = append(a.logChunks, make([]block.Log, logArenaLogChunk))
		a.logUsed = 0
	}
	chunk := a.logChunks[len(a.logChunks)-1]
	l := &chunk[a.logUsed]
	a.logUsed++
	a.blockNum.SetUint64(blockNum) // one shared value per block; every log points at it
	*l = block.Log{Address: addr, BlockNumber: &a.blockNum}

	if ntopics > 0 {
		if cap(a.topics)-len(a.topics) < ntopics {
			a.topics = make([]types.Hash, 0, logArenaTopicChunk)
		}
		start := len(a.topics)
		a.topics = a.topics[:start+ntopics]
		l.Topics = a.topics[start : start+ntopics : start+ntopics]
	} else {
		l.Topics = []types.Hash{}
	}

	switch {
	case dataLen == 0:
		l.Data = []byte{}
	case dataLen > logArenaDataChunk/4:
		l.Data = make([]byte, dataLen)
	default:
		if cap(a.data)-len(a.data) < dataLen {
			a.data = make([]byte, 0, logArenaDataChunk)
		}
		start := len(a.data)
		a.data = a.data[:start+dataLen]
		l.Data = a.data[start : start+dataLen : start+dataLen]
	}
	return l
}

// SetLogArena switches per-block bump allocation of logs on or off. Only a
// caller that discards every log of a block before the next Reset may turn
// it on; after Reset the previous block's logs are overwritten.
func (sdb *IntraBlockState) SetLogArena(on bool) {
	if !on {
		sdb.logArena = nil
		return
	}
	if sdb.logArena == nil {
		sdb.logArena = &logArena{}
	}
}

// NewLog returns a Log to fill in and hand to AddLog. With the arena off it is
// an ordinary allocation, so callers need not care which mode is active.
func (sdb *IntraBlockState) NewLog(addr types.Address, ntopics, dataLen int, blockNum uint64) *block.Log {
	if sdb.logArena != nil {
		return sdb.logArena.newLog(addr, ntopics, dataLen, blockNum)
	}
	return &block.Log{
		Address:     addr,
		Topics:      make([]types.Hash, ntopics),
		Data:        make([]byte, dataLen),
		BlockNumber: uint256.NewInt(blockNum),
	}
}
