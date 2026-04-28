// reth-slot-history scans reth's StorageChangeSets for a specific
// (addr, slot) pair across a block range and prints each matching entry.
//
// Reth layout:
//   Table:  StorageChangeSets  DUPSORT
//   Key:    28B = 8B BE block_number || 20B address
//   Value:  variable: 32B slot || compact-encoded U256 (pre-block value)
//
// With --addr "" the scan matches on slot alone (any addr).
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

func tableCfg(defaults kv.TableCfg) kv.TableCfg {
	defaults[tblStorCS] = kv.TableCfgItem{Flags: kv.DupSort}
	return defaults
}

func main() {
	dbPath := flag.String("db", `d:\reth2k\db`, "reth MDBX path")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive)")
	toBlock := flag.Uint64("to", 0, "end block (inclusive, 0 = unbounded)")
	addrHex := flag.String("addr", "", "20-byte address (hex, empty = any)")
	slotHex := flag.String("slot", "", "32-byte slot (hex)")
	flag.Parse()

	var addr []byte
	if *addrHex != "" {
		a, err := hex.DecodeString(strings.TrimPrefix(*addrHex, "0x"))
		if err != nil || len(a) != 20 {
			fmt.Fprintln(os.Stderr, "bad addr:", err, "len:", len(a))
			os.Exit(1)
		}
		addr = a
	}
	// Empty slot = match every slot (used for "show me everything this
	// address touched in the range"). When set, must be exactly 32 bytes.
	var slot []byte
	if *slotHex != "" {
		s, err := hex.DecodeString(strings.TrimPrefix(*slotHex, "0x"))
		if err != nil || len(s) != 32 {
			fmt.Fprintln(os.Stderr, "bad slot:", err, "len:", len(s))
			os.Exit(1)
		}
		slot = s
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

	cursor, err := tx.Cursor(tblStorCS)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer cursor.Close()

	// Seek by block (addr-agnostic so "any addr" works).
	seekKey := make([]byte, 8)
	binary.BigEndian.PutUint64(seekKey, *fromBlock)

	slotLabel := "any"
	if slot != nil {
		slotLabel = fmt.Sprintf("%x", slot)
	}
	fmt.Printf("scanning reth [%d, %d] addr=%s slot=%s\n", *fromBlock, *toBlock, *addrHex, slotLabel)
	matches := 0
	var scanned int
	for k, v, err := cursor.Seek(seekKey); k != nil; k, v, err = cursor.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			os.Exit(1)
		}
		scanned++
		if len(k) < 28 || len(v) < 32 {
			continue
		}
		blk := binary.BigEndian.Uint64(k[:8])
		if *toBlock > 0 && blk > *toBlock {
			break
		}
		if addr != nil && string(k[8:28]) != string(addr) {
			continue
		}
		if slot != nil && string(v[:32]) != string(slot) {
			continue
		}
		// Reth's `Compact for U256` writes BE-minimal (the trailing N bytes
		// of the 32-byte BE representation, leading zero bytes trimmed).
		// Already in our canonical form — copy as-is.
		var preHex string
		if len(v) > 32 {
			preHex = strings.TrimLeft(hex.EncodeToString(v[32:]), "0")
			if preHex == "" {
				preHex = "0"
			}
		} else {
			preHex = "0"
		}
		fmt.Printf("blk=%d addr=%x slot=%x preVal=0x%s (raw v=%x)\n", blk, k[8:28], v[:32], preHex, v)
		matches++
	}
	fmt.Printf("done: %d match(es), %d rows scanned\n", matches, scanned)
}
