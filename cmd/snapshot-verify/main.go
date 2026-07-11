// snapshot-verify checks an exported snapshot segment against its source
// MDBX: it rescans the source tables and, for every row, looks the key up in
// the snapshot (via internal/ethel/snapshotreader, which auto-detects the v1
// monolithic and v2 sharded layouts) and compares the decoded value.
// Accounts are compared semantically (nonce / balance / codeHash after
// codedict resolution) because the raw bytes embed layout-specific dict ids;
// storage values are compared byte-for-byte.
//
// Intended uses: weekly-regen sanity gate, and v1↔v2 equivalence checks.
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel/snapshotreader"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

var emptyCodeHash = crypto.Keccak256Hash(nil)

func main() {
	dbPath := flag.String("db", "d:/reth2k/db", "Source MDBX path")
	snapDir := flag.String("snap", "", "Snapshot directory to verify (required)")
	accPrefix := flag.String("acc-prefix", "accounts", "Accounts segment prefix")
	stoPrefix := flag.String("sto-prefix", "storage", "Storage segment prefix")
	accountTable := flag.String("account-table", "PlainAccountState", "MDBX table for accounts")
	storageTable := flag.String("storage-table", "PlainStorageState", "MDBX table for storage (dup-sort)")
	limit := flag.Uint64("limit", 0, "Rows to verify per table (0 = all)")
	flag.Parse()
	if *snapDir == "" {
		fatal("--snap is required")
	}

	db, err := mdbx.NewMDBX(log.New()).
		Path(*dbPath).Label(kv.ChainDB).PageSize(4096).
		MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(func(d kv.TableCfg) kv.TableCfg {
			d[*accountTable] = kv.TableCfgItem{}
			d[*storageTable] = kv.TableCfgItem{}
			return d
		}).Open(context.Background())
	if err != nil {
		fatal("open mdbx: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fatal("begin tx: %v", err)
	}
	defer tx.Rollback()

	seg, err := snapshotreader.OpenSegment(*snapDir, *accPrefix, *stoPrefix)
	if err != nil {
		fatal("open segment: %v", err)
	}
	defer seg.Close()

	t0 := time.Now()
	nAcc := verifyAccounts(tx, *accountTable, seg, *limit)
	nSto := verifyStorage(tx, *storageTable, seg, *limit)
	fmt.Printf("OK: %d accounts + %d storage rows verified against %s in %s\n",
		nAcc, nSto, *snapDir, time.Since(t0).Truncate(time.Millisecond))
}

func verifyAccounts(tx kv.Tx, table string, seg *snapshotreader.Segment, limit uint64) uint64 {
	c, err := tx.Cursor(table)
	if err != nil {
		fatal("cursor: %v", err)
	}
	defer c.Close()
	var n uint64
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			fatal("iter: %v", err)
		}
		var addr [20]byte
		copy(addr[:], k)
		raw, ok := seg.AccountValueRaw(addr)
		if !ok {
			fatal("account %x: missing from snapshot", k)
		}
		got, err := seg.DecodeAccount(raw)
		if err != nil {
			fatal("account %x: decode: %v", k, err)
		}
		nonce, balance, codeHash, hasCode, ok := decodeRethCompact(v)
		if !ok {
			fatal("account %x: malformed source value %x", k, v)
		}
		if got.Nonce != nonce {
			fatal("account %x: nonce %d != %d", k, got.Nonce, nonce)
		}
		if got.Balance.Cmp(&balance) != 0 {
			fatal("account %x: balance %s != %s", k, got.Balance.String(), balance.String())
		}
		wantHash := emptyCodeHash
		if hasCode {
			copy(wantHash[:], codeHash[:])
		}
		if got.CodeHash != wantHash {
			fatal("account %x: codeHash %x != %x", k, got.CodeHash, wantHash)
		}
		n++
		if limit > 0 && n >= limit {
			break
		}
		if n%10_000_000 == 0 {
			fmt.Printf("  ... %dM accounts verified\n", n/1_000_000)
		}
	}
	return n
}

func verifyStorage(tx kv.Tx, table string, seg *snapshotreader.Segment, limit uint64) uint64 {
	c, err := tx.Cursor(table)
	if err != nil {
		fatal("cursor: %v", err)
	}
	defer c.Close()
	var n uint64
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			fatal("iter: %v", err)
		}
		if len(v) < 32 {
			continue
		}
		var addr [20]byte
		var slot [32]byte
		copy(addr[:], k)
		copy(slot[:], v[:32])
		got, ok := seg.StorageValue(addr, slot)
		if !ok {
			fatal("storage %x/%x: missing from snapshot", k, v[:32])
		}
		if !bytes.Equal(got, v[32:]) {
			fatal("storage %x/%x: value %x != %x", k, v[:32], got, v[32:])
		}
		n++
		if limit > 0 && n >= limit {
			break
		}
		if n%20_000_000 == 0 {
			fmt.Printf("  ... %dM storage rows verified\n", n/1_000_000)
		}
	}
	return n
}

// decodeRethCompact mirrors cmd/reth-snapshot-export (see there for layout).
func decodeRethCompact(v []byte) (nonce uint64, balance uint256.Int, codeHash [32]byte, hasCode, ok bool) {
	if len(v) < 2 {
		return
	}
	flags := uint16(v[0]) | uint16(v[1])<<8
	nonceLen := int(flags & 0x0f)
	balLen := int((flags >> 4) & 0x3f)
	hasCode = (flags>>10)&1 == 1
	need := 2 + nonceLen + balLen
	if hasCode {
		need += 32
	}
	if balLen > 32 || nonceLen > 8 || len(v) != need {
		hasCode = false
		return
	}
	p := 2
	if nonceLen > 0 {
		var nb [8]byte
		copy(nb[8-nonceLen:], v[p:p+nonceLen])
		nonce = binary.BigEndian.Uint64(nb[:])
	}
	p += nonceLen
	if balLen > 0 {
		var bb [32]byte
		copy(bb[32-balLen:], v[p:p+balLen])
		balance.SetBytes(bb[:])
	}
	p += balLen
	if hasCode {
		copy(codeHash[:], v[p:p+32])
	}
	return nonce, balance, codeHash, hasCode, true
}

func fatal(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
