// Package mptproof generates eth_getProof-style proofs from an N42
// archive (snapshot + history + per-trie MDBX caches built by
// Phase A/B). Combines internal/mpttrie (proof walk) with a
// LeafSource (latest plain state) and optionally
// internal/historicalstate (historical state-as-of).
//
// This MVP package exposes:
//
//	NewGenerator(...) — wire up MPT readers + leaf source
//	LatestAccountProof(addr)              → siblings + leaf bytes
//	LatestStorageProofs(addr, slots)      → siblings + leaf bytes per slot
//	HistoricalAccountValue(addr, blockN)  → historical leaf value
//	HistoricalStorageValue(addr, slot, N) → historical slot value
//
// Phase D will add eth_getProof wire-format encoding (BranchNodeCompact
// → standard RLP MPT node bytes) for direct RPC integration.
package mptproof

import (
	"context"
	"errors"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// LeafSource provides latest leaf values for proof construction.
// Implementations: RethLeafSource (reads reth PlainAccountState +
// PlainStorageState directly), MapLeafSource (in-memory, for tests).
type LeafSource interface {
	AccountValue(addr [20]byte) ([]byte, bool, error)
	StorageValue(addr [20]byte, slot [32]byte) ([]byte, bool, error)

	// ScanAccounts iterates all (addr, value) pairs. SLOW — full table
	// scan. Used by Verify's subtree-rebuild fallback, not by RPC hot
	// paths. Callback may abort iteration by returning a non-nil
	// error which propagates as ScanAccounts's return value.
	ScanAccounts(fn func(addr [20]byte, value []byte) error) error

	// ScanStorage iterates all (addr, slot, value) tuples. Same SLOW
	// caveat — used by Verify's subtree-rebuild for storage proofs.
	ScanStorage(fn func(addr [20]byte, slot [32]byte, value []byte) error) error

	Close() error
}

// MapLeafSource is an in-memory implementation for tests.
type MapLeafSource struct {
	Accounts map[[20]byte][]byte
	Storage  map[[20]byte]map[[32]byte][]byte
}

func (m *MapLeafSource) AccountValue(addr [20]byte) ([]byte, bool, error) {
	v, ok := m.Accounts[addr]
	if !ok {
		return nil, false, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, true, nil
}

func (m *MapLeafSource) StorageValue(addr [20]byte, slot [32]byte) ([]byte, bool, error) {
	slots, ok := m.Storage[addr]
	if !ok {
		return nil, false, nil
	}
	v, ok := slots[slot]
	if !ok {
		return nil, false, nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out, true, nil
}

func (m *MapLeafSource) ScanAccounts(fn func(addr [20]byte, value []byte) error) error {
	for a, v := range m.Accounts {
		if err := fn(a, v); err != nil {
			return err
		}
	}
	return nil
}

func (m *MapLeafSource) ScanStorage(fn func(addr [20]byte, slot [32]byte, value []byte) error) error {
	for a, slots := range m.Storage {
		for s, v := range slots {
			if err := fn(a, s, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *MapLeafSource) Close() error { return nil }

// RethLeafSource reads latest values directly from reth's MDBX.
// PlainAccountState: key=addr20, value=Compact-encoded account.
// PlainStorageState: DupSort, key=addr20, value=slot32 || U256 bytes.
type RethLeafSource struct {
	db          kv.RoDB
	acctTable   string
	storTable   string
}

// NewRethLeafSource opens a reth (or N42 with same schema) MDBX
// readonly for leaf lookups.
func NewRethLeafSource(dbPath, accountTable, storageTable string, mapSizeGB int) (*RethLeafSource, error) {
	if mapSizeGB <= 0 {
		mapSizeGB = 4096
	}
	if accountTable == "" {
		accountTable = "PlainAccountState"
	}
	if storageTable == "" {
		storageTable = "PlainStorageState"
	}
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(mapSizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[accountTable] = kv.TableCfgItem{}
			d[storageTable] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		return nil, err
	}
	return &RethLeafSource{db: db, acctTable: accountTable, storTable: storageTable}, nil
}

func (r *RethLeafSource) Close() error {
	if r.db != nil {
		r.db.Close()
		r.db = nil
	}
	return nil
}

func (r *RethLeafSource) AccountValue(addr [20]byte) ([]byte, bool, error) {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	v, err := tx.GetOne(r.acctTable, addr[:])
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

// StorageValue handles DupSort: cursor.SeekBothExact(addr, slot32-prefix)
// to find the entry whose value starts with the target slot.
func (r *RethLeafSource) StorageValue(addr [20]byte, slot [32]byte) ([]byte, bool, error) {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	c, err := tx.CursorDupSort(r.storTable)
	if err != nil {
		// Fallback: not a DupSort cursor (e.g., N42 schema). Iterate
		// all dup values manually.
		return r.storageValueLinear(tx, addr, slot)
	}
	defer c.Close()
	// SeekBothRange finds the first dup whose value >= the given
	// prefix. We pass slot32 as the prefix and check the returned
	// value starts with that slot.
	v, err := c.SeekBothRange(addr[:], slot[:])
	if err != nil {
		return nil, false, err
	}
	if v == nil || len(v) < 32 {
		return nil, false, nil
	}
	for i := 0; i < 32; i++ {
		if v[i] != slot[i] {
			return nil, false, nil
		}
	}
	out := make([]byte, len(v)-32)
	copy(out, v[32:])
	return out, true, nil
}

func (r *RethLeafSource) storageValueLinear(tx kv.Tx, addr [20]byte, slot [32]byte) ([]byte, bool, error) {
	c, err := tx.Cursor(r.storTable)
	if err != nil {
		return nil, false, err
	}
	defer c.Close()
	for k, v, err := c.Seek(addr[:]); err == nil && k != nil; k, v, err = c.Next() {
		if !bytesEqual(k, addr[:]) {
			break
		}
		if len(v) < 32 {
			continue
		}
		if bytesEqual(v[:32], slot[:]) {
			out := make([]byte, len(v)-32)
			copy(out, v[32:])
			return out, true, nil
		}
	}
	return nil, false, nil
}

func (r *RethLeafSource) ScanAccounts(fn func(addr [20]byte, value []byte) error) error {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c, err := tx.Cursor(r.acctTable)
	if err != nil {
		return err
	}
	defer c.Close()
	var addr [20]byte
	for k, v, err := c.First(); err == nil && k != nil; k, v, err = c.Next() {
		if len(k) != 20 {
			continue
		}
		copy(addr[:], k)
		// copy value before invoking caller (cursor reuses buffer).
		val := make([]byte, len(v))
		copy(val, v)
		if err := fn(addr, val); err != nil {
			return err
		}
	}
	return nil
}

func (r *RethLeafSource) ScanStorage(fn func(addr [20]byte, slot [32]byte, value []byte) error) error {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()
	c, err := tx.Cursor(r.storTable)
	if err != nil {
		return err
	}
	defer c.Close()
	var addr [20]byte
	var slot [32]byte
	for k, v, err := c.First(); err == nil && k != nil; k, v, err = c.Next() {
		if len(k) != 20 || len(v) < 32 {
			continue
		}
		copy(addr[:], k)
		copy(slot[:], v[:32])
		val := make([]byte, len(v)-32)
		copy(val, v[32:])
		if err := fn(addr, slot, val); err != nil {
			return err
		}
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ErrLeafSourceClosed is returned when methods are called after Close.
var ErrLeafSourceClosed = errors.New("mptproof: leaf source closed")
