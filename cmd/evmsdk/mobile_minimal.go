// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// mobile_minimal.go — the gomobile facade for the MINIMAL stateless client (the
// phone "minimal node"). It composes the three trust layers over the IDC's
// read-only serving RPC (n42-stateless-serve):
//
//	① header chain — extend parentHash from a socially-trusted checkpoint to tip
//	                  (stateless.MinimalClient with anchorEvery=0: header-only sync,
//	                  no full-window MPT proof — that is 1MB-207MB, not mobile).
//	② witness EVM  — per block, replay the witness through internal/vm to check
//	                  receiptRoot (reuses evmsdk's own mobile executor; the producer
//	                  must serve StreamPackets for this — TODO, see MobileMinimalSync).
//	③ per-account  — on demand, fetch a bounded EIP-1186 proof (~KB, /account-proof)
//	                  and verify it against the header-chain-trusted stateRoot
//	                  (stateless.VerifyAccountInclusion). This is the mobile layer-③.
//
// All functions return strings (gomobile constraint): "" on success for actions,
// JSON for queries, or "error: ..." on failure. State lives in a hidden singleton.
package evmsdk

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/stateless"
	"github.com/n42blockchain/N42/internal/ethel/stateless/serve"
	"github.com/n42blockchain/N42/params"
)

var (
	minMu     sync.Mutex
	minClient *minimalNode
)

type minimalNode struct {
	src    *serve.HTTPSource // raw transport (per-account proofs)
	capped *cappedSrc        // sync target cap (chunked catch-up)
	mc     *stateless.MinimalClient
	// verifiedHead is the highest block whose witness (②) has been replayed and
	// reconciled with the ①-trusted receiptRoot. MobileFollowTick advances it.
	verifiedHead uint64
	// overlay holds the rolling post-state (last `retention` blocks) folded in
	// from each witness-verified block; MobileBalanceOf reads it before falling
	// back to an IDC proof.
	overlay *mobileOverlay
	// codes is a small hot-only LRU of contract bytecode (content-addressed by
	// codeHash). The witness reader verifies keccak256(code)==hash, so a cache
	// hit is as trustworthy as a fresh fetch. Bounds RAM — only hot contracts
	// stay resident; cold ones are re-fetched on demand from the IDC.
	codes *CodeCache
}

// mobileCodeCacheCap bounds how many distinct hot contract bytecodes the mobile
// node keeps resident. ~1024 covers the working set of the popular contracts a
// rolling window touches; misses fall back to an IDC /code fetch.
const mobileCodeCacheCap = 1024

// cappedSrc caps the sync tip so a phone can catch up in bounded steps (bounding
// per-call work + battery) rather than syncing the whole gap at once. cap==0 → tip.
type cappedSrc struct {
	stateless.Source
	cap uint64
}

func (c *cappedSrc) Head() (uint64, error) {
	h, err := c.Source.Head()
	if err != nil {
		return 0, err
	}
	if c.cap > 0 && c.cap < h {
		return c.cap, nil
	}
	return h, nil
}

// MobileConfig is the single-blob config for a lightweight mobile node.
// Retention defaults to 900 blocks (the rolling cache window) when 0.
type MobileConfig struct {
	IDC            string `json:"idc"`             // IDC base URL serving witness/body/code/proof
	CheckpointBlk  int64  `json:"checkpointBlock"` // socially-trusted anchor block
	CheckpointHash string `json:"checkpointHash"`  // 0x.. 32-byte trusted block hash
	Retention      int64  `json:"retention"`       // rolling window (0 → 900)
}

// MobileStartFromConfig is the one-call config-file launch: parse a JSON config
// blob and bootstrap the minimal node. The caller (app or launcher) reads its
// config file and passes the contents here, then drives MobileFollowTick on a
// 12 s ticker. Returns "" on success or "error: ...". Retention defaults to 900.
func MobileStartFromConfig(configJSON string) string {
	var c MobileConfig
	if err := json.Unmarshal([]byte(configJSON), &c); err != nil {
		return "error: parse config: " + err.Error()
	}
	if c.IDC == "" || c.CheckpointHash == "" {
		return "error: config needs idc + checkpointHash"
	}
	if c.Retention <= 0 {
		c.Retention = 900
	}
	return MobileMinimalInit(c.IDC, c.CheckpointBlk, c.CheckpointHash, c.Retention)
}

// MobileMinimalInit bootstraps a minimal node from a socially-trusted checkpoint
// {block, hash} (hard-coded in the app or from a finalized PoS header). It fetches
// that block's header record from idcURL, verifies its hash equals the trusted
// hash (so the anchor is authentic), and starts a header-only minimal client with
// a rolling retention window. Returns "" on success or "error: ...".
//
//	idcURL            e.g. "https://idc1.n42.example"
//	checkpointBlock   the trusted anchor block number
//	checkpointHashHex 0x.. 32-byte block hash vouched for out-of-band
//	retentionBlocks   rolling window (e.g. 7200 for ~1 day at 12s; 0 = keep all)
func MobileMinimalInit(idcURL string, checkpointBlock int64, checkpointHashHex string, retentionBlocks int64) string {
	minMu.Lock()
	defer minMu.Unlock()

	want, err := hexutil.Decode(checkpointHashHex)
	if err != nil || len(want) != 32 {
		return "error: bad checkpoint hash (want 0x + 32 bytes)"
	}
	src := serve.NewHTTPSource(idcURL)
	h, err := src.Header(uint64(checkpointBlock))
	if err != nil {
		return "error: fetch checkpoint header: " + err.Error()
	}
	got := h.Hash()
	if string(got[:]) != string(want) {
		return fmt.Sprintf("error: checkpoint hash mismatch: server %x != trusted %x", got[:8], want[:8])
	}
	capped := &cappedSrc{Source: src}
	mc, err := stateless.NewMinimalClient(capped, h, uint64(retentionBlocks), 0) // anchorEvery=0 → ① only
	if err != nil {
		return "error: " + err.Error()
	}
	minClient = &minimalNode{
		src:     src,
		capped:  capped,
		mc:      mc,
		overlay: newMobileOverlay(uint64(retentionBlocks)),
		codes:   NewCodeCache(mobileCodeCacheCap),
	}
	return ""
}

// MobileMinimalSync advances the trusted header chain (①) from the current head to
// the IDC tip and prunes/re-anchors the rolling window. Drive it on a 12s ticker
// for live following, or once for catch-up. Returns JSON {head, hash, trustRoot,
// retained, reanchors} or "error: ...".
func MobileMinimalSync() string {
	minMu.Lock()
	defer minMu.Unlock()
	if minClient == nil {
		return "error: not initialized"
	}
	minClient.capped.cap = 0 // sync to tip
	if _, err := minClient.mc.Sync(); err != nil {
		return "error: " + err.Error()
	}
	return minClient.stateJSON()
}

// MobileMinimalSyncTo advances the header chain (①) only up to maxBlock (a bounded
// catch-up step). Call repeatedly with rising maxBlock to spread a long catch-up
// across ticks. Returns the same JSON as MobileMinimalSync.
func MobileMinimalSyncTo(maxBlock int64) string {
	minMu.Lock()
	defer minMu.Unlock()
	if minClient == nil {
		return "error: not initialized"
	}
	minClient.capped.cap = uint64(maxBlock)
	if _, err := minClient.mc.Sync(); err != nil {
		return "error: " + err.Error()
	}
	return minClient.stateJSON()
}

// MobileMinimalState returns the current trusted state as JSON without syncing.
func MobileMinimalState() string {
	minMu.Lock()
	defer minMu.Unlock()
	if minClient == nil {
		return "error: not initialized"
	}
	return minClient.stateJSON()
}

func (n *minimalNode) stateJSON() string {
	head, hash := n.mc.Head()
	trNum, trHash := n.mc.TrustRoot()
	b, _ := json.Marshal(map[string]any{
		"head":      head,
		"hash":      hash.Hex(),
		"trustRoot": map[string]any{"block": trNum, "hash": trHash.Hex()},
		"retained":  n.mc.Retained(),
		"reanchors": n.mc.ReanchorCount(),
	})
	return string(b)
}

// MobileBalanceOf returns the TRUSTLESS balance of addrHex: it fetches a bounded
// EIP-1186 account proof from the IDC, ensures the header chain covers the proof's
// block (syncing ① if needed), and verifies the proof reconstructs to the
// header-chain-trusted stateRoot (layer ③). Returns JSON {address, exists,
// balance, nonce, block, root, proofBytes, verified} or "error: ...".
//
// "verified": true means the balance is proven against a stateRoot whose
// authenticity traces (via parentHash) to the trusted checkpoint — zero trust in
// the server.
func MobileBalanceOf(addrHex string) string {
	minMu.Lock()
	defer minMu.Unlock()
	if minClient == nil {
		return "error: not initialized"
	}
	ab, err := hexutil.Decode(addrHex)
	if err != nil || len(ab) != 20 {
		return "error: bad address (want 0x + 20 bytes)"
	}
	var addr types.Address
	copy(addr[:], ab)

	// Overlay-first: if this account changed within the rolling window, the value
	// is already witness-verified on-device (layer ②, receiptRoot reconciled to
	// the ①-trusted header) — no network round-trip needed.
	if minClient.overlay != nil {
		if raw, at, ok := minClient.overlay.account(addr); ok {
			acc, exists := decodeAccount(raw)
			out := map[string]any{
				"address":  addr.Hex(),
				"exists":   exists,
				"block":    at,
				"source":   "overlay",
				"verified": true,
			}
			if exists {
				out["balance"] = acc.Balance.ToBig().String()
				out["nonce"] = acc.Nonce
			} else {
				out["balance"] = "0"
				out["nonce"] = uint64(0)
			}
			b, _ := json.Marshal(out)
			return string(b)
		}
	}

	resp, err := minClient.src.AccountProof(addr, nil)
	if err != nil {
		return "error: fetch account proof: " + err.Error()
	}
	// Ensure the header chain covers the proof's block so we have a trusted root.
	head, _ := minClient.mc.Head()
	if resp.Block > head {
		if _, serr := minClient.mc.Sync(); serr != nil {
			return "error: sync to proof block: " + serr.Error()
		}
	}
	trusted, ok := minClient.mc.TrustedStateRoot(resp.Block)
	if !ok {
		return fmt.Sprintf("error: proof block %d outside trusted/retained window", resp.Block)
	}
	if string(trusted[:]) != string(resp.Root[:]) {
		return fmt.Sprintf("error: proof root %x != trusted header[%d] root %x", resp.Root[:8], resp.Block, trusted[:8])
	}
	va, verr := stateless.VerifyAccountInclusion(trusted[:], resp.Proof)
	if verr != nil {
		return "error: proof verify: " + verr.Error()
	}
	proofBytes := 0
	for _, nd := range resp.Proof.AccountProof {
		proofBytes += len(nd)
	}
	b, _ := json.Marshal(map[string]any{
		"address":    addr.Hex(),
		"exists":     va.Exists,
		"balance":    resp.Proof.Balance,
		"nonce":      va.Nonce,
		"block":      resp.Block,
		"root":       resp.Root.Hex(),
		"proofBytes": proofBytes,
		"source":     "proof",
		"verified":   true,
	})
	return string(b)
}

// MobileVerifyBlock runs layer ② for block n: it fetches the full header + body +
// witness from the IDC and replays the witness through the EVM on-device, checking
// gasUsed (+ receiptRoot from Byzantium on) against the header-chain-TRUSTED
// receiptRoot. Missing contract bytecode is fetched on demand from /code. Requires
// the header chain to already cover n (syncs if behind). Returns JSON {block,
// txCount, byzantium, verified} or "error: ...".
//
// Trust: the receiptRoot target is overridden with the ①-trusted value, so a
// passing replay means the block's execution reconciles with a receiptRoot whose
// authenticity traces to the checkpoint. (Pre-Byzantium blocks carry no usable
// receiptRoot — there ② checks gasUsed only; pair with ③ for state assurance.)
func MobileVerifyBlock(n int64) string {
	minMu.Lock()
	defer minMu.Unlock()
	if minClient == nil {
		return "error: not initialized"
	}
	bn := uint64(n)
	if head, _ := minClient.mc.Head(); bn > head {
		if _, err := minClient.mc.Sync(); err != nil {
			return "error: sync to block: " + err.Error()
		}
	}
	txCount, codeFetched, ps, verr := minClient.verifyBlockLocked(bn)
	cfg := params.EthereumMainnetChainConfig
	// Only fold into the overlay / advance verifiedHead when this block extends
	// the contiguous verified window. An out-of-order spot-check (bn not equal to
	// verifiedHead+1) verifies execution but must NOT mutate the rolling overlay
	// state — its prune/last-writer invariants assume monotonic application, and
	// advancing verifiedHead on a gap would let MobileFollowTick skip blocks.
	if verr == nil && bn == minClient.verifiedHead+1 {
		if minClient.overlay != nil {
			minClient.overlay.apply(bn, ps)
		}
		minClient.verifiedHead = bn
	}
	b, _ := json.Marshal(map[string]any{
		"block":       bn,
		"txCount":     txCount,
		"byzantium":   cfg.IsByzantium(bn),
		"codeFetched": codeFetched,
		"verified":    verr == nil,
		"error":       errStr(verr),
	})
	return string(b)
}

// verifyBlockLocked runs layer ② for block bn: fetch full header + body + witness
// from the IDC, override receiptRoot with the ①-trusted value, and replay the
// witness through the EVM (missing code fetched by codeHash from /code). Assumes
// the caller holds minMu and the header chain already covers bn. On success it
// returns the block's witness-verified post-state changeset (for the overlay).
// Returns (txCount, codeFetched, postState, err).
func (n *minimalNode) verifyBlockLocked(bn uint64) (int, int, *ethel.PostState, error) {
	trustedReceipt, ok := n.mc.TrustedReceiptRoot(bn)
	if !ok {
		return 0, 0, nil, fmt.Errorf("block %d outside trusted/retained window", bn)
	}
	hdr, err := n.src.FullHeader(bn)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("fetch full header: %w", err)
	}
	hdr.ReceiptHash = trustedReceipt // anchor the ② target to the trusted value
	bodyBytes, err := n.src.Body(bn)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("fetch body: %w", err)
	}
	decoded, err := ethel.DecodeBodyBlock(bodyBytes)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("decode body: %w", err)
	}
	wit, err := n.src.GetWitness(bn)
	if err != nil {
		return 0, 0, nil, fmt.Errorf("fetch witness: %w", err)
	}
	in := &ethel.MinimalVerifyInput{
		Header:  hdr,
		Body:    ethel.GethBodyFromDecoded(decoded),
		Witness: wit,
	}
	ancestor := func(m uint64) types.Hash { h, _ := n.mc.TrustedHash(m); return h }
	codeFetched := 0
	codeFetch := func(h types.Hash) ([]byte, error) {
		// Hot-cache first (content-addressed; reader re-verifies keccak).
		if n.codes != nil {
			if c, ok := n.codes.Get(h); ok {
				return c, nil
			}
		}
		c, err := n.src.Code(h)
		if err == nil && len(c) > 0 {
			codeFetched++
			if n.codes != nil {
				n.codes.Insert(h, c)
			}
		}
		return c, err
	}
	cfg := params.EthereumMainnetChainConfig
	engine := ethel.NewEthReplayEngine(cfg)
	ps, verr := ethel.VerifyWitnessReceiptCapture(in, ancestor, cfg, engine, codeFetch)
	return len(decoded.Txs), codeFetched, ps, verr
}

// MobileFollowTick is the witness-verified 12 s follow step: it advances ① to the
// IDC tip, then replays ② (witness EVM) for each new block since the last verified
// head, within the retention window, up to maxVerify blocks per call (battery
// bound; <=0 means all new blocks). Drive it on a 12 s ticker — each new block is
// header-trusted AND witness-verified before it's counted. Returns JSON
// {head, hash, verifiedHead, newVerified, failedAt, error}.
func MobileFollowTick(maxVerify int64) string {
	minMu.Lock()
	defer minMu.Unlock()
	if minClient == nil {
		return "error: not initialized"
	}
	minClient.capped.cap = 0 // ① to tip
	if _, err := minClient.mc.Sync(); err != nil {
		return "error: sync: " + err.Error()
	}
	head, hash := minClient.mc.Head()
	// First tick: anchor verifiedHead just below head so we verify forward from here
	// (catch-up of the whole retained window on tick 1 would be heavy; the window is
	// header+proof trusted regardless — ② proves execution of blocks seen live).
	if minClient.verifiedHead == 0 || minClient.verifiedHead < head-min64(head, retainedDepth(minClient)) {
		if head > 0 {
			minClient.verifiedHead = head - 1
		}
	}
	newVerified := 0
	var failedAt uint64
	var failErr error
	for bn := minClient.verifiedHead + 1; bn <= head; bn++ {
		if maxVerify > 0 && int64(newVerified) >= maxVerify {
			break
		}
		_, _, ps, err := minClient.verifyBlockLocked(bn)
		if err != nil {
			failedAt, failErr = bn, err
			break
		}
		if minClient.overlay != nil {
			minClient.overlay.apply(bn, ps)
		}
		minClient.verifiedHead = bn
		newVerified++
	}
	ovAccts, ovStor, ovBlocks := 0, 0, 0
	if minClient.overlay != nil {
		ovAccts, ovStor, ovBlocks = minClient.overlay.stats()
	}
	b, _ := json.Marshal(map[string]any{
		"head":          head,
		"hash":          hash.Hex(),
		"verifiedHead":  minClient.verifiedHead,
		"newVerified":   newVerified,
		"failedAt":      failedAt,
		"error":         errStr(failErr),
		"overlayAccts":  ovAccts,
		"overlayStor":   ovStor,
		"overlayBlocks": ovBlocks,
	})
	return string(b)
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// retainedDepth is the rolling-window depth (retention blocks); ② only verifies
// within it since older receiptRoots aren't retained.
func retainedDepth(n *minimalNode) uint64 { return uint64(n.mc.Retained()) }

func errStr(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

// MobileMinimalFree releases the minimal node.
func MobileMinimalFree() string {
	minMu.Lock()
	defer minMu.Unlock()
	minClient = nil
	return ""
}
