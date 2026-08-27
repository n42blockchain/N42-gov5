// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

// TestLogArenaHandsOutIndependentLogs: logs from one block must not alias
// each other's topics or data, across chunk boundaries included.
func TestLogArenaHandsOutIndependentLogs(t *testing.T) {
	sdb := New(discardChangesReader{})
	sdb.SetLogArena(true)
	addr := types.Address{1}
	const n = logArenaLogChunk*2 + 17 // crosses two chunk boundaries
	logs := make([]*logRef, 0, n)
	for i := 0; i < n; i++ {
		l := sdb.NewLog(addr, 1+i%4, 1+i%97, 12345)
		if len(l.Topics) != 1+i%4 || len(l.Data) != 1+i%97 {
			t.Fatalf("log %d: topics=%d data=%d", i, len(l.Topics), len(l.Data))
		}
		for k := range l.Topics {
			l.Topics[k] = types.Hash{byte(i), byte(k)}
		}
		for k := range l.Data {
			l.Data[k] = byte(i + k)
		}
		if l.BlockNumber == nil || l.BlockNumber.Uint64() != 12345 {
			t.Fatalf("log %d: block number %v", i, l.BlockNumber)
		}
		logs = append(logs, &logRef{i: i, topics: l.Topics, data: l.Data})
	}
	for _, r := range logs {
		for k := range r.topics {
			if r.topics[k] != (types.Hash{byte(r.i), byte(k)}) {
				t.Fatalf("log %d topic %d was overwritten", r.i, k)
			}
		}
		for k := range r.data {
			if r.data[k] != byte(r.i+k) {
				t.Fatalf("log %d data byte %d was overwritten", r.i, k)
			}
		}
	}
}

type logRef struct {
	i      int
	topics []types.Hash
	data   []byte
}

// TestLogArenaTopicSliceCannotGrowIntoNeighbour: a caller appending to a log's
// topics must not write over the next log's topics (capacity is clipped).
func TestLogArenaTopicSliceCannotGrowIntoNeighbour(t *testing.T) {
	sdb := New(discardChangesReader{})
	sdb.SetLogArena(true)
	a := sdb.NewLog(types.Address{1}, 1, 4, 1)
	b := sdb.NewLog(types.Address{2}, 1, 4, 1)
	b.Topics[0] = types.Hash{0xbb}
	b.Data[0] = 0xbb
	a.Topics = append(a.Topics, types.Hash{0xaa}) // must reallocate, not clobber b
	a.Data = append(a.Data, 0xaa)
	if b.Topics[0] != (types.Hash{0xbb}) || b.Data[0] != 0xbb {
		t.Fatal("appending to one log's slices clobbered its neighbour")
	}
}

// TestLogArenaLargeDataGetsOwnAllocation: oversized event data must not force
// a huge chunk to be retained, and must still be independent.
func TestLogArenaLargeDataGetsOwnAllocation(t *testing.T) {
	sdb := New(discardChangesReader{})
	sdb.SetLogArena(true)
	big := sdb.NewLog(types.Address{1}, 0, logArenaDataChunk, 1)
	small := sdb.NewLog(types.Address{1}, 0, 8, 1)
	for i := range big.Data {
		big.Data[i] = 0xee
	}
	for i := range small.Data {
		small.Data[i] = 0x11
	}
	if !bytes.Equal(big.Data[:8], bytes.Repeat([]byte{0xee}, 8)) || !bytes.Equal(small.Data, bytes.Repeat([]byte{0x11}, 8)) {
		t.Fatal("large and small data aliased")
	}
	if cap(sdb.logArena.data) > logArenaDataChunk {
		t.Fatalf("arena retained an oversized data chunk: cap=%d", cap(sdb.logArena.data))
	}
}

// TestLogArenaResetReusesChunks: after Reset the arena must start over
// without keeping more than one log chunk, and NewLog must keep working.
func TestLogArenaResetReusesChunks(t *testing.T) {
	sdb := New(discardChangesReader{})
	sdb.SetLogArena(true)
	for i := 0; i < logArenaLogChunk*3; i++ {
		sdb.NewLog(types.Address{1}, 2, 32, 7)
	}
	if got := len(sdb.logArena.logChunks); got != 3 {
		t.Fatalf("chunks before reset = %d, want 3", got)
	}
	sdb.Reset()
	if got := len(sdb.logArena.logChunks); got != 1 {
		t.Fatalf("chunks after reset = %d, want 1", got)
	}
	l := sdb.NewLog(types.Address{2}, 3, 5, 8)
	if len(l.Topics) != 3 || len(l.Data) != 5 || l.BlockNumber.Uint64() != 8 {
		t.Fatal("NewLog after Reset returned a malformed log")
	}
}

// TestNewLogWithoutArenaAllocatesPlainly: with the arena off, NewLog must
// behave like the old direct construction.
func TestNewLogWithoutArenaAllocatesPlainly(t *testing.T) {
	sdb := New(discardChangesReader{})
	l := sdb.NewLog(types.Address{3}, 2, 9, 42)
	if len(l.Topics) != 2 || len(l.Data) != 9 || l.BlockNumber.Uint64() != 42 || l.Address != (types.Address{3}) {
		t.Fatalf("plain NewLog: %+v", l)
	}
}
