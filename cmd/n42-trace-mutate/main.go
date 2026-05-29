// n42-trace-mutate: ACTUALLY changes ONE EIP-2935 storage slot value, runs
// TrieRootComputer.ComputeRoot in incremental mode (mimicking the per-block
// path that N42 ETH-EL uses), then DUMPS the freshly-written TrieOfStorage
// records for the dirty account. RW tx that we Rollback at end — chaindata
// is NEVER mutated on disk.
//
// Reveals: when a single-slot dirty triggers ComputeRoot, what does Phase 4
// actually write for the depth-1 parent record? Specifically, are the 15
// non-dirty inline child hashes preserved (carry-over from previous record)
// or refreshed (re-read from each child's actual subtree)?
//
// Pair with N42_TRACE_STORCOLL=1 / N42_TRACE_STORCOLL_TOP=1 for emission
// trace; this tool dumps the POST-WRITE state too.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func cfg(d kv.TableCfg) kv.TableCfg {
	d["HashedAccount"] = kv.TableCfgItem{}
	d["HashedStorage"] = kv.TableCfgItem{
		Flags:                     kv.DupSort,
		AutoDupSortKeysConversion: true,
		DupFromLen:                64,
		DupToLen:                  32,
	}
	d["TrieAccount"] = kv.TableCfgItem{}
	d["TrieStorage"] = kv.TableCfgItem{Flags: kv.DupSort}
	return d
}

// EIP-2935 raw address (not addrHash).
var eip2935RawAddr = types.HexToAddress("0x0000F90827F1C53a10cb7A02335B175320002935")

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "chaindata dir (RW with rollback)")
	nib := flag.String("nib", "3", "first nibble of slot to target (one hex char)")
	flag.Parse()

	if len(*nib) != 1 {
		fmt.Fprintln(os.Stderr, "bad -nib")
		os.Exit(1)
	}
	nibByte, err := hex.DecodeString("0" + *nib)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad -nib hex:", err)
		os.Exit(1)
	}
	addrHashHex := "6c9d57be05dd69371c4dd2e871bce6e9f4124236825bb612ee18a45e5675be51"
	addrHash, _ := hex.DecodeString(addrHashHex)

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).
		DirtySpace(uint64(4 * datasize.GB)).
		Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()

	tx, err := db.BeginRw(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin rw:", err)
		os.Exit(1)
	}
	// IMPORTANT: never commit. always rollback.
	defer func() {
		tx.Rollback()
		fmt.Fprintln(os.Stderr, "TX ROLLED BACK — no on-disk mutation")
	}()

	// 1. Find an existing slot under EIP-2935 whose hashed-key starts with nibByte.
	c, _ := tx.Cursor(modules.HashedStorage)
	var targetSlotHash, targetSlotOldVal []byte
	for k, v, e := c.Seek(addrHash); k != nil; k, v, e = c.Next() {
		if e != nil {
			break
		}
		if len(k) != 64 {
			continue
		}
		if hex.EncodeToString(k[:32]) != addrHashHex {
			break
		}
		if (k[32] >> 4) == nibByte[0] {
			targetSlotHash = append([]byte(nil), k[32:64]...)
			targetSlotOldVal = append([]byte(nil), v...)
			break
		}
	}
	c.Close()
	if targetSlotHash == nil {
		fmt.Fprintln(os.Stderr, "no slot under nib", *nib)
		os.Exit(1)
	}
	fmt.Printf("targeting slot %x  oldVal=%x  (acct=EIP-2935, nib=%s)\n", targetSlotHash, targetSlotOldVal, *nib)

	// 2. BEFORE: dump the parent depth-1 record at acc+nib.
	parentKey := append(append([]byte(nil), addrHash...), nibByte[0])
	beforeV, _ := tx.GetOne(modules.TrieOfStorage, parentKey)
	fmt.Printf("BEFORE: TrieOfStorage[%x] = %x  (len=%d)\n", parentKey, beforeV, len(beforeV))
	hs0, ht0, hh0, hashes0, rh0 := trie.UnmarshalTrieNode(beforeV)
	fmt.Printf("        hasState=%04x hasTree=%04x hasHash=%04x nHashes=%d rh=%x\n", hs0, ht0, hh0, len(hashes0)/32, rh0)
	for i := 0; i < len(hashes0)/32; i++ {
		fmt.Printf("        inline child[%2d] = %x\n", i, hashes0[i*32:(i+1)*32])
	}

	// 3. Mutate the slot: pick a different value that's deterministic.
	newVal := []byte{0x42, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11}
	if len(targetSlotOldVal) > 0 && targetSlotOldVal[0] == 0x42 {
		newVal = []byte{0xBE, 0xEF, 0xCA, 0xFE}
	}
	fmt.Printf("MUTATING to newVal=%x\n", newVal)

	// 4. Run ComputeRoot incremental on this one slot.
	//    Build dirty maps: storage[EIP-2935] = {slot: newVal}.
	//    accounts: empty (account itself not changed).
	storage := map[types.Address]map[types.Hash]*uint256.Int{}
	addrEIP := eip2935RawAddr
	storage[addrEIP] = map[types.Hash]*uint256.Int{}
	var slotKey types.Hash
	// We don't have the raw slot — only hashed. ComputeRoot keccaks slot before
	// looking up. To make sure ComputeRoot writes to the SAME HashedStorage key
	// we observed, we need slot such that keccak(slot)==targetSlotHash. We
	// don't have that pre-image; instead we directly write to HashedStorage
	// and add the composite key to RetainList manually, bypassing ComputeRoot's
	// keccak step.
	_ = slotKey

	// Direct write: HashedStorage[addrHash + slotHash] = newVal
	composite := append(append([]byte(nil), addrHash...), targetSlotHash...)
	if err := tx.Put(modules.HashedStorage, composite, newVal); err != nil {
		fmt.Fprintln(os.Stderr, "put HashedStorage:", err)
		os.Exit(1)
	}

	// Build RetainList directly with marker=true on the composite key.
	rl := trie.NewRetainList(0)
	rl.AddKeyWithMarker(composite, true)

	// Manually invoke flushTrieRoot via a TrieRootComputer-like setup.
	// We can't easily call the unexported flushTrieRoot. Instead, mimic
	// MerkleStageIncremental's pattern (it's exported).
	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(true)

	// MerkleStageIncremental builds RetainList from changesets — we built our
	// own, so call the private path through the public surface. The simplest
	// is to call ComputeRoot with a fake account dirty (acc.CodeHash empty so
	// it doesn't matter), passing the real storage map. But ComputeRoot
	// keccaks slot — and we DON'T have the pre-image.
	//
	// Workaround: call commitment.FlushTrieRoot directly via an exported
	// helper... if none exists, fall back to NewFlatDBTrieLoader+CalcTrieRoot
	// with a logging shc that mimics Phase 4 in this very process.
	storeUpd := []struct{ k, v []byte }{}
	shc := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := append(append([]byte(nil), accWithInc...), keyHex...)
		if len(k) == 0 {
			return nil
		}
		if hasState == 0 {
			storeUpd = append(storeUpd, struct{ k, v []byte }{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		storeUpd = append(storeUpd, struct{ k, v []byte }{k, append([]byte{}, v...)})
		return nil
	}

	fmt.Println("---- running CalcTrieRoot incremental ----")
	loader := trie.NewFlatDBTrieLoader("trace-mutate", rl, nil, shc, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "CalcTrieRoot:", err)
		os.Exit(1)
	}
	fmt.Printf("computed root = %s\n", root.Hex())

	// Apply storeUpd to TrieOfStorage with Delete-then-Put, mimicking Phase 4.
	for _, u := range storeUpd {
		if err := tx.Delete(modules.TrieOfStorage, u.k); err != nil {
			fmt.Fprintln(os.Stderr, "del:", err)
			os.Exit(1)
		}
		if u.v != nil {
			if err := tx.Put(modules.TrieOfStorage, u.k, u.v); err != nil {
				fmt.Fprintln(os.Stderr, "put:", err)
				os.Exit(1)
			}
		}
	}

	// 5. AFTER: dump the parent depth-1 record at acc+nib.
	afterV, _ := tx.GetOne(modules.TrieOfStorage, parentKey)
	fmt.Printf("\nAFTER:  TrieOfStorage[%x] = %x  (len=%d)\n", parentKey, afterV, len(afterV))
	hs1, ht1, hh1, hashes1, rh1 := trie.UnmarshalTrieNode(afterV)
	fmt.Printf("        hasState=%04x hasTree=%04x hasHash=%04x nHashes=%d rh=%x\n", hs1, ht1, hh1, len(hashes1)/32, rh1)
	for i := 0; i < len(hashes1)/32; i++ {
		marker := ""
		if i < len(hashes0)/32 {
			if string(hashes0[i*32:(i+1)*32]) != string(hashes1[i*32:(i+1)*32]) {
				marker = "  <<< CHANGED"
			}
		}
		fmt.Printf("        inline child[%2d] = %x%s\n", i, hashes1[i*32:(i+1)*32], marker)
	}

	fmt.Println("\nstoreUpd count:", len(storeUpd))
	for i, u := range storeUpd {
		if i < 20 {
			fmt.Printf("  upd[%d] key_suffix=%x v_len=%d\n", i, u.k[32:], len(u.v))
		}
	}
}
