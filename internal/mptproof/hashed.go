package mptproof

import (
	"bytes"
	"context"
	"fmt"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// Hashed-key index table names (must match the bootstrap tool
// cmd/n42-mpt-hashedindex). Both live in the unified chaindata env.
//
//	HashedAccountTable    key = keccak(addr)              32 B
//	                      val = accountRLP (variable)
//
//	HashedStorageRefTable key = keccak(addr)||keccak(slot) 64 B
//	                      val = addr20||slot32             52 B  (Option A:
//	                            value-by-reference; the actual storage
//	                            value is re-fetched via base.StorageValue)
const (
	HashedAccountTable    = "HashedAccount"
	HashedStorageRefTable = "HashedStorageRef"
)

// HashedLeafSource serves leaf-prefix queries from a hashed-key sorted
// index, replacing the O(N) ScanAccounts/ScanStorage that the default
// RethLeafSource has to do for inline-sibling subtree rebuilds.
//
// Storage-value reads still go through `base` (Option A: index stores
// only addr/slot references, not values). Walk-level lookups
// (AccountValue / StorageValue) delegate to base too — those are
// already hashtable-fast in plain state.
//
// Lifecycle: HashedLeafSource OWNS the MDBX env it opens (Close closes
// it). It does NOT close `base` — caller manages that.
type HashedLeafSource struct {
	env  kv.RoDB
	base LeafSource
}

// OpenHashedLeafSource opens a chaindata MDBX dir for read and wires
// the hashed-index tables. `base` is the fallback LeafSource used for
// (a) regular value lookups, (b) storage value re-fetch in Option A,
// and (c) ScanAccounts/ScanStorage when nothing else satisfies the
// caller.
func OpenHashedLeafSource(chaindataDir string, base LeafSource) (*HashedLeafSource, error) {
	if base == nil {
		return nil, fmt.Errorf("OpenHashedLeafSource: base LeafSource required")
	}
	logger := log.New()
	env, err := mdbxkv.NewMDBX(logger).
		Path(chaindataDir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[HashedAccountTable] = kv.TableCfgItem{}
			d[HashedStorageRefTable] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		return nil, fmt.Errorf("OpenHashedLeafSource: %w", err)
	}
	return &HashedLeafSource{env: env, base: base}, nil
}

func (h *HashedLeafSource) Close() error {
	if h.env != nil {
		h.env.Close()
		h.env = nil
	}
	return nil
}

// AccountValue delegates to base. The hashed index is for prefix scans,
// not point lookups (which are already O(1) in plain state).
func (h *HashedLeafSource) AccountValue(addr [20]byte) ([]byte, bool, error) {
	return h.base.AccountValue(addr)
}

func (h *HashedLeafSource) StorageValue(addr [20]byte, slot [32]byte) ([]byte, bool, error) {
	return h.base.StorageValue(addr, slot)
}

func (h *HashedLeafSource) ScanAccounts(fn func(addr [20]byte, value []byte) error) error {
	return h.base.ScanAccounts(fn)
}

func (h *HashedLeafSource) ScanStorage(fn func(addr [20]byte, slot [32]byte, value []byte) error) error {
	return h.base.ScanStorage(fn)
}

// collectAccountLeavesByPrefix scans the HashedAccount table for keys
// whose nibble representation starts with `prefix`. Result is sorted
// (MDBX cursor returns ascending) and effective keys are stripped of
// the prefix + appended 0x10 leaf terminator, matching the subLeaf
// convention used by buildSubtreeRoot.
//
// Cost: O(matches + log N). At depth 6 nibbles over a 386M account
// space, expected matches ≈ 23.
func (h *HashedLeafSource) collectAccountLeavesByPrefix(prefix []byte) ([]subLeaf, error) {
	tx, err := h.env.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := tx.Cursor(HashedAccountTable)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	seekKey, _ := nibblePrefixToByteSeek(prefix)
	var out []subLeaf
	for k, v, err := c.Seek(seekKey); err == nil && k != nil; k, v, err = c.Next() {
		hn := nibblesOf(k)
		if !bytes.HasPrefix(hn, prefix) {
			// MDBX cursor returns ascending; once we step past the
			// prefix's lexicographic range we are done. The Seek
			// landed us on the first byte-aligned candidate, but
			// odd-length prefixes mean the first key may be < prefix
			// at the nibble level — skip those by continuing.
			if bytes.Compare(k, seekKey) > 0 {
				// Check if we're past the supremum.
				if isAfterPrefix(hn, prefix) {
					break
				}
				continue
			}
			continue
		}
		eff := make([]byte, len(hn)-len(prefix)+1)
		copy(eff, hn[len(prefix):])
		eff[len(eff)-1] = 0x10
		// Copy value: MDBX cursor reuses buffer between Next() calls.
		val := make([]byte, len(v))
		copy(val, v)
		out = append(out, subLeaf{effectiveKey: eff, value: val})
	}
	return out, nil
}

// collectStorageLeavesByPrefix scans HashedStorageRef the same way. For
// each matched reference (addr20||slot32), it re-fetches the actual
// storage value via base.StorageValue. The extra ~10 µs per leaf is
// negligible vs the seconds-to-minutes saved.
func (h *HashedLeafSource) collectStorageLeavesByPrefix(prefix []byte) ([]subLeaf, error) {
	tx, err := h.env.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := tx.Cursor(HashedStorageRefTable)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	seekKey, _ := nibblePrefixToByteSeek(prefix)
	var out []subLeaf
	for k, v, err := c.Seek(seekKey); err == nil && k != nil; k, v, err = c.Next() {
		hn := nibblesOf(k)
		if !bytes.HasPrefix(hn, prefix) {
			if bytes.Compare(k, seekKey) > 0 && isAfterPrefix(hn, prefix) {
				break
			}
			continue
		}
		if len(v) != 52 {
			return nil, fmt.Errorf("HashedStorageRef: unexpected value len %d (want 52)", len(v))
		}
		var addr [20]byte
		var slot [32]byte
		copy(addr[:], v[:20])
		copy(slot[:], v[20:])
		value, ok, lerr := h.base.StorageValue(addr, slot)
		if lerr != nil {
			return nil, fmt.Errorf("re-fetch storage %x/%x: %w", addr, slot, lerr)
		}
		if !ok {
			// Reference points to plain state that no longer exists.
			// This can happen during catch-up if the index lags the
			// plain state; safe to skip (the MPT would not include
			// such a leaf either).
			continue
		}
		eff := make([]byte, len(hn)-len(prefix)+1)
		copy(eff, hn[len(prefix):])
		eff[len(eff)-1] = 0x10
		out = append(out, subLeaf{effectiveKey: eff, value: value})
	}
	return out, nil
}

// nibblePrefixToByteSeek converts a nibble-array prefix into a byte
// array suitable for MDBX cursor Seek. For even-length prefixes the
// result is the prefix directly. For odd-length, the last nibble is
// placed in the high half of a byte (low half = 0), giving the
// lexicographic floor of all matching keys.
//
// Returned `partial` is true when the prefix had odd nibble length —
// caller must filter results at the nibble level (the byte Seek alone
// can over-match).
func nibblePrefixToByteSeek(prefix []byte) (seek []byte, partial bool) {
	full := len(prefix) / 2
	seek = make([]byte, full+func() int {
		if len(prefix)%2 != 0 {
			return 1
		}
		return 0
	}())
	for i := 0; i < full; i++ {
		seek[i] = (prefix[i*2] << 4) | prefix[i*2+1]
	}
	if len(prefix)%2 != 0 {
		seek[full] = prefix[len(prefix)-1] << 4
		partial = true
	}
	return seek, partial
}

// isAfterPrefix returns true if `hn` lexicographically follows EVERY
// nibble-string with the given prefix (i.e. hn[0:len(prefix)] > prefix
// element-wise at the first differing position).
func isAfterPrefix(hn, prefix []byte) bool {
	for i := 0; i < len(prefix) && i < len(hn); i++ {
		if hn[i] > prefix[i] {
			return true
		}
		if hn[i] < prefix[i] {
			return false
		}
	}
	return false
}
