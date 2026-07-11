// Package snapshotreader reads eth-el snapshot segments (the RecSplit MPHF +
// Elias-Fano + zstd format written by cmd/reth-snapshot-export) so a
// minimal/full node can serve PlainState account/storage values directly from
// the snapshot at fixed height H0, with no leaves-journal RebuildState.
//
// On-disk format (per table = accounts | storage), authority cmd/reth-snapshot-export:
//
//	<prefix>.idx       RecSplit MPHF: key -> ordinal
//	                   accounts key = 20B address; storage key = 20B addr || 32B slot
//	<prefix>.ef        Elias-Fano: ordinal -> byte offset into .val
//	<prefix>.val[.zst] entries [1B len][len B payload]; payload = [4B fp BE][value]
//	                   fp = uint32(xxhash.Sum64(key)) — phantom-key guard
//	                   accounts value = V2-compact account, trailing 32B codeHash
//	                     rewritten to a 3B big-endian codedict id for contracts
//	                   storage value  = trimmed U256 (big-endian, no leading zeros)
//	accounts.codedict  [4B LE count][32B codeHash]*count (sorted) — id = position
//
// Reader is read-only and concurrency-safe for lookups (RecSplit IndexReader is
// stateless per call; the mmapped .val and EF are read-only).
package snapshotreader

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"os"
	"path/filepath"

	"github.com/cespare/xxhash/v2"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/mmap"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

// table holds one NoValues RecSplit MPHF + a slot→offset Elias-Fano + .val
// bytes for a single snapshot shard. The layout is identical for the v1
// monolithic segment and each v2 shard: Lookup(key) returns a perfect-hash
// slot, ef.Get(slot) gives the byte offset into the slot-ordered .val. ef is
// nil only for an empty shard (KeyCount()==0), which the lookup guard rejects
// before touching it.
//
// The .ef and .val are mmapped read-only (like the .idx): mainnet .val is
// ~30 GB, and a heap copy would be private (pagefile-backed) memory, whereas
// file-backed mappings let the OS evict cold pages and re-fault them from the
// snapshot file itself.
type table struct {
	idx *recsplit.Index
	rd  *recsplit.IndexReader
	ef  *eliasfano32.EliasFano // nil only for an empty shard
	val []byte

	// mmap bookkeeping; nil handles mean the buffer is heap-owned (zstd
	// fallback, N42_SNAP_MMAP=0, or tests) and needs no munmap. efRaw is the
	// original .ef mapping slice (ef itself keeps interior references).
	valF  *os.File
	valM2 *[mmap.MaxMapSize]byte
	efRaw []byte
	efF   *os.File
	efM2  *[mmap.MaxMapSize]byte
}

func openTable(idxPath, efPath, valPath string) (*table, error) {
	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		return nil, fmt.Errorf("open idx %s: %w", idxPath, err)
	}
	efData, efF, efM2, err := mapOrRead(efPath)
	if err != nil {
		idx.Close()
		return nil, fmt.Errorf("read ef %s: %w", efPath, err)
	}
	// An empty shard (.ef is 0 bytes) has KeyCount()==0; leave ef nil — the
	// lookup guard returns before it is ever dereferenced.
	var ef *eliasfano32.EliasFano
	if len(efData) >= 16 {
		ef, _ = eliasfano32.ReadEliasFano(efData)
	}
	val, valF, valM2, err := readMaybeZstd(valPath)
	if err != nil {
		idx.Close()
		closeMapping(efData, efF, efM2)
		return nil, fmt.Errorf("read val %s: %w", valPath, err)
	}
	return &table{
		idx: idx, rd: recsplit.NewIndexReader(idx), ef: ef, val: val,
		valF: valF, valM2: valM2, efRaw: efData, efF: efF, efM2: efM2,
	}, nil
}

// mapOrRead returns the file's bytes as a read-only mmap (file-backed pages —
// no heap, no pagefile) with its keep-alive handles, falling back to a heap
// read when mmap is disabled (N42_SNAP_MMAP=0) or fails.
func mapOrRead(path string) (data []byte, f *os.File, m2 *[mmap.MaxMapSize]byte, err error) {
	if os.Getenv("N42_SNAP_MMAP") != "0" {
		if f, err = os.Open(path); err == nil {
			if st, serr := f.Stat(); serr == nil && st.Size() > 0 {
				if h1, h2, merr := mmap.Mmap(f, int(st.Size())); merr == nil {
					return h1[:st.Size()], f, h2, nil
				}
			}
			f.Close()
		}
	}
	data, err = os.ReadFile(path)
	return data, nil, nil, err
}

// closeMapping releases one mapOrRead result (no-op for heap buffers).
func closeMapping(data []byte, f *os.File, m2 *[mmap.MaxMapSize]byte) {
	if m2 != nil {
		_ = mmap.Munmap(data, m2)
	}
	if f != nil {
		f.Close()
	}
}

// lookupWithHash returns the value (payload after the 4B fingerprint) for key,
// verifying the fingerprint to reject phantom keys (MPHF returns an ordinal
// for ANY key; the fp confirms the key was actually present). keyHash must be
// xxhash.Sum64(key) — passed in so a sharded segment hashes each key once for
// both shard routing and the fingerprint. (nil,false) if absent.
func (t *table) lookupWithHash(key []byte, keyHash uint64) ([]byte, bool) {
	if t.idx.KeyCount() == 0 {
		return nil, false
	}
	ord, found := t.rd.Lookup(key)
	if !found {
		return nil, false
	}
	if ord >= t.idx.KeyCount() {
		return nil, false
	}
	if t.ef == nil {
		return nil, false
	}
	off := t.ef.Get(ord) // slot→offset EF (external .ef, v1 and v2b)
	if off >= uint64(len(t.val)) {
		return nil, false
	}
	n := uint64(t.val[off])
	start := off + 1
	if start+n > uint64(len(t.val)) {
		return nil, false
	}
	payload := t.val[start : start+n]
	if len(payload) < 4 {
		return nil, false
	}
	if binary.BigEndian.Uint32(payload[:4]) != uint32(keyHash) {
		return nil, false // phantom: key not actually in this segment
	}
	return payload[4:], true
}

func (t *table) Close() {
	if t == nil {
		return
	}
	if t.idx != nil {
		t.idx.Close()
	}
	closeMapping(t.val, t.valF, t.valM2)
	closeMapping(t.efRaw, t.efF, t.efM2)
}

// readMaybeZstd mmaps valPath (see mapOrRead). If valPath is absent but
// valPath+".zst" exists, the archive is first decompressed to valPath on disk
// (streaming — never buffered on the heap: a whole-file DecodeAll of the
// multi-GB storage .val once exhausted the machine's commit charge and took
// down every collocated node) and the result is mmapped like any other .val.
// A pre-existing .val always wins over a stale sibling .zst.
func readMaybeZstd(valPath string) ([]byte, *os.File, *[mmap.MaxMapSize]byte, error) {
	if _, err := os.Stat(valPath); os.IsNotExist(err) {
		if _, zerr := os.Stat(valPath + ".zst"); zerr == nil {
			if derr := decompressZstToFile(valPath+".zst", valPath); derr != nil {
				return nil, nil, nil, fmt.Errorf("decompress %s.zst: %w", valPath, derr)
			}
		}
	}
	return mapOrRead(valPath)
}

// decompressZstToFile streams zstPath into dstPath via a temp file + rename so
// a crash mid-decompress never leaves a truncated .val behind.
func decompressZstToFile(zstPath, dstPath string) error {
	in, err := os.Open(zstPath)
	if err != nil {
		return err
	}
	defer in.Close()
	dec, err := zstd.NewReader(in)
	if err != nil {
		return err
	}
	defer dec.Close()
	tmp := dstPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err = io.Copy(out, dec.IOReadCloser()); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err = out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dstPath)
}

// shardedTable routes lookups across a table's hash shards. v1 monolithic
// segments are a single shard with shift=64 (every hash routes to shard 0);
// v2 segments route by the top log2(n) bits of xxhash64(key), matching the
// writer in cmd/reth-snapshot-export.
type shardedTable struct {
	shards []*table
	shift  uint // 64 - log2(len(shards))
}

func (st *shardedTable) lookup(key []byte) ([]byte, bool) {
	h := xxhash.Sum64(key)
	return st.shards[h>>st.shift].lookupWithHash(key, h)
}

func (st *shardedTable) Close() {
	if st == nil {
		return
	}
	for _, t := range st.shards {
		t.Close()
	}
}

// Segment is one snapshot segment: accounts + storage tables + the codeHash dict.
type Segment struct {
	acc      *shardedTable
	sto      *shardedTable
	codeDict [][32]byte // id -> codeHash
}

// OpenSegment opens one snapshot segment from dir. Two layouts are detected
// per table prefix (typical prefixes: "accounts.0-25999999" / plain "accounts"):
//
//   - v2 sharded: <prefix>.s00..sNN.{idx,ef,val[.zst]} — one power-of-two set
//     of shards, each in the same layout as v1;
//   - v1 monolithic: <prefix>.{idx,ef,val[.zst]}.
func OpenSegment(dir, accPrefix, stoPrefix string) (*Segment, error) {
	acc, err := openShardedTable(dir, accPrefix)
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}
	sto, err := openShardedTable(dir, stoPrefix)
	if err != nil {
		acc.Close()
		return nil, fmt.Errorf("storage: %w", err)
	}
	cd, err := loadCodeDict(dir + "/" + accPrefix + ".codedict")
	if err != nil {
		acc.Close()
		sto.Close()
		return nil, fmt.Errorf("codedict: %w", err)
	}
	return &Segment{acc: acc, sto: sto, codeDict: cd}, nil
}

// openShardedTable opens <dir>/<prefix> in whichever layout is present,
// preferring v2 shards when both exist.
func openShardedTable(dir, prefix string) (*shardedTable, error) {
	shardIdxs, err := filepath.Glob(dir + "/" + prefix + ".s*.idx")
	if err != nil {
		return nil, err
	}
	if len(shardIdxs) == 0 {
		t, err := openTable(
			dir+"/"+prefix+".idx", dir+"/"+prefix+".ef", dir+"/"+prefix+".val")
		if err != nil {
			return nil, err
		}
		return &shardedTable{shards: []*table{t}, shift: 64}, nil
	}
	n := len(shardIdxs)
	if bits.OnesCount(uint(n)) != 1 {
		return nil, fmt.Errorf("%s: found %d shard .idx files, want a power of two", prefix, n)
	}
	st := &shardedTable{
		shards: make([]*table, n),
		shift:  uint(64 - bits.TrailingZeros(uint(n))),
	}
	for s := 0; s < n; s++ {
		base := fmt.Sprintf("%s/%s.s%02d", dir, prefix, s)
		t, terr := openTable(base+".idx", base+".ef", base+".val")
		if terr != nil {
			st.Close()
			return nil, fmt.Errorf("shard %02d: %w", s, terr)
		}
		st.shards[s] = t
	}
	return st, nil
}

func loadCodeDict(path string) ([][32]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 4 {
		return nil, fmt.Errorf("codedict too short (%d B)", len(data))
	}
	count := binary.LittleEndian.Uint32(data[:4])
	if 4+uint64(count)*32 > uint64(len(data)) {
		return nil, fmt.Errorf("codedict truncated: count=%d len=%d", count, len(data))
	}
	dict := make([][32]byte, count)
	for i := uint32(0); i < count; i++ {
		copy(dict[i][:], data[4+uint64(i)*32:4+uint64(i)*32+32])
	}
	return dict, nil
}

// AccountValueRaw returns the snapshot-stored account value for addr: a
// V2-compact account whose trailing 32B codeHash (if a contract) has been
// rewritten to a 3-byte big-endian codedict id. Use DecodeAccount to obtain a
// StateAccount with the real codeHash restored. (nil,false) if not present.
func (s *Segment) AccountValueRaw(addr [20]byte) ([]byte, bool) {
	return s.acc.lookup(addr[:])
}

// DecodeAccount decodes a raw snapshot account value into a StateAccount.
// The snapshot stores V2-compact encoding — [fieldBits][nonce][balance][codeHash]
// — but with the trailing 32B codeHash (bit3) rewritten to a 3-byte big-endian
// codedict id for contracts. This restores the real 32B codeHash via the dict.
// (We can't use account.DecodeForStorageV2 directly: it expects a 32B codeHash.)
func (s *Segment) DecodeAccount(raw []byte) (*account.StateAccount, error) {
	a := &account.StateAccount{}
	a.Reset()
	if len(raw) == 0 {
		return a, nil
	}
	fieldBits := raw[0]
	pos := 1
	if fieldBits&1 != 0 { // nonce
		v, n := binary.Uvarint(raw[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("snapshot acct: bad nonce varint")
		}
		a.Nonce = v
		pos += n
	}
	if fieldBits&2 != 0 { // balance
		if pos >= len(raw) {
			return nil, fmt.Errorf("snapshot acct: truncated balance length")
		}
		balLen := int(raw[pos])
		pos++
		if balLen > 32 || pos+balLen > len(raw) {
			return nil, fmt.Errorf("snapshot acct: truncated balance data")
		}
		var bb [32]byte
		copy(bb[32-balLen:], raw[pos:pos+balLen])
		a.Balance.SetBytes32(bb[:])
		pos += balLen
	}
	if fieldBits&4 != 0 { // legacy incarnation — skip the varint
		_, n := binary.Uvarint(raw[pos:])
		if n <= 0 {
			return nil, fmt.Errorf("snapshot acct: bad incarnation varint")
		}
		pos += n
	}
	if fieldBits&8 != 0 {
		// Snapshot stores a 3-byte codedict id here, NOT the 32B codeHash.
		if pos+3 > len(raw) {
			return nil, fmt.Errorf("snapshot acct: truncated codeHash id")
		}
		id := uint32(raw[pos])<<16 | uint32(raw[pos+1])<<8 | uint32(raw[pos+2])
		pos += 3
		ch, ok := s.CodeHashByID(id)
		if !ok {
			return nil, fmt.Errorf("snapshot acct: codeHash id %d out of range (dict size %d)", id, len(s.codeDict))
		}
		a.CodeHash = types.BytesToHash(ch[:])
	} else {
		a.CodeHash = emptyCodeHash
	}
	a.Initialised = true
	return a, nil
}

// StorageValue returns the trimmed big-endian U256 value bytes for addr/slot,
// or (nil,false) if not present (== zero slot, which snapshots omit).
func (s *Segment) StorageValue(addr [20]byte, slot [32]byte) ([]byte, bool) {
	var key [52]byte
	copy(key[:20], addr[:])
	copy(key[20:], slot[:])
	return s.sto.lookup(key[:])
}

// CodeHashByID resolves a 3-byte codedict id (as embedded in a rewritten
// account value) to its 32-byte codeHash.
func (s *Segment) CodeHashByID(id uint32) ([32]byte, bool) {
	if uint64(id) >= uint64(len(s.codeDict)) {
		return [32]byte{}, false
	}
	return s.codeDict[id], true
}

// CodeDictLen returns the number of codeHash dict entries (for diagnostics).
func (s *Segment) CodeDictLen() int { return len(s.codeDict) }

func (s *Segment) Close() {
	if s == nil {
		return
	}
	s.acc.Close()
	s.sto.Close()
}
