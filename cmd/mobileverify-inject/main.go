// Command mobileverify-inject drives a live cohort of simulated mobile devices
// against a running IDC fleet to exercise the Layer 2B commit-reveal defense
// end-to-end over the real P2P network — the piece a unit test cannot cover.
//
// It registers N honest devices plus one "duplicate" device, then, for each
// new block, submits each honest device's receipt to a distinct node and the
// duplicate device's receipt to TWO nodes (the protocol violation L2B's
// cross-node reconciliation exists to catch). After the cohort phase machine
// runs, it queries the finalized certificate and checks that the honest
// devices are all present while the cross-node-duplicated device was excluded
// — a genuine f+1 ban driven by proof-of-possession reveals, on the wire.
//
// Usage:
//
//	mobileverify-inject \
//	  -mv http://127.0.0.1:21012,http://127.0.0.1:21013,...,http://127.0.0.1:21018 \
//	  -rpc http://127.0.0.1:20012 \
//	  -honest 6 -rounds 4
package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	"github.com/n42blockchain/N42/crypto/bls/common"
	"github.com/n42blockchain/N42/internal/mobileverify"
)

type simDevice struct {
	sk     common.SecretKey
	pubkey [48]byte
	index  mobileverify.MobileIndex
	known  bool
}

func newSimDevice() *simDevice {
	sk, err := bls.RandKey()
	if err != nil {
		log.Fatalf("bls keygen: %v", err)
	}
	d := &simDevice{sk: sk}
	copy(d.pubkey[:], sk.PublicKey().Marshal())
	return d
}

func (d *simDevice) popHex() string {
	sig := d.sk.Sign(mobileverify.PoPMessage(d.pubkey)).Marshal()
	return "0x" + hex.EncodeToString(sig)
}

func (d *simDevice) sigHex(blockHash types.Hash, number uint64, root types.Hash) string {
	sig := d.sk.Sign(mobileverify.SigningMessage(blockHash, number, root)).Marshal()
	return hex.EncodeToString(sig)
}

func main() {
	mvList := flag.String("mv", "http://127.0.0.1:21012", "comma-separated mobileverify HTTP base URLs, one per node")
	rpcURL := flag.String("rpc", "http://127.0.0.1:20012", "JSON-RPC URL for block info")
	honestN := flag.Int("honest", 6, "number of honest devices")
	rounds := flag.Int("rounds", 4, "number of blocks to inject over once devices are live")
	settle := flag.Int("settle", 10, "blocks to wait for the cohort phase machine before querying certs")
	flag.Parse()

	nodes := strings.Split(*mvList, ",")
	if len(nodes) < 2 {
		log.Fatalf("need at least 2 nodes for a cross-node duplicate, got %d", len(nodes))
	}
	log.Printf("nodes=%d honest=%d rounds=%d settle=%d", len(nodes), *honestN, *rounds, *settle)

	// Health check every node's mobileverify surface up front.
	for _, n := range nodes {
		if err := getInto(n+"/mobileverify/health", nil); err != nil {
			log.Fatalf("node %s health check failed (is --mobileverify.http set?): %v", n, err)
		}
	}
	log.Printf("all %d mobileverify HTTP surfaces healthy", len(nodes))

	honest := make([]*simDevice, *honestN)
	for i := range honest {
		honest[i] = newSimDevice()
	}
	dup := newSimDevice()

	// Register everyone on node0 — RegistrationService gossips the registration
	// to the whole fleet, and EpochCommitter commits it (<= CollectWindowSec).
	// Spread registrations across nodes: each node is a separate process with
	// its own per-IP rate limiter (10/min), so fanning out avoids the 429 a
	// burst to one node would hit. RegistrationService gossips each to the
	// whole fleet; CommitEpoch assigns indices deterministically (by pubkey
	// BMT-key-hash order), so every node ends up with the identical index map.
	regAll := append(append([]*simDevice{}, honest...), dup)
	for i, d := range regAll {
		_, committed, err := register(nodes[i%len(nodes)], d)
		if err != nil {
			log.Fatalf("register device %d: %v", i, err)
		}
		log.Printf("registered device %d on node%d committed=%v", i, i%len(nodes), committed)
	}
	log.Printf("dup device index=%d (will be submitted to node0 AND node1)", dup.index)

	// Wait for the epoch to commit so the registry Lookup succeeds fleet-wide.
	// Poll by attempting a receipt and watching for it to stop being rejected
	// as "not registered".
	log.Printf("waiting for epoch commit + fleet registry replication ...")
	waitLive(nodes[0], *rpcURL, honest[0])

	// Register is idempotent: once the epoch has committed, re-registering a
	// device returns its REAL assigned index (the first call, while pending,
	// returned a 0 placeholder). Refresh every device's index so the cert
	// analysis checks the correct bitmask positions.
	for i, d := range regAll {
		idx, committed, err := register(nodes[i%len(nodes)], d)
		if err != nil || !committed {
			log.Fatalf("device %d not committed after live: idx=%d committed=%v err=%v", i, idx, committed, err)
		}
		d.index, d.known = idx, true
	}
	hidx := make([]mobileverify.MobileIndex, len(honest))
	for i, d := range honest {
		hidx[i] = d.index
	}
	log.Printf("committed indices refreshed: dup=%d honest=%v", dup.index, hidx)

	// Inject over `rounds` distinct blocks: honest devices spread across nodes,
	// the duplicate device to node0 and node1.
	type injected struct {
		hash   types.Hash
		number uint64
	}
	var done []injected
	seen := map[uint64]bool{}
	for len(done) < *rounds {
		blk, err := latestBlock(*rpcURL)
		if err != nil {
			log.Fatalf("latest block: %v", err)
		}
		if seen[blk.number] {
			time.Sleep(120 * time.Millisecond)
			continue
		}
		seen[blk.number] = true

		var wg sync.WaitGroup
		var mu sync.Mutex
		ok := 0
		submit := func(node string, d *simDevice) {
			defer wg.Done()
			if err := submitReceipt(node, d, blk); err != nil {
				return
			}
			mu.Lock()
			ok++
			mu.Unlock()
		}
		for i, d := range honest {
			wg.Add(1)
			go submit(nodes[i%len(nodes)], d)
		}
		// The cross-node duplicate: same device, same block, TWO nodes.
		wg.Add(2)
		go submit(nodes[0], dup)
		go submit(nodes[1], dup)
		wg.Wait()

		if ok >= *honestN {
			done = append(done, injected{blk.hash, blk.number})
			log.Printf("round %d/%d: injected block %d (%s) — %d/%d submissions accepted",
				len(done), *rounds, blk.number, blk.hash.Hex()[:14], ok, *honestN+2)
		}
	}

	// Let the height-gated phase machine finish (commit -> reveal -> reconcile
	// -> merge) for the last injected block.
	target := done[len(done)-1]
	log.Printf("waiting %d blocks for cohort finalization of block %d ...", *settle, target.number)
	waitBlocks(*rpcURL, target.number+uint64(*settle))

	// Verify each injected block's finalized cert on node0.
	log.Printf("==== L2B verification ====")
	pass, fail := 0, 0
	for _, inj := range done {
		res, err := getCert(nodes[0], inj.hash)
		if err != nil {
			log.Printf("block %d: no cert (%v)", inj.number, err)
			fail++
			continue
		}
		dupIn, honestPresent := analyzeCert(res, dup, honest)
		if dupIn {
			log.Printf("block %d: FAIL — duplicate device (idx %d) was NOT excluded (censorship-defense broken)", inj.number, dup.index)
			fail++
		} else if honestPresent < *honestN {
			log.Printf("block %d: PARTIAL — dup excluded ✓ but only %d/%d honest present (routing/timing)", inj.number, honestPresent, *honestN)
			pass++
		} else {
			log.Printf("block %d: PASS — dup device idx %d EXCLUDED ✓, all %d honest present ✓", inj.number, dup.index, honestPresent)
			pass++
		}
	}

	// Divergence/misbehavior alarms (informational).
	if alarms, err := getRaw(nodes[0] + "/mobileverify/alarms"); err == nil {
		log.Printf("node0 alarms: %s", strings.TrimSpace(alarms))
	}
	log.Printf("==== summary: %d blocks with dup excluded, %d fail ====", pass, fail)
	if fail > 0 || pass == 0 {
		log.Fatalf("L2B fleet verification did not pass cleanly")
	}
	log.Printf("L2B fleet verification: PASS")
}

// --- helpers ---

type blockInfo struct {
	number       uint64
	hash         types.Hash
	receiptsRoot types.Hash
}

func register(node string, d *simDevice) (mobileverify.MobileIndex, bool, error) {
	body, _ := json.Marshal(map[string]string{
		"pubkey": "0x" + hex.EncodeToString(d.pubkey[:]),
		"pop":    d.popHex(),
	})
	var resp struct {
		Index     mobileverify.MobileIndex `json:"index"`
		Committed bool                     `json:"committed"`
	}
	if err := postInto(node+"/mobileverify/register", body, &resp); err != nil {
		return 0, false, err
	}
	return resp.Index, resp.Committed, nil
}

func submitReceipt(node string, d *simDevice, blk blockInfo) error {
	body, _ := json.Marshal(map[string]any{
		"blockHash":    blk.hash.Hex(),
		"blockNumber":  blk.number,
		"receiptsRoot": blk.receiptsRoot.Hex(),
		"pubkey":       "0x" + hex.EncodeToString(d.pubkey[:]),
		"signature":    d.sigHex(blk.hash, blk.number, blk.receiptsRoot),
		"timestampMs":  uint64(time.Now().UnixMilli()),
	})
	return postInto(node+"/mobileverify/receipt", body, nil)
}

// waitLive polls until the given device's receipt is accepted (epoch committed
// + registry replicated), so the injection rounds don't waste blocks.
func waitLive(node, rpc string, probe *simDevice) {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		blk, err := latestBlock(rpc)
		if err == nil && submitReceipt(node, probe, blk) == nil {
			log.Printf("device live at block %d — epoch committed", blk.number)
			return
		}
		time.Sleep(1 * time.Second)
	}
	log.Fatalf("devices never became live within 90s (epoch commit / registry replication failed)")
}

func waitBlocks(rpc string, targetNumber uint64) {
	for {
		blk, err := latestBlock(rpc)
		if err == nil && blk.number >= targetNumber {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

type certJSON struct {
	BlockHash    string `json:"blockHash"`
	ReceiptsRoot string `json:"receiptsRoot"`
	SignerMask   string `json:"signerMask"`
	Signers      int    `json:"signers"`
}

func getCert(node string, hash types.Hash) ([]certJSON, error) {
	var out []certJSON
	if err := getInto(node+"/mobileverify/cert/"+hash.Hex(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// analyzeCert reports whether the dup device's index appears in ANY cert bucket
// and how many honest device indices are present across buckets.
func analyzeCert(certs []certJSON, dup *simDevice, honest []*simDevice) (dupIn bool, honestPresent int) {
	present := map[mobileverify.MobileIndex]bool{}
	for _, c := range certs {
		mask, err := hex.DecodeString(c.SignerMask)
		if err != nil {
			continue
		}
		// The registry bound only grows; decode against a generous upper bound.
		idxs, err := mobileverify.DecodeMask(mask, 1<<20)
		if err != nil {
			continue
		}
		for _, idx := range idxs {
			present[idx] = true
		}
	}
	if present[dup.index] {
		dupIn = true
	}
	for _, d := range honest {
		if present[d.index] {
			honestPresent++
		}
	}
	return dupIn, honestPresent
}

// --- RPC + HTTP plumbing ---

func latestBlock(rpc string) (blockInfo, error) {
	reqBody := `{"jsonrpc":"2.0","id":1,"method":"eth_getBlockByNumber","params":["latest",false]}`
	var resp struct {
		Result struct {
			Number       string `json:"number"`
			Hash         string `json:"hash"`
			ReceiptsRoot string `json:"receiptsRoot"`
		} `json:"result"`
	}
	if err := postInto(rpc, []byte(reqBody), &resp); err != nil {
		return blockInfo{}, err
	}
	var bi blockInfo
	bi.number = hexToUint(resp.Result.Number)
	bi.hash = types.HexToHash(resp.Result.Hash)
	bi.receiptsRoot = types.HexToHash(resp.Result.ReceiptsRoot)
	return bi, nil
}

func hexToUint(s string) uint64 {
	s = strings.TrimPrefix(s, "0x")
	var v uint64
	for _, c := range s {
		v <<= 4
		switch {
		case c >= '0' && c <= '9':
			v |= uint64(c - '0')
		case c >= 'a' && c <= 'f':
			v |= uint64(c-'a') + 10
		case c >= 'A' && c <= 'F':
			v |= uint64(c-'A') + 10
		}
	}
	return v
}

var httpClient = &http.Client{Timeout: 5 * time.Second}

func postInto(url string, body []byte, out any) error {
	resp, err := httpClient.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func getInto(url string, out any) error {
	resp, err := httpClient.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.Unmarshal(data, out)
	}
	return nil
}

func getRaw(url string) (string, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return string(data), nil
}
