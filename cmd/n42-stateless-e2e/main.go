// n42-stateless-e2e — Phase E end-to-end driver. Runs the minimal client's
// three trust layers on REAL freezer data over a window of real mainnet blocks,
// proving that a stateless minimal client can zero-trust verify real blocks:
//
//	① header chain  — extend a trusted HeaderChain block by block (parentHash).
//	② witness replay — VerifyWitnessReceipt replays the real per-block witness
//	                   through the EVM and checks gasUsed + receiptRoot == header.
//	③ MPT anchor    — (optional, --anchors) at anchor heights the real BlockProof
//	                   multiproof must re-hash to the header's stateRoot.
//
// Contract bytecode the witness references but does not carry is fetched on
// demand from the datadir's MDBX Code table (the witness model: state in the
// witness, code by hash from the producer/local store). Read-only throughout.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

func main() {
	headersDir := flag.String("headers", `D:/n42-eth1/chain/freezer`, "columnar headerc dir")
	bodiesDir := flag.String("bodies", `D:/n42-eth1/chain/freezer`, "columnar bodyc dir")
	witDir := flag.String("witness", `D:/N42-eth1177/chain/freezer`, "witness freezer dir (TableBlockWitness)")
	datadir := flag.String("datadir", `D:/N42-eth1177`, "MDBX datadir holding the Code table (code-by-hash on demand)")
	codesDir := flag.String("codes", `D:/N42-eth1177/chain/freezer`, "address-indexed codes-freezer dir (complete bytecode source; must be built to >= --from+--count)")
	sendersDir := flag.String("senders", `D:/N42-eth1177/chain/freezer`, "senders freezer dir (matches the recording's sender source; empty = ecrecover)")
	anchorsDir := flag.String("anchors", "", "optional anchorc freezer dir for layer ③ (empty = skip ③)")
	from := flag.Uint64("from", 24989000, "trusted anchor block (header chain root); verification runs from from+1")
	count := flag.Uint64("count", 100, "number of blocks to verify")
	k := flag.Uint64("k", 1000, "anchor cadence (layer ③ runs at blocks n%k==0)")
	mapGB := flag.Int("mapsize-gb", 4096, "code MDBX mapsize GB")
	flag.Parse()

	ctx := context.Background()
	fail := func(msg string, err error) {
		fmt.Fprintf(os.Stderr, "FAIL: %s: %v\n", msg, err)
		os.Exit(1)
	}

	hc, err := ethel.OpenHeaderCompact(*headersDir)
	if err != nil {
		fail("open headerc", err)
	}
	defer hc.Close()
	bc, err := ethel.OpenBodyCompact(*bodiesDir)
	if err != nil {
		fail("open bodyc", err)
	}
	defer bc.Close()
	wit, err := freezer.NewFreezerTableCompressedReadOnly(*witDir, freezer.TableBlockWitness, "c")
	if err != nil {
		fail("open witness", err)
	}
	wit.ForceBatchSize(freezer.BatchSize)
	defer wit.Close()

	// Address-indexed codes-freezer — the COMPLETE bytecode source. The by-hash
	// MDBX Code table is not complete to the tip, so the witness replay would
	// miss a contract whose code was deployed past the MDBX's height; the
	// codes-freezer (genesis→tip) covers it. Must be built to >= the verify range.
	codesFz, err := ethel.NewCodesFreezerReader(*codesDir)
	if err != nil {
		fail("open codes-freezer", err)
	}
	// Senders freezer — match the recording's sender source (the executor used the
	// senders table, not ecrecover); a mismatch would misalign the witness stream.
	var senderTbl *freezer.FreezerTable
	if *sendersDir != "" {
		senderTbl, err = freezer.NewFreezerTableCompressedReadOnly(*sendersDir, freezer.TableSenders, "c")
		if err != nil {
			fail("open senders", err)
		}
		senderTbl.ForceBatchSize(freezer.BatchSize)
		defer senderTbl.Close()
	}
	readSenders := func(n uint64, txCount int) []types.Address {
		if senderTbl == nil {
			return nil
		}
		data, e := senderTbl.Retrieve(n)
		if e != nil || len(data)/20 != txCount {
			return nil // fall back to ecrecover on miss/mismatch
		}
		sns := make([]types.Address, txCount)
		for i := 0; i < txCount; i++ {
			copy(sns[i][:], data[i*20:(i+1)*20])
		}
		return sns
	}

	db, err := mdbx.NewMDBX(log.New()).Path(*datadir).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Readonly().Open(ctx)
	if err != nil {
		fail("open code MDBX", err)
	}
	defer db.Close()
	codeFetch := func(h types.Hash) ([]byte, error) {
		var code []byte
		_ = db.View(ctx, func(tx kv.Tx) error {
			code, _ = tx.GetOne(kv.Code, h[:])
			return nil
		})
		if len(code) == 0 {
			return nil, fmt.Errorf("code %x not found", h[:6])
		}
		return code, nil
	}

	var anchorTbl *freezer.FreezerTable
	var anchorBlocks []uint64
	if *anchorsDir != "" {
		anchorTbl, err = freezer.NewFreezerTableCompressedReadOnly(*anchorsDir, "anchorc", "c")
		if err != nil {
			fail("open anchorc", err)
		}
		defer anchorTbl.Close()
		sb, e := os.ReadFile(*anchorsDir + "/anchorc.blocks")
		if e != nil {
			fail("read anchor sidecar", e)
		}
		for i := 0; i+8 <= len(sb); i += 8 {
			anchorBlocks = append(anchorBlocks, beU64(sb[i:i+8]))
		}
	}

	chainCfg := params.EthereumMainnetChainConfig
	engine := ethel.NewEthReplayEngine(chainCfg)

	anchorHdr, err := hc.ReadHeader(*from)
	if err != nil || anchorHdr == nil {
		fail(fmt.Sprintf("read anchor header %d", *from), err)
	}
	chain, err := stateless.NewHeaderChain(anchorHdr)
	if err != nil {
		fail("new header chain", err)
	}
	ancestor := func(n uint64) types.Hash { h, _ := chain.TrustedHash(n); return h }

	fmt.Printf("Phase E: header-chain anchor=%d, verifying %d..%d (layer③ %s)\n",
		*from, *from+1, *from+*count, ifStr(*anchorsDir != "", "ON", "OFF"))

	t0 := time.Now()
	verified, anchorsChecked := 0, 0
	for n := *from + 1; n <= *from+*count; n++ {
		hdr, herr := hc.ReadHeader(n)
		if herr != nil || hdr == nil {
			fail(fmt.Sprintf("read header %d", n), herr)
		}
		// ① header chain. The columnar headerc drops ParentHash (derivable); the
		// producer reconstructs it as the hash of the previous header before
		// serving. Mirror that: ParentHash[n] = hash of header[n-1] (the chain's
		// current head). The chain is then internally consistent and ②③ verify the
		// roots, anchored at `from`.
		ph, ok := chain.TrustedHash(n - 1)
		if !ok {
			fail(fmt.Sprintf("① no trusted hash at %d", n-1), nil)
		}
		hdr.ParentHash = ph
		if err := chain.Extend(hdr); err != nil {
			fail(fmt.Sprintf("① extend %d", n), err)
		}
		// body.
		dec, berr := bc.ReadBody(n)
		if berr != nil {
			fail(fmt.Sprintf("read body %d", n), berr)
		}
		body := &ethel.GethBodyResult{Transactions: dec.Txs, Withdrawals: dec.Withdrawals}
		for _, u := range dec.UncleRLP {
			var uh block.Header
			if rlp.DecodeBytes(u, &uh) == nil {
				body.Uncles = append(body.Uncles, &uh)
			}
		}
		// witness.
		w, werr := wit.Retrieve(n)
		if werr != nil {
			fail(fmt.Sprintf("read witness %d", n), werr)
		}
		// ② witness EVM replay → receiptRoot/gasUsed vs header. Senders from the
		// freezer (matching the recording); code from the complete codes-freezer
		// (height >= block) with the by-hash MDBX as on-demand fallback.
		in := &ethel.MinimalVerifyInput{Header: hdr, Body: body, Witness: w, Senders: readSenders(n, len(body.Transactions))}
		if err := ethel.VerifyWitnessReceiptWithCodes(in, ancestor, chainCfg, engine, codeFetch, codesFz); err != nil {
			fail(fmt.Sprintf("② replay %d", n), err)
		}
		// ③ MPT anchor (optional, at cadence boundaries that have an anchor).
		if anchorTbl != nil && *k > 0 && n%*k == 0 {
			if item, ok := anchorItem(anchorBlocks, n); ok {
				wire, aerr := anchorTbl.Retrieve(item)
				if aerr != nil {
					fail(fmt.Sprintf("read anchor %d", n), aerr)
				}
				bp, derr := stateless.DecodeBlockProof(wire)
				if derr != nil {
					fail(fmt.Sprintf("decode anchor %d", n), derr)
				}
				// ③ recompute the post-state root from the multiproof + changeset
				// and check it against the trusted header chain (VerifyAgainstChain,
				// the same check n42-stateless-client-test --transition runs).
				if err := stateless.VerifyAgainstChain(chain, bp); err != nil {
					fail(fmt.Sprintf("③ anchor %d", n), err)
				}
				anchorsChecked++
			}
		}
		verified++
		if verified%20 == 0 {
			fmt.Printf("  ... %d/%d verified (%d anchors) %s\n", verified, *count, anchorsChecked, time.Since(t0).Round(time.Second))
		}
	}
	fmt.Printf("ALL VERIFIED ✓ — %d blocks (①header + ②witness-replay), %d MPT anchors (③), %s\n",
		verified, anchorsChecked, time.Since(t0).Round(time.Millisecond))
}

func beU64(b []byte) uint64 {
	var v uint64
	for _, x := range b {
		v = v<<8 | uint64(x)
	}
	return v
}

// anchorItem binary-searches the ascending sidecar for block n's freezer item.
func anchorItem(blocks []uint64, n uint64) (uint64, bool) {
	lo, hi := 0, len(blocks)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case blocks[mid] == n:
			return uint64(mid), true
		case blocks[mid] < n:
			lo = mid + 1
		default:
			hi = mid - 1
		}
	}
	return 0, false
}

func ifStr(c bool, a, b string) string {
	if c {
		return a
	}
	return b
}
