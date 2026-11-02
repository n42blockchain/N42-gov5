// Command mobileverify-device runs simulated phones against a live IDC fleet
// through the SAME SDK client a phone links (cmd/evmsdk MobileVerifyClient):
// register with proof of possession, fetch each new block's stream packet,
// re-execute it with ExecuteAndVerifyV2, sign the receipt and submit it, then
// read the finalized certificate back.
//
// It differs from mobileverify-inject, which signs the header's receiptsRoot
// taken from RPC and never fetches or executes a packet. Here the root a
// device signs is the one it computed, and it is checked independently
// against the header the node serves over JSON-RPC.
//
// Each device submits to exactly one node: the same receipt at several nodes
// is the cross-node duplicate that Layer 2B bans.
//
// Usage:
//
//	mobileverify-device \
//	  -mv http://127.0.0.1:21012,http://127.0.0.1:21013,http://127.0.0.1:21014 \
//	  -rpc http://127.0.0.1:20012 -chain mainnet_qmdb_staggered -blocks 20
//
// A chain with no params name (a devnet, e.g. n42-rs's n42_devnet.json,
// chain id 1143) is given by its genesis file instead: -genesis <file> reads
// the chain config the way `n42 init` does and overrides -chain.
//
// On a mixed fleet only some members build stream packets (the Go ones);
// -skip-no-packet treats a block whose packet the node does not hold
// ("packet not in window" after -packet-wait) as skipped rather than as a
// failure, and needs at least one verified block.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

type device struct {
	id     int
	node   string
	client *evmsdk.MobileVerifyClient
	codes  *evmsdk.CodeCache
	index  uint32
}

type blockResult struct {
	number    uint64
	hash      string
	txs       int
	verified  int // devices whose re-execution matched the header
	submitted int // devices whose receipt the node accepted
}

func main() {
	mvList := flag.String("mv", "http://127.0.0.1:21012,http://127.0.0.1:21013,http://127.0.0.1:21014", "comma-separated mobileverify HTTP base URLs; device i submits to node i mod len")
	rpcURL := flag.String("rpc", "http://127.0.0.1:20012", "JSON-RPC URL for block headers")
	chain := flag.String("chain", "mainnet_qmdb_staggered", "chain spec the packets are re-executed under")
	devicesN := flag.Int("devices", 3, "number of simulated devices")
	blocks := flag.Int("blocks", 20, "number of new blocks to verify")
	settle := flag.Duration("settle", 30*time.Second, "time to wait for windows to close before reading certificates")
	commitWait := flag.Duration("commit-wait", 3*time.Minute, "max time to wait for the registrations to be committed fleet-wide")
	genesisPath := flag.String("genesis", "", "genesis JSON whose chain config to re-execute under (overrides -chain; for chains with no params name)")
	skipNoPacket := flag.Bool("skip-no-packet", false, "skip blocks whose packet the node does not hold instead of failing (mixed fleets)")
	packetWait := flag.Duration("packet-wait", 3*time.Second, "with -skip-no-packet: how long to retry a missing packet before skipping the block")
	flag.Parse()

	var cfg *params.ChainConfig
	if *genesisPath != "" {
		var err error
		if cfg, err = chainConfigFromGenesis(*genesisPath); err != nil {
			log.Fatalf("genesis %s: %v", *genesisPath, err)
		}
		log.Printf("chain config from %s: chainId=%v", *genesisPath, cfg.ChainID)
	} else if cfg = params.ChainConfigByChainName(*chain); cfg == nil {
		log.Fatalf("unknown chain %q", *chain)
	}
	nodes := strings.Split(*mvList, ",")
	ctx := context.Background()

	baselines := make([]int, len(nodes))
	for i, n := range nodes {
		h, err := health(n)
		if err != nil {
			log.Fatalf("node %s health: %v", n, err)
		}
		baselines[i] = h.Registry
	}

	devs := make([]*device, *devicesN)
	for i := range devs {
		key, err := evmsdk.GenerateMobileBLSKey()
		if err != nil {
			log.Fatalf("keygen: %v", err)
		}
		c, err := evmsdk.NewMobileVerifyClient(nodes[i%len(nodes)], key.PrivHex)
		if err != nil {
			log.Fatalf("client: %v", err)
		}
		devs[i] = &device{id: i, node: nodes[i%len(nodes)], client: c, codes: evmsdk.NewCodeCache(4096)}
		if _, err := c.Register(ctx); err != nil {
			log.Fatalf("device %d register at %s: %v", i, devs[i].node, err)
		}
		log.Printf("device %d registered at %s pubkey=%s...", i, devs[i].node, key.PubHex[:16])
	}

	// Registration is gossiped and committed per node on its own epoch timer.
	// Wait until every node's committed registry has grown by the full batch.
	log.Printf("waiting for epoch commit on all %d nodes ...", len(nodes))
	deadline := time.Now().Add(*commitWait)
	for {
		ready := 0
		for i, n := range nodes {
			if h, err := health(n); err == nil && h.Registry >= baselines[i]+len(devs) {
				ready++
			}
		}
		if ready == len(nodes) {
			break
		}
		if time.Now().After(deadline) {
			log.Fatalf("registrations not committed on all nodes within %s (%d/%d ready)", *commitWait, ready, len(nodes))
		}
		time.Sleep(2 * time.Second)
	}
	// Register is idempotent; after the commit it returns the real index.
	for _, d := range devs {
		idx, err := d.client.Register(ctx)
		if err != nil {
			log.Fatalf("device %d re-register: %v", d.id, err)
		}
		d.index = idx
		log.Printf("device %d committed index=%d", d.id, idx)
	}

	head, err := blockNumber(*rpcURL)
	if err != nil {
		log.Fatalf("eth_blockNumber: %v", err)
	}
	var results []*blockResult
	failures, skipped := 0, 0
	for next := head + 1; len(results) < *blocks; {
		cur, err := blockNumber(*rpcURL)
		if err != nil || cur < next {
			time.Sleep(300 * time.Millisecond)
			continue
		}
		hdr, err := header(*rpcURL, next)
		if err != nil {
			log.Fatalf("header %d: %v", next, err)
		}
		br := &blockResult{number: next, hash: hdr.Hash, txs: len(hdr.Transactions)}
		if *skipNoPacket && !packetHeld(ctx, devs[0].client, hdr.Hash, *packetWait) {
			log.Printf("block %d %s: no packet at %s, skipped", next, hdr.Hash, devs[0].node)
			skipped++
			next++
			continue
		}
		for _, d := range devs {
			res, err := d.client.VerifyBlock(ctx, hdr.Hash, d.codes, cfg)
			if res != nil {
				if !strings.EqualFold(res.ComputedReceiptsRoot.Hex(), hdr.ReceiptsRoot) {
					log.Printf("block %d device %d: computed root %s != header %s", next, d.id, res.ComputedReceiptsRoot.Hex(), hdr.ReceiptsRoot)
					failures++
					continue
				}
				br.verified++
			}
			if err != nil {
				log.Printf("block %d device %d: %v", next, d.id, err)
				failures++
				continue
			}
			br.submitted++
		}
		log.Printf("block %d txs=%d verified=%d/%d submitted=%d/%d", next, br.txs, br.verified, len(devs), br.submitted, len(devs))
		results = append(results, br)
		next++
	}

	log.Printf("waiting %s for windows to close ...", *settle)
	time.Sleep(*settle)

	certified := 0
	for _, br := range results {
		certs, status, err := certsFor(nodes[0], br.hash)
		if err != nil {
			log.Printf("block %d cert: HTTP %d %v", br.number, status, err)
			continue
		}
		maxSigners := 0
		for _, c := range certs {
			if c.Signers > maxSigners {
				maxSigners = c.Signers
			}
		}
		log.Printf("block %d certs=%d signers=%d root=%s", br.number, len(certs), maxSigners, certs[0].ReceiptsRoot)
		if maxSigners >= len(devs) {
			certified++
		}
	}
	fmt.Printf("SUMMARY devices=%d blocks=%d skipped=%d failures=%d certified(all devices)=%d/%d\n", len(devs), len(results), skipped, failures, certified, len(results))
	if failures > 0 || certified < len(results) {
		os.Exit(1)
	}
}

// chainConfigFromGenesis reads a genesis file's chain config as `n42 init`
// does (cmd/n42/initcmd.go): decode, hive env overrides, consensus
// normalisation.
func chainConfigFromGenesis(path string) (*params.ChainConfig, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	genesis := new(conf.Genesis)
	if err := json.NewDecoder(f).Decode(genesis); err != nil {
		return nil, err
	}
	conf.ApplyHiveGenesisEnv(genesis, os.LookupEnv)
	if genesis.Config == nil {
		return nil, fmt.Errorf("missing chain config")
	}
	return params.NormalizeConsensus(genesis.Config), nil
}

// packetHeld reports whether the node serves the block's packet, retrying a
// "packet not in window" for up to wait (a relayed packet may trail the
// block on a mixed fleet).
func packetHeld(ctx context.Context, c *evmsdk.MobileVerifyClient, hash string, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		_, err := c.FetchPacket(ctx, hash)
		if err == nil {
			return true
		}
		if !errors.Is(err, evmsdk.ErrMobileVerifyNotFound) || time.Now().After(deadline) {
			return false
		}
		time.Sleep(250 * time.Millisecond)
	}
}

type healthJSON struct {
	Registry    int `json:"registry"`
	Pending     int `json:"pending"`
	OpenWindows int `json:"openWindows"`
	CertBlocks  int `json:"certBlocks"`
	Alarms      int `json:"alarms"`
}

func health(node string) (*healthJSON, error) {
	var h healthJSON
	if _, err := getJSON(node+"/mobileverify/health", &h); err != nil {
		return nil, err
	}
	return &h, nil
}

type certJSON struct {
	BlockNumber  uint64 `json:"blockNumber"`
	ReceiptsRoot string `json:"receiptsRoot"`
	Signers      int    `json:"signers"`
}

func certsFor(node, hash string) ([]certJSON, int, error) {
	var out []certJSON
	status, err := getJSON(node+"/mobileverify/cert/"+hash, &out)
	if err == nil && len(out) == 0 {
		err = fmt.Errorf("empty certificate list")
	}
	return out, status, err
}

func getJSON(url string, out any) (int, error) {
	resp, err := http.Get(url) // #nosec G107 -- operator-supplied fleet URL
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

type rpcHeader struct {
	Hash         string            `json:"hash"`
	ReceiptsRoot string            `json:"receiptsRoot"`
	Transactions []json.RawMessage `json:"transactions"`
}

func rpcCall(url, method string, params []any, out any) error {
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		return err
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(body)) // #nosec G107 -- operator-supplied fleet URL
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if env.Error != nil {
		return fmt.Errorf("%s: %s", method, env.Error.Message)
	}
	return json.Unmarshal(env.Result, out)
}

func blockNumber(url string) (uint64, error) {
	var s string
	if err := rpcCall(url, "eth_blockNumber", []any{}, &s); err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
}

func header(url string, n uint64) (*rpcHeader, error) {
	var h rpcHeader
	if err := rpcCall(url, "eth_getBlockByNumber", []any{fmt.Sprintf("0x%x", n), false}, &h); err != nil {
		return nil, err
	}
	if h.Hash == "" {
		return nil, fmt.Errorf("block %d not found", n)
	}
	return &h, nil
}
