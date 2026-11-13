// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package rawdb

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// The frame log (N42_FRAME_LOG, r81a).
//
// Frames are immutable blobs named by their hash, ~52 KB each and ~34 MB a
// block at 326,000 transactions. In the MDBX frame store that is 650 random
// inserts of overflow values per block; one such transaction held the store
// 100-140 ms (E1N26 runs 49-51), which is the block period at 3M TPS, and
// seven stores kept one NVMe 85-90% busy at 2.4 GB/s.
//
// With a frame log the frame data of a write is appended to a segment file
// in one write and made durable with one data sync. The frame store keeps
// only an index row per frame (segment, offset, length) in FrameLogIndex,
// committed without its own sync, together with the log position it covers.
// The index is derived data: on open the log is scanned from the recorded
// position, the index rows of complete records are put again and a torn
// tail is cut off. A frame is durable when its append is synced, which is
// what WriteFramesToStore promises.
//
// Record: magic(4) | data length(4, LE) | hash(32) | CRC-32C of data(4) | data.

const (
	frameLogMagic      = 0x4e343246 // "N42F"
	frameLogHeaderSize = 4 + 4 + types.HashLength + 4
	frameLogLocSize    = 4 + 8 + 4
	frameLogMaxFrame   = 64 << 20
)

// frameLogSegmentBytes is the size at which a new segment starts.
var frameLogSegmentBytes int64 = 1 << 30

var frameLogPosKey = []byte("logpos")

var frameLogCRC = crc32.MakeTable(crc32.Castagnoli)

// FrameLogIndexTables adds the frame log's index table to a frame store.
func FrameLogIndexTables(cfg kv.TableCfg) kv.TableCfg {
	cfg[modules.FrameLogIndex] = kv.TableCfgItem{}
	return cfg
}

type frameLog struct {
	dir string
	db  kv.RwDB
	// keepSegments > 0 deletes segments older than the newest keepSegments
	// (BENCH ONLY, N42_FRAME_LOG_KEEP_SEGMENTS).
	keepSegments int

	mu  sync.Mutex // the writer: seg, f, off, buf
	seg uint32
	f   *os.File
	off int64
	buf []byte

	rmu     sync.RWMutex
	readers map[uint32]*os.File
}

func frameLogSegmentName(seg uint32) string { return fmt.Sprintf("frames-%08d.log", seg) }

func frameLogSegments(dir string) ([]uint32, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var segs []uint32
	for _, e := range entries {
		var n uint32
		if _, err := fmt.Sscanf(e.Name(), "frames-%08d.log", &n); err == nil && e.Name() == frameLogSegmentName(n) {
			segs = append(segs, n)
		}
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i] < segs[j] })
	return segs, nil
}

func encodeFrameLogPos(seg uint32, off int64) []byte {
	v := make([]byte, 12)
	binary.LittleEndian.PutUint32(v, seg)
	binary.LittleEndian.PutUint64(v[4:], uint64(off))
	return v
}

func encodeFrameLogLoc(seg uint32, off int64, n int) []byte {
	v := make([]byte, frameLogLocSize)
	binary.LittleEndian.PutUint32(v, seg)
	binary.LittleEndian.PutUint64(v[4:], uint64(off))
	binary.LittleEndian.PutUint32(v[12:], uint32(n))
	return v
}

// openFrameLog opens (creating it if needed) the frame log in dir with its
// index in db, and brings the index up to the end of the log.
func openFrameLog(dir string, db kv.RwDB, keepSegments int) (*frameLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	l := &frameLog{dir: dir, db: db, keepSegments: keepSegments, readers: make(map[uint32]*os.File)}
	segs, err := frameLogSegments(dir)
	if err != nil {
		return nil, err
	}
	var posSeg uint32
	var posOff int64
	if err := db.View(context.Background(), func(tx kv.Tx) error {
		v, err := tx.GetOne(modules.FrameLogIndex, frameLogPosKey)
		if err != nil || len(v) != 12 {
			return err
		}
		posSeg, posOff = binary.LittleEndian.Uint32(v), int64(binary.LittleEndian.Uint64(v[4:]))
		return nil
	}); err != nil {
		return nil, err
	}
	// Recover: index the complete records after the recorded position.
	endSeg, endOff := posSeg, posOff
	if len(segs) > 0 && segs[len(segs)-1] < posSeg {
		// The index is ahead of the files (segments were removed): start a
		// new segment after it rather than reuse a number the index names.
		endSeg, endOff = posSeg+1, 0
	}
	err = db.Update(context.Background(), func(tx kv.RwTx) error {
		for i, seg := range segs {
			if seg < posSeg {
				continue
			}
			from := int64(0)
			if seg == posSeg {
				from = posOff
			}
			end, err := scanFrameLogSegment(filepath.Join(dir, frameLogSegmentName(seg)), from, func(h types.Hash, off int64, n int) error {
				return tx.Put(modules.FrameLogIndex, h[:], encodeFrameLogLoc(seg, off, n))
			})
			if err != nil {
				return err
			}
			endSeg, endOff = seg, end
			if i == len(segs)-1 {
				// Cut a torn tail so the next append starts on a record edge.
				if err := os.Truncate(filepath.Join(dir, frameLogSegmentName(seg)), end); err != nil {
					return err
				}
			}
		}
		return tx.Put(modules.FrameLogIndex, frameLogPosKey, encodeFrameLogPos(endSeg, endOff))
	})
	if err != nil {
		return nil, fmt.Errorf("frame log recovery: %w", err)
	}
	if err := l.openSegment(endSeg, endOff); err != nil {
		return nil, err
	}
	return l, nil
}

// scanFrameLogSegment calls fn for every complete record from offset from
// and returns the offset after the last one.
func scanFrameLogSegment(path string, from int64, fn func(h types.Hash, dataOff int64, n int) error) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := st.Size()
	if from > size {
		return size, nil
	}
	off := from
	var hdr [frameLogHeaderSize]byte
	var data []byte
	for off+frameLogHeaderSize <= size {
		if _, err := f.ReadAt(hdr[:], off); err != nil {
			break
		}
		if binary.BigEndian.Uint32(hdr[:]) != frameLogMagic {
			break
		}
		n := int(binary.LittleEndian.Uint32(hdr[4:]))
		if n <= 0 || n > frameLogMaxFrame || off+frameLogHeaderSize+int64(n) > size {
			break
		}
		if cap(data) < n {
			data = make([]byte, n)
		}
		data = data[:n]
		if _, err := f.ReadAt(data, off+frameLogHeaderSize); err != nil {
			break
		}
		if crc32.Checksum(data, frameLogCRC) != binary.LittleEndian.Uint32(hdr[8+types.HashLength:]) {
			break
		}
		var h types.Hash
		copy(h[:], hdr[8:])
		if err := fn(h, off+frameLogHeaderSize, n); err != nil {
			return 0, err
		}
		off += frameLogHeaderSize + int64(n)
	}
	return off, nil
}

func (l *frameLog) openSegment(seg uint32, off int64) error {
	f, err := os.OpenFile(filepath.Join(l.dir, frameLogSegmentName(seg)), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	if off == 0 {
		frameLogPreallocate(f, frameLogSegmentBytes)
		// A new file is durable only once its directory entry is.
		if d, err := os.Open(l.dir); err == nil {
			_ = d.Sync()
			d.Close()
		}
	}
	l.seg, l.f, l.off = seg, f, off
	return nil
}

// write appends the frames with data and returns once they are durable and
// indexed.
func (l *frameLog) write(ctx context.Context, hashes []types.Hash, raws [][]byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.f == nil {
		return errors.New("frame log is closed")
	}
	buf := l.buf[:0]
	type loc struct {
		h   types.Hash
		rel int64
		n   int
	}
	locs := make([]loc, 0, len(hashes))
	for i, h := range hashes {
		if i >= len(raws) || len(raws[i]) == 0 {
			continue
		}
		raw := raws[i]
		if len(raw) > frameLogMaxFrame {
			return fmt.Errorf("frame %s is %d bytes, over the frame log's record limit", h.Hex(), len(raw))
		}
		var hdr [frameLogHeaderSize]byte
		binary.BigEndian.PutUint32(hdr[:], frameLogMagic)
		binary.LittleEndian.PutUint32(hdr[4:], uint32(len(raw)))
		copy(hdr[8:], h[:])
		binary.LittleEndian.PutUint32(hdr[8+types.HashLength:], crc32.Checksum(raw, frameLogCRC))
		buf = append(buf, hdr[:]...)
		locs = append(locs, loc{h: h, rel: int64(len(buf)), n: len(raw)})
		buf = append(buf, raw...)
	}
	l.buf = buf
	if len(locs) == 0 {
		return nil
	}
	if l.off > 0 && l.off+int64(len(buf)) > frameLogSegmentBytes {
		if err := l.roll(); err != nil {
			return err
		}
	}
	if _, err := l.f.WriteAt(buf, l.off); err != nil {
		return err
	}
	if err := frameLogSync(l.f); err != nil {
		return err
	}
	seg, base := l.seg, l.off
	end := base + int64(len(buf))
	// The index needs no sync of its own: it is rebuilt from the log.
	if err := l.db.UpdateNosync(ctx, func(tx kv.RwTx) error {
		for _, c := range locs {
			if err := tx.Put(modules.FrameLogIndex, c.h[:], encodeFrameLogLoc(seg, base+c.rel, c.n)); err != nil {
				return err
			}
		}
		return tx.Put(modules.FrameLogIndex, frameLogPosKey, encodeFrameLogPos(seg, end))
	}); err != nil {
		return err
	}
	l.off = end
	return nil
}

// roll starts the next segment. The index position moves with the next
// write; recovery scans a finished segment to its end either way.
func (l *frameLog) roll() error {
	old := l.f
	next := l.seg + 1
	if err := l.openSegment(next, 0); err != nil {
		l.f = old
		return err
	}
	old.Close()
	if l.keepSegments > 0 && next >= uint32(l.keepSegments) {
		drop := next - uint32(l.keepSegments)
		l.rmu.Lock()
		if r := l.readers[drop]; r != nil {
			r.Close()
			delete(l.readers, drop)
		}
		l.rmu.Unlock()
		_ = os.Remove(filepath.Join(l.dir, frameLogSegmentName(drop)))
	}
	return nil
}

func (l *frameLog) reader(seg uint32) *os.File {
	l.rmu.RLock()
	f := l.readers[seg]
	l.rmu.RUnlock()
	if f != nil {
		return f
	}
	l.rmu.Lock()
	defer l.rmu.Unlock()
	if f = l.readers[seg]; f != nil {
		return f
	}
	f, err := os.Open(filepath.Join(l.dir, frameLogSegmentName(seg)))
	if err != nil {
		return nil // removed segment: the frame is not held
	}
	l.readers[seg] = f
	return f
}

// read returns the frames of hashes the log holds (nil entries for the
// others).
func (l *frameLog) read(hashes []types.Hash) ([][]byte, error) {
	out := make([][]byte, len(hashes))
	type loc struct {
		seg uint32
		off int64
		n   int
	}
	locs := make([]loc, len(hashes))
	if err := l.db.View(context.Background(), func(tx kv.Tx) error {
		for i, h := range hashes {
			v, err := tx.GetOne(modules.FrameLogIndex, h[:])
			if err != nil {
				return err
			}
			if len(v) == frameLogLocSize {
				locs[i] = loc{binary.LittleEndian.Uint32(v), int64(binary.LittleEndian.Uint64(v[4:])), int(binary.LittleEndian.Uint32(v[12:]))}
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	for i, c := range locs {
		if c.n == 0 {
			continue
		}
		f := l.reader(c.seg)
		if f == nil {
			continue
		}
		data := make([]byte, c.n)
		if _, err := f.ReadAt(data, c.off); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, os.ErrClosed) {
				continue // cut or removed under the reader: not held
			}
			return nil, fmt.Errorf("frame log: reading frame %s: %w", hashes[i].Hex(), err)
		}
		out[i] = data
	}
	return out, nil
}

func (l *frameLog) close() {
	l.mu.Lock()
	if l.f != nil {
		l.f.Close()
		l.f = nil
	}
	l.mu.Unlock()
	l.rmu.Lock()
	for seg, f := range l.readers {
		f.Close()
		delete(l.readers, seg)
	}
	l.rmu.Unlock()
}
