// reth-acct-history-bitmap reads reth's AccountsHistory table for a given
// address and dumps the block list (decoded from the roaring bitmap value).
// AccountsHistory key layout: addr(20B) || highest_block(8B BE).
// Value: Rust roaring-rs RoaringTreemap serialized (we just dump raw hex
// here; need to interpret manually).
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const tbl = "AccountsHistory"

func cfg(d kv.TableCfg) kv.TableCfg {
	d[tbl] = kv.TableCfgItem{}
	return d
}

func main() {
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path")
	addrStr := flag.String("addr", "", "20B addr hex")
	flag.Parse()
	addr, _ := hex.DecodeString(*addrStr)
	if len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "addr must be 20B hex")
		os.Exit(2)
	}

	logger := log.New()
	db, _ := mdbx.NewMDBX(logger).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()
	cur, _ := tx.Cursor(tbl)
	defer cur.Close()

	// Seek to addr (any highest_block). Reth keys are (addr || highest_block).
	seek := make([]byte, 28)
	copy(seek[:20], addr)
	// highest_block = 0 — seek will land at the first entry whose key >= seek.
	k, v, _ := cur.Seek(seek)
	count := 0
	for k != nil {
		if len(k) < 28 {
			k, v, _ = cur.Next()
			continue
		}
		// Did we walk past addr's range?
		entryAddr := k[:20]
		if !bytesEqual(entryAddr, addr) {
			break
		}
		highBlock := binary.BigEndian.Uint64(k[20:28])
		fmt.Printf("highest_block=%d valLen=%d val=0x%x\n", highBlock, len(v), v)
		count++
		k, v, _ = cur.Next()
	}
	fmt.Printf("done: %d shard(s)\n", count)
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
