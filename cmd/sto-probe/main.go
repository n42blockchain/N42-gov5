// sto-probe — read a specific storage slot from N42's Storage table
// (which is DupSort+AutoConv per lib/kv/tables.go) and dump the value.
// Used to verify Storage migration is writing entries.

package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func n42Cfg(d kv.TableCfg) kv.TableCfg {
	d["Storage"] = kv.TableCfgItem{
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                52,
		DupToLen:                  20,
	}
	return d
}

func main() {
	dir := flag.String("n42", `D:/N42-eth1177-test/chaindata`, "n42 chaindata path")
	addr := flag.String("addr", "0x0000000000000000000000000000000000000001", "address")
	slot := flag.String("slot", "0x0000000000000000000000000000000000000000000000000000000000000000", "32-byte slot key (hex)")
	count := flag.Int("count", 5, "how many entries to seek from addr (for sweep mode)")
	flag.Parse()

	addrStripped := *addr
	if len(addrStripped) >= 2 && addrStripped[:2] == "0x" {
		addrStripped = addrStripped[2:]
	}
	a := types.HexToAddress("0x" + addrStripped).Bytes()

	slotStripped := *slot
	if len(slotStripped) >= 2 && slotStripped[:2] == "0x" {
		slotStripped = slotStripped[2:]
	}
	slotBytes, err := hex.DecodeString(slotStripped)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode slot:", err)
		os.Exit(1)
	}

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(n42Cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, _ := db.BeginRo(context.Background())
	defer tx.Rollback()

	composite := make([]byte, 52)
	copy(composite[:20], a)
	copy(composite[20:], slotBytes)

	// Direct Get
	v, _ := tx.GetOne("Storage", composite)
	fmt.Printf("addr  = 0x%x\n", a)
	fmt.Printf("slot  = 0x%x\n", slotBytes)
	if v == nil {
		fmt.Println("value = ABSENT")
	} else {
		fmt.Printf("value = 0x%x\n", v)
	}

	// Cursor sweep — show first N entries starting from this addr
	fmt.Println("\n--- sweep first", *count, "entries from addr (Cursor.Seek + Next) ---")
	c, _ := tx.Cursor("Storage")
	defer c.Close()
	seen := 0
	for k, val, err := c.Seek(a); k != nil && seen < *count; k, val, err = c.Next() {
		if err != nil {
			break
		}
		fmt.Printf("  k=%x  v=%x\n", k, val)
		seen++
	}
	if seen == 0 {
		fmt.Println("  (no entries found for this addr)")
	}
}
