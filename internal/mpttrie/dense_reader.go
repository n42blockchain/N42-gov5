package mpttrie

import (
	"context"
	"fmt"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
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
	return k != nil, nil
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
