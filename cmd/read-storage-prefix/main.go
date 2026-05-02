// read-storage-prefix dumps every Storage row whose key starts with a given
// 20-byte address. Used to verify whether a SELFDESTRUCT'd contract's
// rows actually got purged from MDBX (or whether they orphaned).
//
// modules.Storage on N42 is AutoDupSort (key=20B addr, value=32B slot ||
// raw bytes). The cursor surfaces a synthetic 52B key; this tool prints
// it so you can see exactly what's left.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func tableCfg(d kv.TableCfg) kv.TableCfg {
	d[modules.Storage] = kv.TableCfgItem{Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 52, DupToLen: 20}
	return d
}

func main() {
	dbPath := flag.String("db", `d:\N42-1703m\chain\mdbx.dat`, "n42 MDBX path (file or dir)")
	addrHex := flag.String("addr", "", "20-byte address (hex)")
	max := flag.Int("max", 50, "max rows to print")
	flag.Parse()

	addr, err := hex.DecodeString(strings.TrimPrefix(*addrHex, "0x"))
	if err != nil || len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr:", err, "len:", len(addr))
		os.Exit(1)
	}

	// MDBX wants the directory of the .dat file.
	path := *dbPath
	if strings.HasSuffix(strings.ToLower(path), ".dat") {
		path = path[:len(path)-len("\\mdbx.dat")]
		if !strings.HasSuffix(strings.ToLower(*dbPath), ".dat") {
			path = *dbPath
		}
	}
	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(path).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		Accede().
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

	c, err := tx.Cursor(modules.Storage)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer c.Close()

	fmt.Printf("scanning Storage table for prefix=%x\n", addr)
	count := 0
	for k, v, err := c.Seek(addr); k != nil; k, v, err = c.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			break
		}
		if len(k) < 20 || !strings.EqualFold(hex.EncodeToString(k[:20]), hex.EncodeToString(addr)) {
			break
		}
		if count >= *max {
			fmt.Printf("  ...truncated after %d rows\n", *max)
			break
		}
		fmt.Printf("  k=%x v=%x\n", k, v)
		count++
	}
	fmt.Printf("total rows for addr=%x: %d\n", addr, count)
}
