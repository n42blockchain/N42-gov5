package state

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"path"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/edsrzf/mmap-go"
	"github.com/n42blockchain/N42/lib/log/v3"

	"github.com/n42blockchain/N42/lib/common/dbg"

	"github.com/n42blockchain/N42/lib/common/background"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/seg"
)

// deprecated
type BtIndexReader struct {
	index *BtIndex
}

func NewBtIndexReader(index *BtIndex) *BtIndexReader {
	return &BtIndexReader{
		index: index,
	}
}

// Lookup wraps index Lookup
func (r *BtIndexReader) Lookup(key []byte) uint64 {
	if r.index != nil {
		return r.index.Lookup(key)
	}
	return 0
}

func (r *BtIndexReader) Lookup2(key1, key2 []byte) uint64 {
	fk := make([]byte, 52)
	copy(fk[:length.Addr], key1)
	copy(fk[length.Addr:], key2)

	if r.index != nil {
		return r.index.Lookup(fk)
	}
	return 0
}

func (r *BtIndexReader) Seek(x []byte) (*Cursor, error) {
	if r.index != nil {
		cursor, err := r.index.alloc.Seek(x)
		if err != nil {
			return nil, fmt.Errorf("seek key %x: %w", x, err)
		}
		return cursor, nil
	}
	return nil, fmt.Errorf("seek has been failed")
}

func (r *BtIndexReader) Empty() bool {
	return r.index.Empty()
}

type BtIndexWriter struct {
	built           bool
	lvl             log.Lvl
	maxOffset       uint64
	prevOffset      uint64
	minDelta        uint64
	indexW          *bufio.Writer
	indexF          *os.File
	bucketCollector *etl.Collector // Collector that sorts by buckets

	indexFileName          string
	indexFile, tmpFilePath string

	tmpDir      string
	numBuf      [8]byte
	keyCount    uint64
	etlBufLimit datasize.ByteSize
	bytesPerRec int
	logger      log.Logger
	noFsync     bool // fsync is enabled by default, but tests can manually disable
}

type BtIndexWriterArgs struct {
	IndexFile   string // File name where the index and the minimal perfect hash function will be written to
	TmpDir      string
	KeyCount    int
	EtlBufLimit datasize.ByteSize
}

const BtreeLogPrefix = "btree"

// NewBtIndexWriter creates a new BtIndexWriter instance with given number of keys
// Typical bucket size is 100 - 2048, larger bucket sizes result in smaller representations of hash functions, at a cost of slower access
// salt parameters is used to randomise the hash function construction, to ensure that different Erigon instances (nodes)
// are likely to use different hash function, to collision attacks are unlikely to slow down any meaningful number of nodes at the same time
func NewBtIndexWriter(args BtIndexWriterArgs, logger log.Logger) (*BtIndexWriter, error) {
	btw := &BtIndexWriter{lvl: log.LvlDebug, logger: logger}
	btw.tmpDir = args.TmpDir
	btw.indexFile = args.IndexFile
	btw.tmpFilePath = args.IndexFile + ".tmp"

	_, fname := filepath.Split(btw.indexFile)
	btw.indexFileName = fname
	btw.etlBufLimit = args.EtlBufLimit
	if btw.etlBufLimit == 0 {
		btw.etlBufLimit = etl.BufferOptimalSize
	}

	btw.bucketCollector = etl.NewCollector(BtreeLogPrefix+" "+fname, btw.tmpDir, etl.NewSortableBuffer(btw.etlBufLimit), logger)
	btw.bucketCollector.LogLvl(log.LvlDebug)

	btw.maxOffset = 0
	return btw, nil
}

// loadFuncBucket is required to satisfy the type etl.LoadFunc type, to use with collector.Load
func (btw *BtIndexWriter) loadFuncBucket(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
	if _, err := btw.indexW.Write(v[8-btw.bytesPerRec:]); err != nil {
		return err
	}
	return nil
}

// Build has to be called after all the keys have been added, and it initiates the process
// of building the perfect hash function and writing index into a file
func (btw *BtIndexWriter) Build() error {
	if btw.built {
		return fmt.Errorf("already built")
	}
	var err error
	if btw.indexF, err = os.Create(btw.tmpFilePath); err != nil {
		return fmt.Errorf("create index file %s: %w", btw.indexFile, err)
	}
	defer btw.indexF.Close()
	btw.indexW = bufio.NewWriterSize(btw.indexF, etl.BufIOSize)

	// Write number of keys
	binary.BigEndian.PutUint64(btw.numBuf[:], btw.keyCount)
	if _, err = btw.indexW.Write(btw.numBuf[:]); err != nil {
		return fmt.Errorf("write number of keys: %w", err)
	}
	// Write number of bytes per index record
	btw.bytesPerRec = common.BitLenToByteLen(bits.Len64(btw.maxOffset))
	if err = btw.indexW.WriteByte(byte(btw.bytesPerRec)); err != nil {
		return fmt.Errorf("write bytes per record: %w", err)
	}

	defer btw.bucketCollector.Close()
	log.Log(btw.lvl, "[index] calculating", "file", btw.indexFileName)
	if err := btw.bucketCollector.Load(nil, "", btw.loadFuncBucket, etl.TransformArgs{}); err != nil {
		return err
	}

	btw.logger.Log(btw.lvl, "[index] write", "file", btw.indexFileName)
	btw.built = true

	if err = btw.indexW.Flush(); err != nil {
		return err
	}
	if err = btw.fsync(); err != nil {
		return err
	}
	if err = btw.indexF.Close(); err != nil {
		return err
	}
	if err = os.Rename(btw.tmpFilePath, btw.indexFile); err != nil {
		return err
	}
	return nil
}

func (btw *BtIndexWriter) DisableFsync() { btw.noFsync = true }

// fsync - other processes/goroutines must see only "fully-complete" (valid) files. No partial-writes.
// To achieve it: write to .tmp file then `rename` when file is ready.
// Machine may power-off right after `rename` - it means `fsync` must be before `rename`
func (btw *BtIndexWriter) fsync() error {
	if btw.noFsync {
		return nil
	}
	if err := btw.indexF.Sync(); err != nil {
		btw.logger.Warn("couldn't fsync", "err", err, "file", btw.tmpFilePath)
		return err
	}
	return nil
}

func (btw *BtIndexWriter) Close() {
	if btw.indexF != nil {
		btw.indexF.Close()
	}
	if btw.bucketCollector != nil {
		btw.bucketCollector.Close()
	}
}

func (btw *BtIndexWriter) AddKey(key []byte, offset uint64) error {
	if btw.built {
		return fmt.Errorf("cannot add keys after perfect hash function had been built")
	}

	binary.BigEndian.PutUint64(btw.numBuf[:], offset)
	if offset > btw.maxOffset {
		btw.maxOffset = offset
	}
	if btw.keyCount > 0 {
		delta := offset - btw.prevOffset
		if btw.keyCount == 1 || delta < btw.minDelta {
			btw.minDelta = delta
		}
	}

	if err := btw.bucketCollector.Collect(key, btw.numBuf[:]); err != nil {
		return err
	}
	btw.keyCount++
	btw.prevOffset = offset
	return nil
}

type BtIndex struct {
	alloc        *btAlloc
	m            mmap.MMap
	data         []byte
	file         *os.File
	size         int64
	modTime      time.Time
	filePath     string
	keyCount     uint64
	bytesPerRec  int
	dataoffset   uint64
	auxBuf       []byte
	decompressor *seg.Decompressor
	getter       *seg.Getter
}

func CreateBtreeIndex(indexPath, dataPath string, M uint64, logger log.Logger) (*BtIndex, error) {
	err := BuildBtreeIndex(dataPath, indexPath, logger)
	if err != nil {
		return nil, err
	}
	return OpenBtreeIndex(indexPath, dataPath, M)
}

var DefaultBtreeM = uint64(2048)

func CreateBtreeIndexWithDecompressor(indexPath string, M uint64, decompressor *seg.Decompressor, p *background.Progress, tmpdir string, logger log.Logger) (*BtIndex, error) {
	err := BuildBtreeIndexWithDecompressor(indexPath, decompressor, p, tmpdir, logger)
	if err != nil {
		return nil, err
	}
	return OpenBtreeIndexWithDecompressor(indexPath, M, decompressor)
}

func BuildBtreeIndexWithDecompressor(indexPath string, kv *seg.Decompressor, p *background.Progress, tmpdir string, logger log.Logger) error {
	defer kv.EnableReadAhead().DisableReadAhead()

	args := BtIndexWriterArgs{
		IndexFile: indexPath,
		TmpDir:    tmpdir,
	}

	iw, err := NewBtIndexWriter(args, logger)
	if err != nil {
		return err
	}

	getter := kv.MakeGetter()
	getter.Reset(0)

	key := make([]byte, 0, 64)

	var pos uint64
	for getter.HasNext() {
		p.Processed.Add(1)
		key, _ = getter.Next(key[:0])
		err = iw.AddKey(key, pos)
		if err != nil {
			return err
		}

		pos, _ = getter.Skip()
	}

	if err := iw.Build(); err != nil {
		return err
	}
	iw.Close()
	return nil
}

// Opens .kv at dataPath and generates index over it to file 'indexPath'
func BuildBtreeIndex(dataPath, indexPath string, logger log.Logger) error {
	decomp, err := seg.NewDecompressor(dataPath)
	if err != nil {
		return err
	}
	defer decomp.Close()

	defer decomp.EnableReadAhead().DisableReadAhead()

	args := BtIndexWriterArgs{
		IndexFile: indexPath,
		TmpDir:    filepath.Dir(indexPath),
	}

	iw, err := NewBtIndexWriter(args, logger)
	if err != nil {
		return err
	}
	defer iw.Close()

	getter := decomp.MakeGetter()
	getter.Reset(0)

	key := make([]byte, 0, 64)

	var pos uint64
	for getter.HasNext() {
		key, _ = getter.Next(key[:0])
		err = iw.AddKey(key, pos)
		if err != nil {
			return err
		}

		pos, _ = getter.Skip()
	}
	decomp.Close()

	if err := iw.Build(); err != nil {
		return err
	}
	iw.Close()
	return nil
}

func OpenBtreeIndexWithDecompressor(indexPath string, M uint64, kv *seg.Decompressor) (*BtIndex, error) {
	s, err := os.Stat(indexPath)
	if err != nil {
		return nil, err
	}

	idx := &BtIndex{
		filePath: indexPath,
		size:     s.Size(),
		modTime:  s.ModTime(),
		auxBuf:   make([]byte, 64),
	}

	idx.file, err = os.Open(indexPath)
	if err != nil {
		return nil, err
	}

	idx.m, err = mmap.MapRegion(idx.file, int(idx.size), mmap.RDONLY, 0, 0)
	if err != nil {
		return nil, err
	}
	idx.data = idx.m[:idx.size]

	// Read number of keys and bytes per record
	pos := 8
	idx.keyCount = binary.BigEndian.Uint64(idx.data[:pos])
	if idx.keyCount == 0 {
		return idx, nil
	}
	idx.bytesPerRec = int(idx.data[pos])
	pos++

	idx.getter = kv.MakeGetter()

	idx.dataoffset = uint64(pos)
	idx.alloc = newBtAlloc(idx.keyCount, M, false)
	if idx.alloc != nil {
		idx.alloc.dataLookup = idx.dataLookup
		idx.alloc.traverseDfs()
		defer idx.decompressor.EnableReadAhead().DisableReadAhead()
		idx.alloc.fillSearchMx()
	}
	return idx, nil
}

func OpenBtreeIndex(indexPath, dataPath string, M uint64) (*BtIndex, error) {
	s, err := os.Stat(indexPath)
	if err != nil {
		return nil, err
	}

	idx := &BtIndex{
		filePath: indexPath,
		size:     s.Size(),
		modTime:  s.ModTime(),
		auxBuf:   make([]byte, 64),
	}

	idx.file, err = os.Open(indexPath)
	if err != nil {
		return nil, err
	}

	idx.m, err = mmap.MapRegion(idx.file, int(idx.size), mmap.RDONLY, 0, 0)
	if err != nil {
		return nil, err
	}
	idx.data = idx.m[:idx.size]

	// Read number of keys and bytes per record
	pos := 8
	idx.keyCount = binary.BigEndian.Uint64(idx.data[:pos])
	idx.bytesPerRec = int(idx.data[pos])
	pos++

	idx.decompressor, err = seg.NewDecompressor(dataPath)
	if err != nil {
		idx.Close()
		return nil, err
	}
	idx.getter = idx.decompressor.MakeGetter()

	idx.dataoffset = uint64(pos)
	idx.alloc = newBtAlloc(idx.keyCount, M, false)
	if idx.alloc != nil {
		idx.alloc.dataLookup = idx.dataLookup
		idx.alloc.traverseDfs()
		defer idx.decompressor.EnableReadAhead().DisableReadAhead()
		idx.alloc.fillSearchMx()
	}
	return idx, nil
}

var ErrBtIndexLookupBounds = errors.New("BtIndex: lookup di bounds error")

// dataLookup fetches key and value from data file by di (data index)
// di starts from 0 so di is never >= keyCount
func (b *BtIndex) dataLookup(di uint64) ([]byte, []byte, error) {
	if di >= b.keyCount {
		return nil, nil, fmt.Errorf("%w: keyCount=%d, item %d requested. file: %s", ErrBtIndexLookupBounds, b.keyCount, di+1, b.FileName())
	}
	p := int(b.dataoffset) + int(di)*b.bytesPerRec
	if len(b.data) < p+b.bytesPerRec {
		return nil, nil, fmt.Errorf("data lookup gone too far (%d after %d). keyCount=%d, requesed item %d. file: %s", p+b.bytesPerRec-len(b.data), len(b.data), b.keyCount, di, b.FileName())
	}

	var aux [8]byte
	dst := aux[8-b.bytesPerRec:]
	copy(dst, b.data[p:p+b.bytesPerRec])

	offset := binary.BigEndian.Uint64(aux[:])
	b.getter.Reset(offset)
	if !b.getter.HasNext() {
		return nil, nil, fmt.Errorf("pair %d not found. keyCount=%d. file: %s", di, b.keyCount, b.FileName())
	}

	key, _ := b.getter.Next(nil)

	if !b.getter.HasNext() {
		return nil, nil, fmt.Errorf("pair %d not found. keyCount=%d. file: %s", di, b.keyCount, b.FileName())
	}
	val, _ := b.getter.Next(nil)
	return key, val, nil
}

func (b *BtIndex) Size() int64 { return b.size }

func (b *BtIndex) ModTime() time.Time { return b.modTime }

func (b *BtIndex) FilePath() string { return b.filePath }

func (b *BtIndex) FileName() string { return path.Base(b.filePath) }

func (b *BtIndex) Empty() bool { return b == nil || b.keyCount == 0 }

func (b *BtIndex) KeyCount() uint64 { return b.keyCount }

func (b *BtIndex) Close() {
	if b == nil {
		return
	}
	if b.file != nil {
		if err := b.m.Unmap(); err != nil {
			log.Log(dbg.FileCloseLogLevel, "unmap", "err", err, "file", b.FileName(), "stack", dbg.Stack())
		}
		b.m = nil
		if err := b.file.Close(); err != nil {
			log.Log(dbg.FileCloseLogLevel, "close", "err", err, "file", b.FileName(), "stack", dbg.Stack())
		}
		b.file = nil
	}
	if b.decompressor != nil {
		b.decompressor.Close()
		b.decompressor = nil
	}
}

func (b *BtIndex) Seek(x []byte) (*Cursor, error) {
	if b.alloc == nil {
		return nil, nil
	}
	cursor, err := b.alloc.Seek(x)
	if err != nil {
		return nil, fmt.Errorf("seek key %x: %w", x, err)
	}
	// cursor could be nil along with err if nothing found
	return cursor, nil
}

// deprecated
func (b *BtIndex) Lookup(key []byte) uint64 {
	if b.alloc == nil {
		return 0
	}
	cursor, err := b.alloc.Seek(key)
	if err != nil {
		panic(err)
	}
	return binary.BigEndian.Uint64(cursor.value)
}

func (b *BtIndex) OrdinalLookup(i uint64) *Cursor {
	if b.alloc == nil {
		return nil
	}
	if i > b.alloc.K {
		return nil
	}
	k, v, err := b.dataLookup(i)
	if err != nil {
		return nil
	}

	return &Cursor{
		key: k, value: v, d: i, ix: b.alloc,
	}
}
