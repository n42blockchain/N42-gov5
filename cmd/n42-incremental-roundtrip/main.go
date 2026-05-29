// n42-incremental-roundtrip: decisive experiment for #150.
//
// In a single RW tx (ROLLED BACK at end — no on-disk mutation):
//  1. full-retain rebuild the EIP-2935 storage subtree + write it back
//     (delete-then-put) → establishes a SELF-CONSISTENT cached TrieOfStorage
//     for that account.
//  2. dump depth-1 record '3' (rootHash_1, inline_1) — should be consistent.
//  3. mutate ONE slot under a chosen nibble.
//  4. INCREMENTAL pass: RetainList = {that one slot}, run CalcTrieRoot over
//     the just-written cached trie, capture emitted nodes, Phase-4 write-back
//     (delete-then-put) — exactly mimics MerkleStageIncremental/ComputeRoot.
//  5. dump depth-1 record '3' (rootHash_2, inline_2).
//  6. full-retain rebuild '3' again = GROUND TRUTH after mutation
//     (rootHash_gt, inline_gt).
//  7. compare incremental result (step 5) vs ground truth (step 6).
//
// If inline_2 == inline_gt but rootHash_2 != rootHash_gt → REPRODUCES the
// bug: the incremental write updates inline children but leaves rootHash
// stale. That pins the fix to gen_struct_step / hashbuilder emit. If they
// match, the incremental path is correct and the on-disk stale is a pure
// historical (bootstrap) artifact → rebuild-trie once suffices, no code fix.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules"
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

type kvPair struct{ k, v []byte }

// runLoader runs CalcTrieRoot with the given RetainList over the account's
// storage, capturing emitted storage-trie nodes. Returns the emitted updates
// and the computed root.
func runLoader(tx kv.Tx, rl *trie.RetainList) ([]kvPair, error) {
	var upd []kvPair
	shc := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		k := append(append([]byte(nil), accWithInc...), keyHex...)
		if len(k) == 0 {
			return nil
		}
		if hasState == 0 {
			upd = append(upd, kvPair{k, nil})
			return nil
		}
		buf := make([]byte, len(hashes)+len(rootHash)+6)
		v := trie.MarshalTrieNode(hasState, hasTree, hasHash, hashes, rootHash, buf)
		upd = append(upd, kvPair{k, append([]byte{}, v...)})
		return nil
	}
	loader := trie.NewFlatDBTrieLoader("roundtrip", rl, nil, shc, false)
	_, err := loader.CalcTrieRoot(tx, nil)
	return upd, err
}

// applyPhase4 mimics TrieRootComputer Phase 4: delete-then-put each update.
func applyPhase4(tx kv.RwTx, upd []kvPair) error {
	for _, u := range upd {
		if err := tx.Delete(modules.TrieOfStorage, u.k); err != nil {
			return err
		}
		if u.v != nil {
			if err := tx.Put(modules.TrieOfStorage, u.k, u.v); err != nil {
				return err
			}
		}
	}
	return nil
}

func fullRetainRL(tx kv.Tx, ah []byte) (*trie.RetainList, int, error) {
	rl := trie.NewRetainList(0)
	c, err := tx.Cursor(modules.HashedStorage)
	if err != nil {
		return nil, 0, err
	}
	defer c.Close()
	n := 0
	for k, _, e := c.Seek(ah); k != nil; k, _, e = c.Next() {
		if e != nil {
			return nil, 0, e
		}
		if len(k) != 64 {
			if len(k) >= 32 && !bytes.Equal(k[:32], ah) {
				break
			}
			continue
		}
		if !bytes.Equal(k[:32], ah) {
			break
		}
		rl.AddKeyWithMarker(append([]byte(nil), k...), true)
		n++
	}
	return rl, n, nil
}

func dumpRec(tx kv.Tx, ah []byte, pathNib byte) (rootHash string, inline []string) {
	key := append(append([]byte(nil), ah...), pathNib)
	v, _ := tx.GetOne(modules.TrieOfStorage, key)
	if len(v) == 0 {
		return "<absent>", nil
	}
	_, _, _, hashes, rh := trie.UnmarshalTrieNode(v)
	rootHash = hex.EncodeToString(rh)
	for i := 0; i < len(hashes)/32; i++ {
		inline = append(inline, hex.EncodeToString(hashes[i*32:i*32+8]))
	}
	return rootHash, inline
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "chaindata (RW+rollback)")
	nib := flag.String("nib", "3", "nibble subtree to mutate under (one hex char)")
	flag.Parse()

	nibByte, err := hex.DecodeString("0" + *nib)
	if err != nil || len(*nib) != 1 {
		fmt.Fprintln(os.Stderr, "bad -nib")
		os.Exit(1)
	}
	pathNib := nibByte[0]
	addrHashHex := "6c9d57be05dd69371c4dd2e871bce6e9f4124236825bb612ee18a45e5675be51"
	ah, _ := hex.DecodeString(addrHashHex)

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).
		DirtySpace(uint64(4 * datasize.GB)).Accede().
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
	defer func() {
		tx.Rollback()
		fmt.Fprintln(os.Stderr, "TX ROLLED BACK — no on-disk mutation")
	}()

	// STEP 1: full-retain rebuild + write back (establish consistent cached).
	rlFull, n, err := fullRetainRL(tx, ah)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fullRetainRL:", err)
		os.Exit(1)
	}
	fmt.Printf("STEP1 full-retain rebuild (%d leaves)\n", n)
	upd1, err := runLoader(tx, rlFull)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loader1:", err)
		os.Exit(1)
	}
	// First clear the whole EIP-2935 subtree so write-back is clean.
	{
		c, _ := tx.Cursor(modules.TrieOfStorage)
		var toDel [][]byte
		for k, _, e := c.Seek(ah); k != nil && len(k) >= 32 && bytes.Equal(k[:32], ah); k, _, e = c.Next() {
			if e != nil {
				break
			}
			toDel = append(toDel, append([]byte(nil), k...))
		}
		c.Close()
		for _, k := range toDel {
			tx.Delete(modules.TrieOfStorage, k)
		}
	}
	if err := applyPhase4(tx, upd1); err != nil {
		fmt.Fprintln(os.Stderr, "phase4-1:", err)
		os.Exit(1)
	}

	// STEP 2: dump '3' record (should be self-consistent now).
	rh1, in1 := dumpRec(tx, ah, pathNib)
	fmt.Printf("STEP2 after full rebuild: path='%s' rootHash=%s inline[0..3]=%v\n", *nib, rh1[:16], in1[:4])

	// STEP 3: mutate one slot under nibByte.
	c, _ := tx.Cursor(modules.HashedStorage)
	var slotKey, oldVal []byte
	for k, v, e := c.Seek(ah); k != nil; k, v, e = c.Next() {
		if e != nil {
			break
		}
		if len(k) != 64 || !bytes.Equal(k[:32], ah) {
			if len(k) >= 32 && !bytes.Equal(k[:32], ah) {
				break
			}
			continue
		}
		if (k[32] >> 4) == pathNib {
			slotKey = append([]byte(nil), k...)
			oldVal = append([]byte(nil), v...)
			break
		}
	}
	c.Close()
	if slotKey == nil {
		fmt.Fprintln(os.Stderr, "no slot under nib", *nib)
		os.Exit(1)
	}
	newVal := []byte{0x77, 0x88, 0x99, 0xaa}
	if len(oldVal) > 0 && oldVal[0] == 0x77 {
		newVal = []byte{0x12, 0x34}
	}
	if err := tx.Put(modules.HashedStorage, slotKey, newVal); err != nil {
		fmt.Fprintln(os.Stderr, "put slot:", err)
		os.Exit(1)
	}
	fmt.Printf("STEP3 mutated slot %x oldVal=%x newVal=%x\n", slotKey[32:40], oldVal, newVal)

	// STEP 4: INCREMENTAL pass — RetainList = {that one slot}.
	rlInc := trie.NewRetainList(0)
	rlInc.AddKeyWithMarker(slotKey, true)
	updInc, err := runLoader(tx, rlInc)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loaderInc:", err)
		os.Exit(1)
	}
	if err := applyPhase4(tx, updInc); err != nil {
		fmt.Fprintln(os.Stderr, "phase4-inc:", err)
		os.Exit(1)
	}
	fmt.Printf("STEP4 incremental wrote %d nodes\n", len(updInc))

	// STEP 5: dump '3' after incremental.
	rh2, in2 := dumpRec(tx, ah, pathNib)
	fmt.Printf("STEP5 after incremental: path='%s' rootHash=%s inline[0..3]=%v\n", *nib, rh2[:16], in2[:min(4, len(in2))])

	// STEP 6: ground truth = full-retain rebuild again (don't write, just capture).
	rlFull2, _, _ := fullRetainRL(tx, ah)
	updGT, err := runLoader(tx, rlFull2)
	if err != nil {
		fmt.Fprintln(os.Stderr, "loaderGT:", err)
		os.Exit(1)
	}
	var gtRH string
	var gtInline []string
	gtKey := append(append([]byte(nil), ah...), pathNib)
	for _, u := range updGT {
		if bytes.Equal(u.k, gtKey) && u.v != nil {
			_, _, _, hashes, rh := trie.UnmarshalTrieNode(u.v)
			gtRH = hex.EncodeToString(rh)
			for i := 0; i < len(hashes)/32; i++ {
				gtInline = append(gtInline, hex.EncodeToString(hashes[i*32:i*32+8]))
			}
		}
	}
	fmt.Printf("STEP6 ground truth:      path='%s' rootHash=%s inline[0..3]=%v\n", *nib, gtRH[:min(16, len(gtRH))], gtInline[:min(4, len(gtInline))])

	// STEP 7: verdict.
	fmt.Println("\n=== VERDICT ===")
	inlineMatch := len(in2) == len(gtInline)
	if inlineMatch {
		for i := range in2 {
			if in2[i] != gtInline[i] {
				inlineMatch = false
				break
			}
		}
	}
	rhMatch := rh2 == gtRH
	fmt.Printf("inline children: incremental %s ground-truth\n", boolStr(inlineMatch, "==", "!="))
	fmt.Printf("rootHash field:  incremental %s ground-truth\n", boolStr(rhMatch, "==", "!="))
	switch {
	case inlineMatch && rhMatch:
		fmt.Println(">>> incremental write is FULLY CORRECT. On-disk stale is a historical (bootstrap) artifact. rebuild-trie once suffices; no code fix needed for drift.")
	case inlineMatch && !rhMatch:
		fmt.Println(">>> BUG REPRODUCED: incremental updates inline children but leaves rootHash STALE. Fix must target gen_struct_step/hashbuilder emit (depth-1 rootHash).")
	default:
		fmt.Println(">>> incremental diverges in inline children too — deeper cursor bug.")
	}
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
