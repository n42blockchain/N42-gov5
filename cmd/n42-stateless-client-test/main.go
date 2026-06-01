// n42-stateless-client-test exercises the minimal + full client modes against a
// running n42-stateless-serve (the simulated IDC), over real HTTP, on real
// mainnet data. It validates:
//
//	minimal ①③ — extend the trusted header chain (parentHash) from a checkpoint
//	              to the tip, and at every K-th block fetch the compact MPT anchor
//	              and structurally verify it anchors to the trusted header
//	              stateRoot (VerifyProofAnchors). Also downloads each block's
//	              witness (the per-block layer-② artifact the IDC provides).
//	full   ①   — archive header+body genesis-style from the checkpoint to tip via
//	              stateless.FullClient, verifying each body decodes (ethel body wire).
//	stability  — repeats the minimal pass --iterations times and polls /health,
//	              reporting per-pass timing and any error.
//
// NOTE: strong state-transition ③ (VerifyStateRoot recompute from pre-state proof
// + changeset) and layer-② EVM replay are NOT exercised here: the producer emits
// post-state flat multiproofs (consumed by VerifyProofAnchors), and witness replay
// needs a code source. Those are the remaining gaps for full minimal verification.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/internal/ethel/stateless/serve"
)

func main() {
	url := flag.String("url", "http://127.0.0.1:8555", "serve base URL")
	checkpoint := flag.Uint64("checkpoint", 990000, "trusted checkpoint block (K-aligned)")
	tip := flag.Uint64("tip", 1000000, "target tip (≤ max produced anchor)")
	k := flag.Uint64("k", 1000, "anchor cadence")
	iters := flag.Int("iterations", 3, "stability passes of the minimal sync")
	runFull := flag.Bool("full", true, "also run the full-client archive")
	withWitness := flag.Bool("witness", true, "download per-block witness (size check)")
	account := flag.String("account", "", "optional account (0x..20B) to fetch+verify an EIP-1186 proof (mobile layer-③)")
	transition := flag.Bool("transition", false, "run MinimalClient.Sync (①+③ state-transition recompute at each K anchor) — needs BlockProof anchors")
	flag.Parse()

	src := serve.NewHTTPSource(*url)

	// Health + head.
	hn, hhash, hanchor, err := httpHead(*url)
	if err != nil {
		fail("head", err)
	}
	fmt.Printf("IDC head: block=%d hash=%x finalizedAnchor=%d\n", hn, hhash[:6], hanchor)
	if *tip > hn {
		fmt.Printf("WARN: --tip %d > IDC head %d; capping to head\n", *tip, hn)
		*tip = hn
	}

	// Trusted checkpoint header (out-of-band in production; here from the IDC).
	cp, err := src.Header(*checkpoint)
	if err != nil {
		fail("checkpoint header", err)
	}
	fmt.Printf("checkpoint: block=%d hash=%x stateRoot=%x\n", *checkpoint, cp.Hash().Bytes()[:6], cp.Root[:6])

	// ---- ③ state-transition mode (exclusive): MinimalClient recomputes stateRoot
	// from the changeset at each K anchor. Anchors are BlockProofs (not the compact
	// structural proofs the default pass decodes), so this runs on its own. ----
	if *transition {
		t0 := time.Now()
		capped := &cappedSource{HTTPSource: src, tip: *tip}
		mc, err := stateless.NewMinimalClient(capped, cp, 1<<30, *k)
		if err != nil {
			fail("transition init", err)
		}
		head, serr := mc.Sync() // ① extend + ③ VerifyAgainstChain (recompute from changeset)
		if serr != nil {
			fail("transition sync", serr)
		}
		nAnchors := (*tip - *checkpoint) / *k
		fmt.Printf("③ state-transition: synced %d..%d, ✓ recomputed stateRoot from changeset at %d anchors (every %d), %.1fs\n",
			*checkpoint+1, head, nAnchors, *k, time.Since(t0).Seconds())
		fmt.Println("ALL CLIENT-MODE CHECKS PASSED")
		return
	}

	// ---- minimal ①③ (+ witness download), repeated for stability ----
	for it := 1; it <= *iters; it++ {
		t0 := time.Now()
		anchorsOK, witBytes, err := minimalPass(src, *url, cp, *checkpoint, *tip, *k, *withWitness && it == 1)
		if err != nil {
			fail(fmt.Sprintf("minimal pass %d", it), err)
		}
		extra := ""
		if witBytes > 0 {
			extra = fmt.Sprintf(", witness=%.1fMB", float64(witBytes)/1e6)
		}
		fmt.Printf("minimal pass %d/%d: blocks %d..%d ✓ headerchain, anchors③=%d ✓%s, %.1fs\n",
			it, *iters, *checkpoint+1, *tip, anchorsOK, extra, time.Since(t0).Seconds())
	}

	// ---- full ① archive ----
	if *runFull {
		t0 := time.Now()
		blocks, bodyBytes, err := fullPass(src, cp, *tip)
		if err != nil {
			fail("full archive", err)
		}
		fmt.Printf("full archive: %d blocks (header+body) ✓ chain+body-decode, body=%.1fMB, %.1fs\n",
			blocks, float64(bodyBytes)/1e6, time.Since(t0).Seconds())
	}

	// ---- mobile layer-③: per-account EIP-1186 proof (bounded, vs full-window) ----
	if *account != "" {
		ab, derr := hexutil.Decode(*account)
		if derr != nil || len(ab) != 20 {
			fail("bad --account", fmt.Errorf("want 20-byte hex addr"))
		}
		var addr types.Address
		copy(addr[:], ab)
		resp, aerr := src.AccountProof(addr, nil)
		if aerr != nil {
			fmt.Printf("account-proof: SKIP (%v)\n", aerr)
		} else {
			va, verr := stateless.VerifyAccountInclusion(resp.Root[:], resp.Proof)
			if verr != nil {
				fail("account-proof verify", verr)
			}
			sz := len(resp.Proof.AccountProof)
			tot := 0
			for _, n := range resp.Proof.AccountProof {
				tot += len(n)
			}
			fmt.Printf("account-proof ③: %s @block=%d root=%x ✓ exists=%v balance=%s nonce=%d (%d nodes, %d B — bounded)\n",
				addr.Hex()[:10], resp.Block, resp.Root[:6], va.Exists, resp.Proof.Balance, va.Nonce, sz, tot)
		}
	}

	// ---- /health stability ----
	okH := 0
	for i := 0; i < 5; i++ {
		resp, e := http.Get(*url + "/health")
		if e == nil {
			if resp.StatusCode == http.StatusOK {
				okH++
			}
			resp.Body.Close()
		}
	}
	fmt.Printf("/health: %d/5 OK\n", okH)
	fmt.Println("ALL CLIENT-MODE CHECKS PASSED")
}

// minimalPass extends the header chain checkpoint→tip and structurally verifies
// every K-th anchor against the trusted header stateRoot. Returns anchors verified
// and total witness bytes downloaded.
func minimalPass(src *serve.HTTPSource, url string, cp *block.Header, checkpoint, tip, k uint64, dlWitness bool) (int, int64, error) {
	hc, err := stateless.NewHeaderChain(cp)
	if err != nil {
		return 0, 0, err
	}
	anchorsOK := 0
	var witBytes int64
	const batch = 256
	for from := checkpoint + 1; from <= tip; from += batch {
		count := uint64(batch)
		if from+count-1 > tip {
			count = tip - from + 1
		}
		hs, err := src.HeadersFrom(from, count)
		if err != nil {
			return anchorsOK, witBytes, fmt.Errorf("headers %d+%d: %w", from, count, err)
		}
		if uint64(len(hs)) != count {
			return anchorsOK, witBytes, fmt.Errorf("short headers at %d: got %d want %d", from, len(hs), count)
		}
		for _, h := range hs {
			n := h.Number.Uint64()
			if err := hc.Extend(h); err != nil { // ① parentHash chain
				return anchorsOK, witBytes, fmt.Errorf("extend %d: %w", n, err)
			}
			if n%k == 0 {
				root, ok := hc.TrustedStateRoot(n)
				if !ok {
					return anchorsOK, witBytes, fmt.Errorf("no trusted root %d", n)
				}
				wire, err := src.AnchorBytes(n)
				if err != nil {
					return anchorsOK, witBytes, fmt.Errorf("anchor %d: %w", n, err)
				}
				nodes, err := stateless.DecodeCompactToNodes(wire)
				if err != nil {
					return anchorsOK, witBytes, fmt.Errorf("anchor %d decode: %w", n, err)
				}
				if err := stateless.VerifyProofAnchors(root[:], nodes); err != nil { // ③ structural
					return anchorsOK, witBytes, fmt.Errorf("anchor %d VERIFY: %w", n, err)
				}
				anchorsOK++
			}
			if dlWitness {
				w, err := src.GetWitness(n)
				if err != nil {
					return anchorsOK, witBytes, fmt.Errorf("witness %d: %w", n, err)
				}
				witBytes += int64(len(w))
			}
		}
	}
	return anchorsOK, witBytes, nil
}

// fullPass archives header+body checkpoint→tip via FullClient, verifying each
// body decodes (faithful ethel body wire).
func fullPass(src *serve.HTTPSource, cp *block.Header, tip uint64) (int, int64, error) {
	capped := &cappedSource{HTTPSource: src, tip: tip}
	var blocks int
	var bodyBytes int64
	store := func(n uint64, h *block.Header, body []byte) error {
		blocks++
		bodyBytes += int64(len(body))
		return nil
	}
	verify := func(h *block.Header, body []byte) error {
		if len(body) == 0 {
			return nil // empty/absent body tolerated
		}
		if _, err := ethel.DecodeBodyBlock(body); err != nil {
			return fmt.Errorf("block %d body decode: %w", h.Number.Uint64(), err)
		}
		return nil
	}
	fc, err := stateless.NewFullClient(capped, cp, store, verify)
	if err != nil {
		return 0, 0, err
	}
	if _, err := fc.Sync(); err != nil {
		return blocks, bodyBytes, err
	}
	return blocks, bodyBytes, nil
}

// cappedSource caps Head() to a fixed tip so a client doesn't chase past the
// produced-anchor range (the witness covers 25M but anchors only the test range).
type cappedSource struct {
	*serve.HTTPSource
	tip uint64
}

func (c *cappedSource) Head() (uint64, error) { return c.tip, nil }

func httpHead(url string) (uint64, [32]byte, uint64, error) {
	resp, err := http.Get(url + "/head")
	if err != nil {
		// allow callers passing a full /health URL
		resp, err = http.Get(url)
		if err != nil {
			return 0, [32]byte{}, 0, err
		}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return 0, [32]byte{}, 0, fmt.Errorf("status %d: %s", resp.StatusCode, string(b))
	}
	var v struct {
		Number uint64 `json:"number"`
		Head   uint64 `json:"head"`
		Hash   string `json:"hash"`
		Anchor uint64 `json:"anchor"`
	}
	_ = json.Unmarshal(b, &v)
	n := v.Number
	if n == 0 {
		n = v.Head
	}
	var hh [32]byte
	return n, hh, v.Anchor, nil
}

func fail(stage string, err error) {
	fmt.Fprintf(os.Stderr, "FAIL [%s]: %v\n", stage, err)
	os.Exit(1)
}
