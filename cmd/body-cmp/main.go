// body-cmp: byte-level diff a single block's body decoded from two
// sources — geth ancient (raw RLP) vs N42 columnar (HeaderCompact +
// BodyCompact). Pinpoints which field of which tx drifts when
// witness-replay reports a gas mismatch.
//
// Use: build\bin\body-cmp.exe --geth <dir> --n42 <dir> --block N
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/lib/kv"
)

func main() {
	gethDir := flag.String("geth", "", "geth ancient dir (raw RLP)")
	n42Dir := flag.String("n42", "", "N42 columnar freezer dir (with headers.cidx)")
	blockNum := flag.Uint64("block", 0, "block number")
	flag.Parse()
	if *gethDir == "" || *n42Dir == "" {
		fmt.Fprintln(os.Stderr, "usage: body-cmp --geth <dir> --n42 <dir> --block N")
		os.Exit(1)
	}

	modules.N42Init()
	for name, cfg := range modules.N42TableCfg {
		kv.ChaindataTablesCfg[name] = cfg
	}

	// Geth side.
	gf, err := freezer.NewReadOnly(*gethDir)
	if err != nil {
		die("open geth: %v", err)
	}
	defer gf.Close()
	gethHdrData, err := gf.Ancient(freezer.TableHeaders, *blockNum)
	if err != nil {
		die("read geth header: %v", err)
	}
	gethHdr, err := ethel.DecodeGethHeader(gethHdrData)
	if err != nil {
		die("decode geth header: %v", err)
	}
	gethBodyData, err := gf.Ancient(freezer.TableBodies, *blockNum)
	if err != nil {
		die("read geth body: %v", err)
	}
	gethBody, err := ethel.DecodeGethBody(gethBodyData)
	if err != nil {
		die("decode geth body: %v", err)
	}

	// N42 side: walk from 0 chaining parentHash forward AND
	// recomputing Bloom from receipts (mirrors n42CompactSource so
	// returned Hash() equals canonical mainnet hash).
	hr, err := ethel.OpenHeaderCompact(*n42Dir)
	if err != nil {
		die("open n42 header compact: %v", err)
	}
	defer hr.Close()
	// Receipts come from geth ancient (the n42 columnar dir's
	// receipts.cdat is sometimes truncated; geth ancient is full).
	var prevHash [32]byte
	var n42Hdr *block.Header
	for n := uint64(0); n <= *blockNum; n++ {
		h, err := hr.ReadHeader(n)
		if err != nil {
			die("read n42 header %d: %v", n, err)
		}
		if n > 0 {
			h.ParentHash = prevHash
		}
		// Recompute bloom from geth ancient receipts.
		if rd, rerr := gf.Ancient(freezer.TableReceipts, n); rerr == nil && len(rd) > 0 {
			rec, derr := ethel.DecodeGethReceipts(rd)
			if derr == nil {
				h.Bloom = block.CreateBloom(rec)
			}
		}
		h.ResetHashCache()
		hh := h.Hash()
		prevHash = hh
		if n == *blockNum {
			n42Hdr = h
		}
	}
	br, err := ethel.OpenBodyCompact(*n42Dir)
	if err != nil {
		die("open n42 body compact: %v", err)
	}
	defer br.Close()
	n42Decoded, err := br.ReadBody(*blockNum)
	if err != nil {
		die("read n42 body: %v", err)
	}

	fmt.Println("--- HEADER ---")
	fmt.Printf("  geth hash=%x gasUsed=%d gasLimit=%d coinbase=%x\n",
		gethHdr.Hash(), gethHdr.GasUsed, gethHdr.GasLimit, gethHdr.Coinbase)
	fmt.Printf("  n42  hash=%x gasUsed=%d gasLimit=%d coinbase=%x\n",
		n42Hdr.Hash(), n42Hdr.GasUsed, n42Hdr.GasLimit, n42Hdr.Coinbase)
	if gethHdr.Hash() != n42Hdr.Hash() {
		fmt.Println("  HASH DIFFERS")
		fmt.Printf("  geth time=%d diff=%s nonce=%v root=%x\n",
			gethHdr.Time, gethHdr.Difficulty, gethHdr.Nonce, gethHdr.Root)
		fmt.Printf("  n42  time=%d diff=%s nonce=%v root=%x\n",
			n42Hdr.Time, n42Hdr.Difficulty, n42Hdr.Nonce, n42Hdr.Root)
		fmt.Printf("  geth parent=%x txRoot=%x receiptRoot=%x bloom_first16=%x extra=%x mix=%x uncle=%x\n",
			gethHdr.ParentHash, gethHdr.TxHash, gethHdr.ReceiptHash, gethHdr.Bloom[:16], gethHdr.Extra, gethHdr.MixDigest, gethHdr.UncleHash)
		fmt.Printf("  n42  parent=%x txRoot=%x receiptRoot=%x bloom_first16=%x extra=%x mix=%x uncle=%x\n",
			n42Hdr.ParentHash, n42Hdr.TxHash, n42Hdr.ReceiptHash, n42Hdr.Bloom[:16], n42Hdr.Extra, n42Hdr.MixDigest, n42Hdr.UncleHash)
	}
	fmt.Println("--- BODY ---")
	fmt.Printf("Block %d: geth %d txs, n42 %d txs\n",
		*blockNum, len(gethBody.Transactions), len(n42Decoded.Txs))
	if len(gethBody.Transactions) != len(n42Decoded.Txs) {
		die("tx count mismatch")
	}

	for i := range gethBody.Transactions {
		gtx := gethBody.Transactions[i]
		ntx := n42Decoded.Txs[i]
		diffTx(i, gtx, ntx)
	}

	// Uncles too.
	fmt.Printf("Uncles: geth %d, n42 %d\n", len(gethBody.Uncles), len(n42Decoded.UncleRLP))
}

func diffTx(idx int, g, n *transaction.Transaction) {
	gh, nh := g.Hash(), n.Hash()
	if gh == nh {
		// A hash comparison does catch a damaged authorization — the auth list
		// is part of the type-4 preimage — but it only tells you THAT the tx
		// differs. Diffing the authorization fields here names the offending
		// one directly, which is the difference between "tx[53] differs" and
		// "auth[0].V is 27 upstream and 0 locally".
		if d := diffAuthList(g, n); d != "" {
			fmt.Printf("  tx[%d] hash match but AUTH LIST DIFFERS: %x type=%d\n", idx, gh[:8], g.Type())
			fmt.Print(d)
			return
		}
		fmt.Printf("  tx[%d] hash match: %x type=%d\n", idx, gh[:8], g.Type())
		return
	}
	fmt.Printf("  tx[%d] HASH DIFFER:\n", idx)
	fmt.Printf("    geth %x type=%d nonce=%d gas=%d\n", gh, g.Type(), g.Nonce(), g.Gas())
	fmt.Printf("    n42  %x type=%d nonce=%d gas=%d\n", nh, n.Type(), n.Nonce(), n.Gas())
	fmt.Printf("    geth value=%s data_len=%d\n", g.Value().String(), len(g.Data()))
	fmt.Printf("    n42  value=%s data_len=%d\n", n.Value().String(), len(n.Data()))
	gv, gr, gs := g.RawSignatureValues()
	nv, nr, ns := n.RawSignatureValues()
	fmt.Printf("    geth V=%s R=%s S=%s\n", gv.String(), gr.String(), gs.String())
	fmt.Printf("    n42  V=%s R=%s S=%s\n", nv.String(), nr.String(), ns.String())
	gTo := g.To()
	nTo := n.To()
	gtoStr := "nil"
	if gTo != nil {
		gtoStr = fmt.Sprintf("%x", gTo)
	}
	ntoStr := "nil"
	if nTo != nil {
		ntoStr = fmt.Sprintf("%x", nTo)
	}
	fmt.Printf("    geth to=%s\n", gtoStr)
	fmt.Printf("    n42  to=%s\n", ntoStr)
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// diffAuthList names the differing field inside an EIP-7702 authorization.
// Returns "" when they match.
func diffAuthList(g, n *transaction.Transaction) string {
	ga, na := g.AuthList(), n.AuthList()
	if len(ga) == 0 && len(na) == 0 {
		return ""
	}
	var b strings.Builder
	if len(ga) != len(na) {
		fmt.Fprintf(&b, "    auth count geth=%d n42=%d\n", len(ga), len(na))
		return b.String()
	}
	for i := range ga {
		a, c := ga[i], na[i]
		if a.ChainID.Cmp(&c.ChainID) != 0 {
			fmt.Fprintf(&b, "    auth[%d].ChainID geth=%s n42=%s\n", i, a.ChainID.String(), c.ChainID.String())
		}
		if a.Address != c.Address {
			fmt.Fprintf(&b, "    auth[%d].Address geth=%x n42=%x\n", i, a.Address, c.Address)
		}
		if a.Nonce != c.Nonce {
			fmt.Fprintf(&b, "    auth[%d].Nonce geth=%d n42=%d\n", i, a.Nonce, c.Nonce)
		}
		if u256(a.V) != u256(c.V) {
			fmt.Fprintf(&b, "    auth[%d].V geth=%s n42=%s  <-- non-parity V is flattened by a pre-bfAuthVFull segment\n",
				i, u256(a.V), u256(c.V))
		}
		if u256(a.R) != u256(c.R) {
			fmt.Fprintf(&b, "    auth[%d].R geth=%s n42=%s\n", i, u256(a.R), u256(c.R))
		}
		if u256(a.S) != u256(c.S) {
			fmt.Fprintf(&b, "    auth[%d].S geth=%s n42=%s\n", i, u256(a.S), u256(c.S))
		}
	}
	return b.String()
}

func u256(v *uint256.Int) string {
	if v == nil {
		return "<nil>"
	}
	return v.String()
}
