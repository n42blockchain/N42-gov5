package snapshotreader

import (
	"os"
	"strconv"

	lru "github.com/hashicorp/golang-lru/v2"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/modules/state"
)

// StateReader adapts a snapshot Segment to modules/state.StateReader: it serves
// account + storage values from the IMMUTABLE H0 snapshot and delegates contract
// code to a CodeSource (snapshots store only the codeHash dict, not code bytes;
// code lives in the codes freezer). This is the COLD tier — the warm overlay
// (WarmOverlayReader) sits above it for H0+1..tip changes and tombstones.
//
// All reads return (nil,0,nil) "absent" for keys not in the snapshot; the warm
// overlay is responsible for tombstones (keys deleted after H0).
//
// Cross-block warm cache. Because the H0 snapshot is IMMUTABLE, every cold
// lookup for a given key returns the same result forever, so we memoize both
// hits and misses in a shared LRU that persists across blocks. This is the
// snapshot-direct analogue of geth/erigon's hot-state cache: without it, a hot
// contract (USDT/Uniswap/...) re-runs a full RecSplit MPHF lookup + Elias-Fano
// decode + .idx page fault + fingerprint check on EVERY block it is touched in,
// because the warm overlay builds a fresh IBS + reader per block. The warm tier
// still takes precedence (WarmOverlayReader consults this cold reader only on a
// warm miss), so caching immutable cold results never serves a stale post-H0
// value. Correctness is independent of eviction (any entry can be dropped and
// recomputed identically).
type StateReader struct {
	seg  *Segment
	code state.CodeSource // optional; nil → code reads return empty

	// accCache: addr -> raw snapshot account value (present) or absence.
	// stoCache: addr||slot -> trimmed storage value (present) or absence.
	// Values are sub-slices of the mmapped (or heap-loaded) .val, so caching
	// them is zero-copy; a cached pointer also keeps its mapped page warm-ish
	// but never pins heap. nil when caching is disabled.
	accCache *lru.Cache[[20]byte, coldEntry]
	stoCache *lru.Cache[[52]byte, coldEntry]
}

// coldEntry memoizes one immutable snapshot lookup. present distinguishes a real
// hit (val = the stored bytes) from a cached miss (val nil, present false).
type coldEntry struct {
	val     []byte
	present bool
}

// Default cache sizes (entries). Accounts are ~100 B raw, slots ~40 B; the
// defaults cost roughly acc 2M×~150 B ≈ 300 MB + sto 8M×~120 B ≈ 1 GB, small
// against the machine's RAM and the ~30 GB of resident .val. Override via
// N42_SNAP_ACC_CACHE / N42_SNAP_STO_CACHE (0 = disable that tier).
const (
	defaultAccCacheEntries = 2_000_000
	defaultStoCacheEntries = 8_000_000
)

// NewStateReader wraps seg. code may be nil (e.g. minimal nodes that never
// execute CALLs needing bytecode); then code reads return empty. A cross-block
// LRU is attached by default (see StateReader doc); size it via the
// N42_SNAP_ACC_CACHE / N42_SNAP_STO_CACHE env vars, or set either to 0 to
// disable that tier.
func NewStateReader(seg *Segment, code state.CodeSource) *StateReader {
	r := &StateReader{seg: seg, code: code}
	if n := cacheSize("N42_SNAP_ACC_CACHE", defaultAccCacheEntries); n > 0 {
		r.accCache, _ = lru.New[[20]byte, coldEntry](n)
	}
	if n := cacheSize("N42_SNAP_STO_CACHE", defaultStoCacheEntries); n > 0 {
		r.stoCache, _ = lru.New[[52]byte, coldEntry](n)
	}
	return r
}

// cacheSize reads an entry-count override from env; unset/invalid → def, an
// explicit "0" disables the tier (returns 0).
func cacheSize(env string, def int) int {
	v := os.Getenv(env)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return def
	}
	return n
}

func (r *StateReader) ReadAccountData(addr types.Address) (*account.StateAccount, error) {
	var a20 [20]byte
	copy(a20[:], addr[:])
	if r.accCache != nil {
		if e, ok := r.accCache.Get(a20); ok {
			if !e.present {
				return nil, nil
			}
			return r.seg.DecodeAccount(e.val)
		}
	}
	raw, ok := r.seg.AccountValueRaw(a20)
	if r.accCache != nil {
		r.accCache.Add(a20, coldEntry{val: raw, present: ok})
	}
	if !ok {
		return nil, nil // not in snapshot
	}
	return r.seg.DecodeAccount(raw)
}

func (r *StateReader) ReadAccountStorage(addr types.Address, key *types.Hash) ([]byte, error) {
	var a20 [20]byte
	copy(a20[:], addr[:])
	var s32 [32]byte
	copy(s32[:], key[:])
	if r.stoCache != nil {
		var ck [52]byte
		copy(ck[:20], a20[:])
		copy(ck[20:], s32[:])
		if e, ok := r.stoCache.Get(ck); ok {
			if !e.present {
				return nil, nil
			}
			return e.val, nil
		}
		v, ok := r.seg.StorageValue(a20, s32)
		r.stoCache.Add(ck, coldEntry{val: v, present: ok})
		if !ok {
			return nil, nil
		}
		return v, nil
	}
	v, ok := r.seg.StorageValue(a20, s32)
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (r *StateReader) ReadAccountCode(addr types.Address, codeHash types.Hash) ([]byte, error) {
	if account.IsEmptyCodeHash(codeHash) || r.code == nil {
		return nil, nil
	}
	return r.code.GetCode(addr)
}

func (r *StateReader) ReadAccountCodeSize(addr types.Address, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(addr, codeHash)
	if err != nil {
		return 0, err
	}
	return len(code), nil
}

var _ state.StateReader = (*StateReader)(nil)
