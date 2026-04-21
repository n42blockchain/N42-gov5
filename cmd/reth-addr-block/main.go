// reth-addr-block: count rows and list sample slots for a given (block, addr)
// in reth StorageChangeSets.
package main

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const tblStorCS = "StorageChangeSets"

func tableCfg(d kv.TableCfg) kv.TableCfg {
	d[tblStorCS] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

func main() {
	dbPath := flag.String("db", `d:\reth2k\db`, "reth MDBX path")
	block := flag.Uint64("block", 0, "block")
	addrHex := flag.String("addr", "", "20B addr hex")
	sample := flag.Int("sample", 20, "rows to print")
	flag.Parse()

	addr, err := hex.DecodeString(strings.TrimPrefix(*addrHex, "0x"))
	if err != nil || len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr")
		os.Exit(1)
	}

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		WithTableCfg(tableCfg).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "tx:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	c, err := tx.Cursor(tblStorCS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer c.Close()

	// Seek to (block, addr).
	seekKey := make([]byte, 28)
	binary.BigEndian.PutUint64(seekKey[:8], *block)
	copy(seekKey[8:], addr)

	total := 0
	nonzero := 0
	shown := 0
	for k, v, err := c.Seek(seekKey); k != nil; k, v, err = c.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			break
		}
		if len(k) < 28 {
			continue
		}
		blk := binary.BigEndian.Uint64(k[:8])
		if blk != *block {
			break
		}
		if string(k[8:28]) != string(addr) {
			break // cursor sorted: once addr changes we're done for this block
		}
		total++
		isZero := len(v) == 32
		if !isZero {
			nonzero++
		}
		if shown < *sample {
			fmt.Printf("  slot=%x preVal_raw=%x (%s)\n", v[:32], v,
				map[bool]string{true: "zero", false: "non-zero"}[!isZero])
			shown++
		}
	}
	fmt.Printf("blk=%d addr=%x total_rows=%d nonzero_preVal=%d zero_preVal=%d\n",
		*block, addr, total, nonzero, total-nonzero)
}
