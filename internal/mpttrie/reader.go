// Package mpttrie provides read access to an MPT cache built by
// internal/mptbuild. It opens the MDBX directory written by Phase A,
// exposes point-lookup of branch nodes, and walks the trie path from
// root toward a target hashed key (collecting siblings for proof
// construction in Phase C).
//
// File layout (built by Phase A):
//
//	<dir>/mdbx.dat               MDBX database
//	  bucket AccountsTrie        OR StoragesTrie
//	    key:   nibble path (varlen, lex-sorted)
//	    value: BranchNodeCompact encoding (6B masks + hashes [+ optional rootHash])
//	  bucket Meta
//	    "state_root" → 32 byte root hash
//	    "built_at"   → RFC3339 build timestamp string
//
// Reader is safe for concurrent calls (each call opens a fresh RoTx);
// the underlying MDBX env is opened once at Open.
package mpttrie

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const metaTable = "Meta"

// Reader opens an MPT MDBX directory for read.
type Reader struct {
	db    kv.RoDB
	table string // AccountsTrie or StoragesTrie
}

// Open mounts the MDBX directory <dir>. The table name must match what
// Phase A built (typically "AccountsTrie" or "StoragesTrie").
func Open(dir, table string) (*Reader, error) {
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(64 * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[table] = kv.TableCfgItem{}
			d[metaTable] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		return nil, fmt.Errorf("mpttrie: open %s: %w", dir, err)
	}
	return &Reader{db: db, table: table}, nil
}

func (r *Reader) Close() error {
	if r.db != nil {
		r.db.Close()
		r.db = nil
	}
	return nil
}

// StateRoot returns the 32-byte state root recorded by the builder.
func (r *Reader) StateRoot() ([32]byte, error) {
	var out [32]byte
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	v, err := tx.GetOne(metaTable, []byte("state_root"))
	if err != nil {
		return out, err
	}
	if len(v) != 32 {
		return out, fmt.Errorf("mpttrie: state_root length=%d (want 32)", len(v))
	}
	copy(out[:], v)
	return out, nil
}

// BuiltAt returns the RFC3339 build timestamp recorded by the builder,
// or the zero time if not present.
func (r *Reader) BuiltAt() (time.Time, error) {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return time.Time{}, err
	}
	defer tx.Rollback()
	v, err := tx.GetOne(metaTable, []byte("built_at"))
	if err != nil || len(v) == 0 {
		return time.Time{}, err
	}
	return time.Parse(time.RFC3339, string(v))
}

// Stats reports counters for the trie bucket.
type Stats struct {
	BranchCount uint64
	BranchBytes uint64 // approximate: total leaf+branch+overflow pages × pageSize
	Root        [32]byte
	BuiltAt     time.Time
}

func (r *Reader) Stats() (Stats, error) {
	var s Stats
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return s, err
	}
	defer tx.Rollback()

	mtx, ok := tx.(*mdbxkv.MdbxTx)
	if !ok {
		return s, errors.New("mpttrie: tx does not support BucketStat")
	}
	st, err := mtx.BucketStat(r.table)
	if err != nil {
		return s, err
	}
	s.BranchCount = st.Entries
	s.BranchBytes = (st.LeafPages + st.BranchPages + st.OverflowPages) * 4096

	if v, err := tx.GetOne(metaTable, []byte("state_root")); err == nil && len(v) == 32 {
		copy(s.Root[:], v)
	}
	if v, err := tx.GetOne(metaTable, []byte("built_at")); err == nil && len(v) > 0 {
		s.BuiltAt, _ = time.Parse(time.RFC3339, string(v))
	}
	return s, nil
}

// GetBranchRaw returns the raw BranchNodeCompact bytes stored at the
// given nibble path. ok=false means no branch exists at that path.
func (r *Reader) GetBranchRaw(nibblePath []byte) (raw []byte, ok bool, err error) {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	v, err := tx.GetOne(r.table, nibblePath)
	if err != nil {
		return nil, false, err
	}
	if v == nil {
		return nil, false, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, true, nil
}

// BranchNode is the decoded form of a stored branch encoding.
type BranchNode struct {
	HasState uint16     // bitmap of children that exist (any reason)
	HasTree  uint16     // bitmap of children that are themselves deeper branches
	HasHash  uint16     // bitmap of children for which Hashes contains a stored hash
	Hashes   [][32]byte // one entry per bit set in HasHash, in nibble order 0..15
	RootHash [32]byte   // optional: present when HasState==1 (root) carries trie root hash
	HasRoot  bool
}

// GetBranch returns a decoded branch at the given nibble path.
func (r *Reader) GetBranch(nibblePath []byte) (*BranchNode, bool, error) {
	raw, ok, err := r.GetBranchRaw(nibblePath)
	if err != nil || !ok {
		return nil, ok, err
	}
	return DecodeBranch(raw)
}

// DecodeBranch parses the MarshalTrieNode wire format.
//
//	[2B hasState BE][2B hasTree BE][2B hasHash BE][optional 32B rootHash][N × 32B hashes]
//
// The optional rootHash is present iff len(payload after masks) ==
// (popcount(hasHash)+1) × 32.
func DecodeBranch(raw []byte) (*BranchNode, bool, error) {
	if len(raw) < 6 {
		return nil, false, fmt.Errorf("mpttrie: branch too short: %d bytes", len(raw))
	}
	bn := &BranchNode{
		HasState: binary.BigEndian.Uint16(raw[0:2]),
		HasTree:  binary.BigEndian.Uint16(raw[2:4]),
		HasHash:  binary.BigEndian.Uint16(raw[4:6]),
	}
	payload := raw[6:]
	expectedHashes := bits.OnesCount16(bn.HasHash)
	totalHashBytes := len(payload)
	if totalHashBytes%32 != 0 {
		return nil, false, fmt.Errorf("mpttrie: payload not multiple of 32: %d", totalHashBytes)
	}
	hashCount := totalHashBytes / 32
	if hashCount == expectedHashes+1 {
		bn.HasRoot = true
		copy(bn.RootHash[:], payload[:32])
		payload = payload[32:]
	} else if hashCount != expectedHashes {
		return nil, false, fmt.Errorf("mpttrie: hash count mismatch: have %d, expect %d (or %d with rootHash)",
			hashCount, expectedHashes, expectedHashes+1)
	}
	bn.Hashes = make([][32]byte, expectedHashes)
	for i := 0; i < expectedHashes; i++ {
		copy(bn.Hashes[i][:], payload[i*32:(i+1)*32])
	}
	return bn, true, nil
}

// ChildHash returns the hash for child nibble `n` (0..15), or
// (zero, false) if no hash is stored for that child.
func (b *BranchNode) ChildHash(n byte) ([32]byte, bool) {
	bit := uint16(1) << n
	if b.HasHash&bit == 0 {
		return [32]byte{}, false
	}
	// Count set bits in HasHash below position n to index into Hashes.
	idx := bits.OnesCount16(b.HasHash & (bit - 1))
	return b.Hashes[idx], true
}

// HasChild reports whether the branch claims a non-empty child at
// nibble n (state bit set).
func (b *BranchNode) HasChild(n byte) bool {
	return b.HasState&(uint16(1)<<n) != 0
}

// IsChildSubtree reports whether the child at n is itself a deeper
// branch node (i.e. another branch is stored at the extended path).
func (b *BranchNode) IsChildSubtree(n byte) bool {
	return b.HasTree&(uint16(1)<<n) != 0
}

// SiblingHashes returns the hashes of all children OTHER than nibble
// n. Order is by nibble index (0..15 skipping n). Useful for proof
// construction: caller folds these into proof bytes at the current
// level.
func (b *BranchNode) SiblingHashes(n byte) [][32]byte {
	out := make([][32]byte, 0, 15)
	for i := byte(0); i < 16; i++ {
		if i == n {
			continue
		}
		h, ok := b.ChildHash(i)
		if !ok {
			continue
		}
		out = append(out, h)
	}
	return out
}
