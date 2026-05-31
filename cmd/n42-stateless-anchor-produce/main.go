// n42-stateless-anchor-produce is the real-data forward producer: it builds the
// state trie from GENESIS forward into a fresh TEMP datadir by streaming the V2
// forward changesets (acctcs/storcs), and every K blocks verifies the state root
// against the real header.Root (columnar headerc) and captures + saves the
// MPT-stateless anchor proof (compact wire).
//
// INCREMENTAL: per sub-batch (K blocks) the net changes are applied via
// TrieRootComputer.ComputeRoot in incremental mode — O(dirty), maintaining
// HashedAccounts + TrieOf* across sub-batches — instead of an O(state) full
// rebuild per boundary. This makes full-chain (25M+) production feasible. The
// first sub-batch bootstraps with a full build (empty TrieOf*); the rest are
// incremental. The anchor proof is captured read-only (ExtractMultiproof) over
// the sub-batch's touched keys after the root is advanced.
//
// Data sources (read-only): --cs acctcs/storcs (V2 forward changesets),
// --headers columnar headerc. Output: <tmp>/anchors/anchor-<block>.bin +
// <tmp>/anchors.tsv.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/account"
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
	mapGB := flag.Int("mapsize-gb", 256, "temp trie MDBX mapsize GB")
	flag.Parse()

	ctx := context.Background()
	logger := log.New()
	if *K == 0 {
		fmt.Fprintln(os.Stderr, "--k must be > 0")
		os.Exit(1)
	}

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

	tx, err := db.BeginRw(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "begin rw:", err)
		os.Exit(1)
	}
	trc := commitment.NewTrieRootComputer()
	trc.SetRwTx(tx)
	trc.SetIncremental(false) // first sub-batch bootstraps a full build

	// Per-sub-batch net change accumulators.
	dA := map[types.Address]*account.StateAccount{}
	dS := map[types.Address]map[types.Hash]*uint256.Int{}

	t0 := time.Now()
	var anchors int
	bootstrapped := false

	for n := uint64(0); n < *endBlock; n++ {
		if d, e := acctTbl.Retrieve(n); e == nil && len(d) > 0 {
			es, de := ethel.DecodeAccountChanges(d)
			if de != nil {
				fmt.Fprintf(os.Stderr, "decode acctcs %d: %v\n", n, de)
				os.Exit(1)
			}
			for _, ch := range es {
				if len(ch.NewValue) == 0 {
					dA[ch.Address] = nil // delete
					continue
				}
				a := new(account.StateAccount)
				if err := a.DecodeForStorage(ch.NewValue); err != nil {
					fmt.Fprintf(os.Stderr, "decode account %d %x: %v\n", n, ch.Address[:6], err)
					os.Exit(1)
				}
				dA[ch.Address] = a
			}
		}
		if d, e := stoTbl.Retrieve(n); e == nil && len(d) > 0 {
			es, de := ethel.DecodeStorageChanges(d)
			if de != nil {
				fmt.Fprintf(os.Stderr, "decode storcs %d: %v\n", n, de)
				os.Exit(1)
			}
			for _, ch := range es {
				if len(ch.CompositeKey) < 52 {
					continue
				}
				var addr types.Address
				copy(addr[:], ch.CompositeKey[:20])
				var slot types.Hash
				copy(slot[:], ch.CompositeKey[20:52])
				if dS[addr] == nil {
					dS[addr] = map[types.Hash]*uint256.Int{}
				}
				v := new(uint256.Int) // zero = delete
				if len(ch.NewValue) > 0 {
					v.SetBytes(ch.NewValue)
				}
				dS[addr][slot] = v
			}
		}

		// Anchor at block heights that are multiples of K (1000, 2000, ...),
		// post-state of block n == header[n].Root. This matches the consumer
		// convention (MinimalClient / buildAnchorChain request anchors at n%K==0),
		// so the producer's anchor-<n>.bin heights align with client requests.
		if n == 0 || n%*K != 0 {
			continue
		}

		// Sub-batch boundary: advance the trie (incremental), verify root, capture
		// the anchor proof. The anchor is captured DURING the root computation
		// (EnableProofCapture) so there is only ONE trie walk per sub-batch — the
		// dominant cost. Only the first (full-bootstrap, non-incremental) batch
		// can't capture inline, so it does a separate read-only ExtractMultiproof.
		var root types.Hash
		var proof [][]byte
		if bootstrapped {
			root, err = trc.ComputeRoot(dA, dS) // incremental + capture (one walk)
			if err != nil {
				fmt.Fprintf(os.Stderr, "ComputeRoot at %d: %v\n", n, err)
				os.Exit(1)
			}
			proof = trc.CapturedProof()
		} else {
			root, err = trc.ComputeRoot(dA, dS) // first: full bootstrap build
			if err != nil {
				fmt.Fprintf(os.Stderr, "ComputeRoot at %d: %v\n", n, err)
				os.Exit(1)
			}
			rl := trie.NewRetainList(0)
			for addr := range dA {
				rl.AddKeyWithMarker(crypto.Keccak256(addr[:]), true)
			}
			for addr, slots := range dS {
				ah := crypto.Keccak256(addr[:])
				for slot := range slots {
					var comp [64]byte
					copy(comp[:32], ah)
					copy(comp[32:], crypto.Keccak256(slot[:]))
					rl.AddKeyWithMarker(comp[:], true)
				}
			}
			if _, proof, err = commitment.ExtractMultiproof(tx, rl); err != nil {
				fmt.Fprintf(os.Stderr, "extract %d: %v\n", n, err)
				os.Exit(1)
			}
			// Enable inline capture for all subsequent (incremental) sub-batches.
			trc.SetIncremental(true)
			trc.EnableProofCapture(true)
			bootstrapped = true
		}

		hdr, herr := hc.ReadHeader(n)
		if herr != nil {
			fmt.Fprintf(os.Stderr, "read header %d: %v\n", n, herr)
			os.Exit(1)
		}
		if root != hdr.Root {
			fmt.Fprintf(os.Stderr, "ROOT MISMATCH block %d: computed %s header %s\n", n, root.Hex(), hdr.Root.Hex())
			os.Exit(2)
		}
		if len(proof) == 0 {
			fmt.Fprintf(os.Stderr, "empty anchor proof at %d\n", n)
			os.Exit(1)
		}
		var fullBytes int
		for _, nd := range proof {
			fullBytes += len(nd)
		}
		wire, cerr := stateless.CompactProofFromNodes(root[:], proof)
		if cerr != nil {
			fmt.Fprintf(os.Stderr, "compact %d: %v\n", n, cerr)
			os.Exit(1)
		}
		if verr := stateless.VerifyProofAnchors(root[:], proof); verr != nil {
			fmt.Fprintf(os.Stderr, "anchor self-check %d: %v\n", n, verr)
			os.Exit(1)
		}
		if werr := os.WriteFile(filepath.Join(anchorDir, fmt.Sprintf("anchor-%010d.bin", n)), wire, 0o644); werr != nil {
			fmt.Fprintf(os.Stderr, "write anchor %d: %v\n", n, werr)
			os.Exit(1)
		}
		anchors++
		fmt.Fprintf(manifest, "%d\t0x%x\t%d\t%d\t%d\n", n, root[:], len(proof), fullBytes, len(wire))
		fmt.Fprintf(os.Stderr, "ANCHOR block=%d root=0x%x proofNodes=%d full=%dB compact=%dB elapsed=%s\n",
			n, root[:6], len(proof), fullBytes, len(wire), time.Since(t0).Truncate(time.Second))

		// Commit the sub-batch (HashedAccounts + TrieOf*), re-begin, reset.
		if err := tx.Commit(); err != nil {
			fmt.Fprintf(os.Stderr, "commit %d: %v\n", n, err)
			os.Exit(1)
		}
		tx, err = db.BeginRw(ctx)
		if err != nil {
			fmt.Fprintln(os.Stderr, "re-begin:", err)
			os.Exit(1)
		}
		trc.SetRwTx(tx)
		dA = map[types.Address]*account.StateAccount{}
		dS = map[types.Address]map[types.Hash]*uint256.Int{}
	}
	tx.Rollback()
	fmt.Fprintf(os.Stderr, "DONE: %d anchors (every %d) saved to %s in %s\n",
		anchors, *K, anchorDir, time.Since(t0).Truncate(time.Second))
}
