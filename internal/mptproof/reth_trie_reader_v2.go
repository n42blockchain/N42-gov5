package mptproof

import (
	"bytes"
	"context"
	"fmt"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/internal/mpttrie"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

// rethStoragesTrieV2Table is the table name in the compressed-format
// destination env (produced by cmd/n42-storage-trie-transcode). Same
// shape as reth's StoragesTrie but with 33-byte packed subkey
// instead of 65-byte v1.
const rethStoragesTrieV2Table = "StoragesTrieV2"

// RethTrieReaderV2 reads a transcoded StoragesTrie V2 env. Each dup
// value layout is:
//
//	[33-byte packed subkey][BranchNodeCompact]
//
// Saves 32 bytes per row vs the v1 format. Empirically validated
// at ~4.13 GB saving on D:\reth2k (138.6M rows).
//
// Use this instead of (or alongside) RethTrieReader.StorageBranchAt
// when reading from a V2 transcoded env. Account trie reading is
// unchanged — reth's AccountsTrie already uses variable-length raw
// nibble keys and benefits less from this optimisation; for
// homogeneity it remains in the source env.
type RethTrieReaderV2 struct {
	db    kv.RoDB
	owned bool
}

// OpenRethTrieReaderV2 opens a destination env produced by the
// n42-storage-trie-transcode tool. Read-only.
func OpenRethTrieReaderV2(dir string, mapSizeGB int) (*RethTrieReaderV2, error) {
	if mapSizeGB <= 0 {
		mapSizeGB = 64
	}
	logger := log.New()
	db, err := mdbxkv.NewMDBX(logger).
		Path(dir).Label(kv.ChainDB).PageSize(4096).
		MapSize(datasize.ByteSize(mapSizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[rethStoragesTrieV2Table] = kv.TableCfgItem{Flags: kv.DupSort}
			return d
		}).Open(context.Background())
	if err != nil {
		return nil, fmt.Errorf("OpenRethTrieReaderV2(%s): %w", dir, err)
	}
	return &RethTrieReaderV2{db: db, owned: true}, nil
}

func (r *RethTrieReaderV2) Close() error {
	if r.owned && r.db != nil {
		r.db.Close()
		r.db = nil
	}
	return nil
}

// StorageBranchAt returns the StoragesTrieV2 branch for the given
// (addrHash, nibblePath). Uses the 33-byte packed subkey format.
//
// Semantics identical to RethTrieReader.StorageBranchAt — only
// the on-disk encoding differs.
func (r *RethTrieReaderV2) StorageBranchAt(addrHash []byte, nibblePath []byte) (*mpttrie.BranchNode, bool, error) {
	if len(addrHash) != 32 {
		return nil, false, fmt.Errorf("StorageBranchAt: addrHash must be 32 bytes, got %d", len(addrHash))
	}
	tx, err := r.db.BeginRo(context.Background())
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	c, err := tx.CursorDupSort(rethStoragesTrieV2Table)
	if err != nil {
		return nil, false, err
	}
	defer c.Close()

	subKey, perr := PackNibblesV2(nibblePath)
	if perr != nil {
		return nil, false, fmt.Errorf("StorageBranchAt: pack subkey: %w", perr)
	}
	v, err := c.SeekBothRange(addrHash, subKey)
	if err != nil {
		return nil, false, err
	}
	if v == nil || len(v) < 33 {
		return nil, false, nil
	}
	gotSub := v[:33]
	if !bytes.Equal(gotSub, subKey) {
		return nil, false, nil
	}
	bn, err := DecodeRethBranchNodeCompact(v[33:])
	if err != nil {
		return nil, false, fmt.Errorf("StoragesTrieV2 at addr %x path %x: %w",
			addrHash, nibblePath, err)
	}
	return bn, true, nil
}
