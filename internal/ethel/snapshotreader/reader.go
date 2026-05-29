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
	"os"

	"github.com/cespare/xxhash/v2"
	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/recsplit"
	"github.com/n42blockchain/N42/lib/recsplit/eliasfano32"
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

// table holds one RecSplit MPHF + EF offsets + decompressed .val for a single
// snapshot table (accounts or storage).
type table struct {
	idx *recsplit.Index
	rd  *recsplit.IndexReader
	ef  *eliasfano32.EliasFano
	val []byte
}

func openTable(idxPath, efPath, valPath string) (*table, error) {
	idx, err := recsplit.OpenIndex(idxPath)
	if err != nil {
		return nil, fmt.Errorf("open idx %s: %w", idxPath, err)
	}
	efData, err := os.ReadFile(efPath)
	if err != nil {
		idx.Close()
		return nil, fmt.Errorf("read ef %s: %w", efPath, err)
	}
	ef, _ := eliasfano32.ReadEliasFano(efData)
	val, err := readMaybeZstd(valPath)
	if err != nil {
		idx.Close()
		return nil, fmt.Errorf("read val %s: %w", valPath, err)
	}
	return &table{idx: idx, rd: recsplit.NewIndexReader(idx), ef: ef, val: val}, nil
}

// lookup returns the value (payload after the 4B fingerprint) for key, verifying
// the fingerprint to reject phantom keys (MPHF returns an ordinal for ANY key;
// the fp confirms the key was actually present). (nil,false) if absent.
func (t *table) lookup(key []byte) ([]byte, bool) {
	ord, found := t.rd.Lookup(key)
	if !found {
		return nil, false
	}
	if ord >= t.idx.KeyCount() {
		return nil, false
	}
	off := t.ef.Get(ord)
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
	if binary.BigEndian.Uint32(payload[:4]) != uint32(xxhash.Sum64(key)) {
		return nil, false // phantom: key not actually in this segment
	}
	return payload[4:], true
}

func (t *table) Close() {
	if t != nil && t.idx != nil {
		t.idx.Close()
	}
}

// readMaybeZstd reads valPath+".zst" (zstd-decompressed) if present, else valPath.
func readMaybeZstd(valPath string) ([]byte, error) {
	if data, err := os.ReadFile(valPath + ".zst"); err == nil {
		dec, derr := zstd.NewReader(nil)
		if derr != nil {
			return nil, derr
		}
		defer dec.Close()
		return dec.DecodeAll(data, nil)
	}
	return os.ReadFile(valPath)
}

// Segment is one snapshot segment: accounts + storage tables + the codeHash dict.
type Segment struct {
	acc      *table
	sto      *table
	codeDict [][32]byte // id -> codeHash
}

// OpenSegment opens <dir>/<accPrefix>.{idx,ef,val[.zst],codedict} and
// <dir>/<stoPrefix>.{idx,ef,val[.zst]}. Typical prefixes: "accounts.0-25999999"
// / "storage.0-25999999" (H.3 segment naming) or plain "accounts"/"storage".
func OpenSegment(dir, accPrefix, stoPrefix string) (*Segment, error) {
	acc, err := openTable(
		dir+"/"+accPrefix+".idx", dir+"/"+accPrefix+".ef", dir+"/"+accPrefix+".val")
	if err != nil {
		return nil, fmt.Errorf("accounts: %w", err)
	}
	sto, err := openTable(
		dir+"/"+stoPrefix+".idx", dir+"/"+stoPrefix+".ef", dir+"/"+stoPrefix+".val")
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
