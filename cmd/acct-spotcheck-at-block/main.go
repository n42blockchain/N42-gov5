// acct-spotcheck-at-block reconstructs mainnet's account state at a chosen
// block by harvesting "first prevValue" entries from reth's AccountChangeSets
// in the range (block, rethHead], then walks n42's Account table at the
// same height and reports the first N divergences.
//
// Logic:
//   - reth AccountChangeSets row at block N stores (addr, prev) where
//     prev = state of addr just before processing block N.
//   - For block B, the FIRST appearance of any addr in entries with
//     block > B gives prev = state(addr) at block B (because the addr
//     was untouched between B+1 and the first-appearance block).
//   - Coverage = addrs that were modified at least once in (B, rethHead].
//     For B = 24,998,143 and rethHead ≈ 25,045,128 (47K blocks), this is
//     hundreds of thousands of addrs — plenty for a high-confidence
//     spot-check without rebuilding any trie.
//
// Memory: a single map[addr][]byte. ~1M entries × ~30 B avg ≈ 30 MB heap.
// Walking n42 Account is cursor-streaming, constant memory.
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const (
	n42Acct       = "Account"
	rethAcctChSet = "AccountChangeSets"
)

func n42Cfg(d kv.TableCfg) kv.TableCfg {
	d[n42Acct] = kv.TableCfgItem{}
	return d
}
func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[rethAcctChSet] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

// decodeRethAccount: see cmd/acct-diff-vs-reth for layout.
func decodeRethAccount(v []byte) (nonce uint64, balance uint256.Int, codeHash types.Hash, ok bool) {
	if len(v) == 0 {
		return 0, uint256.Int{}, types.Hash{}, true // empty = account didn't exist
	}
	if len(v) < 2 {
		return 0, uint256.Int{}, types.Hash{}, false
	}
	flags := uint16(v[0]) | (uint16(v[1]) << 8)
	nonceLen := int(flags & 0x0f)
	balanceLen := int((flags >> 4) & 0x3f)
	hasCode := flags&(1<<10) != 0
	expected := 2 + nonceLen + balanceLen
	if hasCode {
		expected += 32
	}
	if len(v) != expected {
		return 0, uint256.Int{}, types.Hash{}, false
	}
	pos := 2
	for i := 0; i < nonceLen; i++ {
		nonce = (nonce << 8) | uint64(v[pos+i])
	}
	pos += nonceLen
	if balanceLen > 0 {
		balance.SetBytes(v[pos : pos+balanceLen])
		pos += balanceLen
	}
	if hasCode {
		copy(codeHash[:], v[pos:pos+32])
	}
	return nonce, balance, codeHash, true
}

var emptyCodeHash = [32]byte{
	0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c,
	0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0,
	0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b,
	0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70,
}

func main() {
	n42Dir := flag.String("n42", `d:\rebuilt-state`, "n42 datadir (Account table)")
	rethDir := flag.String("reth", `d:\reth2k\db`, "reth MDBX path")
	snapBlock := flag.Uint64("block", 24998143, "n42 head block — state snapshot anchor")
	rethEnd := flag.Uint64("reth-end", 25045128, "reth head block")
	maxDiffs := flag.Int("max-diffs", 20, "stop after this many diffs")
	flag.Parse()

	logger := log.New()

	// Phase 1: harvest mainnet ground truth from reth AccountChangeSets.
	rethDB, err := mdbx.NewMDBX(logger).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(rethCfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer rethDB.Close()
	rethTx, _ := rethDB.BeginRo(context.Background())
	defer rethTx.Rollback()
	cur, _ := rethTx.CursorDupSort(rethAcctChSet)
	defer cur.Close()

	// Capture first prevValue per addr in (snapBlock, rethEnd].
	mainnetAt := make(map[[20]byte][]byte, 1_000_000)
	seek := make([]byte, 8)
	binary.BigEndian.PutUint64(seek, *snapBlock+1)
	t0 := time.Now()
	scanned := 0
	k, v, err := cur.Seek(seek)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reth seek:", err)
		os.Exit(1)
	}
	for k != nil {
		if len(k) < 8 {
			k, v, err = cur.Next()
			_ = err
			continue
		}
		blk := binary.BigEndian.Uint64(k[:8])
		if blk > *rethEnd {
			break
		}
		scanned++
		if len(v) >= 20 {
			var addr [20]byte
			copy(addr[:], v[:20])
			if _, ok := mainnetAt[addr]; !ok {
				prev := make([]byte, len(v)-20)
				copy(prev, v[20:])
				mainnetAt[addr] = prev
			}
		}
		k, v, err = cur.Next()
		if err != nil {
			fmt.Fprintln(os.Stderr, "iter:", err)
			break
		}
	}
	fmt.Fprintf(os.Stderr, "Phase 1 done: reth changeset rows scanned=%d unique_addrs=%d elapsed=%v\n",
		scanned, len(mainnetAt), time.Since(t0).Truncate(time.Second))

	// Phase 2: walk n42 Account, look up each addr in mainnetAt, diff.
	n42DB, err := mdbx.NewMDBX(logger).Path(*n42Dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(n42Cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open n42:", err)
		os.Exit(1)
	}
	defer n42DB.Close()
	n42Tx, _ := n42DB.BeginRo(context.Background())
	defer n42Tx.Rollback()
	n42Cur, _ := n42Tx.Cursor(n42Acct)
	defer n42Cur.Close()

	t1 := time.Now()
	checked := 0
	diffs := 0
	n42MissingButMainnetHas := 0
	for k, v, err := n42Cur.First(); k != nil; k, v, err = n42Cur.Next() {
		if err != nil {
			fmt.Fprintln(os.Stderr, "n42 iter:", err)
			break
		}
		if len(k) != 20 {
			continue
		}
		var addr [20]byte
		copy(addr[:], k)
		rethRaw, has := mainnetAt[addr]
		if !has {
			continue // addr not modified in (snapBlock, rethEnd] — no ground truth
		}
		checked++

		var n42Acc account.StateAccount
		if err := n42Acc.DecodeForStorage(v); err != nil {
			fmt.Printf("n42 decode err addr=%x: %v\n", addr, err)
			continue
		}
		rethNonce, rethBal, rethCH, ok := decodeRethAccount(rethRaw)
		if !ok {
			fmt.Printf("reth decode FAIL addr=%x raw=%x\n", addr, rethRaw)
			continue
		}
		// Normalise empty codeHash on both sides (n42 V2 stores explicit
		// emptyCodeHash for EOAs; reth omits the hash slot).
		nCH := n42Acc.CodeHash
		if nCH == (types.Hash{}) {
			copy(nCH[:], emptyCodeHash[:])
		}
		rCH := rethCH
		if rCH == (types.Hash{}) {
			copy(rCH[:], emptyCodeHash[:])
		}
		if n42Acc.Nonce != rethNonce || n42Acc.Balance.Cmp(&rethBal) != 0 || nCH != rCH {
			fmt.Printf("[DIFF @ block %d] addr=%x\n", *snapBlock, addr)
			fmt.Printf("  mainnet nonce=%d bal=%s codeHash=%x\n", rethNonce, rethBal.Hex(), rCH[:])
			fmt.Printf("  n42     nonce=%d bal=%s codeHash=%x\n", n42Acc.Nonce, n42Acc.Balance.Hex(), nCH[:])
			diffs++
			if diffs >= *maxDiffs {
				break
			}
		}
		// Also detect addrs reth had but n42 has nothing (would show up
		// as n42 Account row simply absent — we catch that by checking
		// the inverse below).
		_ = n42MissingButMainnetHas
	}
	fmt.Printf("\n=== summary ===\n")
	fmt.Printf("mainnet ground-truth addrs (from reth changesets): %d\n", len(mainnetAt))
	fmt.Printf("checked (present in both n42 and reth): %d\n", checked)
	fmt.Printf("diffs: %d\n", diffs)
	fmt.Printf("phase2 elapsed: %v\n", time.Since(t1).Truncate(time.Second))

	// Phase 3 (cheap): how many ground-truth addrs are missing from n42 Account?
	missing := 0
	for addr, prev := range mainnetAt {
		if len(prev) == 0 {
			// reth said "account didn't exist at this block" — n42 should also
			// not have a row. If it does, that's a diff too — flag it.
			val, err := n42Tx.GetOne(n42Acct, addr[:])
			if err == nil && val != nil {
				if missing < 5 {
					fmt.Printf("[EXTRA in n42] addr=%x (mainnet had no account here) val=%x\n", addr, val)
				}
				missing++
			}
			continue
		}
		val, err := n42Tx.GetOne(n42Acct, addr[:])
		if err != nil {
			continue
		}
		if val == nil {
			if missing < 5 {
				fmt.Printf("[MISSING in n42] addr=%x mainnet_prev=%x\n", addr, prev)
			}
			missing++
		}
	}
	fmt.Printf("presence-only diffs (missing-from-n42 OR n42-extra-vs-mainnet-empty): %d\n", missing)
}
