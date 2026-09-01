// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package ancientera

import (
	"container/list"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// RangeState is the availability of one (class, era) range.
type RangeState int

const (
	// RangeAvailable — sealed file present and healthy.
	RangeAvailable RangeState = iota
	// RangePruned — manifest says pruned, or an optional-class file is
	// missing/damaged (degradation: treated exactly like pruned).
	RangePruned
	// RangeNotSealed — beyond the sealed horizon (still in hot MDBX).
	RangeNotSealed
)

// ErrPruned is returned for reads into pruned/degraded ranges.
var ErrPruned = errors.New("ancientera: range pruned")

// Store is the read-only view over an era directory. Nodes never write
// through it — sealing happens offline (replay/seal tool).
type Store struct {
	dir      string
	manifest *Manifest
	span     uint64

	mu      sync.Mutex
	readers map[string]*Reader // key: class-era
	bad     map[string]string  // quarantined: key → reason
	cache   *frameCache

	sealedEnd uint64 // first block NOT covered by any sealed chain era
}

// StoreHealth summarizes boot verification for logging.
type StoreHealth struct {
	Sealed    int
	Pruned    int
	Degraded  []string // "class-era: reason" for unexpected missing/corrupt
	ChainGaps []string // class A problems — CRITICAL
	SealedEnd uint64
}

// OpenStore opens dir, loads (or rebuilds) the manifest, and runs the
// light check on every sealed entry: file exists, size matches, footer
// parses, payload hash agrees with the manifest, class A eras chain.
// Optional-class failures degrade to pruned; class A failures are
// reported in Health.ChainGaps but the store still opens (the node keeps
// consensus alive on hot data and refuses to serve the damaged range).
func OpenStore(dir string) (*Store, *StoreHealth, error) {
	m, err := LoadManifest(dir)
	if err != nil {
		if !os.IsNotExist(err) && !isManifestParseErr(err) {
			return nil, nil, err
		}
		// Footers are the root of trust; a lost or corrupt manifest is
		// rebuilt from them (pruned markers are lost — those ranges will
		// surface as "unexpectedly missing" warnings).
		m, err = RebuildManifest(dir)
		if err != nil {
			return nil, nil, err
		}
		if len(m.Entries) > 0 {
			if _, err := m.Save(dir); err != nil {
				return nil, nil, err
			}
		}
	}
	s := &Store{
		dir: dir, manifest: m, span: m.Span,
		readers: make(map[string]*Reader),
		bad:     make(map[string]string),
		cache:   newFrameCache(32),
	}
	h := &StoreHealth{}
	var lastChainHash string
	for i := range m.Entries {
		e := &m.Entries[i]
		key := e.Class + "-" + fmt.Sprint(e.Era)
		if e.Status == StatusPruned {
			h.Pruned++
			continue
		}
		path := filepath.Join(dir, e.File)
		st, err := os.Stat(path)
		reason := ""
		var r *Reader
		switch {
		case err != nil:
			reason = "missing"
		case st.Size() != e.Size:
			reason = fmt.Sprintf("size %d != manifest %d", st.Size(), e.Size)
		default:
			r, err = OpenReader(path)
			if err != nil {
				reason = err.Error()
			} else if r.Meta.PayloadBlake3 != e.PayloadBlake3 {
				reason = "payload hash disagrees with manifest"
				r.Close()
				r = nil
			}
		}
		if e.Class == ClassChain.String() {
			// Seal geometry comes from the manifest (anchored at seal
			// time), independent of file health: a damaged era must not
			// shrink the horizon and misroute later reads to hot MDBX.
			if end := (e.Era + 1) * m.Span; end > s.sealedEnd {
				s.sealedEnd = end
			}
		}
		if reason != "" {
			s.bad[key] = reason
			if e.Class == ClassChain.String() {
				h.ChainGaps = append(h.ChainGaps, key+": "+reason)
				// Bridge the linkage over the damaged era using the
				// manifest's seal-time hashes so healthy successors are
				// not falsely quarantined.
				lastChainHash = e.LastHash
			} else {
				h.Degraded = append(h.Degraded, key+": "+reason)
			}
			continue
		}
		if e.Class == ClassChain.String() {
			if lastChainHash != "" && r.Meta.ParentHash != lastChainHash {
				s.bad[key] = "era chain linkage broken"
				h.ChainGaps = append(h.ChainGaps, key+": parent hash does not link to previous era")
				r.Close()
				lastChainHash = e.LastHash
				continue
			}
			lastChainHash = r.Meta.LastHash
		}
		s.readers[key] = r
		h.Sealed++
	}
	h.SealedEnd = s.sealedEnd
	return s, h, nil
}

// Dir returns the store directory.
func (s *Store) Dir() string { return s.dir }

// Span returns blocks per era.
func (s *Store) Span() uint64 { return s.span }

// SealedEnd returns the first block not covered by sealed chain eras
// (0 when the store is empty).
func (s *Store) SealedEnd() uint64 { return s.sealedEnd }

// Manifest returns the loaded manifest (read-only use).
func (s *Store) Manifest() *Manifest { return s.manifest }

// State reports availability of block num in the given class.
func (s *Store) State(class Class, num uint64) RangeState {
	if s.span == 0 || num >= s.sealedEnd {
		return RangeNotSealed
	}
	era := num / s.span
	key := class.String() + "-" + fmt.Sprint(era)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.readers[key]; ok {
		return RangeAvailable
	}
	return RangePruned
}

// reader returns the healthy reader for (class, era) or nil.
func (s *Store) reader(class Class, era uint64) *Reader {
	key := class.String() + "-" + fmt.Sprint(era)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readers[key]
}

// quarantine marks a reader bad after a read-time integrity failure.
func (s *Store) quarantine(class Class, era uint64, reason string) {
	key := class.String() + "-" + fmt.Sprint(era)
	s.mu.Lock()
	defer s.mu.Unlock()
	if r, ok := s.readers[key]; ok {
		r.Close()
		delete(s.readers, key)
	}
	s.bad[key] = reason
}

// Quarantined returns the current quarantine map (copy).
func (s *Store) Quarantined() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.bad))
	for k, v := range s.bad {
		out[k] = v
	}
	return out
}

// Block returns the raw record for a block in a class, via the frame
// cache. Returns ErrPruned for pruned/degraded/missing ranges and
// quarantines files that fail read-time checks.
func (s *Store) Block(class Class, num uint64) (BlockEntry, error) {
	if s.span == 0 || num >= s.sealedEnd {
		return nil, fmt.Errorf("ancientera: block %d not sealed", num)
	}
	era := num / s.span
	r := s.reader(class, era)
	if r == nil {
		return nil, ErrPruned
	}
	frame := r.frameForBlock(num)
	ck := cacheKey{class: class, era: era, frame: frame}
	raw, ok := s.cache.get(ck)
	if !ok {
		var err error
		raw, err = r.ReadFrame(frame)
		if err != nil {
			if errors.Is(err, ErrIntegrity) {
				// Content damage: quarantine, degrade to pruned.
				s.quarantine(class, era, err.Error())
				return nil, fmt.Errorf("%w (quarantined: %v)", ErrPruned, err)
			}
			// Transient I/O failure: plain error, retry on next read.
			return nil, err
		}
		s.cache.put(ck, raw)
	}
	return r.entryFromFrame(raw, num)
}

// Chain returns (canonicalHash, headerRaw, evidenceRaw) for a block.
func (s *Store) Chain(num uint64) (hash [32]byte, headerRaw, evidenceRaw []byte, err error) {
	e, err := s.Block(ClassChain, num)
	if err != nil {
		return hash, nil, nil, err
	}
	if h := e[TblCanonicalHash]; len(h) == 32 {
		copy(hash[:], h)
	} else {
		return hash, nil, nil, errors.New("ancientera: chain record missing canonical hash")
	}
	return hash, e[TblHeader], e[TblEvidence], nil
}

// ExecRecord is the decoded class B payload for one block.
type ExecRecord struct {
	BodyRaw     []byte   // body storage value (BaseTxId + TxAmount)
	Txs         [][]byte // raw transaction bytes, in order
	ReceiptsRaw []byte   // whole-block receipts value (nil if none)
	Logs        []LogRec // per-transaction logs
}

// LogRec is one transaction's logs entry.
type LogRec struct {
	TxID uint32
	Data []byte
}

// Exec returns the execution record for a block.
func (s *Store) Exec(num uint64) (*ExecRecord, error) {
	e, err := s.Block(ClassExec, num)
	if err != nil {
		return nil, err
	}
	rec := &ExecRecord{BodyRaw: e[TblBody], ReceiptsRaw: e[TblReceipts]}
	if rec.Txs, err = splitU32List(e[TblTxs]); err != nil {
		return nil, err
	}
	rec.Logs, err = splitLogs(e[TblLogs])
	return rec, err
}

// AuxRecord is the decoded class C payload for one block.
type AuxRecord struct {
	Witness []byte
	AcctCS  [][]byte // AccountChangeSet dup values (key = blockNum)
	StorCS  []KVPair // StorageChangeSet dup values: addr+slot key part + value
}

// KVPair carries a table row whose key extends beyond the block number:
// Suffix is the key part after the 8-byte block prefix.
type KVPair struct {
	Suffix []byte
	Value  []byte
}

// Aux returns the auxiliary record for a block.
func (s *Store) Aux(num uint64) (*AuxRecord, error) {
	e, err := s.Block(ClassAux, num)
	if err != nil {
		return nil, err
	}
	rec := &AuxRecord{Witness: e[TblWitness]}
	if rec.AcctCS, err = splitU32List(e[TblAcctCS]); err != nil {
		return nil, err
	}
	rec.StorCS, err = splitKVPairs(e[TblStorCS])
	return rec, err
}

// EncodeKVPairs packs rows as u32 suffixLen | suffix | u32 valLen | value.
func EncodeKVPairs(pairs []KVPair) []byte {
	if len(pairs) == 0 {
		return nil
	}
	n := 0
	for _, p := range pairs {
		n += 8 + len(p.Suffix) + len(p.Value)
	}
	out := make([]byte, 0, n)
	var b4 [4]byte
	for _, p := range pairs {
		putU32(b4[:], uint32(len(p.Suffix)))
		out = append(out, b4[:]...)
		out = append(out, p.Suffix...)
		putU32(b4[:], uint32(len(p.Value)))
		out = append(out, b4[:]...)
		out = append(out, p.Value...)
	}
	return out
}

func splitKVPairs(b []byte) ([]KVPair, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out []KVPair
	for off := 0; off < len(b); {
		if off+4 > len(b) {
			return nil, errors.New("ancientera: truncated kv pair list")
		}
		sl := int(beU32(b[off:]))
		off += 4
		if off+sl+4 > len(b) {
			return nil, errors.New("ancientera: kv suffix exceeds buffer")
		}
		suffix := b[off : off+sl]
		off += sl
		vl := int(beU32(b[off:]))
		off += 4
		if off+vl > len(b) {
			return nil, errors.New("ancientera: kv value exceeds buffer")
		}
		out = append(out, KVPair{Suffix: suffix, Value: b[off : off+vl]})
		off += vl
	}
	return out, nil
}

// EarliestExecBlock returns the first block whose execution history is
// still available: 0 when every sealed exec era is healthy, otherwise
// one past the last pruned/degraded exec era. (Blocks beyond the sealed
// horizon live in hot MDBX and are always available.)
func (s *Store) EarliestExecBlock() uint64 {
	if s.span == 0 {
		return 0
	}
	var earliest uint64
	for start := uint64(0); start < s.sealedEnd; start += s.span {
		if s.State(ClassExec, start) != RangeAvailable {
			earliest = start + s.span
		}
	}
	return earliest
}

// Close closes all readers.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.readers {
		r.Close()
	}
	s.readers = map[string]*Reader{}
}

// EncodeU32List packs a [][]byte as concat of u32-len-prefixed items.
func EncodeU32List(items [][]byte) []byte {
	if len(items) == 0 {
		return nil
	}
	n := 0
	for _, it := range items {
		n += 4 + len(it)
	}
	out := make([]byte, 0, n)
	var b4 [4]byte
	for _, it := range items {
		putU32(b4[:], uint32(len(it)))
		out = append(out, b4[:]...)
		out = append(out, it...)
	}
	return out
}

func splitU32List(b []byte) ([][]byte, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out [][]byte
	for off := 0; off < len(b); {
		if off+4 > len(b) {
			return nil, errors.New("ancientera: truncated u32 list")
		}
		l := int(beU32(b[off:]))
		off += 4
		if off+l > len(b) {
			return nil, errors.New("ancientera: u32 list item exceeds buffer")
		}
		out = append(out, b[off:off+l])
		off += l
	}
	return out, nil
}

// EncodeLogs packs per-tx logs as concat of u32 txId | u32 len | data.
func EncodeLogs(logs []LogRec) []byte {
	if len(logs) == 0 {
		return nil
	}
	n := 0
	for _, l := range logs {
		n += 8 + len(l.Data)
	}
	out := make([]byte, 0, n)
	var b4 [4]byte
	for _, l := range logs {
		putU32(b4[:], l.TxID)
		out = append(out, b4[:]...)
		putU32(b4[:], uint32(len(l.Data)))
		out = append(out, b4[:]...)
		out = append(out, l.Data...)
	}
	return out
}

func splitLogs(b []byte) ([]LogRec, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var out []LogRec
	for off := 0; off < len(b); {
		if off+8 > len(b) {
			return nil, errors.New("ancientera: truncated logs list")
		}
		txID := beU32(b[off:])
		l := int(beU32(b[off+4:]))
		off += 8
		if off+l > len(b) {
			return nil, errors.New("ancientera: logs item exceeds buffer")
		}
		out = append(out, LogRec{TxID: txID, Data: b[off : off+l]})
		off += l
	}
	return out, nil
}

func beU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// --- frame LRU cache ---

type cacheKey struct {
	class Class
	era   uint64
	frame int
}

type frameCache struct {
	mu    sync.Mutex
	max   int
	ll    *list.List
	items map[cacheKey]*list.Element
}

type cacheItem struct {
	key cacheKey
	raw []byte
}

func newFrameCache(max int) *frameCache {
	return &frameCache{max: max, ll: list.New(), items: make(map[cacheKey]*list.Element)}
}

func (c *frameCache) get(k cacheKey) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*cacheItem).raw, true
	}
	return nil, false
}

func (c *frameCache) put(k cacheKey, raw []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.items[k]; ok {
		return
	}
	c.items[k] = c.ll.PushFront(&cacheItem{key: k, raw: raw})
	for c.ll.Len() > c.max {
		el := c.ll.Back()
		c.ll.Remove(el)
		delete(c.items, el.Value.(*cacheItem).key)
	}
}

// IsPruned reports whether err is (or wraps) ErrPruned.
func IsPruned(err error) bool { return errors.Is(err, ErrPruned) }

// StoreDirName is the conventional era directory name inside a node
// datadir.
const StoreDirName = "ancient-era"

// DefaultDir returns <datadir>/ancient-era.
func DefaultDir(datadir string) string { return filepath.Join(datadir, StoreDirName) }

// Exists reports whether dir looks like an era store (manifest or any
// .era file present).
func Exists(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ManifestName)); err == nil {
		return true
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, de := range des {
		if strings.HasSuffix(de.Name(), FileExt) {
			return true
		}
	}
	return false
}
