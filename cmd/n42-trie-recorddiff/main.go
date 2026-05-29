// n42-trie-recorddiff: for ONE account, full-retain rebuild its storage
// subtree from HashedStorage leaves, capture each emitted TrieOfStorage
// record's COMPLETE inline child-hash list + rootHash, then diff every
// path AND every inline child slot against the cached MDBX record.
//
// Answers "how deep does the stale cached-IH go" for the #150 bug:
//   - depth-0 (root) only?  -> Fix A (drop depth-0/1) suffices
//   - depth-1 inline children stale -> Fix A must rebuild from depth-2
//   - depth-2+ inline children stale -> Fix A insufficient, need full
//     subtree drop (or proper cursor fix)
//
// Read-only.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
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

func nibStr(b []byte) string {
	const hd = "0123456789abcdef"
	out := make([]byte, len(b))
	for i, n := range b {
		out[i] = hd[n&0xf]
	}
	return string(out)
}

type rec struct {
	hasState, hasTree, hasHash uint16
	hashes                     []byte // inline children, 32B each
	rootHash                   []byte // 32B or empty
}

func main() {
	datadir := flag.String("datadir", `D:/N42-hashed/chaindata`, "chaindata (RO)")
	acct := flag.String("acct", "6c9d57be05dd69371c4dd2e871bce6e9f4124236825bb612ee18a45e5675be51", "addrHash (64 hex)")
	maxDepth := flag.Int("max-depth", 2, "max path depth to print diffs for")
	flag.Parse()

	ah, err := hex.DecodeString(*acct)
	if err != nil || len(ah) != 32 {
		fmt.Fprintln(os.Stderr, "need --acct 64 hex")
		os.Exit(1)
	}

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*datadir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(cfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	// 1. Capture cached records under acct from TrieStorage.
	cached := map[string]rec{}
	tc, _ := tx.Cursor("TrieStorage")
	for k, v, e := tc.Seek(ah); k != nil && len(k) >= 32 && string(k[:32]) == string(ah); k, v, e = tc.Next() {
		if e != nil {
			break
		}
		hs, ht, hh, hashes, rh := trie.UnmarshalTrieNode(v)
		cached[nibStr(k[32:])] = rec{hs, ht, hh, append([]byte(nil), hashes...), append([]byte(nil), rh...)}
	}
	tc.Close()

	// 2. Full-retain rebuild: capture each emitted record.
	rebuilt := map[string]rec{}
	rl := trie.NewRetainList(0)
	c, _ := tx.Cursor("HashedStorage")
	n := 0
	for k, _, e := c.Seek(ah); k != nil; k, _, e = c.Next() {
		if e != nil {
			break
		}
		if len(k) != 64 || string(k[:32]) != string(ah) {
			if len(k) >= 32 && string(k[:32]) != string(ah) {
				break
			}
			continue
		}
		rl.AddKeyWithMarker(append([]byte(nil), k...), true)
		n++
	}
	c.Close()
	fmt.Printf("acct=%s retained %d leaves, cached records=%d\n", *acct, n, len(cached))

	shc := func(accWithInc, keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		rebuilt[nibStr(keyHex)] = rec{hasState, hasTree, hasHash, append([]byte(nil), hashes...), append([]byte(nil), rootHash...)}
		return nil
	}
	loader := trie.NewFlatDBTrieLoader("recorddiff", rl, nil, shc, false)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "CalcTrieRoot:", err)
		os.Exit(1)
	}
	fmt.Printf("rebuilt root=%s  rebuilt records=%d\n\n", root.Hex(), len(rebuilt))

	// 3. Per-depth summary: how many paths have matching cached vs rebuilt
	//    inline-children + rootHash.
	allPaths := map[string]bool{}
	for p := range cached {
		allPaths[p] = true
	}
	for p := range rebuilt {
		allPaths[p] = true
	}
	pathList := make([]string, 0, len(allPaths))
	for p := range allPaths {
		pathList = append(pathList, p)
	}
	sort.Slice(pathList, func(i, j int) bool {
		if len(pathList[i]) != len(pathList[j]) {
			return len(pathList[i]) < len(pathList[j])
		}
		return pathList[i] < pathList[j]
	})

	eqRec := func(a, b rec) bool {
		if a.hasState != b.hasState || a.hasTree != b.hasTree || a.hasHash != b.hasHash {
			return false
		}
		if len(a.hashes) != len(b.hashes) {
			return false
		}
		for i := range a.hashes {
			if a.hashes[i] != b.hashes[i] {
				return false
			}
		}
		if len(a.rootHash) != len(b.rootHash) {
			return false
		}
		for i := range a.rootHash {
			if a.rootHash[i] != b.rootHash[i] {
				return false
			}
		}
		return true
	}

	type stat struct{ total, both, eq, diff, cachedOnly, rebuiltOnly int }
	depthStats := map[int]*stat{}
	for _, p := range pathList {
		d := len(p)
		if depthStats[d] == nil {
			depthStats[d] = &stat{}
		}
		s := depthStats[d]
		s.total++
		cr, inC := cached[p]
		rr, inR := rebuilt[p]
		switch {
		case inC && inR:
			s.both++
			if eqRec(cr, rr) {
				s.eq++
			} else {
				s.diff++
			}
		case inC:
			s.cachedOnly++
		case inR:
			s.rebuiltOnly++
		}
	}
	fmt.Println("=== Per-depth record comparison (full record: bitmaps + inline children + rootHash) ===")
	depths := make([]int, 0, len(depthStats))
	for d := range depthStats {
		depths = append(depths, d)
	}
	sort.Ints(depths)
	for _, d := range depths {
		s := depthStats[d]
		fmt.Printf("  depth=%d total=%d both=%d EQ=%d DIFF=%d cachedOnly=%d rebuiltOnly=%d\n",
			d, s.total, s.both, s.eq, s.diff, s.cachedOnly, s.rebuiltOnly)
	}

	// 4. Detailed per-child diff for paths up to maxDepth.
	fmt.Printf("\n=== Inline-child-level diffs (paths with depth<=%d that have a cached record) ===\n", *maxDepth)
	for _, p := range pathList {
		if len(p) > *maxDepth {
			continue
		}
		cr, inC := cached[p]
		rr, inR := rebuilt[p]
		if !inC || !inR {
			if inC != inR {
				fmt.Printf("path='%s' presence: cached=%v rebuilt=%v\n", p, inC, inR)
			}
			continue
		}
		if eqRec(cr, rr) {
			continue
		}
		fmt.Printf("path='%s' DIFF:\n", p)
		if cr.hasState != rr.hasState {
			fmt.Printf("    hasState cached=%04x rebuilt=%04x\n", cr.hasState, rr.hasState)
		}
		if cr.hasTree != rr.hasTree {
			fmt.Printf("    hasTree  cached=%04x rebuilt=%04x\n", cr.hasTree, rr.hasTree)
		}
		if cr.hasHash != rr.hasHash {
			fmt.Printf("    hasHash  cached=%04x rebuilt=%04x\n", cr.hasHash, rr.hasHash)
		}
		// rootHash
		if hex.EncodeToString(cr.rootHash) != hex.EncodeToString(rr.rootHash) {
			fmt.Printf("    rootHash cached=%x rebuilt=%x\n", cr.rootHash, rr.rootHash)
		}
		// inline children
		nc := len(cr.hashes) / 32
		nr := len(rr.hashes) / 32
		maxN := nc
		if nr > maxN {
			maxN = nr
		}
		for i := 0; i < maxN; i++ {
			var ch, rh string
			if i < nc {
				ch = hex.EncodeToString(cr.hashes[i*32 : i*32+8])
			} else {
				ch = "<none>"
			}
			if i < nr {
				rh = hex.EncodeToString(rr.hashes[i*32 : i*32+8])
			} else {
				rh = "<none>"
			}
			if ch != rh {
				fmt.Printf("    inline[%2d] cached=%s rebuilt=%s  <<< DIFF\n", i, ch, rh)
			}
		}
	}
}
