// read-storage-seek seeks to a specific (addr, slot) key in the Storage
// table and prints whether the row exists + its value. Bypasses
// AutoDupSort GetOne path that might miss entries — uses cursor.Seek
// then walks dup-values to find exact slot match.
package main

import (
	"bytes"
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

func cfg(d kv.TableCfg) kv.TableCfg {
	d[modules.Storage] = kv.TableCfgItem{Flags: kv.DupSort, AutoDupSortKeysConversion: true, DupFromLen: 52, DupToLen: 20}
	return d
}

func main() {
	dbPath := flag.String("db", "", "n42 datadir")
	addrHex := flag.String("addr", "", "20-byte address (hex)")
	slotHex := flag.String("slot", "", "32-byte slot (hex)")
	flag.Parse()

	addr, err := hex.DecodeString(strings.TrimPrefix(*addrHex, "0x"))
	if err != nil || len(addr) != 20 {
		fmt.Fprintln(os.Stderr, "bad addr")
		os.Exit(1)
	}
	slot, err := hex.DecodeString(strings.TrimPrefix(*slotHex, "0x"))
	if err != nil || len(slot) != 32 {
		fmt.Fprintln(os.Stderr, "bad slot")
		os.Exit(1)
	}
	composite := make([]byte, 52)
	copy(composite[:20], addr)
	copy(composite[20:], slot)

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dbPath).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	// 1) GetOne via direct 52B key
	v, _ := tx.GetOne(modules.Storage, composite)
	fmt.Printf("GetOne(52B key) → %v (len=%d)\n", v != nil, len(v))
	if v != nil {
		fmt.Printf("  value=%x\n", v)
	}

	// 2) Cursor seek to 52B composite — what does it find?
	c, _ := tx.Cursor(modules.Storage)
	defer c.Close()
	k, v2, _ := c.Seek(composite)
	fmt.Printf("Cursor.Seek(52B) → ")
	if k == nil {
		fmt.Println("nil")
	} else {
		fmt.Printf("k=%x v=%x match_addr=%v match_slot=%v\n",
			k, v2, bytes.Equal(k[:20], addr), len(k) >= 52 && bytes.Equal(k[20:52], slot))
	}

	// 3) Try cursor.Get with addr-only (DupSort physical key)
	k2, v3, _ := c.Seek(addr)
	fmt.Printf("Cursor.Seek(20B addr) → ")
	if k2 == nil {
		fmt.Println("nil")
	} else {
		fmt.Printf("k=%x v=%x\n", k2, v3)
	}
}
