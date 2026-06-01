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
}

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
	minClient = &minimalNode{src: src, capped: capped, mc: mc}
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
	trustedReceipt, ok := minClient.mc.TrustedReceiptRoot(bn)
	if !ok {
		return fmt.Sprintf("error: block %d outside trusted/retained window", bn)
	}

	hdr, err := minClient.src.FullHeader(bn)
	if err != nil {
		return "error: fetch full header: " + err.Error()
	}
	hdr.ReceiptHash = trustedReceipt // anchor the ② target to the trusted value
	bodyBytes, err := minClient.src.Body(bn)
	if err != nil {
		return "error: fetch body: " + err.Error()
	}
	decoded, err := ethel.DecodeBodyBlock(bodyBytes)
	if err != nil {
		return "error: decode body: " + err.Error()
	}
	wit, err := minClient.src.GetWitness(bn)
	if err != nil {
		return "error: fetch witness: " + err.Error()
	}

	in := &ethel.MinimalVerifyInput{
		Header:  hdr,
		Body:    ethel.GethBodyFromDecoded(decoded),
		Witness: wit,
	}
	ancestor := func(m uint64) types.Hash { h, _ := minClient.mc.TrustedHash(m); return h }
	// On a contract call the replay fetches bytecode by codeHash from the producer
	// /code endpoint (content-addressed; the reader verifies keccak256==codeHash).
	codeFetched := 0
	codeFetch := func(h types.Hash) ([]byte, error) {
		c, err := minClient.src.Code(h)
		if err == nil && len(c) > 0 {
			codeFetched++
		}
		return c, err
	}
	cfg := params.EthereumMainnetChainConfig
	engine := ethel.NewEthReplayEngine(cfg)

	verr := ethel.VerifyWitnessReceipt(in, ancestor, cfg, engine, codeFetch)
	b, _ := json.Marshal(map[string]any{
		"block":       bn,
		"txCount":     len(decoded.Txs),
		"byzantium":   cfg.IsByzantium(bn),
		"codeFetched": codeFetched,
		"verified":    verr == nil,
		"error":       errStr(verr),
	})
	return string(b)
}

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
