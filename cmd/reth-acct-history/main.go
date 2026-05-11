// reth-acct-history walks reth's AccountChangeSets for a given address and
// prints (block, prevValue) for each entry. Used as ground truth against
// n42's addr-history output to find the first block where n42 missed an
// acctcs entry that mainnet had (= record-side bug site).
//
// Reth AccountChangeSets layout (DupSort):
//
//	Key:   block(8B BE)
//	Value: addr(20B) || compact-encoded prev account
//
// The "compact-encoded prev account" is reth's pre-block Account; absence
// (zero-byte value after the 20B addr) means the account didn't exist
// before this block.
package main

import (
	"bytes"
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

const tblAcctCS = "AccountChangeSets"

func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[tblAcctCS] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func main() {
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path")
	addrStr := flag.String("addr", "", "20B addr hex (no 0x prefix)")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive)")
	toBlock := flag.Uint64("to", 24998143, "end block (inclusive)")
	flag.Parse()

	if *addrStr == "" {
		fmt.Fprintln(os.Stderr, "--addr required")
		os.Exit(2)
	}
	addrBytes, err := hex.DecodeString(*addrStr)
	if err != nil || len(addrBytes) != 20 {
		fmt.Fprintln(os.Stderr, "--addr must be 20B hex")
		os.Exit(2)
	}

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*rethDir).Label(kv.ChainDB).PageSize(4096).
		MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(rethCfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tx:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	// Flat cursor.Next() walks every key,value pair across all DupSort
	// values (verified against reth-cs-dump-block — 245 entries in block
	// 14698850 are all visible via cursor.Next()). Per-block SeekExact
	// for 25M blocks is unusably slow; flat scan is O(table-size).
	cur, err := tx.CursorDupSort(tblAcctCS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer cur.Close()

	seek := make([]byte, 8)
	binary.BigEndian.PutUint64(seek, *fromBlock)
	k, v, err := cur.Seek(seek)
	if err != nil {
		fmt.Fprintln(os.Stderr, "seek:", err)
		os.Exit(1)
	}

	fmt.Printf("scanning reth AccountChangeSets [%d, %d] for addr=%s\n", *fromBlock, *toBlock, *addrStr)
	count := 0
	for k != nil {
		if len(k) < 8 {
			k, v, err = cur.Next()
			_ = err
			continue
		}
		blk := binary.BigEndian.Uint64(k[:8])
		if blk > *toBlock {
			break
		}
		if blk < *fromBlock {
			k, v, err = cur.Next()
			_ = err
			continue
		}
		if len(v) >= 20 && bytes.Equal(v[:20], addrBytes) {
			prev := v[20:]
			fmt.Printf("block=%d prevLen=%d prev=0x%x\n", blk, len(prev), prev)
			count++
		}
		k, v, err = cur.Next()
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			break
		}
	}
	fmt.Printf("done: %d match(es)\n", count)
}
