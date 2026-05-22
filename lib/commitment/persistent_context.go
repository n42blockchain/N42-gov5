package commitment

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/lib/kv"
)

// CommitmentBranchesTable is the MDBX table that stores BranchData
// keyed by nibble path. It's the persistent backing store for
// PersistentPatriciaContext.Branch / PutBranch.
//
// Schema:
//
//	key   = nibble path (one nibble per byte, 0..15)
//	value = BranchData ([touchMap u16 BE | afterMap u16 BE | cell data...])
//
// The table is intentionally declared by the caller (not in the
// global ChaindataTables list) so HA-3a can be developed and
// tested without touching the rest of the schema. Once HA-4 lands
// it can move into the canonical schema.
const CommitmentBranchesTable = "CommitmentBranches"

// AccountReader fetches the latest committed Account update for a
// 20-byte address plain key. Returning a nil *Update with no error
// signals "not present" (or "deleted"); HPH treats that as an
// absent leaf.
type AccountReader interface {
	Account(plainKey []byte) (*Update, error)
}

// StorageReader fetches the latest committed Storage update for a
// 52-byte plain key (address || slot). nil *Update = absent.
type StorageReader interface {
	Storage(plainKey []byte) (*Update, error)
}

// PersistentPatriciaContext implements lib/commitment.PatriciaContext
// by routing Branch/PutBranch through an MDBX RoTx/RwTx and
// Account/Storage through caller-supplied readers.
//
// Read-mode use: pass a kv.Tx via SetReadTx; PutBranch returns an
// error.
// Write-mode use: pass a kv.RwTx via SetWriteTx; Branch reads from
// the same tx (so writes within a transaction are visible to
// subsequent reads).
//
// txNum is the current transaction number (per erigon's
// PatriciaContext contract). Callers control it; HA-3a tests
// usually keep it at 0.
type PersistentPatriciaContext struct {
	readTx  kv.Tx
	writeTx kv.RwTx
	accts   AccountReader
	stors   StorageReader
	txNum   uint64

	// HA-3d compression. zstdLevel == 0 = no compression (raw
	// BranchData bytes stored as-is — compatible with HA-3a/b
	// snapshots). zstdLevel > 0 = zstd-compressed BranchData with a
	// 1-byte version prefix on every value:
	//
	//	prefix 0x00 = raw (no following bytes interpreted as zstd)
	//	prefix 0x01 = zstd-compressed (decompress remaining bytes)
	//
	// The prefix is REQUIRED to distinguish formats on read because
	// raw BranchData can start with any byte value (touchMap u16 BE).
	// An env can mix-and-match: rows written by an uncompressed
	// writer remain readable by a compressed reader (it sees
	// prefix 0x00 and returns the rest verbatim).
	zstdLevel  int
	zstdEnc    *zstd.Encoder
	zstdDec    *zstd.Decoder
	zstdInit   sync.Once
	zstdInitOK error
}

// CompressionPrefixRaw / CompressionPrefixZstd: the first byte of
// every CommitmentBranches value when a zstd-aware writer/reader is
// used. Pre-HA-3d data lacks a prefix entirely (legacy raw stream).
// To preserve readability of old snapshots, the writer always emits
// a prefix; the reader's hot path checks the first byte ONLY when
// CompressionEnabled (set via SetZstdLevel).
const (
	CompressionPrefixRaw  byte = 0x00
	CompressionPrefixZstd byte = 0x01
)

// NewPersistentPatriciaContext builds the context. Either readTx or
// writeTx (or both) must be set before use; the readers are
// required for any code path that calls Account/Storage.
func NewPersistentPatriciaContext(accts AccountReader, stors StorageReader) *PersistentPatriciaContext {
	return &PersistentPatriciaContext{accts: accts, stors: stors}
}

func (p *PersistentPatriciaContext) SetReadTx(tx kv.Tx) {
	p.readTx = tx
}

func (p *PersistentPatriciaContext) SetWriteTx(tx kv.RwTx) {
	p.writeTx = tx
	p.readTx = tx // RwTx satisfies Tx; reads after writes see the writes
}

func (p *PersistentPatriciaContext) SetTxNum(n uint64) {
	p.txNum = n
}

// SetZstdLevel enables zstd compression on persisted BranchData.
// level == 0 disables compression (default). level == 1..22 picks
// the corresponding zstd encoder level (we cap at zstd-3 in
// practice — higher levels rarely pay off on per-row BranchData
// payloads). PutBranch will then prefix each row with one byte
// indicating raw (0x00) or zstd (0x01); Branch reads the prefix
// and decompresses if needed.
//
// Compression-prefix bytes appear on the wire even when level == 0,
// to keep the on-disk format consistent. Snapshots written before
// HA-3d (pure raw BranchData, no prefix) MUST NOT be opened with a
// zstd-aware reader — the format is incompatible. Tests reset the
// dir between bootstrap runs to avoid this case.
func (p *PersistentPatriciaContext) SetZstdLevel(level int) error {
	if level < 0 {
		return fmt.Errorf("SetZstdLevel: negative level %d", level)
	}
	p.zstdLevel = level
	if level == 0 {
		return nil
	}
	p.zstdInit.Do(func() {
		enc, err := zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.EncoderLevel(level)),
			zstd.WithEncoderConcurrency(1))
		if err != nil {
			p.zstdInitOK = err
			return
		}
		dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
		if err != nil {
			enc.Close()
			p.zstdInitOK = err
			return
		}
		p.zstdEnc = enc
		p.zstdDec = dec
	})
	return p.zstdInitOK
}

// CompressionEnabled reports whether PutBranch/Branch are routed
// through the zstd codec.
func (p *PersistentPatriciaContext) CompressionEnabled() bool {
	return p.zstdLevel > 0 && p.zstdEnc != nil
}

func (p *PersistentPatriciaContext) TxNum() uint64 {
	return p.txNum
}

func (p *PersistentPatriciaContext) Branch(prefix []byte) ([]byte, kv.Step, error) {
	if p.readTx == nil {
		return nil, 0, errors.New("PersistentPatriciaContext: no readTx (call SetReadTx or SetWriteTx)")
	}
	v, err := p.readTx.GetOne(CommitmentBranchesTable, prefix)
	if err != nil {
		return nil, 0, err
	}
	if v == nil {
		return nil, 0, nil
	}
	if !p.CompressionEnabled() {
		// MDBX returns slices that alias the page until tx commit;
		// caller may keep the value past that, so copy.
		out := make([]byte, len(v))
		copy(out, v)
		return out, 0, nil
	}
	// Compression-aware path: read 1-byte prefix.
	if len(v) < 1 {
		return nil, 0, fmt.Errorf("Branch at %x: empty value", prefix)
	}
	switch v[0] {
	case CompressionPrefixRaw:
		out := make([]byte, len(v)-1)
		copy(out, v[1:])
		return out, 0, nil
	case CompressionPrefixZstd:
		out, err := p.zstdDec.DecodeAll(v[1:], nil)
		if err != nil {
			return nil, 0, fmt.Errorf("Branch at %x: zstd decode: %w", prefix, err)
		}
		return out, 0, nil
	default:
		return nil, 0, fmt.Errorf("Branch at %x: unknown compression prefix 0x%02x", prefix, v[0])
	}
}

func (p *PersistentPatriciaContext) PutBranch(prefix []byte, data []byte, _ []byte) error {
	if p.writeTx == nil {
		return errors.New("PersistentPatriciaContext: PutBranch needs a writeTx (call SetWriteTx)")
	}
	if !p.CompressionEnabled() {
		return p.writeTx.Put(CommitmentBranchesTable, prefix, data)
	}
	// Compression-aware path: try zstd; emit raw prefix if the
	// compressed payload is larger than data (i.e. negative gain
	// because BranchData is already small / high-entropy).
	compressed := p.zstdEnc.EncodeAll(data, nil)
	if len(compressed) >= len(data) {
		// Raw wins (or ties). Prefix 0x00 + verbatim data.
		buf := make([]byte, 1+len(data))
		buf[0] = CompressionPrefixRaw
		copy(buf[1:], data)
		return p.writeTx.Put(CommitmentBranchesTable, prefix, buf)
	}
	buf := make([]byte, 1+len(compressed))
	buf[0] = CompressionPrefixZstd
	copy(buf[1:], compressed)
	return p.writeTx.Put(CommitmentBranchesTable, prefix, buf)
}

func (p *PersistentPatriciaContext) Account(plainKey []byte) (*Update, error) {
	if p.accts == nil {
		// Mirror MockState's behaviour: an "empty" Update with the
		// DeleteUpdate flag signals "not present".
		u := new(Update)
		u.Flags = DeleteUpdate
		return u, nil
	}
	upd, err := p.accts.Account(plainKey)
	if err != nil {
		return nil, err
	}
	if upd == nil {
		u := new(Update)
		u.Flags = DeleteUpdate
		return u, nil
	}
	return upd, nil
}

func (p *PersistentPatriciaContext) Storage(plainKey []byte) (*Update, error) {
	if p.stors == nil {
		u := new(Update)
		u.Flags = DeleteUpdate
		return u, nil
	}
	upd, err := p.stors.Storage(plainKey)
	if err != nil {
		return nil, err
	}
	if upd == nil {
		u := new(Update)
		u.Flags = DeleteUpdate
		return u, nil
	}
	return upd, nil
}

// CommitmentBranchesSize returns the entry count and approximate
// data bytes in the CommitmentBranches table. Useful for HA-3b
// bootstrap progress reporting and HA-4 size verification.
func CommitmentBranchesSize(ctx context.Context, tx kv.Tx) (entries uint64, valueBytes uint64, err error) {
	c, err := tx.Cursor(CommitmentBranchesTable)
	if err != nil {
		return 0, 0, err
	}
	defer c.Close()
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			return 0, 0, err
		}
		entries++
		valueBytes += uint64(len(v))
		if entries%1_000_000 == 0 {
			select {
			case <-ctx.Done():
				return entries, valueBytes, ctx.Err()
			default:
			}
		}
	}
	return entries, valueBytes, nil
}
