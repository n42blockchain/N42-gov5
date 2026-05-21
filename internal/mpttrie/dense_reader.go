package mpttrie

import (
	"context"
	"fmt"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
)

// DenseReader reads Phase G1 dense branch entries. Each entry is the
// full per-child slot encoding (variable bytes per child: 33 B for
// 0xa0||hash, 1..32 B for inline RLP). This is what proof generation
// needs to emit standard EIP-1186 branch RLP byte-for-byte without
// any sub-tree rebuild.
type DenseReader struct {
	db    kv.RoDB
	table string
	owned bool
}

// OpenDense mounts the dense table (AccountsDense or StoragesDense)
// for read. If the env is already open (unified chaindata mode), use
// OpenDenseShared instead.
func OpenDense(dir, table string) (*DenseReader, error) {
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dir).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[table] = kv.TableCfgItem{}
			return d
		}).
		Open(context.Background())
	if err != nil {
		return nil, fmt.Errorf("mpttrie: open dense %s: %w", dir, err)
	}
	return &DenseReader{db: db, table: table, owned: true}, nil
}

// OpenDenseShared wraps an already-open env (e.g. one returned by
// OpenUnifiedDB) with a dense reader for the given table. Close on
// the returned reader is a no-op — the caller owns the env.
func OpenDenseShared(env kv.RoDB, table string) *DenseReader {
	return &DenseReader{db: env, table: table, owned: false}
}

func (r *DenseReader) Close() error {
	if r.owned && r.db != nil {
		r.db.Close()
		r.db = nil
	}
	return nil
}

// Has reports whether the dense table contains any data. Used to
// decide whether to take the dense fast path or fall back to compact
// + leaf-source rebuild.
//
// IMPLEMENTATION NOTE: MDBX's RO bucket-create path leaves the DBI
// handle as the default zero value for tables that were declared in
// TableCfg but never actually written (the env was created without
// the table, then re-opened with a TableCfg adding it). A subsequent
// Cursor() against that bogus DBI silently iterates the main/META
// bucket, returning a phantom non-nil first key — i.e. Has would
// false-positive without further checks.
//
// Defense: require that the first key (if any) is a valid nibble
// path — every byte must be 0..15. Dense table keys are always
// nibbles. A phantom Meta key like "accounts:state_root" starts with
// 'a' = 0x61 → instantly rejected.
func (r *DenseReader) Has() (bool, error) {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	c, err := tx.Cursor(r.table)
	if err != nil {
		return false, err
	}
	defer c.Close()
	k, _, err := c.First()
	if err != nil {
		return false, err
	}
	if k == nil {
		return false, nil
	}
	for _, b := range k {
		if b > 0x0f {
			// Phantom key from misrouted DBI; treat table as empty.
			return false, nil
		}
	}
	return true, nil
}

// DenseBranch is the decoded form of one dense entry.
type DenseBranch struct {
	StateMask uint16
	TreeMask  uint16
	// Slots[i] = the parent-facing bytes for child nibble i. For
	// nibbles not in StateMask, Slots[i] == nil. For nibbles in
	// StateMask, Slots[i] is either:
	//   - 33 bytes 0xa0||hash  (child encoding >= 32 B, hashed)
	//   - 1..32 bytes RLP      (child encoding inline)
	Slots [16][]byte
}

// SingleLeafLookup is the minimal interface a base data source must
// implement to expand G2 LeafMarker slots. Given a nibble prefix
// (= branchPath + childNibble), return the unique leaf below that
// prefix as (fullHashedKey, accountOrSlotValue). The implementation
// is expected to be O(1) MDBX cursor seek on a keccak-sorted table
// (e.g. reth's HashedAccounts / HashedStorages DupSort).
type SingleLeafLookup interface {
	// AccountLeafByPrefix returns the keccak(addr) and the account
	// value for the unique account whose keccak starts with prefix.
	AccountLeafByPrefix(prefix []byte) (hashedAddr [32]byte, value []byte, ok bool, err error)
	// StorageLeafByPrefix returns keccak(addr)||keccak(slot) and the
	// storage value for the unique storage entry whose composite
	// hashed key starts with prefix.
	StorageLeafByPrefix(prefix []byte) (composite [64]byte, value []byte, ok bool, err error)
}

// GetV2 fetches a V2 dense entry and expands LeafMarker slots into
// full 33-byte 0xa0||hash by prefix-scanning the base table for the
// unique leaf below each marker's nibble path. `isStorage` selects
// AccountLeafByPrefix vs StorageLeafByPrefix on base.
//
// Pre-condition: r.table refers to AccountsDenseV2Table or
// StoragesDenseV2Table (caller's responsibility to wire correctly).
func (r *DenseReader) GetV2(nibblePath []byte, isStorage bool, base SingleLeafLookup) (DenseBranch, bool, error) {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return DenseBranch{}, false, err
	}
	defer tx.Rollback()
	raw, err := tx.GetOne(r.table, nibblePath)
	if err != nil {
		return DenseBranch{}, false, err
	}
	if raw == nil {
		return DenseBranch{}, false, nil
	}
	stateMask, treeMask, slots, err := trie.UnmarshalTrieNodeDenseV2(raw)
	if err != nil {
		return DenseBranch{}, false, fmt.Errorf("V2 dense at path %x: %w", nibblePath, err)
	}

	var out DenseBranch
	out.StateMask = stateMask
	out.TreeMask = treeMask

	// First pass: copy non-marker slots, sum bytes, count markers
	// for sizing.
	markerCount := 0
	rawTotal := 0
	for digit := 0; digit < 16; digit++ {
		if stateMask&(1<<digit) == 0 {
			continue
		}
		if trie.IsLeafMarker(slots[digit]) {
			markerCount++
			rawTotal += 33 // post-expansion size
		} else {
			rawTotal += len(slots[digit])
		}
	}
	pool := make([]byte, rawTotal)
	off := 0

	for digit := 0; digit < 16; digit++ {
		if stateMask&(1<<digit) == 0 {
			continue
		}
		if !trie.IsLeafMarker(slots[digit]) {
			copy(pool[off:off+len(slots[digit])], slots[digit])
			out.Slots[digit] = pool[off : off+len(slots[digit])]
			off += len(slots[digit])
			continue
		}
		// Expand marker: prefix = nibblePath + digit
		prefix := make([]byte, len(nibblePath)+1)
		copy(prefix, nibblePath)
		prefix[len(nibblePath)] = byte(digit)
		if base == nil {
			return DenseBranch{}, false, fmt.Errorf("V2 leaf marker at path %x digit %d: base SingleLeafLookup required", nibblePath, digit)
		}
		hash, ferr := reconstructLeafHash(prefix, isStorage, base)
		if ferr != nil {
			return DenseBranch{}, false, ferr
		}
		pool[off] = 0xa0
		copy(pool[off+1:off+33], hash[:])
		out.Slots[digit] = pool[off : off+33]
		off += 33
	}
	return out, true, nil
}

// reconstructLeafHash performs the V2 marker → 33B-hash expansion:
//  1. Look up the unique leaf below `prefix` (nibbles) via base
//  2. Compute the leaf's key suffix in HP-encoded form
//  3. Build leaf RLP = [hp_encode(suffix), value]
//  4. Return keccak256(leafRLP)
func reconstructLeafHash(prefix []byte, isStorage bool, base SingleLeafLookup) ([32]byte, error) {
	var (
		fullHashedKey []byte
		value         []byte
	)
	if isStorage {
		comp, v, ok, err := base.StorageLeafByPrefix(prefix)
		if err != nil {
			return [32]byte{}, fmt.Errorf("StorageLeafByPrefix(%x): %w", prefix, err)
		}
		if !ok {
			return [32]byte{}, fmt.Errorf("V2 marker at %x: no leaf in base", prefix)
		}
		fullHashedKey = comp[:]
		value = v
	} else {
		ha, v, ok, err := base.AccountLeafByPrefix(prefix)
		if err != nil {
			return [32]byte{}, fmt.Errorf("AccountLeafByPrefix(%x): %w", prefix, err)
		}
		if !ok {
			return [32]byte{}, fmt.Errorf("V2 marker at %x: no leaf in base", prefix)
		}
		fullHashedKey = ha[:]
		value = v
	}
	// Compute suffix = nibbles of fullHashedKey beyond `prefix`, +0x10
	suffix := make([]byte, 0, len(fullHashedKey)*2-len(prefix)+1)
	for i, b := range fullHashedKey {
		hi, lo := b>>4, b&0x0f
		if 2*i >= len(prefix) {
			suffix = append(suffix, hi)
		}
		if 2*i+1 >= len(prefix) {
			suffix = append(suffix, lo)
		}
	}
	suffix = append(suffix, 0x10) // leaf terminator

	// Use HashBuilder's own leaf-hash path. Same wrapping
	// (RlpEncodedBytes) the builder uses for mptbuild.Build, so the
	// recovered hash matches the parent's stored 32-byte slot exactly.
	return trie.LeafHashStandalone(suffix, rlphacks.RlpEncodedBytes(value))
}

// encodeLeafRLP — kept for backward compatibility with synthetic
// reconstruction tests; production reconstructLeafHash above calls
// lib/trie.LeafHashStandalone instead.
func encodeLeafRLP(hpKey, value []byte) []byte {
	// hp-encode: leaf with even / odd nibble length + terminator.
	// suffix already includes 0x10. Build the HP key bytes.
	hp := nibblesToHP(hpKey, true /* terminate */)
	// Inner [keyBytes, valueBytes] list:
	keyEnc := encodeBytesRLP(hp)
	valEnc := encodeBytesRLP(value)
	innerLen := len(keyEnc) + len(valEnc)
	out := make([]byte, 0, 4+innerLen)
	out = appendListHeader(out, innerLen)
	out = append(out, keyEnc...)
	out = append(out, valEnc...)
	return out
}

// nibblesToHP converts a nibble slice (with optional terminator 0x10
// at the end if terminate=true) to its HP (hex prefix) byte encoding
// per Ethereum yellow-paper Appendix C.
func nibblesToHP(nibbles []byte, terminated bool) []byte {
	// Strip terminator nibble from length for parity calculation.
	n := nibbles
	if terminated && len(n) > 0 && n[len(n)-1] == 0x10 {
		n = n[:len(n)-1]
	}
	odd := len(n)%2 == 1
	var prefix byte
	if terminated {
		prefix = 0x20
	}
	if odd {
		prefix |= 0x10 | n[0]
		n = n[1:]
	}
	out := make([]byte, 1+len(n)/2)
	out[0] = prefix
	for i := 0; i < len(n); i += 2 {
		out[1+i/2] = (n[i] << 4) | n[i+1]
	}
	return out
}

func encodeBytesRLP(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		return []byte{b[0]}
	}
	if len(b) < 56 {
		out := make([]byte, 1+len(b))
		out[0] = 0x80 + byte(len(b))
		copy(out[1:], b)
		return out
	}
	// Longer strings: 0xb7 + sizeOfLength + lengthBytes + payload
	szBytes := encodeUintBE(uint64(len(b)))
	out := make([]byte, 1+len(szBytes)+len(b))
	out[0] = 0xb7 + byte(len(szBytes))
	copy(out[1:], szBytes)
	copy(out[1+len(szBytes):], b)
	return out
}

func appendListHeader(out []byte, payloadLen int) []byte {
	if payloadLen < 56 {
		return append(out, 0xc0+byte(payloadLen))
	}
	szBytes := encodeUintBE(uint64(payloadLen))
	out = append(out, 0xf7+byte(len(szBytes)))
	return append(out, szBytes...)
}

func encodeUintBE(v uint64) []byte {
	switch {
	case v <= 0xff:
		return []byte{byte(v)}
	case v <= 0xffff:
		return []byte{byte(v >> 8), byte(v)}
	case v <= 0xffffff:
		return []byte{byte(v >> 16), byte(v >> 8), byte(v)}
	default:
		return []byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}
	}
}

// Get fetches the dense branch at the given nibble path. Returns
// (branch, true, nil) on hit, (zero, false, nil) on miss, error on I/O.
func (r *DenseReader) Get(nibblePath []byte) (DenseBranch, bool, error) {
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return DenseBranch{}, false, err
	}
	defer tx.Rollback()
	raw, err := tx.GetOne(r.table, nibblePath)
	if err != nil {
		return DenseBranch{}, false, err
	}
	if raw == nil {
		return DenseBranch{}, false, nil
	}
	stateMask, treeMask, slots, err := trie.UnmarshalTrieNodeDense(raw)
	if err != nil {
		return DenseBranch{}, false, fmt.Errorf("dense at path %x: %w", nibblePath, err)
	}
	// Copy slot bytes (raw aliases the MDBX page that becomes invalid
	// after tx.Rollback). Use a single backing array for locality.
	total := 0
	for digit := 0; digit < 16; digit++ {
		total += len(slots[digit])
	}
	pool := make([]byte, total)
	off := 0
	var out DenseBranch
	out.StateMask = stateMask
	out.TreeMask = treeMask
	for digit := 0; digit < 16; digit++ {
		n := len(slots[digit])
		if n == 0 {
			continue
		}
		copy(pool[off:off+n], slots[digit])
		out.Slots[digit] = pool[off : off+n]
		off += n
	}
	return out, true, nil
}
