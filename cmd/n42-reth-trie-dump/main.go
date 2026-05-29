// n42-reth-trie-dump reads the RAW reth StoragesTrie nodes for one account
// (default EIP-2935) and parses each BranchNodeCompact the way N42's
// UnmarshalTrieNode does (implicit "popcount(hash_mask)+1 == numHashes →
// first hash is root_hash"), printing masks + hash count + the inferred
// root_hash + first child. This pins WHY a verbatim copy of reth trie nodes
// produced a stale/wrong cached root in N42 (#150): we compare reth's stored
// root_hash against N42's leaves-rebuild truth for the same path.
//
// reth StoragesTrie: DupSort, key = 32B addrHash, dup-value = 65B
// StoredNibblesSubKey (64 nibbles padded + 1 len byte) + BranchNodeCompact.
// Read-only.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"math/bits"
	"os"
	"sort"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

const rethStoragesTrie = "StoragesTrie"

func rethCfg(d kv.TableCfg) kv.TableCfg {
	d[rethStoragesTrie] = kv.TableCfgItem{Flags: kv.DupSort}
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

// parseN42 mimics trie.UnmarshalTrieNode: masks(6B) then hashes; if
// popcount(hashMask)+1 == numHashes, the FIRST hash is root_hash.
func parseN42(node []byte) (state, tree, hash uint16, rootHash []byte, children [][]byte, ok bool) {
	if len(node) < 6 {
		return
	}
	state = uint16(node[0])<<8 | uint16(node[1])
	tree = uint16(node[2])<<8 | uint16(node[3])
	hash = uint16(node[4])<<8 | uint16(node[5])
	rest := node[6:]
	if len(rest)%32 != 0 {
		return state, tree, hash, nil, nil, false
	}
	n := len(rest) / 32
	pc := bits.OnesCount16(hash)
	idx := 0
	if pc+1 == n {
		rootHash = rest[:32]
		idx = 32
	}
	for i := idx; i+32 <= len(rest); i += 32 {
		children = append(children, rest[i:i+32])
	}
	return state, tree, hash, rootHash, children, true
}

func main() {
	rethDir := flag.String("reth", `D:/reth2k/db`, "reth db dir (read-only)")
	acctHex := flag.String("acct", "6c9d57be05dd69371c4dd2e871bce6e9f4124236825bb612ee18a45e5675be51", "addrHash (64 hex)")
	onlyPath := flag.String("path", "", "only print this nibble path (e.g. '3', '' for root); empty prints all up to maxdepth")
	maxDepth := flag.Int("maxdepth", 2, "max nibble-path depth to print")
	flag.Parse()

	addrHash, err := hex.DecodeString(*acctHex)
	if err != nil || len(addrHash) != 32 {
		fmt.Fprintln(os.Stderr, "bad -acct")
		os.Exit(1)
	}

	db, err := mdbx.NewMDBX(log.New()).Path(*rethDir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().Accede().
		WithTableCfg(rethCfg).Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open reth:", err)
		os.Exit(1)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	c, err := tx.CursorDupSort(rethStoragesTrie)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cursor:", err)
		os.Exit(1)
	}
	defer c.Close()

	type rec struct {
		path    string
		state   uint16
		tree    uint16
		hash    uint16
		nHash   int
		pc      int
		hasRoot bool
		root    string
		child0  string
		nodeLen int
		subLen  int
	}
	var recs []rec

	// SeekBothRange returns the first dup-value for addrHash; NextDup walks the
	// remaining dup-values within the same key (returns nil when exhausted).
	v, e := c.SeekBothRange(addrHash, nil)
	for ; v != nil && e == nil; _, v, e = c.NextDup() {
		if len(v) < 65 {
			continue
		}
		sub := v[:65]
		node := v[65:]
		pathLen := int(sub[64])
		if pathLen > 64 {
			continue
		}
		path := sub[:pathLen]
		ps := nibStr(path)
		if *onlyPath != "" {
			if ps != *onlyPath {
				continue
			}
		} else if pathLen > *maxDepth {
			continue
		}
		state, tree, hash, root, children, ok := parseN42(node)
		if *onlyPath != "" && ps == *onlyPath {
			fmt.Printf("reth path='%s' nodeLen=%d state=%04x tree=%04x hash=%04x pc=%d numHashes=%d hasRoot(N42)=%v\n",
				ps, len(node), state, tree, hash, bits.OnesCount16(hash), (len(node)-6)/32, root != nil)
			if root != nil {
				fmt.Printf("  root_hash = %x\n", root)
			}
			for i, ch := range children {
				fmt.Printf("  child[%2d] = %x\n", i, ch)
			}
			fmt.Printf("  RAW node hex = %x\n", node)
		}
		r := rec{
			path: ps, state: state, tree: tree, hash: hash,
			pc: bits.OnesCount16(hash), nodeLen: len(node), subLen: len(sub),
		}
		if ok {
			r.nHash = (len(node) - 6) / 32
			r.hasRoot = root != nil
			if root != nil {
				r.root = hex.EncodeToString(root[:8])
			}
			if len(children) > 0 {
				r.child0 = hex.EncodeToString(children[0][:8])
			}
		} else {
			r.root = "PARSE_FAIL"
		}
		recs = append(recs, r)
	}

	sort.Slice(recs, func(i, j int) bool {
		if len(recs[i].path) != len(recs[j].path) {
			return len(recs[i].path) < len(recs[j].path)
		}
		return recs[i].path < recs[j].path
	})

	fmt.Printf("reth StoragesTrie for acct %s — %d records (depth<=%d)\n", *acctHex, len(recs), *maxDepth)
	fmt.Printf("%-6s %-6s %-6s %-6s %-4s %-4s %-7s %-18s %-18s %s\n",
		"path", "state", "tree", "hash", "nH", "pc", "hasRoot", "root_hash[:8]", "child0[:8]", "nodeLen")
	for _, r := range recs {
		fmt.Printf("'%-4s' %04x   %04x   %04x   %-4d %-4d %-7v %-18s %-18s %d\n",
			r.path, r.state, r.tree, r.hash, r.nHash, r.pc, r.hasRoot, r.root, r.child0, r.nodeLen)
	}
}
