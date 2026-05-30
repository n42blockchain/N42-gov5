// n42-stateless-produce validates Phase A on real data: it captures the MPT
// stateless multiproof for a block as a byproduct of the existing changeset
// forward root computation (commitment.ExtractBlockMultiproof, the read-only
// twin of MerkleStageIncrementalWithProof — no witness-gen change, no TrieOf*
// write), then checks the computed root equals the block's real, independently
// stored header.Root and that the captured proof anchors to it.
//
// Read-only on a live datadir (D:/N42-hashed). Defaults to the canonical head
// block, whose post-state trie the datadir holds (so the computed root is
// header[head].Root).
package main

import (
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

// countAcctChangeset counts AccountChangeSet entries (touched accounts) for a
// block. Key=blockNum(8B), DupSort, one dup per touched account.
func countAcctChangeset(tx kv.Tx, blockN uint64) int {
	var bk [8]byte
	binary.BigEndian.PutUint64(bk[:], blockN)
	c, err := tx.CursorDupSort("AccountChangeSet")
	if err != nil {
		return 0
	}
	defer c.Close()
	n := 0
	for v, e := c.SeekBothRange(bk[:], nil); v != nil; _, v, e = c.NextDup() {
		if e != nil {
			break
		}
		n++
	}
	return n
}

// countStorChangeset counts StorageChangeSet entries (touched slots) for a
// block. Key=blockNum(8B)+addr(20B), DupSort dup=slot+oldval; scan by blockNum
// prefix over all (key,dup) pairs.
func countStorChangeset(tx kv.Tx, blockN uint64) int {
	var bk [8]byte
	binary.BigEndian.PutUint64(bk[:], blockN)
	c, err := tx.CursorDupSort("StorageChangeSet")
	if err != nil {
		return 0
	}
	defer c.Close()
	n := 0
	for k, _, e := c.Seek(bk[:]); k != nil; k, _, e = c.Next() {
		if e != nil || len(k) < 8 || binary.BigEndian.Uint64(k[:8]) != blockN {
			break
		}
		n++
	}
	return n
}

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "hashed-canonical N42 chaindata dir")
	blockFlag := flag.Uint64("block", 0, "block to capture (0 = canonical head)")
	statsN := flag.Int("stats", 0, "if >0, scan changeset acct/stor counts over the last N blocks and report avg/min/max")
	outPath := flag.String("out", "stateless-produce.txt", "result summary file")
	flag.Parse()

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).Path(*dir).Label(kv.ChainDB).
		PageSize(4096).MapSize(4 * datasize.TB).Readonly().
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(context.Background())
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer db.Close()
	ctx := context.Background()
	tx, err := db.BeginRo(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin ro:", err)
		os.Exit(1)
	}
	defer tx.Rollback()

	blockN := *blockFlag
	if blockN == 0 {
		c, _ := tx.Cursor(kv.HeaderCanonical)
		k, _, lerr := c.Last()
		c.Close()
		if lerr != nil || len(k) != 8 {
			fmt.Fprintln(os.Stderr, "cannot find canonical head")
			os.Exit(1)
		}
		blockN = binary.BigEndian.Uint64(k)
	}

	// Real header.Root for the block (independent anchor).
	hash, err := rawdb.ReadCanonicalHash(tx, blockN)
	if err != nil || hash == (types.Hash{}) {
		fmt.Fprintf(os.Stderr, "no canonical hash at %d\n", blockN)
		os.Exit(1)
	}
	hdr := rawdb.ReadHeader(tx, hash, blockN)
	if hdr == nil {
		fmt.Fprintf(os.Stderr, "no header at %d\n", blockN)
		os.Exit(1)
	}
	headerRoot := hdr.Root

	// Capture the multiproof from the forward changeset root computation.
	t0 := time.Now()
	root, proof, err := commitment.ExtractBlockMultiproof(tx, blockN, blockN)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ExtractBlockMultiproof:", err)
		os.Exit(1)
	}
	elapsed := time.Since(t0).Truncate(time.Millisecond)

	rootMatch := root == headerRoot
	anchorErr := stateless.VerifyProofAnchors(root[:], proof)

	var nbytes int
	for _, n := range proof {
		nbytes += len(n)
	}

	// Compact-wire compression (faithful): encode -> decode-to-nodes -> the
	// round-tripped set must still anchor to the same root.
	var compactBytes, compactRT int
	var compactErr error
	if wire, err := stateless.CompactProofFromNodes(root[:], proof); err == nil {
		compactBytes = len(wire)
		if nodes2, derr := stateless.DecodeCompactToNodes(wire); derr == nil {
			for _, n := range nodes2 {
				compactRT += len(n)
			}
			compactErr = stateless.VerifyProofAnchors(root[:], nodes2)
		} else {
			compactErr = derr
		}
	} else {
		compactErr = err
	}

	var b []byte
	add := func(f string, a ...interface{}) { b = append(b, []byte(fmt.Sprintf(f, a...))...) }
	add("=== n42-stateless-produce (Phase A real-data validation) ===\n")
	add("dir            : %s\n", *dir)
	add("block          : %d\n", blockN)
	add("computed root  : 0x%x\n", root[:])
	add("header.Root    : 0x%x\n", headerRoot[:])
	add("root == header : %v\n", rootMatch)
	add("proof nodes    : %d (%d bytes, full RLP)\n", len(proof), nbytes)
	add("proof anchors  : %v\n", anchorErr == nil)
	if anchorErr != nil {
		add("anchor error   : %v\n", anchorErr)
	}
	if compactBytes > 0 {
		add("compact wire   : %d bytes (%.1f%% of full RLP)\n", compactBytes, 100*float64(compactBytes)/float64(nbytes))
		add("compact decode : %d bytes round-tripped, anchors=%v\n", compactRT, compactErr == nil)
	}
	if compactErr != nil {
		add("compact error  : %v\n", compactErr)
	}
	add("elapsed        : %s\n", elapsed)

	// Per-touched-key proof estimate from the head block.
	headAcct := countAcctChangeset(tx, blockN)
	headStor := countStorChangeset(tx, blockN)
	headTouched := headAcct + headStor
	add("--- head block %d changeset ---\n", blockN)
	add("acct entries   : %d\n", headAcct)
	add("stor entries   : %d\n", headStor)
	add("trie nodes     : %d\n", len(proof))
	if headTouched > 0 {
		add("per touched key: %d B full-RLP, %d B compact, %.2f nodes\n",
			nbytes/headTouched, compactBytes/headTouched, float64(len(proof))/float64(headTouched))
	}

	if *statsN > 0 {
		var sumA, sumS, minA, maxA, minS, maxS uint64
		minA, minS = ^uint64(0), ^uint64(0)
		lo := uint64(1)
		if blockN >= uint64(*statsN) {
			lo = blockN - uint64(*statsN) + 1
		}
		cnt := uint64(0)
		for b := lo; b <= blockN; b++ {
			a := uint64(countAcctChangeset(tx, b))
			s := uint64(countStorChangeset(tx, b))
			sumA += a
			sumS += s
			if a < minA {
				minA = a
			}
			if a > maxA {
				maxA = a
			}
			if s < minS {
				minS = s
			}
			if s > maxS {
				maxS = s
			}
			cnt++
		}
		add("--- changeset stats over %d blocks [%d..%d] ---\n", cnt, lo, blockN)
		add("acct/blk       : avg %.1f  min %d  max %d  (total %d)\n", float64(sumA)/float64(cnt), minA, maxA, sumA)
		add("stor/blk       : avg %.1f  min %d  max %d  (total %d)\n", float64(sumS)/float64(cnt), minS, maxS, sumS)
		add("touched/blk    : avg %.1f\n", float64(sumA+sumS)/float64(cnt))
	}

	pass := rootMatch && anchorErr == nil && compactErr == nil
	add("RESULT         : %s\n", map[bool]string{true: "PASS", false: "FAIL"}[pass])

	if werr := os.WriteFile(*outPath, b, 0o644); werr != nil {
		fmt.Fprintln(os.Stderr, "write out:", werr)
	}
	fmt.Fprint(os.Stderr, string(b))
	if !pass {
		os.Exit(2)
	}
}
