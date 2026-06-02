// bodyc-hashidx-proto: stage-2 prototype for the F1.5 tier — a tx-hash → (block,
// index) lookup index built with the production RecSplit MPHF coldstore
// (internal/history.MPHFWriter). Validates that getTransactionByHash can be
// served WITHOUT storing the 32B hash per tx:
//
//   - key   = 32B tx hash
//   - blob  = varint(blockNum) || varint(txIndex)
//   - guard = 4B xxhash fingerprint (rejects out-of-set / phantom hashes)
//
// Reports the on-disk index size (.mphf + .kv + .idx) in B/tx, then verifies a
// sample of in-set hashes resolve to the right location and that random
// out-of-set hashes are rejected.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/history"
	"github.com/n42blockchain/N42/lib/log/v3"
)

func main() {
	dir := flag.String("dir", "", "bodyc freezer dir (source, read-only)")
	start := flag.Uint64("start", 0, "start block")
	count := flag.Uint64("count", 16384, "blocks to scan")
	out := flag.String("out", "D:/n42-hashidx-proto", "build dir for the index files")
	flag.Parse()
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "usage: bodyc-hashidx-proto --dir <freezer> --start N --count M [--out D]")
		os.Exit(1)
	}
	log.Root().SetHandler(log.LvlFilterHandler(log.LvlWarn, log.StderrHandler))

	br, err := ethel.OpenBodyCompact(*dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open:", err)
		os.Exit(1)
	}
	defer br.Close()

	// Pass 1: collect (hash, block, index) for every tx — RecSplit needs the count upfront.
	type rec struct {
		hash         types.Hash
		block, index uint64
	}
	var recs []rec
	end := *start + *count
	for n := *start; n < end; n++ {
		body, err := br.ReadBody(n)
		if err != nil {
			fmt.Fprintf(os.Stderr, "stop at %d: %v\n", n, err)
			end = n
			break
		}
		for i, tx := range body.Txs {
			recs = append(recs, rec{hash: tx.Hash(), block: n, index: uint64(i)})
		}
	}
	if len(recs) == 0 {
		fmt.Fprintln(os.Stderr, "no txs in range")
		os.Exit(1)
	}

	if err := os.MkdirAll(*out, 0755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir out:", err)
		os.Exit(1)
	}
	w, err := history.NewMPHFWriter(history.MPHFWriterOpts{
		BaseDir:  *out,
		Prefix:   "txhash",
		PageSize: 64,
		TmpDir:   *out + "/etl",
		KeyCount: len(recs),
		EtlBufMB: 256,
		Logger:   log.Root(),
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "new writer:", err)
		os.Exit(1)
	}
	// blob = global tx ordinal (append order). (block,index) derives from a
	// per-block cumulative-count table (store-wide ~9.46M*4B=38MB ≈ 0.015 B/tx).
	for gi, r := range recs {
		_ = r
		var b [binary.MaxVarintLen64]byte
		nn := binary.PutUvarint(b[:], uint64(gi))
		if err := w.Append(recs[gi].hash[:], b[:nn]); err != nil {
			fmt.Fprintln(os.Stderr, "append:", err)
			os.Exit(1)
		}
	}
	if err := w.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "close/build:", err)
		os.Exit(1)
	}
	st := w.Stats()
	total := st.MphfSize + st.KvSize + st.IdxSize
	n := uint64(len(recs))
	fmt.Printf("built index over %d tx hashes (blocks %d..%d)\n", n, *start, end)
	fmt.Println("=== on-disk index size ===")
	fmt.Printf("  .mphf %d B (%.3f B/tx)   .kv %d B (%.3f B/tx)   .idx %d B (%.3f B/tx)\n",
		st.MphfSize, f(st.MphfSize, n), st.KvSize, f(st.KvSize, n), st.IdxSize, f(st.IdxSize, n))
	fmt.Printf("  TOTAL %d B → %.2f B/tx  (vs 32 B/tx for storing the hash; ratio %.1f×)\n",
		total, f(total, n), 32.0/f(total, n))

	// Verify lookups.
	rdr, err := history.OpenMPHF(*out, "txhash")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open mphf:", err)
		os.Exit(1)
	}
	defer rdr.Close()

	checked, wrong := 0, 0
	step := 1
	if len(recs) > 200000 {
		step = len(recs) / 200000
	}
	for i := 0; i < len(recs); i += step {
		r := recs[i]
		blob, ok, err := rdr.Get(r.hash[:])
		checked++
		if err != nil || !ok {
			wrong++
			continue
		}
		gi, _ := binary.Uvarint(blob)
		if int(gi) >= len(recs) || recs[gi].hash != r.hash {
			wrong++
		}
	}

	// Out-of-set hashes must be rejected (fingerprint phantom guard).
	fp := 0
	for i := 0; i < 100000; i++ {
		var h types.Hash
		binary.BigEndian.PutUint64(h[:8], uint64(i)*0x9e3779b97f4a7c15+1)
		binary.BigEndian.PutUint64(h[24:], ^uint64(i))
		if _, ok, _ := rdr.Get(h[:]); ok {
			fp++
		}
	}

	fmt.Println("=== lookup verification ===")
	fmt.Printf("  in-set: checked=%d wrong=%d → %s\n", checked, wrong,
		pass(wrong == 0))
	fmt.Printf("  out-of-set: 100000 random hashes, false-positives=%d (4B fp → expect ~0; rate 1/2^32)\n", fp)
}

func f(v, n uint64) float64 {
	if n == 0 {
		return 0
	}
	return float64(v) / float64(n)
}
func pass(b bool) string {
	if b {
		return "PASS"
	}
	return "FAIL"
}
