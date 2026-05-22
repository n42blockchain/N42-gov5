package commitment

import (
	"context"
	"errors"

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
}

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
	// MDBX returns slices that alias the page until tx commit; the
	// caller may keep the value past that, so copy.
	out := make([]byte, len(v))
	copy(out, v)
	return out, 0, nil
}

func (p *PersistentPatriciaContext) PutBranch(prefix []byte, data []byte, _ []byte) error {
	if p.writeTx == nil {
		return errors.New("PersistentPatriciaContext: PutBranch needs a writeTx (call SetWriteTx)")
	}
	return p.writeTx.Put(CommitmentBranchesTable, prefix, data)
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
