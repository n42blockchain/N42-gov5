// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"time"

	"github.com/n42blockchain/N42/common/types"
)

// N42_FRAME_LOG_LEAN_LOCK=1 (default off, r95a): a frame log write builds
// its record headers (and their CRC-32C) before it takes the log's mutex,
// and appends header and frame data straight from the caller's slices with
// a vectored write, instead of building all of it under the mutex and
// copying every frame into the log's write buffer.
//
// Run 81 (node0, full blocks): the mutex is busy 82-87% of wall time,
// ~70 ms per write, of which Enc (CRC + copy of ~34 MB) is 23 ms; the
// writes queue behind each other and the queue reaches the block writer
// as framesWait. With the switch on Enc leaves the mutex, and so does the
// copy: the data goes from the frames to the file once.
//
// Nothing else changes: the records are the same bytes in the same order
// at the same offsets (offsets are still assigned under the mutex), the
// segment rolls at the same point, the data sync and the index rows are
// those of the copying path (appendLocked is shared).
var frameLogLeanLockOn = parseFrameOptIn(os.Getenv("N42_FRAME_LOG_LEAN_LOCK"))

// SetFrameLogLeanLockForTest switches N42_FRAME_LOG_LEAN_LOCK and returns
// the restore.
func SetFrameLogLeanLockForTest(on bool) func() {
	prev := frameLogLeanLockOn
	frameLogLeanLockOn = on
	return func() { frameLogLeanLockOn = prev }
}

// frameLogRecords is one write's records, built off the mutex: iov holds
// header and data of each record in order (the headers in hdrs).
type frameLogRecords struct {
	hdrs []byte
	iov  [][]byte
	locs []frameLogWriteLoc
	size int64
}

// buildFrameLogRecords encodes the records of a write: what the copying
// path appends to its buffer, as slices.
func buildFrameLogRecords(hashes []types.Hash, raws [][]byte) (*frameLogRecords, error) {
	n := 0
	for i := range hashes {
		if i < len(raws) && len(raws[i]) > 0 {
			n++
		}
	}
	r := &frameLogRecords{
		hdrs: make([]byte, n*frameLogHeaderSize),
		iov:  make([][]byte, 0, 2*n),
		locs: make([]frameLogWriteLoc, 0, n),
	}
	k := 0
	for i, h := range hashes {
		if i >= len(raws) || len(raws[i]) == 0 {
			continue
		}
		raw := raws[i]
		if len(raw) > frameLogMaxFrame {
			return nil, fmt.Errorf("frame %s is %d bytes, over the frame log's record limit", h.Hex(), len(raw))
		}
		hdr := r.hdrs[k*frameLogHeaderSize : (k+1)*frameLogHeaderSize : (k+1)*frameLogHeaderSize]
		k++
		binary.BigEndian.PutUint32(hdr, frameLogMagic)
		binary.LittleEndian.PutUint32(hdr[4:], uint32(len(raw)))
		copy(hdr[8:], h[:])
		binary.LittleEndian.PutUint32(hdr[8+types.HashLength:], crc32.Checksum(raw, frameLogCRC))
		r.size += frameLogHeaderSize
		r.iov = append(r.iov, hdr, raw)
		r.locs = append(r.locs, frameLogWriteLoc{h: h, rel: r.size, n: len(raw)})
		r.size += int64(len(raw))
	}
	return r, nil
}

// writeLean is write with N42_FRAME_LOG_LEAN_LOCK.
func (l *frameLog) writeLean(ctx context.Context, hashes []types.Hash, raws [][]byte, times *FrameWriteTimes) error {
	t0 := time.Now()
	if times != nil {
		defer func() { times.Whole = time.Since(t0) }()
	}
	recs, err := buildFrameLogRecords(hashes, raws)
	tLock := time.Now()
	if times != nil {
		// Encode is off the mutex here: Whole - LockWait - Encode - ... is
		// still the unaccounted rest.
		times.Encode = tLock.Sub(t0)
	}
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if times != nil {
		times.LockWait = time.Since(tLock)
		times.Bytes = recs.size
	}
	if l.f == nil {
		return errors.New("frame log is closed")
	}
	if len(recs.locs) == 0 {
		return nil
	}
	return l.appendLocked(ctx, recs.size, func(f *os.File, off int64) error {
		return frameLogWriteVec(f, recs.iov, off)
	}, recs.locs, times)
}

// frameLogWriteVecAt writes iov at off with writeAt, one slice after the
// other (the portable fallback of frameLogWriteVec).
func frameLogWriteVecAt(f *os.File, iov [][]byte, off int64) error {
	for _, b := range iov {
		if _, err := f.WriteAt(b, off); err != nil {
			return err
		}
		off += int64(len(b))
	}
	return nil
}
