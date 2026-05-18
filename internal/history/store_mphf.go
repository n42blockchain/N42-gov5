package history

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/cespare/xxhash/v2"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/recsplit"
)

// MPHF mode file magics (distinct from plain mode).
const (
	MPHFKVMagic  uint32 = 0x4D484B56 // "MHKV"
	MPHFIdxMagic uint32 = 0x4D484958 // "MHIX"
)

// MPHFWriter builds a (key → blob) coldstore using:
//
//   - RecSplit MPHF mapping key → ordinal (written to <prefix>.mphf)
//   - 4-byte xxhash64-lo32 fingerprint per entry (phantom-key guard)
//   - Pages indexed by ordinal: page i covers ordinals [i*pageSize, (i+1)*pageSize)
//
// File layout:
//
//	<prefix>.mphf   RecSplit MPHF index (key → ordinal in [0, N))
//	<prefix>.kv     [16B header][page 0][page 1]...
//	                page = [4B comp_len LE][zstd-bytes]
//	                page-decompressed = consecutive [4B fp][varint blobLen][blob]
//	<prefix>.idx    [16B header][8B page byte-offset]...   one per page
//
// Read flow (MPHFReader):
//
//	ord, ok := mphf.Lookup(key)
//	if !ok { return nil, false }
//	pageIdx := ord / pageSize
//	pageOff := idx[pageIdx]
//	page = zstd.Decompress(kv[pageOff..])
//	walk to entry at (ord mod pageSize)
//	if entry.fp != fp(key) return nil, false   // phantom (lookup returned a stranger's ordinal)
//	return entry.blob, true
//
// Build flow:
//
//	Pass 1 (Append):     ETL.Collect(key, blob)  AND  RecSplit.AddKey(key)
//	Pass 2 (Close.A):    RecSplit.Build() → writes .mphf
//	Pass 2 (Close.B):    re-load Pass 1's ETL by key → for each (k,v): ord=mphf(k); Collect(ord, fp||blob)
//	Pass 3 (Close.C):    load by ordinal → write pages (zstd) + .idx
type MPHFWriter struct {
	baseDir, prefix string
	pageSize        int
	tmpDir          string
	etlBufMB        uint64

	logger log.Logger
	rs     *recsplit.RecSplit

	keyValColl *etl.Collector
	keyCount   uint64

	closed bool
}

// MPHFWriterOpts configures a new MPHFWriter.
type MPHFWriterOpts struct {
	BaseDir   string
	Prefix    string
	PageSize  int           // entries per page (e.g., 64)
	TmpDir    string        // ETL spill directory (must exist)
	KeyCount  int           // expected key count (must match Pass 1 Append count)
	EtlBufMB  uint64        // ETL buffer size in MB (default 512)
	Logger    log.Logger    // RecSplit needs a logger
}

func NewMPHFWriter(opts MPHFWriterOpts) (*MPHFWriter, error) {
	if opts.PageSize <= 0 || opts.PageSize > 65535 {
		return nil, fmt.Errorf("history-mphf: bad pageSize %d", opts.PageSize)
	}
	if opts.KeyCount <= 0 {
		return nil, fmt.Errorf("history-mphf: KeyCount must be > 0 (RecSplit needs upfront count)")
	}
	if opts.TmpDir == "" {
		opts.TmpDir = opts.BaseDir + "/etl-mphf"
	}
	if err := os.MkdirAll(opts.TmpDir, 0755); err != nil {
		return nil, fmt.Errorf("history-mphf: mkdir tmp: %w", err)
	}
	if opts.EtlBufMB == 0 {
		opts.EtlBufMB = 512
	}
	if opts.Logger == nil {
		opts.Logger = log.Root()
	}

	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:    opts.KeyCount,
		BucketSize:  2000,
		IndexFile:   opts.BaseDir + "/" + opts.Prefix + ".mphf",
		TmpDir:      opts.TmpDir,
		LeafSize:    8,
		Enums:       false,
		NoValues:    true,
		EtlBufLimit: datasize.ByteSize(opts.EtlBufMB) * datasize.MB,
	}, opts.Logger)
	if err != nil {
		return nil, fmt.Errorf("history-mphf: new recsplit: %w", err)
	}

	bufSize := datasize.ByteSize(opts.EtlBufMB) * datasize.MB
	coll := etl.NewCollector(opts.Prefix+"-mphf-kv", opts.TmpDir,
		etl.NewSortableBuffer(bufSize), opts.Logger)

	return &MPHFWriter{
		baseDir:    opts.BaseDir,
		prefix:     opts.Prefix,
		pageSize:   opts.PageSize,
		tmpDir:     opts.TmpDir,
		etlBufMB:   opts.EtlBufMB,
		logger:     opts.Logger,
		rs:         rs,
		keyValColl: coll,
	}, nil
}

// Append adds one (key, blob) pair. Key uniqueness is the caller's
// responsibility (RecSplit will return an error during Build on
// duplicates).
func (w *MPHFWriter) Append(key, blob []byte) error {
	if w.closed {
		return fmt.Errorf("history-mphf: writer closed")
	}
	if err := w.rs.AddKey(key, w.keyCount); err != nil {
		return fmt.Errorf("history-mphf: AddKey #%d: %w", w.keyCount, err)
	}
	if err := w.keyValColl.Collect(key, blob); err != nil {
		return fmt.Errorf("history-mphf: collect #%d: %w", w.keyCount, err)
	}
	w.keyCount++
	return nil
}

// Close runs the 2-pass finalisation. After this returns successfully,
// .mphf + .kv + .idx are durably written.
func (w *MPHFWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	defer func() {
		w.keyValColl.Close()
		w.rs.Close()
	}()

	// --- Step A: build MPHF ---
	if err := w.rs.Build(context.Background()); err != nil {
		return fmt.Errorf("history-mphf: RecSplit.Build: %w", err)
	}

	// --- Step B: open the freshly-built MPHF for reads ---
	mphfPath := w.baseDir + "/" + w.prefix + ".mphf"
	idx, err := recsplit.OpenIndex(mphfPath)
	if err != nil {
		return fmt.Errorf("history-mphf: open .mphf: %w", err)
	}
	defer idx.Close()
	rdr := recsplit.NewIndexReader(idx)

	// --- Step C: re-load Pass 1 entries, re-Collect by ordinal ---
	// Use the same EtlBufMB as the first-pass collector. The default
	// 512 MB hardcode in earlier versions was a pathological bottleneck:
	// at 2B+ entries / 100+ GB of ord-keyed data it produced 360+ spill
	// files, making step D's heap-merge take 8+ hours of single-threaded
	// log(N) overhead per element. Larger buffer = fewer spills =
	// shallower merge heap = much faster step D.
	ordBufSize := datasize.ByteSize(w.etlBufMB) * datasize.MB
	if ordBufSize == 0 {
		ordBufSize = datasize.MB * 512 // legacy fallback
	}
	ordColl := etl.NewCollector(w.prefix+"-mphf-ord", w.tmpDir,
		etl.NewSortableBuffer(ordBufSize), w.logger)
	defer ordColl.Close()

	var ordBuf [8]byte
	var payload []byte
	var stepCCount uint64
	stepCT0 := time.Now()
	if err := w.keyValColl.Load(nil, "", func(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		ord, ok := rdr.Lookup(k)
		if !ok {
			return fmt.Errorf("history-mphf: MPHF lookup miss for key %x (collision in build?)", k)
		}
		binary.BigEndian.PutUint64(ordBuf[:], ord)
		fp := uint32(xxhash.Sum64(k))
		payload = payload[:0]
		payload = append(payload,
			byte(fp>>24), byte(fp>>16), byte(fp>>8), byte(fp))
		// varint blob len then blob
		var vlen [binary.MaxVarintLen64]byte
		n := binary.PutUvarint(vlen[:], uint64(len(v)))
		payload = append(payload, vlen[:n]...)
		payload = append(payload, v...)
		stepCCount++
		if stepCCount%10_000_000 == 0 {
			elapsed := time.Since(stepCT0).Seconds()
			fmt.Fprintf(os.Stderr, "    step C: %dM lookups (%.0f k/s)\n",
				stepCCount/1_000_000, float64(stepCCount)/elapsed/1000)
		}
		return ordColl.Collect(ordBuf[:], payload)
	}, etl.TransformArgs{}); err != nil {
		return fmt.Errorf("history-mphf: step C load: %w", err)
	}
	fmt.Fprintf(os.Stderr, "    step C done: %d lookups in %s\n",
		stepCCount, time.Since(stepCT0).Truncate(time.Second))

	// --- Step D: load by ordinal → write pages + .idx ---
	kvPath := w.baseDir + "/" + w.prefix + ".kv"
	idxPath := w.baseDir + "/" + w.prefix + ".idx"
	kvF, err := os.Create(kvPath)
	if err != nil {
		return fmt.Errorf("history-mphf: create .kv: %w", err)
	}
	idxF, err := os.Create(idxPath)
	if err != nil {
		kvF.Close()
		return fmt.Errorf("history-mphf: create .idx: %w", err)
	}

	hdr := fileHeader{Magic: MPHFKVMagic, Version: Version, KeyLen: 0, PageSize: uint16(w.pageSize)}
	if _, err := kvF.Write(hdr.marshal()); err != nil {
		kvF.Close()
		idxF.Close()
		return err
	}
	hdr.Magic = MPHFIdxMagic
	if _, err := idxF.Write(hdr.marshal()); err != nil {
		kvF.Close()
		idxF.Close()
		return err
	}

	kvBuf := bufio.NewWriterSize(kvF, 1<<20)
	idxBuf := bufio.NewWriterSize(idxF, 1<<20)
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		kvF.Close()
		idxF.Close()
		return err
	}
	defer enc.Close()

	var (
		rawPage    []byte
		entryCount int
		pageOff    int64 = int64(HeaderLen)
		pages      uint64
	)

	flushPage := func() error {
		if entryCount == 0 {
			return nil
		}
		comp := enc.EncodeAll(rawPage, nil)
		var lenBuf [4]byte
		binary.LittleEndian.PutUint32(lenBuf[:], uint32(len(comp)))
		if _, err := kvBuf.Write(lenBuf[:]); err != nil {
			return err
		}
		if _, err := kvBuf.Write(comp); err != nil {
			return err
		}
		var offBuf [8]byte
		binary.LittleEndian.PutUint64(offBuf[:], uint64(pageOff))
		if _, err := idxBuf.Write(offBuf[:]); err != nil {
			return err
		}
		pageOff += int64(4 + len(comp))
		pages++
		rawPage = rawPage[:0]
		entryCount = 0
		return nil
	}

	var stepDCount uint64
	stepDT0 := time.Now()
	if err := ordColl.Load(nil, "", func(_, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		// v is already [4B fp][varint blobLen][blob] from step C.
		rawPage = append(rawPage, v...)
		entryCount++
		stepDCount++
		if stepDCount%10_000_000 == 0 {
			elapsed := time.Since(stepDT0).Seconds()
			fmt.Fprintf(os.Stderr, "    step D: %dM written (%.0f k/s), %d pages\n",
				stepDCount/1_000_000, float64(stepDCount)/elapsed/1000, pages)
		}
		if entryCount >= w.pageSize {
			return flushPage()
		}
		return nil
	}, etl.TransformArgs{}); err != nil {
		kvBuf.Flush()
		idxBuf.Flush()
		kvF.Close()
		idxF.Close()
		return fmt.Errorf("history-mphf: step D load: %w", err)
	}
	fmt.Fprintf(os.Stderr, "    step D done: %d entries / %d pages in %s\n",
		stepDCount, pages, time.Since(stepDT0).Truncate(time.Second))
	if err := flushPage(); err != nil {
		return err
	}

	if err := kvBuf.Flush(); err != nil {
		return err
	}
	if err := idxBuf.Flush(); err != nil {
		return err
	}
	if err := kvF.Sync(); err != nil {
		return err
	}
	if err := idxF.Sync(); err != nil {
		return err
	}
	if err := kvF.Close(); err != nil {
		return err
	}
	if err := idxF.Close(); err != nil {
		return err
	}

	return nil
}

// MPHFStats reports counters for the build tool's summary.
type MPHFStats struct {
	KeyCount  uint64
	PageCount uint64
	KvSize    uint64 // .kv file size on disk
	IdxSize   uint64 // .idx file size on disk
	MphfSize  uint64 // .mphf file size on disk
}

func (w *MPHFWriter) Stats() MPHFStats {
	st := MPHFStats{KeyCount: w.keyCount}
	if !w.closed {
		return st
	}
	if fi, err := os.Stat(w.baseDir + "/" + w.prefix + ".kv"); err == nil {
		st.KvSize = uint64(fi.Size())
	}
	if fi, err := os.Stat(w.baseDir + "/" + w.prefix + ".idx"); err == nil {
		st.IdxSize = uint64(fi.Size())
		st.PageCount = (st.IdxSize - HeaderLen) / 8
	}
	if fi, err := os.Stat(w.baseDir + "/" + w.prefix + ".mphf"); err == nil {
		st.MphfSize = uint64(fi.Size())
	}
	return st
}

// MPHFReader serves point lookups against a MPHF-mode coldstore.
type MPHFReader struct {
	pageSize int
	idxOff   []byte // mmap'd .idx body (8B per page)
	kvFile   *os.File
	dec      *zstd.Decoder

	mphf   *recsplit.Index
	reader *recsplit.IndexReader
}

func OpenMPHF(baseDir, prefix string) (*MPHFReader, error) {
	kvPath := baseDir + "/" + prefix + ".kv"
	idxPath := baseDir + "/" + prefix + ".idx"
	mphfPath := baseDir + "/" + prefix + ".mphf"

	idxBytes, err := os.ReadFile(idxPath)
	if err != nil {
		return nil, fmt.Errorf("read idx: %w", err)
	}
	if len(idxBytes) < HeaderLen {
		return nil, fmt.Errorf("idx too short")
	}
	ihdr, err := unmarshalHeader(idxBytes[:HeaderLen])
	if err != nil {
		return nil, err
	}
	if ihdr.Magic != MPHFIdxMagic {
		return nil, fmt.Errorf("idx magic mismatch: 0x%08x (expected MPHF)", ihdr.Magic)
	}
	if ihdr.Version != Version {
		return nil, fmt.Errorf("idx version %d != %d", ihdr.Version, Version)
	}

	kvF, err := os.Open(kvPath)
	if err != nil {
		return nil, err
	}
	var kvHdrBuf [HeaderLen]byte
	if _, err := io.ReadFull(kvF, kvHdrBuf[:]); err != nil {
		kvF.Close()
		return nil, err
	}
	kvHdr, _ := unmarshalHeader(kvHdrBuf[:])
	if kvHdr.Magic != MPHFKVMagic {
		kvF.Close()
		return nil, fmt.Errorf("kv magic mismatch")
	}
	if kvHdr.PageSize != ihdr.PageSize {
		kvF.Close()
		return nil, fmt.Errorf("kv/idx pageSize mismatch")
	}

	mphfIdx, err := recsplit.OpenIndex(mphfPath)
	if err != nil {
		kvF.Close()
		return nil, fmt.Errorf("open .mphf: %w", err)
	}
	dec, err := zstd.NewReader(nil)
	if err != nil {
		kvF.Close()
		mphfIdx.Close()
		return nil, err
	}
	return &MPHFReader{
		pageSize: int(ihdr.PageSize),
		idxOff:   idxBytes[HeaderLen:],
		kvFile:   kvF,
		dec:      dec,
		mphf:     mphfIdx,
		reader:   recsplit.NewIndexReader(mphfIdx),
	}, nil
}

func (r *MPHFReader) Close() error {
	r.dec.Close()
	r.mphf.Close()
	return r.kvFile.Close()
}

func (r *MPHFReader) PageCount() int { return len(r.idxOff) / 8 }

func (r *MPHFReader) KeyCount() uint64 { return r.mphf.KeyCount() }

// Get returns the blob for key (or nil, false if absent / phantom).
func (r *MPHFReader) Get(key []byte) ([]byte, bool, error) {
	ord, ok := r.reader.Lookup(key)
	if !ok {
		return nil, false, nil
	}
	pageIdx := int(ord) / r.pageSize
	if pageIdx >= r.PageCount() {
		return nil, false, nil
	}
	pageOff := binary.LittleEndian.Uint64(r.idxOff[pageIdx*8 : pageIdx*8+8])

	var lenBuf [4]byte
	if _, err := r.kvFile.ReadAt(lenBuf[:], int64(pageOff)); err != nil {
		return nil, false, fmt.Errorf("read page len: %w", err)
	}
	compLen := binary.LittleEndian.Uint32(lenBuf[:])
	comp := make([]byte, compLen)
	if _, err := r.kvFile.ReadAt(comp, int64(pageOff)+4); err != nil {
		return nil, false, fmt.Errorf("read page body: %w", err)
	}
	raw, err := r.dec.DecodeAll(comp, nil)
	if err != nil {
		return nil, false, fmt.Errorf("decompress page %d: %w", pageIdx, err)
	}

	target := int(ord) % r.pageSize
	wantFp := uint32(xxhash.Sum64(key))
	pos := 0
	for i := 0; i <= target && pos < len(raw); i++ {
		if pos+4 > len(raw) {
			return nil, false, fmt.Errorf("page %d truncated fp at pos %d", pageIdx, pos)
		}
		fp := binary.BigEndian.Uint32(raw[pos : pos+4])
		pos += 4
		blobLen, n := binary.Uvarint(raw[pos:])
		if n <= 0 {
			return nil, false, fmt.Errorf("page %d bad blobLen at entry %d", pageIdx, i)
		}
		pos += n
		if pos+int(blobLen) > len(raw) {
			return nil, false, fmt.Errorf("page %d truncated blob at entry %d", pageIdx, i)
		}
		if i == target {
			if fp != wantFp {
				return nil, false, nil // phantom — MPHF assigned this ordinal to a stranger
			}
			out := make([]byte, blobLen)
			copy(out, raw[pos:pos+int(blobLen)])
			return out, true, nil
		}
		pos += int(blobLen)
	}
	return nil, false, fmt.Errorf("page %d short: wanted entry %d, only %d entries", pageIdx, target, target)
}
