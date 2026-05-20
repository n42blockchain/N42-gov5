// n42-topcache-phase1: Phase 1 feasibility verification for the
// "TopCache K=4 + on-demand bottom subtree" commitment compression
// proposal (see docs/ethel/commitment-compression-evidence.md A4).
//
// Goal: settle whether TopCache K=4 actually delivers storage savings
// vs reth's 5.4 GB AccountsTrie, given that the existing
// D:\n42-snapshot is NOT sorted by hashed_key (it's MPHF-ordinal
// order = pseudorandom), so making TopCache work requires either:
//   (a) reshuffling snapshot to hashed-key order, or
//   (b) building a parallel hashed-key index.
//
// This tool:
//
//  1. Samples N (addr, value) pairs from reth's PlainAccountState.
//  2. Computes keccak(addr) for each → hashed_key.
//  3. Sorts pairs by hashed_key.
//  4. Measures the storage cost of two representations:
//     - "sorted-snapshot": [hashed_key 32B || value 14B] inline
//     - "delta-encoded":    sorted hashed_keys with prefix-compression
//  5. Builds a K=4 TopCache from the sorted leaves:
//     - 65,536 anchor buckets (top 2 bytes of hashed_key)
//     - For each anchor, build local MPT subtree from its leaves
//     - Hash the subtree up to its root (which is the anchor's hash)
//     - Persist anchor_id → hash mapping (16-ary MPT structure to depth 4)
//  6. Extrapolates all sizes to the full 386M-account scale.
//  7. Reports the real compression ratio vs reth's 5.4 GB AccountsTrie.
//
// The honest question this answers: is "TopCache + reshuffled snapshot"
// actually smaller than reth's "PlainState + MPT", or do we just shift
// bytes around?
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
)

func tableCfg(table string) func(kv.TableCfg) kv.TableCfg {
	return func(d kv.TableCfg) kv.TableCfg {
		d[table] = kv.TableCfgItem{}
		return d
	}
}

type leaf struct {
	hashedKey [32]byte
	value     []byte
}

func main() {
	dbPath := flag.String("db", `D:\reth2k\db`, "reth MDBX dir (readonly)")
	table := flag.String("table", "PlainAccountState", "source table")
	samples := flag.Int("samples", 100_000, "leaves to sample")
	depthK := flag.Int("depth-k", 4, "TopCache depth (anchor at this nibble depth)")
	mapSizeGB := flag.Int("mapsize-gb", 4096, "DB MapSize cap")
	flag.Parse()

	logger := log.New()
	t0 := time.Now()
	db, err := mdbxkv.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(*mapSizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(tableCfg(*table)).
		Open(context.Background())
	if err != nil {
		fatal("open: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fatal("tx: %v", err)
	}
	defer tx.Rollback()

	mtx := tx.(*mdbxkv.MdbxTx)
	st, err := mtx.BucketStat(*table)
	if err != nil {
		fatal("stat: %v", err)
	}
	total := st.Entries
	fmt.Printf("source %s: %d entries (%.2f GB raw in MDBX)\n",
		*table, total, float64((st.LeafPages+st.BranchPages)*4096)/1e9)

	c, err := tx.Cursor(*table)
	if err != nil {
		fatal("cursor: %v", err)
	}
	defer c.Close()

	hasher := sha3.NewLegacyKeccak256()
	leaves := make([]leaf, 0, *samples)
	var (
		rawKeyBytes uint64
		rawValBytes uint64
	)
	for k, v, err := c.First(); err == nil && k != nil && len(leaves) < *samples; k, v, err = c.Next() {
		hasher.Reset()
		hasher.Write(k)
		var hk [32]byte
		copy(hk[:], hasher.Sum(nil))
		val := make([]byte, len(v))
		copy(val, v)
		leaves = append(leaves, leaf{hashedKey: hk, value: val})
		rawKeyBytes += uint64(len(k))
		rawValBytes += uint64(len(v))
	}
	n := len(leaves)
	fmt.Printf("collected %d leaves (avg key=%.1f val=%.1f bytes)\n",
		n, float64(rawKeyBytes)/float64(n), float64(rawValBytes)/float64(n))

	// Sort by hashed_key.
	sortT0 := time.Now()
	sort.Slice(leaves, func(i, j int) bool {
		return string(leaves[i].hashedKey[:]) < string(leaves[j].hashedKey[:])
	})
	fmt.Printf("sort: %s\n", time.Since(sortT0).Truncate(time.Millisecond))

	// --- Variant 1: inline [hashed_key 32B || varlen_value] ---
	var inlineBytes uint64
	for _, l := range leaves {
		inlineBytes += 32 + 1 + uint64(len(l.value)) // 32B key + 1B len prefix + value
	}

	// --- Variant 2: prefix-compressed hashed_key sorted run ---
	// For each leaf after the first, store (prefix_len varint + suffix bytes + 1B val_len + value).
	// Within a sorted run, consecutive keys share several high-order bytes.
	var deltaBytes uint64
	deltaBytes += 32 + 1 + uint64(len(leaves[0].value)) // first leaf in full
	for i := 1; i < n; i++ {
		prev := leaves[i-1].hashedKey
		cur := leaves[i].hashedKey
		shared := 0
		for shared < 32 && prev[shared] == cur[shared] {
			shared++
		}
		// 1B shared count + (32-shared) suffix + 1B val_len + value
		deltaBytes += 1 + uint64(32-shared) + 1 + uint64(len(leaves[i].value))
	}

	// --- Build TopCache at depth K ---
	cacheT0 := time.Now()
	anchorCount := 1 << uint(*depthK*4) // 16^K
	anchorHashes := make([][]byte, anchorCount)
	var anchorBuckets [][]int
	if *depthK > 0 {
		anchorBuckets = make([][]int, anchorCount)
		for i, l := range leaves {
			// Top K nibbles = top K/2 bytes (if K even), or specific bit pattern.
			anchorID := topNibblesToInt(l.hashedKey[:], *depthK)
			anchorBuckets[anchorID] = append(anchorBuckets[anchorID], i)
		}
	} else {
		// K=0: single anchor = root, all leaves under it.
		anchorBuckets = [][]int{make([]int, n)}
		for i := range leaves {
			anchorBuckets[0][i] = i
		}
	}

	var populatedAnchors int
	for aid, idxs := range anchorBuckets {
		if len(idxs) == 0 {
			continue
		}
		populatedAnchors++
		// Compute local subtree root: hash all leaves' (key, value) bottom-up.
		// For this spike we just take SHA-256 over concatenated entries —
		// the absolute hash bytes don't matter, the BYTE COUNT does.
		h := sha256.New()
		for _, idx := range idxs {
			h.Write(leaves[idx].hashedKey[:])
			h.Write([]byte{byte(len(leaves[idx].value))})
			h.Write(leaves[idx].value)
		}
		anchorHashes[aid] = h.Sum(nil)
	}

	// TopCache = 16-ary MPT with depth K, sparse encoding.
	// Per-level branch node = 2B child mask + 32B × children_present.
	var topCacheBytes uint64
	// Bottom level (depth K): anchor → 32B hash entries
	for _, h := range anchorHashes {
		if h != nil {
			topCacheBytes += 32 // the hash itself
		}
	}
	// Upper levels: internal nodes from depth K-1 to 0
	// Count populated nodes at each level by collapsing anchor IDs
	for level := *depthK - 1; level >= 0; level-- {
		levelNodes := make(map[int]bool)
		mask := (1 << uint((level+1)*4)) - 1 // top (level+1) nibbles
		divider := 1 << uint(4) // anchors per immediate child group
		_ = mask
		_ = divider
		// Compute distinct parent IDs at this level
		for aid, h := range anchorHashes {
			if h == nil {
				continue
			}
			parentID := aid >> uint((*depthK-level-1)*4+4)
			levelNodes[parentID] = true
		}
		// Per internal node: 2B mask + ~5 children × 32B hash = ~162B avg
		// Approximation: assume avg 5 children
		topCacheBytes += uint64(len(levelNodes)) * (2 + 5*32)
	}
	fmt.Printf("TopCache K=%d build: %s\n", *depthK, time.Since(cacheT0).Truncate(time.Millisecond))

	// --- Reporting ---
	fmt.Println()
	fmt.Println("=== TopCache Phase-1 measurement (n =", n, ") ===")
	fmt.Println()
	fmt.Println("Sorted snapshot variants (per-leaf storage):")
	inlinePerLeaf := float64(inlineBytes) / float64(n)
	deltaPerLeaf := float64(deltaBytes) / float64(n)
	fmt.Printf("  inline  [hk32 + len + val]    %.2f B/leaf  →  %.2f GB / 386M\n",
		inlinePerLeaf, inlinePerLeaf*386e6/1e9)
	fmt.Printf("  delta   [prefix-compressed]   %.2f B/leaf  →  %.2f GB / 386M\n",
		deltaPerLeaf, deltaPerLeaf*386e6/1e9)
	fmt.Println()
	fmt.Println("TopCache K=" + fmt.Sprint(*depthK) + ":")
	fmt.Printf("  anchor space             %d (16^%d)\n", anchorCount, *depthK)
	fmt.Printf("  populated anchors        %d (%.1f%%)\n",
		populatedAnchors, 100*float64(populatedAnchors)/float64(anchorCount))
	fmt.Printf("  TopCache total bytes     %.2f KB (this sample)\n", float64(topCacheBytes)/1024)
	tcPerAnchor := float64(topCacheBytes) / float64(populatedAnchors)
	fmt.Printf("  bytes per populated anchor %.1f B\n", tcPerAnchor)
	// Extrapolate: full 386M would populate roughly all 16^K anchors at K=4
	// (since 386M / 65536 = 5895 leaves/anchor avg). At K=4 ~100% populated.
	// At K=5: 386M / 1M = 386 leaves/anchor, ~100% populated for accounts.
	fullPopulated := anchorCount
	if anchorCount > int(386e6) {
		fullPopulated = int(386e6 / 5)
	}
	fullTopCacheBytes := tcPerAnchor * float64(fullPopulated)
	fmt.Printf("  extrapolated full K=%d    %.2f MB\n", *depthK, fullTopCacheBytes/1e6)

	fmt.Println()
	fmt.Println("=== Summary vs reth ===")
	reth := 5.4 * 1e9
	bestSnap := deltaBytes
	if inlineBytes < bestSnap {
		bestSnap = inlineBytes
	}
	fullSnap := float64(bestSnap) / float64(n) * 386e6
	fullCache := fullTopCacheBytes
	fullTotal := fullSnap + fullCache
	fmt.Printf("  reth AccountsTrie         %.2f GB  (just trie nodes; PlainState=23.1 GB separate)\n", reth/1e9)
	fmt.Printf("  proposed sorted snapshot  %.2f GB  (replaces both PlainState AND MPT)\n", fullSnap/1e9)
	fmt.Printf("  proposed TopCache         %.2f MB\n", fullCache/1e6)
	fmt.Printf("  proposed total            %.2f GB\n", fullTotal/1e9)
	fmt.Println()
	rethPlainPlusTrie := 23.1 + 5.4
	fmt.Printf("  reth (PlainState + MPT)   %.2f GB total\n", rethPlainPlusTrie)
	fmt.Printf("  ratio (proposed / reth)   %.2fx %s\n",
		fullTotal/1e9/rethPlainPlusTrie,
		whichWay(fullTotal/1e9, rethPlainPlusTrie))

	fmt.Println()
	fmt.Printf("Total elapsed: %s\n", time.Since(t0).Truncate(time.Millisecond))
}

// topNibblesToInt extracts the top K nibbles of a hashed key as an integer.
// E.g. K=4 returns an int in [0, 65536) from the top 2 bytes.
func topNibblesToInt(hk []byte, k int) int {
	v := 0
	for i := 0; i < k; i++ {
		var nibble int
		if i%2 == 0 {
			nibble = int(hk[i/2] >> 4)
		} else {
			nibble = int(hk[i/2] & 0xF)
		}
		v = (v << 4) | nibble
	}
	return v
}

func whichWay(proposed, ref float64) string {
	if proposed < ref {
		return "(✓ smaller)"
	}
	return "(✗ LARGER)"
}

func hexShort(b []byte) string {
	if len(b) > 8 {
		return hex.EncodeToString(b[:4]) + "..." + hex.EncodeToString(b[len(b)-4:])
	}
	return hex.EncodeToString(b)
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}

var _ = binary.LittleEndian
