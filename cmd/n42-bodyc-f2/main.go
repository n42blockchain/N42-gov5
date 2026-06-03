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
	"encoding/binary"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/bodyf2"
	"github.com/n42blockchain/N42/internal/history"
	l3 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

const segSize = 8192

type hashRec struct {
	h          types.Hash
	block, idx uint64
}

var zenc, _ = zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault))

// buildHashIndex writes an MPHF tx-hash -> (block,index) index (f2.txhash.*) so
// getTransactionByHash can resolve F2 ledger txs (F1.5). Blob = varint(block) ||
// varint(index). Returns the index size.
func buildHashIndex(outDir string, recs []hashRec) (int64, error) {
	mw, err := history.NewMPHFWriter(history.MPHFWriterOpts{
		BaseDir: outDir, Prefix: "f2.txhash", PageSize: 64,
		TmpDir: filepath.Join(outDir, "etl-txhash"), KeyCount: len(recs), EtlBufMB: 256, Logger: l3.Root(),
	})
	if err != nil {
		return 0, err
	}
	var b [binary.MaxVarintLen64 * 2]byte
	for _, hr := range recs {
		n := binary.PutUvarint(b[:], hr.block)
		n += binary.PutUvarint(b[n:], hr.idx)
		if err := mw.Append(hr.h[:], b[:n]); err != nil {
			return 0, err
		}
	}
	if err := mw.Close(); err != nil {
		return 0, err
	}
	st := mw.Stats()
	return int64(st.MphfSize + st.KvSize + st.IdxSize), nil
}

// buildHashIndexFromSidecar builds the tx-hash MPHF index WITHOUT holding hashes
// in memory: it scans the on-disk tx-hash sidecar (f2.txhashes.*) block by block
// and streams each (hash, loc) into the MPHFWriter, whose ETL spills to disk.
// keyCount must equal the total txs written. OOM-safe for full-chain runs.
func buildHashIndexFromSidecar(outDir string, startBlock, lastBlock uint64, keyCount int64) (int64, error) {
	hsr, err := bodyf2.OpenHashReader(outDir)
	if err != nil {
		return 0, err
	}
	defer hsr.Close()
	mw, err := history.NewMPHFWriter(history.MPHFWriterOpts{
		BaseDir: outDir, Prefix: "f2.txhash", PageSize: 64,
		TmpDir: filepath.Join(outDir, "etl-txhash"), KeyCount: int(keyCount), EtlBufMB: 256, Logger: l3.Root(),
	})
	if err != nil {
		return 0, err
	}
	var b [binary.MaxVarintLen64 * 2]byte
	var appended int64
	for n := startBlock; n <= lastBlock; n++ {
		hs, herr := hsr.BlockHashes(n)
		if herr != nil {
			continue // gap / beyond written range
		}
		for idx := range hs {
			nn := binary.PutUvarint(b[:], n)
			nn += binary.PutUvarint(b[nn:], uint64(idx))
			if err := mw.Append(hs[idx][:], b[:nn]); err != nil {
				return 0, err
			}
			appended++
		}
	}
	if appended != keyCount {
		return 0, fmt.Errorf("sidecar tx count %d != expected %d", appended, keyCount)
	}
	if err := mw.Close(); err != nil {
		return 0, err
	}
	st := mw.Stats()
	return int64(st.MphfSize + st.KvSize + st.IdxSize), nil
}

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
	if tx.Type() == 3 {
		f.BlobFeeCap = tx.BlobFeeCap()
		for _, h := range tx.BlobHashes() {
			var hb [32]byte
			copy(hb[:], h[:])
			f.BlobHashes = append(f.BlobHashes, hb)
		}
	}
	if tx.Type() == 4 {
		for _, a := range tx.AuthList() {
			if a == nil {
				continue
			}
			cid := a.ChainID
			f.AuthList = append(f.AuthList, bodyf2.F2Auth{
				ChainID: &cid, Address: a.Address, Nonce: a.Nonce, V: a.V, R: a.R, S: a.S,
			})
		}
	}
	return f
}

func main() {
	dir := flag.String("dir", "", "source bodyc freezer dir")
	out := flag.String("out", "D:/n42-bodyf2", "output dir (f2 segments + addr.dict)")
	start := flag.Uint64("start", 0, "start block (segment-aligned)")
	limit := flag.Int("limit", 2, "segments to convert")
	write := flag.Bool("write", false, "write a real F2 store (f2.cidx + f2.NNNN.cdat + f2.addr.dict) and reopen-verify it")
	sendersDir := flag.String("senders", "", "freezer dir with a pre-computed senders table — read From from it instead of ecrecover (no CPU-bound recovery)")
	txhashes := flag.Bool("txhashes", false, "also write the optional per-block tx-hash sidecar (f2.txhashes.*) so fullTx=false hash lists serve (+32 B/tx)")
	stream := flag.Bool("stream", false, "OOM-safe full run: no in-memory hash accumulation — build the MPHF index by scanning the on-disk tx-hash sidecar afterward (forces --txhashes; --limit 0 = all segments to the tail)")
	flag.Parse()
	if *stream {
		*write = true
		*txhashes = true // the sidecar is the disk-backed hash source for the MPHF
	}
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

	var senderTable *freezer.FreezerTable
	if *sendersDir != "" {
		fz, ferr := freezer.NewReadOnly(*sendersDir)
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "open senders freezer:", ferr)
			os.Exit(1)
		}
		senderTable, ferr = fz.EnsureTableCompressed("senders", "c")
		if ferr != nil {
			fmt.Fprintln(os.Stderr, "open senders table:", ferr)
			os.Exit(1)
		}
		fmt.Printf("senders table: %d blocks covered (reading From from it, no ecrecover)\n", senderTable.Items())
	}

	dict := bodyf2.NewAddrDict()
	var writer *bodyf2.Writer
	if *write {
		if writer, err = bodyf2.NewWriter(*out, dict); err != nil {
			fmt.Fprintln(os.Stderr, "new writer:", err)
			os.Exit(1)
		}
	}
	var hashRecs []hashRec
	var hashWriter *bodyf2.HashWriter
	if *write && *txhashes {
		if hashWriter, err = bodyf2.NewHashWriter(*out); err != nil {
			fmt.Fprintln(os.Stderr, "new hash writer:", err)
			os.Exit(1)
		}
	}
	startSeg := *start / segSize
	var nTx int64
	var f2Bytes int64
	var checked, mismatch int64
	t0 := time.Now()

	tailReached := false
	var lastBlock uint64
	for s := 0; (*limit == 0 || s < *limit) && !tailReached; s++ {
		segStart := (startSeg + uint64(s)) * segSize
		var blocks []bodyf2.F2Block
		var segHashes [][][32]byte
		type srcRec struct {
			from types.Address
			tx   *transaction.Transaction
		}
		var srcByBlock [][]srcRec
		for n := segStart; n < segStart+segSize; n++ {
			body, err := br.ReadBody(n)
			if err != nil {
				if *stream {
					tailReached = true // reached the body tail — stop cleanly
					break
				}
				fmt.Fprintf(os.Stderr, "read %d: %v\n", n, err)
				os.Exit(1)
			}
			// Prefer the pre-computed senders table (pure I/O) over ecrecover.
			var segSenders []types.Address
			if senderTable != nil && n < senderTable.Items() {
				if data, derr := senderTable.Retrieve(n); derr == nil && len(data)/20 == len(body.Txs) {
					segSenders = make([]types.Address, len(body.Txs))
					for i := range segSenders {
						copy(segSenders[i][:], data[i*20:(i+1)*20])
					}
				}
			}
			var signer transaction.Signer
			if segSenders == nil {
				signer = transaction.MakeSignerWithTimestamp(params.EthereumMainnetChainConfig, big.NewInt(int64(n)), 0)
			}
			var blk bodyf2.F2Block
			var recs []srcRec
			var blkHashes [][32]byte
			for ti, tx := range body.Txs {
				var from types.Address
				if segSenders != nil {
					from = segSenders[ti]
				} else {
					from, _ = transaction.Sender(signer, tx)
				}
				blk.Txs = append(blk.Txs, toF2Tx(tx, from))
				recs = append(recs, srcRec{from, tx})
				if writer != nil {
					th := tx.Hash()
					if !*stream {
						hashRecs = append(hashRecs, hashRec{th, n, uint64(ti)})
					}
					if hashWriter != nil {
						var h [32]byte
						copy(h[:], th[:])
						blkHashes = append(blkHashes, h)
					}
				}
				nTx++
			}
			for _, w := range body.Withdrawals {
				blk.Withdrawals = append(blk.Withdrawals, bodyf2.F2Withdrawal{
					Index: w.Index, Validator: w.Validator, Address: w.Address, Amount: w.Amount,
				})
			}
			blocks = append(blocks, blk)
			srcByBlock = append(srcByBlock, recs)
			if hashWriter != nil {
				segHashes = append(segHashes, blkHashes)
			}
			lastBlock = n
		}
		if len(blocks) == 0 {
			continue // tail fell exactly on a segment boundary — nothing to write
		}
		segNum := startSeg + uint64(s)
		raw := bodyf2.EncodeSegment(blocks, dict)
		f2Bytes += int64(len(zenc.EncodeAll(raw, nil)))
		if writer != nil {
			if err := writer.AppendSegment(segNum, blocks); err != nil {
				fmt.Fprintln(os.Stderr, "append segment:", err)
				os.Exit(1)
			}
		}
		if hashWriter != nil {
			if err := hashWriter.AppendSegment(segNum, segHashes); err != nil {
				fmt.Fprintln(os.Stderr, "append hash segment:", err)
				os.Exit(1)
			}
		}

		// Per-segment decode-verify (skipped in stream mode: with senders the
		// From compare is tautological and the reopen + sidecar checks cover it).
		if *stream {
			continue
		}
		got, err := bodyf2.DecodeSegment(raw, dict)
		if err != nil {
			fmt.Fprintf(os.Stderr, "decode seg %d: %v\n", segNum, err)
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

	storeMsg := ""
	if writer != nil {
		if err := writer.Close(); err != nil {
			fmt.Fprintln(os.Stderr, "writer close:", err)
			os.Exit(1)
		}
		if hashWriter != nil {
			if err := hashWriter.Close(); err != nil {
				fmt.Fprintln(os.Stderr, "hash writer close:", err)
				os.Exit(1)
			}
			fmt.Println("tx-hash sidecar f2.txhashes.* written (fullTx=false hash lists)")
		}
		// Build the tx-hash -> (block,index) MPHF index (F1.5: getTransactionByHash).
		// Stream mode builds it OOM-free by scanning the on-disk sidecar.
		var idxBytes int64
		if *stream {
			idxBytes, err = buildHashIndexFromSidecar(*out, startSeg*segSize, lastBlock, nTx)
		} else {
			idxBytes, err = buildHashIndex(*out, hashRecs)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "build hash index:", err)
			os.Exit(1)
		}
		fmt.Printf("tx-hash MPHF index: %d keys, %d B (%.2f B/tx)\n", nTx, idxBytes, float64(idxBytes)/float64(max1(nTx)))
		// Verify a sample of hash lookups resolve to the right (block,index).
		if hr, herr := history.OpenMPHF(*out, "f2.txhash"); herr == nil {
			hChecked, hBad := 0, 0
			if *stream {
				if hsr, e := bodyf2.OpenHashReader(*out); e == nil {
					span := lastBlock - startSeg*segSize
					stepN := span/2000 + 1
					for n := startSeg * segSize; n <= lastBlock; n += stepN {
						hs, herr2 := hsr.BlockHashes(n)
						if herr2 != nil {
							continue
						}
						for idx := range hs {
							blob, ok, _ := hr.Get(hs[idx][:])
							hChecked++
							if !ok {
								hBad++
								continue
							}
							b, nn := binary.Uvarint(blob)
							gi, _ := binary.Uvarint(blob[nn:])
							if b != n || gi != uint64(idx) {
								hBad++
							}
						}
					}
					hsr.Close()
				}
			} else {
				step := 1
				if len(hashRecs) > 100000 {
					step = len(hashRecs) / 100000
				}
				for i := 0; i < len(hashRecs); i += step {
					rec := hashRecs[i]
					blob, ok, _ := hr.Get(rec.h[:])
					hChecked++
					if !ok {
						hBad++
						continue
					}
					b, n := binary.Uvarint(blob)
					idx, _ := binary.Uvarint(blob[n:])
					if b != rec.block || idx != rec.idx {
						hBad++
					}
				}
			}
			hr.Close()
			fmt.Printf("tx-hash index lookup: checked=%d bad=%d → %s\n", hChecked, hBad,
				map[bool]string{true: "PASS", false: "FAIL"}[hBad == 0])
		}
		// Verification sampling range (stream converts an unknown #segments to the
		// tail; sample at most ~2000 segments).
		verifySegs := *limit
		if *stream {
			verifySegs = int((lastBlock-startSeg*segSize)/segSize) + 1
		}
		sampleStep := 1
		if verifySegs > 2000 {
			sampleStep = verifySegs / 2000
		}
		// Verify the optional per-block tx-hash sidecar vs source tx.Hash().
		if hashWriter != nil {
			if hsr, e := bodyf2.OpenHashReader(*out); e == nil {
				hsChecked, hsBad := 0, 0
				for s := 0; s < verifySegs; s += sampleStep {
					n := (startSeg+uint64(s))*segSize + 1
					got, gerr := hsr.BlockHashes(n)
					body, berr := br.ReadBody(n)
					if gerr != nil || berr != nil || len(got) != len(body.Txs) {
						hsBad++
						continue
					}
					for ti, tx := range body.Txs {
						hsChecked++
						var th [32]byte
						h := tx.Hash()
						copy(th[:], h[:])
						if got[ti] != th {
							hsBad++
						}
					}
				}
				hsr.Close()
				fmt.Printf("tx-hash sidecar verify: checked=%d bad=%d → %s\n", hsChecked, hsBad,
					map[bool]string{true: "PASS", false: "FAIL"}[hsBad == 0])
			}
		}
		// Reopen the written store and verify a sample against the source.
		rd, err := bodyf2.OpenReader(*out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "reopen:", err)
			os.Exit(1)
		}
		rtChecked, rtBad := 0, 0
		for s := 0; s < verifySegs; s += sampleStep {
			segStart := (startSeg + uint64(s)) * segSize
			for _, n := range []uint64{segStart, segStart + segSize/2, segStart + segSize - 1} {
				if n > lastBlock {
					continue
				}
				fb, e1 := rd.ReadBlock(n)
				body, e2 := br.ReadBody(n)
				if e1 != nil || e2 != nil {
					rtBad++
					continue
				}
				signer := transaction.MakeSignerWithTimestamp(params.EthereumMainnetChainConfig, big.NewInt(int64(n)), 0)
				for ti, tx := range body.Txs {
					if ti >= len(fb.Txs) {
						rtBad++
						break
					}
					from, _ := transaction.Sender(signer, tx)
					rtChecked++
					if fb.Txs[ti].From != from || fb.Txs[ti].Value.Cmp(tx.Value()) != 0 {
						rtBad++
					}
				}
			}
		}
		rd.Close()
		storeMsg = fmt.Sprintf("F2 store written to %s; reopen+verify vs source: checked=%d bad=%d → %s",
			*out, rtChecked, rtBad, map[bool]string{true: "PASS", false: "FAIL"}[rtBad == 0])
	} else if err := dict.Save(*out + "/addr.dict"); err != nil {
		fmt.Fprintln(os.Stderr, "save dict:", err)
		os.Exit(1)
	}
	perTx := func(v int64) float64 { return float64(v) / float64(max1(nTx)) }
	fmt.Printf("converted %d segments from block %d: txs=%d in %s\n", *limit, startSeg*segSize, nTx, time.Since(t0).Truncate(time.Second))
	fmt.Printf("F2 on-disk (zstd): %d B → %.2f B/tx  (proto L baseline ~148.7 → F2 ~81; sig dropped)\n", f2Bytes, perTx(f2Bytes))
	fmt.Printf("addr dict: %d unique addrs (%d B sidecar, %.3f B/tx over this range)\n", dict.Len(), dict.Len()*20, float64(dict.Len()*20)/float64(max1(nTx)))
	fmt.Printf("verify From/To/Value/Nonce/Gas vs source (ecrecover): checked=%d mismatch=%d → %s\n",
		checked, mismatch, map[bool]string{true: "PASS", false: "FAIL"}[mismatch == 0])
	if storeMsg != "" {
		fmt.Println(storeMsg)
	}
}

func max1(v int64) int64 {
	if v < 1 {
		return 1
	}
	return v
}
