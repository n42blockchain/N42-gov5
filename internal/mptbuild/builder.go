package mptbuild

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/etl"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
)

// Source abstracts the input cursor stream (k, v) pairs feeding the
// builder. Production source is reth's MDBX cursor; tests use an
// in-memory implementation.
type Source interface {
	// Iter calls fn for every (k, v) pair in source order. Stop early
	// by returning a non-nil error from fn (it propagates as the
	// Iter return value).
	Iter(fn func(k, v []byte) error) error
}

// Target abstracts the output trie storage. Production target is an
// MDBX bucket using AppendDup for fastest sorted insertion; tests use
// an in-memory map.
type Target interface {
	// Begin starts a write session.
	Begin() error
	// Append writes one (key, value) pair. Calls MUST be in
	// ascending key order — Append is allowed to use the MDBX
	// AppendDup fast path which rejects out-of-order keys.
	Append(key, value []byte) error
	// Commit finalises the write session. Sets the state root.
	Commit(stateRoot [32]byte) error
	// Close releases resources.
	Close() error
}

// Opts bundles a build invocation.
type Opts struct {
	Source    Source
	Target    Target
	Extractor Extractor

	TmpDir   string        // ETL spill directory
	BufMB    uint64        // ETL buffer per collector (default 1024)
	Logger   log.Logger    // optional; defaults to log.New()
	Progress func(rows int64)

	// DenseBranchSink, when set, is invoked once per branch with the
	// FULL per-child slot data (33 bytes per child, prefix encodes
	// inline-vs-hash). Pairs 1:1 with the standard compact write to
	// Target. Used by Phase G1 to populate a dense CommitmentDomain
	// table alongside (or instead of) the compact AccountsTrie /
	// StoragesTrie tables.
	//
	//   keyHex   = nibble path of the branch (same as the compact key)
	//   stateMask, treeMask = same masks the compact form would record
	//   slotData = hashStackStride * popcount(stateMask) bytes
	//
	// Returning a non-nil error aborts the build.
	DenseBranchSink func(keyHex []byte, stateMask, treeMask uint16, slotData []byte) error
}

// Result captures the outcome of a build.
type Result struct {
	Leaves      int64
	Branches    int64
	BranchBytes int64
	StateRoot   [32]byte
	Pass1       time.Duration
	Pass2       time.Duration
	Pass3       time.Duration
}

// Build runs the 3-pass pipeline. Idempotent w.r.t. Target as long as
// Target.Begin truncates any prior state.
func Build(ctx context.Context, opts Opts) (*Result, error) {
	if opts.Source == nil {
		return nil, fmt.Errorf("mptbuild: nil Source")
	}
	if opts.Target == nil {
		return nil, fmt.Errorf("mptbuild: nil Target")
	}
	if opts.Extractor == nil {
		return nil, fmt.Errorf("mptbuild: nil Extractor")
	}
	if opts.TmpDir == "" {
		return nil, fmt.Errorf("mptbuild: TmpDir required")
	}
	if opts.BufMB == 0 {
		opts.BufMB = 1024
	}
	if opts.Logger == nil {
		opts.Logger = log.New()
	}

	res := &Result{}

	// =====================================================================
	// Pass 1: cursor scan → hash key → ETL Collect (external sort)
	// =====================================================================
	t1 := time.Now()
	bufSize := datasize.ByteSize(opts.BufMB) * datasize.MB
	leafColl := etl.NewCollector(
		"mptbuild-leaves-"+opts.Extractor.Name(),
		opts.TmpDir,
		etl.NewSortableBuffer(bufSize),
		opts.Logger,
	)
	defer leafColl.Close()

	nibbleScratch := make([]byte, 0, 130)
	collectErr := opts.Source.Iter(func(k, v []byte) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		nibbles, value, err := opts.Extractor.Extract(k, v, nibbleScratch)
		if err != nil {
			return fmt.Errorf("extract row %d: %w", res.Leaves, err)
		}
		nibbleScratch = nibbles[:0] // reuse buffer
		// Collect copies bytes internally so passing slices is safe.
		if err := leafColl.Collect(nibbles, value); err != nil {
			return fmt.Errorf("ETL.Collect row %d: %w", res.Leaves, err)
		}
		res.Leaves++
		if opts.Progress != nil && res.Leaves%1_000_000 == 0 {
			opts.Progress(res.Leaves)
		}
		return nil
	})
	if collectErr != nil {
		return nil, collectErr
	}
	res.Pass1 = time.Since(t1)
	if res.Leaves == 0 {
		return nil, ErrEmptySource
	}

	// =====================================================================
	// Pass 2: ETL.Load sorted → HashBuilder → branch ETL.Collect
	// =====================================================================
	t2 := time.Now()
	hb := trie.NewHashBuilder(false)

	branchColl := etl.NewCollector(
		"mptbuild-branches-"+opts.Extractor.Name(),
		opts.TmpDir,
		etl.NewSortableBuffer(bufSize),
		opts.Logger,
	)
	defer branchColl.Close()

	var (
		groups, hasTreeArr, hasHashArr []uint16
		curr, succ, currVal            []byte
		leafData                       trie.GenStructStepLeafData
		marshalBuf                     []byte
	)
	retain := func(_ []byte) bool { return false }

	hc := func(keyHex []byte, hasState, hasTreeM, hasHashM uint16, hashes, rootHash []byte) error {
		if hasState == 0 {
			return nil
		}
		need := 6 + len(hashes) + len(rootHash)
		if cap(marshalBuf) < need {
			marshalBuf = make([]byte, need)
		}
		marshalBuf = marshalBuf[:need]
		_ = trie.MarshalTrieNode(hasState, hasTreeM, hasHashM, hashes, rootHash, marshalBuf)
		keyCopy := make([]byte, len(keyHex))
		copy(keyCopy, keyHex)
		valCopy := make([]byte, len(marshalBuf))
		copy(valCopy, marshalBuf)
		if err := branchColl.Collect(keyCopy, valCopy); err != nil {
			return err
		}
		res.Branches++
		res.BranchBytes += int64(need)
		// Dense sink: hb.LastDenseSlots() returns the snapshot taken
		// by gen_struct_step right after topHashes (before branchHash
		// pops the children). The snapshot covers all bits in hasState
		// regardless of how many made it into hasHash, so dense always
		// has full per-child data even when the compact hc passes a
		// trimmed `hashes` arg.
		if opts.DenseBranchSink != nil {
			slot := hb.LastDenseSlots()
			if err := opts.DenseBranchSink(keyCopy, hasState, hasTreeM, slot); err != nil {
				return err
			}
		}
		return nil
	}

	loadFn := func(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		succ = append(succ[:0], k...)
		if len(curr) > 0 {
			leafData.Value = rlphacks.RlpEncodedBytes(currVal)
			var err error
			groups, hasTreeArr, hasHashArr, err = trie.GenStructStep(
				retain, curr, succ, hb, hc, &leafData,
				groups, hasTreeArr, hasHashArr, false,
			)
			if err != nil {
				return fmt.Errorf("GenStructStep: %w", err)
			}
		}
		curr = append(curr[:0], succ...)
		currVal = append(currVal[:0], v...)
		return nil
	}

	if err := leafColl.Load(nil, "", loadFn, etl.TransformArgs{}); err != nil {
		return nil, fmt.Errorf("pass-2 Load: %w", err)
	}

	if len(curr) > 0 {
		leafData.Value = rlphacks.RlpEncodedBytes(currVal)
		if _, _, _, err := trie.GenStructStep(
			retain, curr, []byte{}, hb, hc, &leafData,
			groups, hasTreeArr, hasHashArr, false,
		); err != nil {
			return nil, fmt.Errorf("final GenStructStep: %w", err)
		}
	}

	root, err := hb.RootHash()
	if err != nil {
		return nil, fmt.Errorf("RootHash: %w", err)
	}
	copy(res.StateRoot[:], root[:])
	res.Pass2 = time.Since(t2)

	// =====================================================================
	// Pass 3: ETL.Load sorted branches → Target.Append (AppendDup fast path)
	// =====================================================================
	t3 := time.Now()
	if err := opts.Target.Begin(); err != nil {
		return nil, fmt.Errorf("Target.Begin: %w", err)
	}

	writeFn := func(k, v []byte, _ etl.CurrentTableReader, _ etl.LoadNextFunc) error {
		return opts.Target.Append(k, v)
	}
	if err := branchColl.Load(nil, "", writeFn, etl.TransformArgs{}); err != nil {
		return nil, fmt.Errorf("pass-3 Load: %w", err)
	}
	if err := opts.Target.Commit(res.StateRoot); err != nil {
		return nil, fmt.Errorf("Target.Commit: %w", err)
	}
	res.Pass3 = time.Since(t3)

	return res, nil
}

// ============================================================================
// Source implementations
// ============================================================================

// MDBXSource reads (k, v) pairs sequentially from a single MDBX table.
type MDBXSource struct {
	DBPath    string
	Table     string
	MapSizeGB int
	MaxRows   int64 // 0 = all
	Logger    log.Logger
}

func (s *MDBXSource) Iter(fn func(k, v []byte) error) error {
	logger := s.Logger
	if logger == nil {
		logger = log.New()
	}
	mapSize := s.MapSizeGB
	if mapSize <= 0 {
		mapSize = 4096
	}
	db, err := mdbxkv.NewMDBX(logger).
		Path(s.DBPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(mapSize) * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[s.Table] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx %s: %w", s.DBPath, err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	c, err := tx.Cursor(s.Table)
	if err != nil {
		return err
	}
	defer c.Close()

	var n int64
	for k, v, err := c.First(); err == nil && k != nil; k, v, err = c.Next() {
		if err := fn(k, v); err != nil {
			return err
		}
		n++
		if s.MaxRows > 0 && n >= s.MaxRows {
			return nil
		}
	}
	return nil
}

// MapSource is an in-memory source for tests.
type MapSource struct {
	Entries [][2][]byte // ordered (k, v) pairs
}

func (m *MapSource) Iter(fn func(k, v []byte) error) error {
	for _, e := range m.Entries {
		if err := fn(e[0], e[1]); err != nil {
			return err
		}
	}
	return nil
}

// ============================================================================
// Target implementations
// ============================================================================

// MDBXTarget writes the built trie into an MDBX bucket via AppendDup
// for the fastest sorted-insert path (~95% page fill). Suitable for the
// initial one-shot archive build; per-block updates use the standard
// transactional writer (not this Target).
//
// The state root and build metadata are written into a small Meta
// bucket, so the result is fully self-contained in one MDBX directory.
type MDBXTarget struct {
	DBPath    string
	Table     string // e.g. "AccountsTrie" or "StoragesTrie"
	MapSizeGB int
	Logger    log.Logger

	db             kv.RwDB
	tx             kv.RwTx
	cursor         kv.RwCursor
	lastBucketSize uint64 // populated by Commit
}

const metaTable = "Meta"

func (t *MDBXTarget) Begin() error {
	logger := t.Logger
	if logger == nil {
		logger = log.New()
	}
	mapSize := t.MapSizeGB
	if mapSize <= 0 {
		mapSize = 64
	}
	db, err := mdbxkv.NewMDBX(logger).
		Path(t.DBPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(mapSize) * datasize.GB).
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[t.Table] = kv.TableCfgItem{}
			d[metaTable] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		return fmt.Errorf("open mdbx %s: %w", t.DBPath, err)
	}
	t.db = db

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		db.Close()
		return err
	}
	t.tx = tx

	// Truncate any prior content.
	if err := tx.ClearBucket(t.Table); err != nil {
		tx.Rollback()
		db.Close()
		return fmt.Errorf("ClearBucket %s: %w", t.Table, err)
	}
	if err := tx.ClearBucket(metaTable); err != nil {
		tx.Rollback()
		db.Close()
		return fmt.Errorf("ClearBucket %s: %w", metaTable, err)
	}

	cur, err := tx.RwCursor(t.Table)
	if err != nil {
		tx.Rollback()
		db.Close()
		return err
	}
	t.cursor = cur
	return nil
}

func (t *MDBXTarget) Append(key, value []byte) error {
	// Cursor.Append is MDBX's fast sorted-insert path. Requires
	// strictly ascending keys; ETL's loader guarantees this for us.
	return t.cursor.Append(key, value)
}

func (t *MDBXTarget) Commit(stateRoot [32]byte) error {
	if t.cursor != nil {
		t.cursor.Close()
		t.cursor = nil
	}
	if err := t.tx.Put(metaTable, []byte("state_root"), stateRoot[:]); err != nil {
		t.tx.Rollback()
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := t.tx.Put(metaTable, []byte("built_at"), []byte(now)); err != nil {
		t.tx.Rollback()
		return err
	}
	// Capture BucketSize (actual used pages × pageSize) before commit
	// so the caller sees real storage cost, not MapSize preallocation.
	if mtx, ok := t.tx.(interface {
		BucketSize(name string) (uint64, error)
	}); ok {
		if sz, err := mtx.BucketSize(t.Table); err == nil {
			t.lastBucketSize = sz
		}
	}
	if err := t.tx.Commit(); err != nil {
		return err
	}
	t.tx = nil
	return nil
}

// BucketSize reports the bytes actually used by the target bucket
// (leaf_pages + branch_pages + overflow_pages) × pageSize. Set by
// Commit; zero before Commit.
func (t *MDBXTarget) BucketSize() uint64 {
	return t.lastBucketSize
}

func (t *MDBXTarget) Close() error {
	if t.cursor != nil {
		t.cursor.Close()
		t.cursor = nil
	}
	if t.tx != nil {
		t.tx.Rollback()
		t.tx = nil
	}
	if t.db != nil {
		t.db.Close()
		t.db = nil
	}
	return nil
}

// AbsoluteOutPath returns the MDBX directory layout we use:
// <out>/<prefix>-mptcache/   contains mdbx.dat + mdbx.lck
func AbsoluteOutPath(outDir, prefix string) string {
	return filepath.Join(outDir, prefix+"-mptcache")
}

// MapTarget is an in-memory target for tests. Append checks ordering.
type MapTarget struct {
	Entries   []MapEntry
	StateRoot [32]byte
	LastKey   []byte
	began     bool
	committed bool
}

type MapEntry struct {
	Key, Value []byte
}

func (m *MapTarget) Begin() error {
	m.began = true
	m.committed = false
	m.Entries = m.Entries[:0]
	m.LastKey = nil
	return nil
}

func (m *MapTarget) Append(key, value []byte) error {
	if !m.began {
		return fmt.Errorf("MapTarget: Append before Begin")
	}
	if m.LastKey != nil {
		// Verify strictly ascending — same invariant MDBX AppendDup enforces.
		for i := 0; i < len(m.LastKey) && i < len(key); i++ {
			if m.LastKey[i] < key[i] {
				goto ok
			}
			if m.LastKey[i] > key[i] {
				return fmt.Errorf("MapTarget: key out of order: prev=%x cur=%x", m.LastKey, key)
			}
		}
		if len(m.LastKey) >= len(key) {
			return fmt.Errorf("MapTarget: duplicate or shorter key: prev=%x cur=%x", m.LastKey, key)
		}
	ok:
	}
	kc := make([]byte, len(key))
	copy(kc, key)
	vc := make([]byte, len(value))
	copy(vc, value)
	m.Entries = append(m.Entries, MapEntry{kc, vc})
	m.LastKey = kc
	return nil
}

func (m *MapTarget) Commit(stateRoot [32]byte) error {
	m.StateRoot = stateRoot
	m.committed = true
	return nil
}

func (m *MapTarget) Close() error { return nil }
