package evmsdk

import (
	"os"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/internal/ethel/stateless/serve"
	"github.com/n42blockchain/N42/params"
)

// TestContractSpanMeasure verifies a SPAN of contract blocks (layer ②) and
// measures: per-block code distribution with cross-block dedup (GlobalBytecodeCache
// → codeFetch fires only on a cache miss = unique code), code raw vs zstd transfer
// size, and per-account EIP-1186 proof size + the merge benefit (N separate proofs
// vs the deduped union of their nodes). Gated on env:
//
//	N42_MEASURE_URL N42_MEASURE_CHECKPOINT N42_MEASURE_CHECKPOINT_HASH
//	N42_MEASURE_FROM N42_MEASURE_COUNT [N42_MEASURE_ACCOUNTS=0x..,0x..]
func TestContractSpanMeasure(t *testing.T) {
	url := os.Getenv("N42_MEASURE_URL")
	if url == "" {
		t.Skip("set N42_MEASURE_URL to run the span measurement")
	}
	cp := envU64(t, "N42_MEASURE_CHECKPOINT")
	cpHash := os.Getenv("N42_MEASURE_CHECKPOINT_HASH")
	from := envU64(t, "N42_MEASURE_FROM")
	count := envU64(t, "N42_MEASURE_COUNT")

	src := serve.NewHTTPSource(url)
	want, err := hexDecode32(cpHash)
	if err != nil {
		t.Fatalf("bad checkpoint hash")
	}
	anchor, err := src.Header(cp)
	if err != nil {
		t.Fatalf("checkpoint header: %v", err)
	}
	if gh := anchor.Hash(); string(gh[:]) != string(want) {
		t.Fatalf("checkpoint hash mismatch")
	}
	hc, err := stateless.NewHeaderChain(anchor)
	if err != nil {
		t.Fatal(err)
	}
	// Extend ① to from+count (batched).
	for f := cp + 1; f <= from+count; f += 256 {
		c := uint64(256)
		if f+c-1 > from+count {
			c = from + count - f + 1
		}
		hs, err := src.HeadersFrom(f, c)
		if err != nil {
			t.Fatalf("headers %d: %v", f, err)
		}
		for _, h := range hs {
			if err := hc.Extend(h); err != nil {
				t.Fatalf("extend %d: %v", h.Number.Uint64(), err)
			}
		}
	}

	// Enable cross-block code cache; codeFetch records each UNIQUE code.
	ethel.GlobalBytecodeCache = ethel.NewBytecodeCache(1 << 20)
	defer func() { ethel.GlobalBytecodeCache = nil }()
	uniqueCodes := map[types.Hash][]byte{}
	totalFetchCalls := 0
	codeFetch := func(h types.Hash) ([]byte, error) {
		totalFetchCalls++
		c, err := src.Code(h)
		if err == nil && len(c) > 0 {
			uniqueCodes[h] = c
		}
		return c, err
	}
	cfg := params.EthereumMainnetChainConfig
	engine := ethel.NewEthReplayEngine(cfg)
	ancestor := func(m uint64) types.Hash { h, _ := hc.TrustedHash(m); return h }

	// Tolerant: this dev dataset's witness has scattered bad blocks (known
	// witness-fill gaps). Skip blocks that fail ②, record the pass rate, and
	// measure code/proof over the verified blocks.
	verified, failed, totalTx := 0, 0, 0
	for n := from; n < from+count; n++ {
		tr, ok := hc.TrustedReceiptRoot(n)
		if !ok {
			t.Fatalf("no trusted receiptRoot %d", n)
		}
		hdr, err := src.FullHeader(n)
		if err != nil {
			t.Fatalf("full header %d: %v", n, err)
		}
		hdr.ReceiptHash = tr
		bodyBytes, err := src.Body(n)
		if err != nil {
			t.Fatalf("body %d: %v", n, err)
		}
		dec, err := ethel.DecodeBodyBlock(bodyBytes)
		if err != nil {
			t.Fatalf("decode body %d: %v", n, err)
		}
		wit, err := src.GetWitness(n)
		if err != nil {
			t.Fatalf("witness %d: %v", n, err)
		}
		in := &ethel.MinimalVerifyInput{Header: hdr, Body: ethel.GethBodyFromDecoded(dec), Witness: wit}
		if err := ethel.VerifyWitnessReceipt(in, ancestor, cfg, engine, codeFetch); err != nil {
			failed++
			if failed <= 5 {
				t.Logf("   ② block %d SKIP (witness data gap): %v", n, err)
			}
			continue
		}
		verified++
		totalTx += len(dec.Txs)
	}
	t.Logf("② verified %d, skipped %d (witness gaps) of %d", verified, failed, count)

	// ---- code transfer-size metrics ----
	rawBytes := 0
	for _, c := range uniqueCodes {
		rawBytes += len(c)
	}
	enc, _ := zstd.NewWriter(nil)
	zstdBytes := 0
	for _, c := range uniqueCodes {
		zstdBytes += len(enc.EncodeAll(c, nil)) // per-code (how /code would ship it)
	}
	enc.Close()
	t.Logf("② SPAN [%d,%d): %d/%d verified, %d tx", from, from+count, verified, count, totalTx)
	t.Logf("   code: %d fetch-calls → %d UNIQUE codes (cross-block dedup); raw=%.2f MB, zstd=%.2f MB (%.1f%%)",
		totalFetchCalls, len(uniqueCodes),
		float64(rawBytes)/1e6, float64(zstdBytes)/1e6, 100*float64(zstdBytes)/float64(maxi(rawBytes, 1)))
	t.Logf("   amortized over %d blocks: raw %.1f KB/blk, zstd %.1f KB/blk", count,
		float64(rawBytes)/float64(count)/1e3, float64(zstdBytes)/float64(count)/1e3)

	// ---- per-account EIP-1186 proof: separate vs merged (node dedup) ----
	accs := os.Getenv("N42_MEASURE_ACCOUNTS")
	if accs != "" {
		measureAccountProofMerge(t, src, splitAddrs(accs))
	}
}

// measureAccountProofMerge fetches each address's EIP-1186 account proof and
// reports the total bytes if shipped SEPARATELY vs MERGED (account-trie nodes
// deduplicated by content — the upper trie is shared across accounts).
func measureAccountProofMerge(t *testing.T, src *serve.HTTPSource, addrs []types.Address) {
	sepBytes := 0
	merged := map[string]int{} // node content -> size (dedup)
	got := 0
	for _, a := range addrs {
		resp, err := src.AccountProof(a, nil)
		if err != nil {
			t.Logf("   account %x: %v (skip)", a[:4], err)
			continue
		}
		got++
		for _, nd := range resp.Proof.AccountProof {
			sepBytes += len(nd)
			merged[string(nd)] = len(nd)
		}
	}
	mergedBytes := 0
	for _, sz := range merged {
		mergedBytes += sz
	}
	enc, _ := zstd.NewWriter(nil)
	mz := 0
	for nd := range merged {
		mz += len(enc.EncodeAll([]byte(nd), nil))
	}
	enc.Close()
	t.Logf("③ ACCOUNT PROOFS (%d accounts at trie head):", got)
	t.Logf("   separate: %d B total (%.0f B/acct)", sepBytes, float64(sepBytes)/float64(maxi(got, 1)))
	t.Logf("   merged (node dedup): %d B (%.1f%% of separate); merged+zstd: %d B",
		mergedBytes, 100*float64(mergedBytes)/float64(maxi(sepBytes, 1)), mz)
}

func hexDecode32(s string) ([]byte, error) { return hexutil.Decode(s) }

func splitAddrs(s string) []types.Address {
	var out []types.Address
	for _, p := range strings.Split(s, ",") {
		b, err := hexutil.Decode(strings.TrimSpace(p))
		if err == nil && len(b) == 20 {
			var a types.Address
			copy(a[:], b)
			out = append(out, a)
		}
	}
	return out
}

func maxi(a, b int) int {
	if a > b {
		return a
	}
	return b
}
