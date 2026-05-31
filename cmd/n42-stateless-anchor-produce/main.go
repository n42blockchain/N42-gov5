// n42-stateless-anchor-produce is the real-data forward producer: it builds the
// state trie from GENESIS forward into a fresh TEMP datadir by streaming the V2
// forward changesets (acctcs/storcs), and every K blocks computes the state
// root, verifies it against the real header.Root (read from the columnar headerc
// freezer), and captures + saves the MPT-stateless anchor proof (compact wire).
//
// Data sources (read-only, never mutated):
//   --cs       acctcs/storcs (V2 forward changesets) — the forward-build input
//   --headers  headerc (columnar headers) — the trusted root to verify against
// Output: <tmp>/anchors/anchor-<block>.bin (compact proof per anchor) +
//         <tmp>/anchors.tsv (manifest: block, root, proofNodes, compactBytes).
//
// Forward build needs the POST-block state; the V2 changesets carry new values
// (unlike MDBX backward changesets), so no EVM runs — RebuildStateWith streams
// the diffs. The anchor proof is captured from the freshly-populated trie inside
// the verify boundary (RebuildOptions.OnVerify).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func main() {
	csDir := flag.String("cs", `D:/N42-eth1177/chain/freezer`, "freezer dir with acctcs/storcs (V2 forward changesets)")
	hdrDir := flag.String("headers", `D:/n42-eth1/chain/freezer`, "freezer dir with columnar headerc")
	tmp := flag.String("tmp", filepath.Join(os.TempDir(), "n42-anchor-trie"), "temp writable trie datadir (recreated)")
	endBlock := flag.Uint64("end", 100000, "build genesis..end (exclusive)")
	K := flag.Uint64("k", 1000, "anchor cadence (verify + capture every K blocks)")
	mapGB := flag.Int("mapsize-gb", 64, "temp trie MDBX mapsize GB")
	flag.Parse()

	ctx := context.Background()
	logger := log.New()

	if err := os.RemoveAll(*tmp); err != nil {
		fmt.Fprintln(os.Stderr, "clean tmp:", err)
		os.Exit(1)
	}
	anchorDir := filepath.Join(*tmp, "anchors")
	if err := os.MkdirAll(anchorDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}

	db, err := mdbx.NewMDBX(logger).Path(filepath.Join(*tmp, "chaindata")).Label(kv.ChainDB).
		PageSize(4096).MapSize(datasize.ByteSize(*mapGB) * datasize.GB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open temp db:", err)
		os.Exit(1)
	}
	defer db.Close()

	hc, err := ethel.OpenHeaderCompact(*hdrDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open headerc:", err)
		os.Exit(1)
	}
	defer hc.Close()

	acctTbl, err := freezer.NewFreezerTableReadOnly(*csDir, "acctcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open acctcs:", err)
		os.Exit(1)
	}
	defer acctTbl.Close()
	acctTbl.ForceBatchSize(freezer.BatchSize)
	acctTbl.SetCompressed(true)
	stoTbl, err := freezer.NewFreezerTableReadOnly(*csDir, "storcs", "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open storcs:", err)
		os.Exit(1)
	}
	defer stoTbl.Close()
	stoTbl.ForceBatchSize(freezer.BatchSize)
	stoTbl.SetCompressed(true)

	manifest, err := os.Create(filepath.Join(*tmp, "anchors.tsv"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "create manifest:", err)
		os.Exit(1)
	}
	defer manifest.Close()
	fmt.Fprintln(manifest, "block\troot\tproofNodes\tfullRLPBytes\tcompactBytes")

	var anchors int
	lastAnchor := ^uint64(0) // none yet → first interval starts at genesis (block 0)

	onVerify := func(blockNum uint64, tx kv.Tx, root types.Hash) error {
		// Trusted root from the columnar header freezer.
		hdr, herr := hc.ReadHeader(blockNum)
		if herr != nil {
			return fmt.Errorf("read header %d: %w", blockNum, herr)
		}
		if root != hdr.Root {
			return fmt.Errorf("ROOT MISMATCH block %d: computed %s header %s", blockNum, root.Hex(), hdr.Root.Hex())
		}

		// Build the touched-key RetainList over the whole window since the last
		// anchor [lastAnchor+1 .. blockNum] (deduped). The anchor's multiproof
		// thus covers every key the K-block span touched, anchored at the
		// verified header[blockNum].stateRoot — a minimal client with the span's
		// changesets reverse-verifies it to the previous anchor's root.
		from := uint64(0)
		if lastAnchor != ^uint64(0) {
			from = lastAnchor + 1
		}
		rl := trie.NewRetainList(0)
		seenA := map[types.Address]struct{}{}
		seenS := map[string]struct{}{}
		for b := from; b <= blockNum; b++ {
			if d, e := acctTbl.Retrieve(b); e == nil && len(d) > 0 {
				es, de := ethel.DecodeAccountChanges(d)
				if de != nil {
					return fmt.Errorf("decode acctcs %d: %w", b, de)
				}
				for _, ch := range es {
					if _, ok := seenA[ch.Address]; ok {
						continue
					}
					seenA[ch.Address] = struct{}{}
					rl.AddKeyWithMarker(crypto.Keccak256(ch.Address[:]), true)
				}
			}
			if d, e := stoTbl.Retrieve(b); e == nil && len(d) > 0 {
				es, de := ethel.DecodeStorageChanges(d)
				if de != nil {
					return fmt.Errorf("decode storcs %d: %w", b, de)
				}
				for _, ch := range es {
					if len(ch.CompositeKey) < 52 {
						continue
					}
					ck := string(ch.CompositeKey[:52])
					if _, ok := seenS[ck]; ok {
						continue
					}
					seenS[ck] = struct{}{}
					var comp [64]byte
					copy(comp[:32], crypto.Keccak256(ch.CompositeKey[:20]))
					copy(comp[32:], crypto.Keccak256(ch.CompositeKey[20:52]))
					rl.AddKeyWithMarker(comp[:], true)
				}
			}
		}
		lastAnchor = blockNum

		// Capture the anchor multiproof from the freshly-populated trie.
		r2, proof, eerr := commitment.ExtractMultiproof(tx, rl)
		if eerr != nil {
			return fmt.Errorf("extract proof %d: %w", blockNum, eerr)
		}
		if r2 != root {
			return fmt.Errorf("extract root %s != verified root %s at %d", r2.Hex(), root.Hex(), blockNum)
		}
		var fullBytes int
		for _, n := range proof {
			fullBytes += len(n)
		}
		wire, cerr := stateless.CompactProofFromNodes(root[:], proof)
		if cerr != nil {
			return fmt.Errorf("compact %d: %w", blockNum, cerr)
		}
		// Self-check: the compact proof anchors to the verified header root.
		if verr := stateless.VerifyProofAnchors(root[:], proof); verr != nil {
			return fmt.Errorf("anchor self-check %d: %w", blockNum, verr)
		}
		fname := filepath.Join(anchorDir, fmt.Sprintf("anchor-%010d.bin", blockNum))
		if werr := os.WriteFile(fname, wire, 0o644); werr != nil {
			return fmt.Errorf("write anchor %d: %w", blockNum, werr)
		}
		anchors++
		fmt.Fprintf(manifest, "%d\t0x%x\t%d\t%d\t%d\n", blockNum, root[:], len(proof), fullBytes, len(wire))
		fmt.Fprintf(os.Stderr, "ANCHOR block=%d root=0x%x proofNodes=%d full=%dB compact=%dB\n",
			blockNum, root[:6], len(proof), fullBytes, len(wire))
		return nil
	}

	opts := ethel.RebuildOptions{VerifyInterval: *K, OnVerify: onVerify}
	if err := ethel.RebuildStateWith(ctx, db, *csDir, *endBlock, opts); err != nil {
		fmt.Fprintln(os.Stderr, "RebuildStateWith:", err)
		os.Exit(2)
	}
	fmt.Fprintf(os.Stderr, "DONE: %d anchors (every %d) saved to %s\n", anchors, *K, anchorDir)
}
