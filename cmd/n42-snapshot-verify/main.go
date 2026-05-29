// n42-snapshot-verify is the light-weight P1 E2E check: it reads a snapshot
// produced by cmd/reth-snapshot-export and confirms that snapshotreader returns
// the SAME account/storage values as the source reth DB. This validates the
// end-to-end format contract (writer ↔ reader) on real data without a full
// node + catch-up.
//
//	n42-snapshot-verify --reth d:/reth2k/db --snap D:/n42-snapshot-mini \
//	    --acc-prefix accounts.0-25188781 --sto-prefix storage.0-25188781 --n 50000
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel/snapshotreader"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	rethAcct = "PlainAccountState"
	rethSto  = "PlainStorageState"
)

func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[rethAcct] = kv.TableCfgItem{}
	d[rethSto] = kv.TableCfgItem{}
	return d
}

// decodeRethAccount: reth Compact account -> (nonce, balance, codeHash).
func decodeRethAccount(v []byte) (nonce uint64, balance uint256.Int, codeHash types.Hash, ok bool) {
	if len(v) < 2 {
		return
	}
	flags := uint16(v[0]) | uint16(v[1])<<8
	nonceLen := int(flags & 0x0f)
	balLen := int((flags >> 4) & 0x3f)
	hasHash := (flags>>10)&1 == 1
	want := 2 + nonceLen + balLen
	if hasHash {
		want += 32
	}
	if len(v) != want {
		return
	}
	p := 2
	var nb [8]byte
	if nonceLen > 0 {
		copy(nb[8-nonceLen:], v[p:p+nonceLen])
		nonce = binary.BigEndian.Uint64(nb[:])
	}
	p += nonceLen
	var bb [32]byte
	if balLen > 0 {
		copy(bb[32-balLen:], v[p:p+balLen])
	}
	balance.SetBytes(bb[:])
	p += balLen
	if hasHash {
		copy(codeHash[:], v[p:p+32])
	}
	return nonce, balance, codeHash, true
}

func main() {
	rethDir := flag.String("reth", `d:/reth2k/db`, "reth db (read-only)")
	snapDir := flag.String("snap", `D:/n42-snapshot-mini`, "snapshot dir")
	accPrefix := flag.String("acc-prefix", "accounts.0-25188781", "accounts segment prefix")
	stoPrefix := flag.String("sto-prefix", "storage.0-25188781", "storage segment prefix")
	nCheck := flag.Int("n", 50000, "max entries to verify per table")
	flag.Parse()

	logger := log.New()
	rdb, err := mdbx.NewMDBX(logger).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(rethCfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer rdb.Close()
	rtx, _ := rdb.BeginRo(context.Background())
	defer rtx.Rollback()

	seg, err := snapshotreader.OpenSegment(*snapDir, *accPrefix, *stoPrefix)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open snapshot:", err)
		os.Exit(1)
	}
	defer seg.Close()
	fmt.Printf("snapshot opened: %s (%s / %s), codedict=%d\n", *snapDir, *accPrefix, *stoPrefix, seg.CodeDictLen())

	// --- accounts ---
	var accChecked, accMiss, accBad int
	ac, _ := rtx.Cursor(rethAcct)
	for k, v, e := ac.First(); k != nil && accChecked < *nCheck; k, v, e = ac.Next() {
		if e != nil {
			break
		}
		if len(k) != 20 {
			continue
		}
		rn, rb, rch, ok := decodeRethAccount(v)
		if !ok {
			continue
		}
		if rch == (types.Hash{}) {
			rch = crypto.Keccak256Hash(nil)
		}
		// reth-snapshot-export skips all-zero (empty) accounts
		if rn == 0 && rb.IsZero() && rch == crypto.Keccak256Hash(nil) {
			continue
		}
		var addr [20]byte
		copy(addr[:], k)
		raw, found := seg.AccountValueRaw(addr)
		if !found {
			accMiss++
			if accMiss <= 5 {
				fmt.Printf("  ACC MISS %x\n", addr[:6])
			}
			accChecked++
			continue
		}
		a, derr := seg.DecodeAccount(raw)
		if derr != nil {
			accBad++
			accChecked++
			continue
		}
		if a.Nonce != rn || a.Balance.Cmp(&rb) != 0 || a.CodeHash != rch {
			accBad++
			if accBad <= 5 {
				fmt.Printf("  ACC BAD %x: snap(n=%d bal=%s ch=%x) reth(n=%d bal=%s ch=%x)\n",
					addr[:6], a.Nonce, a.Balance.String(), a.CodeHash[:6], rn, rb.String(), rch[:6])
			}
		}
		accChecked++
	}
	ac.Close()

	// --- storage ---
	var stoChecked, stoMiss, stoBad int
	sc, _ := rtx.Cursor(rethSto)
	for k, v, e := sc.First(); k != nil && stoChecked < *nCheck; k, v, e = sc.Next() {
		if e != nil {
			break
		}
		if len(k) != 20 || len(v) < 32 {
			continue
		}
		var addr [20]byte
		copy(addr[:], k)
		var slot [32]byte
		copy(slot[:], v[:32])
		rethVal := v[32:]
		if len(rethVal) == 0 {
			continue
		}
		got, found := seg.StorageValue(addr, slot)
		if !found {
			stoMiss++
			if stoMiss <= 5 {
				fmt.Printf("  STO MISS %x/%x\n", addr[:6], slot[:6])
			}
			stoChecked++
			continue
		}
		if string(got) != string(rethVal) {
			stoBad++
			if stoBad <= 5 {
				fmt.Printf("  STO BAD %x/%x: snap=%x reth=%x\n", addr[:6], slot[:6], got, rethVal)
			}
		}
		stoChecked++
	}
	sc.Close()

	fmt.Printf("\n=== RESULT ===\n")
	fmt.Printf("accounts: checked=%d miss=%d bad=%d\n", accChecked, accMiss, accBad)
	fmt.Printf("storage:  checked=%d miss=%d bad=%d\n", stoChecked, stoMiss, stoBad)
	if accMiss == 0 && accBad == 0 && stoMiss == 0 && stoBad == 0 {
		fmt.Printf(">>> PASS — snapshotreader returns exactly the reth source values\n")
	} else {
		fmt.Printf(">>> FAIL — see MISS/BAD above\n")
		os.Exit(2)
	}
}
