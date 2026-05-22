package mptproof

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// Reth table names — keccak-keyed plain state.
//
//	HashedAccounts  key = keccak(addr) 32B          val = Account compact
//	HashedStorages  DupSort
//	                key = keccak(addr) 32B
//	                dup = keccak(slot) 32B || U256 (variable length, trimmed)
const (
	rethHashedAccountsTable = "HashedAccounts"
	rethHashedStoragesTable = "HashedStorages"
)

// RethHashedLeafSource reads reth's keccak-keyed state tables. This
// replaces our briefly-built HashedAccount/HashedStorageRef tables
// (now cleared); reth already stores the same data with better
// compaction (29.7 GB accounts + 127.7 GB storage), and DupSort gives
// us efficient prefix scans natively.
//
// Implements LeafSource + HashedKeyScanner. ScanAccounts/ScanStorage
// fall back to error because plain-key callbacks can't recover the
// original addr/slot from a keccak hash. Proof callers should always
// dispatch through HashedKeyScanner; the dispatch happens
// automatically in collectAccountLeavesWithPrefix / collectStorage*.
type RethHashedLeafSource struct {
	db kv.RoDB
}

// NewRethHashedLeafSource opens a reth (or compatible) MDBX env
// readonly. mapSizeGB defaults to 4096 for a full mainnet archive.
func NewRethHashedLeafSource(dbPath string, mapSizeGB int) (*RethHashedLeafSource, error) {
	if mapSizeGB <= 0 {
		mapSizeGB = 4096
	}
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(mapSizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[rethHashedAccountsTable] = kv.TableCfgItem{}
			d[rethHashedStoragesTable] = kv.TableCfgItem{Flags: kv.DupSort}
			return d
		}).
		Open(context.Background())
	if err != nil {
		return nil, fmt.Errorf("RethHashedLeafSource: %w", err)
	}
	return &RethHashedLeafSource{db: db}, nil
}

func (r *RethHashedLeafSource) Close() error {
	if r.db != nil {
		r.db.Close()
		r.db = nil
	}
	return nil
}

// AccountValue: hash addr first, then point-lookup HashedAccounts.
func (r *RethHashedLeafSource) AccountValue(addr [20]byte) ([]byte, bool, error) {
	hashed := keccak(addr[:])
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	v, err := tx.GetOne(rethHashedAccountsTable, hashed)
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

// StorageValue: hash both, then DupSort SeekBothRange on
// (keccak(addr), keccak(slot)+...).
func (r *RethHashedLeafSource) StorageValue(addr [20]byte, slot [32]byte) ([]byte, bool, error) {
	hAddr := keccak(addr[:])
	hSlot := keccak(slot[:])

	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	c, err := tx.CursorDupSort(rethHashedStoragesTable)
	if err != nil {
		return nil, false, err
	}
	defer c.Close()

	v, err := c.SeekBothRange(hAddr, hSlot)
	if err != nil {
		return nil, false, err
	}
	if v == nil || len(v) < 32 {
		return nil, false, nil
	}
	if !bytes.Equal(v[:32], hSlot) {
		return nil, false, nil
	}
	out := make([]byte, len(v)-32)
	copy(out, v[32:])
	return out, true, nil
}

// ErrPlainKeyScanUnsupported signals callers to use HashedKeyScanner.
var ErrPlainKeyScanUnsupported = errors.New("RethHashedLeafSource: ScanAccounts/ScanStorage unsupported (use HashedKeyScanner)")

func (r *RethHashedLeafSource) ScanAccounts(fn func(addr [20]byte, value []byte) error) error {
	return ErrPlainKeyScanUnsupported
}
func (r *RethHashedLeafSource) ScanStorage(fn func(addr [20]byte, slot [32]byte, value []byte) error) error {
	return ErrPlainKeyScanUnsupported
}

// --------------------------------------------------------------------
// HashedKeyScanner — the real entry points used by proof generation.
// --------------------------------------------------------------------

// AccountsByHashedPrefix range-scans HashedAccounts. Prefix is in
// nibbles (each byte 0..15). Returns subLeaf entries with effective
// keys = remainder nibbles + 0x10 leaf terminator.
func (r *RethHashedLeafSource) AccountsByHashedPrefix(prefix []byte) ([]subLeaf, error) {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := tx.Cursor(rethHashedAccountsTable)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	seekKey := nibblesToByteSeek(prefix)
	var out []subLeaf
	for k, v, err := c.Seek(seekKey); err == nil && k != nil; k, v, err = c.Next() {
		hn := nibblesOf(k)
		if !bytes.HasPrefix(hn, prefix) {
			if isAfterPrefixNibbles(hn, prefix) {
				break
			}
			continue
		}
		eff := make([]byte, len(hn)-len(prefix)+1)
		copy(eff, hn[len(prefix):])
		eff[len(eff)-1] = 0x10
		val := make([]byte, len(v))
		copy(val, v)
		out = append(out, subLeaf{effectiveKey: eff, value: val})
	}
	return out, nil
}

// StorageByHashedPrefix range-scans HashedStorages.
//
// HashedStorages is DupSort: key = keccak(addr) 32B, dup = keccak(slot)
// 32B || U256. The COMPOSITE hashed key (64B) used by the unified
// storage trie is keccak(addr) || keccak(slot). We map the prefix
// across these two key parts:
//
//   - prefix len ≤ 64 nibbles (≤ 32 bytes): scoped within the
//     keccak(addr) portion — multiple accounts possibly match.
//     Iterate keys (Cursor.Seek + Next) while HasPrefix.
//     For each matching account, iterate ALL its dups.
//
//   - prefix len > 64 nibbles: scoped within ONE account + a partial
//     keccak(slot). Seek to that account, iterate dups via
//     SeekBothRange + NextDup while dup-prefix matches.
func (r *RethHashedLeafSource) StorageByHashedPrefix(prefix []byte) ([]subLeaf, error) {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	c, err := tx.CursorDupSort(rethHashedStoragesTable)
	if err != nil {
		return nil, err
	}
	defer c.Close()

	// Hard cap on enumerated leaves for the address-portion branch
	// only. With prefix ≤ 64 nibbles, a short prefix can match many
	// accounts × all their slots — millions of leaves across many
	// contracts — and OOM the caller. The cap turns those pathological
	// proofs into a clean error so the caller can fail fast and the
	// workload owner knows to extend the dense-recursion path (or move
	// to per-contract storage tries).
	//
	// The deep-prefix branch (len > 64 nibbles, scoped to a single
	// contract) is left uncapped: a heavy contract can legitimately
	// own millions of slots, and the resulting subtree must be fully
	// enumerated to compute the subtree root — capping there would
	// reject correct proofs. Memory is bounded by that one contract's
	// storage size.
	const maxLeavesCap = 200_000

	var out []subLeaf

	if len(prefix) <= 64 {
		// Address-keccak portion only. Iterate all accounts whose
		// keccak starts with prefix (treated as bytes), then for each
		// such account enumerate ALL its slots.
		seekAddr := nibblesToByteSeek(prefix)
		for k, _, err := c.Seek(seekAddr); err == nil && k != nil; k, _, err = c.NextNoDup() {
			hnAddr := nibblesOf(k)
			if !bytes.HasPrefix(hnAddr, prefix) {
				if isAfterPrefixNibbles(hnAddr, prefix) {
					break
				}
				continue
			}
			// Account matches. Enumerate dups (sorted by keccak(slot)).
			// SeekBothRange returns just the dup value (key implicit);
			// NextDup advances and returns (k, v).
			v, err := c.SeekBothRange(k, nil)
			for ; err == nil && v != nil; _, v, err = c.NextDup() {
				if len(v) < 32 {
					continue
				}
				appendStorageSubLeaf(&out, hnAddr, v[:32], v[32:], prefix)
				if len(out) > maxLeavesCap {
					return nil, fmt.Errorf("StorageByHashedPrefix: subtree under prefix %x (len %d nib) exceeds %d-leaf cap — proof needs dense-recursive expansion or per-contract storage trie",
						prefix, len(prefix), maxLeavesCap)
				}
			}
		}
		return out, nil
	}

	// prefix > 64 nibbles. First 64 = keccak(addr); rest = partial
	// keccak(slot).
	addrPrefix := prefix[:64]
	slotPrefix := prefix[64:]
	addrKey := nibblesToByteSeek(addrPrefix) // exact 32B
	slotSeek := nibblesToByteSeek(slotPrefix)

	v, err := c.SeekBothRange(addrKey, slotSeek)
	if err != nil {
		return nil, err
	}
	for ; v != nil; _, v, err = c.NextDup() {
		if err != nil {
			return nil, err
		}
		if len(v) < 32 {
			continue
		}
		slotNb := nibblesOf(v[:32])
		if !bytes.HasPrefix(slotNb, slotPrefix) {
			break
		}
		// Full hashed nibbles = addrPrefix (64) || slotNb (64)
		fullHn := append(append([]byte{}, addrPrefix...), slotNb...)
		eff := make([]byte, len(fullHn)-len(prefix)+1)
		copy(eff, fullHn[len(prefix):])
		eff[len(eff)-1] = 0x10
		val := make([]byte, len(v)-32)
		copy(val, v[32:])
		out = append(out, subLeaf{effectiveKey: eff, value: val})
	}
	return out, nil
}

// appendStorageSubLeaf records one (addr, slot, value) tuple as a
// subLeaf whose effective key is `hnAddr || nibblesOf(slot)` stripped
// of `prefix` and terminated with 0x10.
func appendStorageSubLeaf(out *[]subLeaf, hnAddr []byte, slotBytes, val []byte, prefix []byte) {
	slotNb := nibblesOf(slotBytes)
	fullHn := append(append([]byte{}, hnAddr...), slotNb...)
	if !bytes.HasPrefix(fullHn, prefix) {
		return
	}
	eff := make([]byte, len(fullHn)-len(prefix)+1)
	copy(eff, fullHn[len(prefix):])
	eff[len(eff)-1] = 0x10
	cv := make([]byte, len(val))
	copy(cv, val)
	*out = append(*out, subLeaf{effectiveKey: eff, value: cv})
}

// --------------------------------------------------------------------
// Nibble helpers (kept local for self-containment).
// --------------------------------------------------------------------

// nibblesToByteSeek converts a nibble-array prefix into the byte
// array used by MDBX Seek. For even-length prefixes the result is
// the prefix bytes directly; odd-length appends one byte with the
// last nibble in the high half and 0 in the low half (lex floor).
func nibblesToByteSeek(prefix []byte) []byte {
	full := len(prefix) / 2
	odd := len(prefix) % 2
	out := make([]byte, full+odd)
	for i := 0; i < full; i++ {
		out[i] = (prefix[i*2] << 4) | prefix[i*2+1]
	}
	if odd != 0 {
		out[full] = prefix[len(prefix)-1] << 4
	}
	return out
}

// --------------------------------------------------------------------
// G2 SingleLeafLookup adapter — lets the dense V2 reader expand
// LeafMarker slots by querying reth's keccak-sorted tables.
// --------------------------------------------------------------------

// AccountLeafByPrefix returns the unique HashedAccounts row whose
// 32-byte key starts with the given nibble prefix. The MPT guarantees
// uniqueness when this is called on a HasTree=0 + hashed-leaf slot.
// Returns (keccak(addr), value, true, nil) on hit; (zero, nil, false, nil)
// on no match; error on I/O.
func (r *RethHashedLeafSource) AccountLeafByPrefix(prefix []byte) (hashedAddr [32]byte, value []byte, ok bool, err error) {
	leaves, lerr := r.AccountsByHashedPrefix(prefix)
	if lerr != nil {
		return hashedAddr, nil, false, lerr
	}
	if len(leaves) == 0 {
		return hashedAddr, nil, false, nil
	}
	if len(leaves) > 1 {
		return hashedAddr, nil, false, fmt.Errorf("AccountLeafByPrefix(%x): %d matches (expected 1)", prefix, len(leaves))
	}
	leaf := leaves[0]
	// effectiveKey = remaining nibbles + 0x10 terminator
	if len(leaf.effectiveKey) == 0 {
		return hashedAddr, nil, false, fmt.Errorf("empty effectiveKey")
	}
	full := append(append([]byte{}, prefix...), leaf.effectiveKey[:len(leaf.effectiveKey)-1]...)
	if len(full) != 64 {
		return hashedAddr, nil, false, fmt.Errorf("AccountLeafByPrefix: expected 64 nibbles, got %d", len(full))
	}
	for i := 0; i < 32; i++ {
		hashedAddr[i] = (full[2*i] << 4) | full[2*i+1]
	}
	return hashedAddr, leaf.value, true, nil
}

// StorageLeafByPrefix returns the unique HashedStorages entry whose
// composite (keccak(addr) || keccak(slot)) nibble representation
// starts with the given prefix.
func (r *RethHashedLeafSource) StorageLeafByPrefix(prefix []byte) (composite [64]byte, value []byte, ok bool, err error) {
	leaves, lerr := r.StorageByHashedPrefix(prefix)
	if lerr != nil {
		return composite, nil, false, lerr
	}
	if len(leaves) == 0 {
		return composite, nil, false, nil
	}
	if len(leaves) > 1 {
		return composite, nil, false, fmt.Errorf("StorageLeafByPrefix(%x): %d matches (expected 1)", prefix, len(leaves))
	}
	leaf := leaves[0]
	if len(leaf.effectiveKey) == 0 {
		return composite, nil, false, fmt.Errorf("empty effectiveKey")
	}
	full := append(append([]byte{}, prefix...), leaf.effectiveKey[:len(leaf.effectiveKey)-1]...)
	if len(full) != 128 {
		return composite, nil, false, fmt.Errorf("StorageLeafByPrefix: expected 128 nibbles, got %d", len(full))
	}
	for i := 0; i < 64; i++ {
		composite[i] = (full[2*i] << 4) | full[2*i+1]
	}
	return composite, leaf.value, true, nil
}

// isAfterPrefixNibbles reports whether hn's first len(prefix)
// nibbles compare strictly greater than prefix.
func isAfterPrefixNibbles(hn, prefix []byte) bool {
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
