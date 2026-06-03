// n42-bodyc-f2: convert a bodyc store to the production F2 (no-signature) body
// format and verify it. Single pass: read each block, ecrecover the sender,
// intern from/to into a global AddrDict, encode the F2 segment, zstd it. Then
// decode and check From == ecrecover and To/Value/Nonce/Gas == source for a
// sample. Reports F2 on-disk B/tx (vs the proto's L baseline ~148.7).
//
// Heavy (ecrecover per tx) — run on a --limit range to validate; a full
// conversion is a long background job (don't co-run with another CPU task).
package main

import (
	"flag"
	"fmt"
	"math/big"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/bodyf2"
	"github.com/n42blockchain/N42/params"
)

const segSize = 8192

var zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))

func toF2Tx(tx *transaction.Transaction, from types.Address) bodyf2.F2Tx {
	f := bodyf2.F2Tx{
		Type: tx.Type(), From: from, To: tx.To(),
		Nonce: tx.Nonce(), Gas: tx.Gas(),
		Value: tx.Value(), GasFeeCap: tx.GasFeeCap(),
	}
	if tx.Type() >= 2 {
		f.GasTipCap = tx.GasTipCap()
	}
	if d := tx.Data(); len(d) > 0 {
		f.Data = d
	}
	for _, t := range tx.AccessList() {
		at := bodyf2.F2AccessTuple{Address: t.Address}
		for _, k := range t.StorageKeys {
			var kb [32]byte
			copy(kb[:], k[:])
			at.StorageKeys = append(at.StorageKeys, kb)
		}
		f.Access = append(f.Access, at)
	}
	return f
}

func main() {
	dir := flag.String("dir", "", "source bodyc freezer dir")
	out := flag.String("out", "D:/n42-bodyf2", "output dir (f2 segments + addr.dict)")
	start := flag.Uint64("start", 0, "start block (segment-aligned)")
	limit := flag.Int("limit", 2, "segments to convert")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-bodyc-f2 --dir <bodyc> [--out D] [--start N] [--limit K]")
		os.Exit(1)
	}
	br, err := ethel.OpenBodyCompact(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer br.Close()
	if err := os.MkdirAll(*out, 0755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	dict := bodyf2.NewAddrDict()
	startSeg := *start / segSize
	var nTx int64
	var f2Bytes int64
	var checked, mismatch int64
	t0 := time.Now()

	for s := 0; s < *limit; s++ {
		segStart := (startSeg + uint64(s)) * segSize
		var blocks []bodyf2.F2Block
		type srcRec struct {
			from types.Address
			tx   *transaction.Transaction
		}
		var srcByBlock [][]srcRec
		for n := segStart; n < segStart+segSize; n++ {
			body, err := br.ReadBody(n)
			if err != nil {
				fmt.Fprintf(os.Stderr, "read %d: %v\n", n, err)
				os.Exit(1)
			}
			signer := transaction.MakeSignerWithTimestamp(params.EthereumMainnetChainConfig, big.NewInt(int64(n)), 0)
			var blk bodyf2.F2Block
			var recs []srcRec
			for _, tx := range body.Txs {
				from, _ := transaction.Sender(signer, tx)
				blk.Txs = append(blk.Txs, toF2Tx(tx, from))
				recs = append(recs, srcRec{from, tx})
				nTx++
			}
			for _, w := range body.Withdrawals {
				blk.Withdrawals = append(blk.Withdrawals, bodyf2.F2Withdrawal{
					Index: w.Index, Validator: w.Validator, Address: w.Address, Amount: w.Amount,
				})
			}
			blocks = append(blocks, blk)
			srcByBlock = append(srcByBlock, recs)
		}
		raw := bodyf2.EncodeSegment(blocks, dict)
		f2Bytes += int64(len(zenc.EncodeAll(raw, nil)))

		// Verify: decode + compare From/To/Value/Nonce/Gas to source.
		got, err := bodyf2.DecodeSegment(raw, dict)
		if err != nil {
			fmt.Fprintf(os.Stderr, "decode seg %d: %v\n", startSeg+uint64(s), err)
			os.Exit(1)
		}
		for bi := range got {
			for ti := range got[bi].Txs {
				g := got[bi].Txs[ti]
				src := srcByBlock[bi][ti]
				checked++
				bad := g.From != src.from || g.Nonce != src.tx.Nonce() || g.Gas != src.tx.Gas() ||
					g.Value.Cmp(src.tx.Value()) != 0
				if (g.To == nil) != (src.tx.To() == nil) || (src.tx.To() != nil && g.To != nil && *g.To != *src.tx.To()) {
					bad = true
				}
				if bad {
					mismatch++
				}
			}
		}
	}

	dictPath := *out + "/addr.dict"
	if err := dict.Save(dictPath); err != nil {
		fmt.Fprintln(os.Stderr, "save dict:", err)
		os.Exit(1)
	}
	perTx := func(v int64) float64 { return float64(v) / float64(max1(nTx)) }
	fmt.Printf("converted %d segments from block %d: txs=%d in %s\n", *limit, startSeg*segSize, nTx, time.Since(t0).Truncate(time.Second))
	fmt.Printf("F2 on-disk (zstd): %d B → %.2f B/tx  (proto L baseline ~148.7 → F2 ~81; sig dropped)\n", f2Bytes, perTx(f2Bytes))
	fmt.Printf("addr dict: %d unique addrs (%d B sidecar, %.3f B/tx over this range)\n", dict.Len(), dict.Len()*20, float64(dict.Len()*20)/float64(max1(nTx)))
	fmt.Printf("verify From/To/Value/Nonce/Gas vs source (ecrecover): checked=%d mismatch=%d → %s\n",
		checked, mismatch, map[bool]string{true: "PASS", false: "FAIL"}[mismatch == 0])
}

func max1(v int64) int64 {
	if v < 1 {
		return 1
	}
	return v
}
