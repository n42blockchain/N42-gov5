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

func main() {
	dir := flag.String("dir", `D:/N42-hashed/chaindata`, "hashed-canonical N42 chaindata dir")
	blockFlag := flag.Uint64("block", 0, "block to capture (0 = canonical head)")
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

	var b []byte
	add := func(f string, a ...interface{}) { b = append(b, []byte(fmt.Sprintf(f, a...))...) }
	add("=== n42-stateless-produce (Phase A real-data validation) ===\n")
	add("dir            : %s\n", *dir)
	add("block          : %d\n", blockN)
	add("computed root  : 0x%x\n", root[:])
	add("header.Root    : 0x%x\n", headerRoot[:])
	add("root == header : %v\n", rootMatch)
	add("proof nodes    : %d (%d bytes)\n", len(proof), nbytes)
	add("proof anchors  : %v\n", anchorErr == nil)
	if anchorErr != nil {
		add("anchor error   : %v\n", anchorErr)
	}
	add("elapsed        : %s\n", elapsed)
	pass := rootMatch && anchorErr == nil
	add("RESULT         : %s\n", map[bool]string{true: "PASS", false: "FAIL"}[pass])

	if werr := os.WriteFile(*outPath, b, 0o644); werr != nil {
		fmt.Fprintln(os.Stderr, "write out:", werr)
	}
	fmt.Fprint(os.Stderr, string(b))
	if !pass {
		os.Exit(2)
	}
}
