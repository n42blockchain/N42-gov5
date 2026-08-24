// body-cmp: byte-level diff a single block's body decoded from two
// sources — geth ancient (raw RLP) vs N42 columnar (HeaderCompact +
// BodyCompact). Pinpoints which field of which tx drifts when
// witness-replay reports a gas mismatch.
//
// Use: build\bin\body-cmp.exe --geth <dir> --n42 <dir> --block N
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
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

	// N42 side. Current headerc stores each canonical hash in the segment
	// trailer, so a single target read is sufficient. Recover ParentHash
	// from the preceding stored hash, matching n42CompactSource.
	hr, err := ethel.OpenHeaderCompact(*n42Dir)
	if err != nil {
		die("open n42 header compact: %v", err)
	}
	defer hr.Close()
	n42Hdr, err := hr.ReadHeader(*blockNum)
	if err != nil {
		die("read n42 header %d: %v", *blockNum, err)
	}
	if *blockNum > 0 {
		parent, perr := hr.ReadHeader(*blockNum - 1)
		if perr != nil {
			die("read n42 parent header %d: %v", *blockNum-1, perr)
		}
		n42Hdr.ParentHash = parent.Hash()
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
		fmt.Printf("  tx[%d] hash match: %x type=%d\n", idx, gh[:8], g.Type())
		return
	}
	fmt.Printf("  tx[%d] HASH DIFFER:\n", idx)
	fmt.Printf("    geth %x type=%d nonce=%d gas=%d\n", gh, g.Type(), g.Nonce(), g.Gas())
	fmt.Printf("    n42  %x type=%d nonce=%d gas=%d\n", nh, n.Type(), n.Nonce(), n.Gas())
	fmt.Printf("    geth value=%s data_len=%d\n", g.Value().String(), len(g.Data()))
	fmt.Printf("    n42  value=%s data_len=%d\n", n.Value().String(), len(n.Data()))
	fmt.Printf("    chainID geth=%s n42=%s\n", g.ChainId().String(), n.ChainId().String())
	fmt.Printf("    feeCap geth=%s n42=%s tipCap geth=%s n42=%s\n",
		g.GasFeeCap().String(), n.GasFeeCap().String(), g.GasTipCap().String(), n.GasTipCap().String())
	fmt.Printf("    data_equal=%t accessList geth=%d n42=%d authList geth=%d n42=%d\n",
		bytes.Equal(g.Data(), n.Data()), len(g.AccessList()), len(n.AccessList()), len(g.AuthList()), len(n.AuthList()))
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
	gal, nal := g.AuthList(), n.AuthList()
	for i := 0; i < len(gal) || i < len(nal); i++ {
		if i < len(gal) {
			a := gal[i]
			fmt.Printf("    geth auth[%d] chainID=%s addr=%s nonce=%d V=%s R=%s S=%s\n",
				i, a.ChainID.String(), a.Address.Hex(), a.Nonce, a.V.String(), a.R.String(), a.S.String())
		}
		if i < len(nal) {
			a := nal[i]
			fmt.Printf("    n42  auth[%d] chainID=%s addr=%s nonce=%d V=%s R=%s S=%s\n",
				i, a.ChainID.String(), a.Address.Hex(), a.Nonce, a.V.String(), a.R.String(), a.S.String())
		}
	}
}

func die(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
