// n42-stateless-serve mounts the stateless serving RPC (internal/ethel/stateless/
// serve) onto the production HTTP transport, backed by the read-only freezers a
// producer/full node already maintains. It serves the three trust layers to
// minimal/full/archive clients:
//
//	① header  — columnar headerc (block.Header.Marshal)
//	② witness + body — witness freezer (TableBlockWitness) + columnar bodyc
//	                   (re-encoded faithfully via ethel.EncodeBodyBlock)
//	③ MPT anchor — compact proof files emitted by the live producer hook
//	               (N42_ANCHOR_DIR / cmd/n42-stateless-anchor-produce)
//
// All artifacts are immutable + content-verifiable, so a CDN can front everything
// but the live /head. Attack protection: per-IP request-rate limiter (429),
// per-IP bandwidth + per-request byte caps, max-concurrent-requests (503),
// hardened HTTP timeouts. Code (by keccak) is served only if --chaindata is set.
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/stateless/serve"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/trie"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func main() {
	addr := flag.String("addr", ":8555", "HTTP listen address")
	hdrDir := flag.String("headers", `D:/n42-eth1/chain/freezer`, "columnar headerc dir")
	bodyDir := flag.String("bodies", `D:/n42-eth1/chain/freezer`, "columnar bodyc dir")
	witDir := flag.String("witness", `D:/N42-eth1177/chain/freezer`, "witness freezer dir")
	anchorDir := flag.String("anchors", "", "MPT anchor proof dir (anchor-<n>.bin); empty = no ③ serving")
	chaindata := flag.String("chaindata", "", "optional MDBX chaindata for code-by-hash (kv.Code); empty = no code serving")
	trieDir := flag.String("trie", "", "optional MDBX trie dir (HashedAccounts+TrieOf*) for /account-proof; empty = disabled")
	codesDir := flag.String("codes", "", "optional codes-freezer dir (codes.cidx + codes.NNNN.cdat) for /code-by-addr (contract-block ②); empty = disabled")
	trieHead := flag.Uint64("trie-head", 0, "block number the trie's state is at (when the trie DB has no HeaderCanonical, e.g. a producer temp trie)")
	chainID := flag.Uint64("chainid", 1, "chain id embedded in served bodies")
	rps := flag.Int("rps", 50, "per-IP requests/sec")
	burst := flag.Int("burst", 100, "per-IP burst")
	bwMBps := flag.Int("bw-mbps", 0, "per-IP bandwidth cap MB/s (0 = unlimited)")
	maxConc := flag.Int("max-concurrent", 1024, "max concurrent in-flight requests")
	flag.Parse()

	logger := log.New()
	be, err := openBackend(*hdrDir, *bodyDir, *witDir, *anchorDir, *chaindata, *trieDir, *codesDir, *chainID)
	if be != nil && *trieHead > 0 {
		be.trieHead = *trieHead
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "open backend:", err)
		os.Exit(1)
	}
	defer be.Close()

	var bw *serve.ByteLimiter
	if *bwMBps > 0 {
		perSec := float64(*bwMBps) * 1024 * 1024
		bw = serve.NewByteLimiter(perSec, perSec, 0)
	}
	svc := serve.NewService(be, serve.DefaultCaps(), bw)

	rl := jsonrpc.NewRateLimiter(&jsonrpc.RateLimitConfig{RequestsPerSecond: *rps, BurstSize: *burst})
	defer rl.Stop()

	cfg := serve.DefaultServerConfig(*addr)
	cfg.MaxConcurrent = *maxConc
	srv := serve.NewServer(cfg, svc, rl)

	tip, _, anchor, _ := be.Head()
	logger.Info("stateless serve: listening", "addr", *addr, "tip", tip, "finalizedAnchor", anchor,
		"anchors", *anchorDir != "", "code", *chaindata != "")
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}

// freezerBackend implements serve.Backend over the read-only producer freezers.
type freezerBackend struct {
	hc        *ethel.HeaderCompactReader
	bc        *ethel.BodyCompactReader
	wit       *freezer.FreezerTable
	anchorDir string
	chainID   uint64

	codeDB kv.RoDB // optional

	trieDB   kv.RoDB // optional (HashedAccounts + TrieOf*) for /account-proof
	trieHead uint64  // head block of the trie DB (canonical head)

	codes *ethel.CodesFreezerReader // optional address-indexed code source for /code-by-addr

	mu      sync.Mutex
	tipSeen uint64 // monotonic cache of the highest readable header
}

func openBackend(hdrDir, bodyDir, witDir, anchorDir, chaindata, trieDir, codesDir string, chainID uint64) (*freezerBackend, error) {
	hc, err := ethel.OpenHeaderCompact(hdrDir)
	if err != nil {
		return nil, fmt.Errorf("headerc: %w", err)
	}
	bc, err := ethel.OpenBodyCompact(bodyDir)
	if err != nil {
		hc.Close()
		return nil, fmt.Errorf("bodyc: %w", err)
	}
	wit, err := freezer.NewFreezerTableCompressedReadOnly(witDir, freezer.TableBlockWitness, "c")
	if err != nil {
		hc.Close()
		return nil, fmt.Errorf("witness: %w", err)
	}
	wit.ForceBatchSize(freezer.BatchSize)

	be := &freezerBackend{hc: hc, bc: bc, wit: wit, anchorDir: anchorDir, chainID: chainID}

	if chaindata != "" {
		db, err := mdbx.NewMDBX(log.New()).Path(chaindata).Label(kv.ChainDB).
			MapSize(datasize.ByteSize(8) * datasize.TB).Readonly().
			WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
			Open(context.Background())
		if err != nil {
			be.Close()
			return nil, fmt.Errorf("chaindata: %w", err)
		}
		be.codeDB = db
	}

	if trieDir != "" {
		db, err := mdbx.NewMDBX(log.New()).Path(trieDir).Label(kv.ChainDB).
			MapSize(datasize.ByteSize(8) * datasize.TB).Readonly().
			WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
			Open(context.Background())
		if err != nil {
			be.Close()
			return nil, fmt.Errorf("trie: %w", err)
		}
		be.trieDB = db
		// Canonical head of the trie DB (HeaderCanonical cursor.Last), so the proof
		// response can name the block its root anchors to.
		if e := db.View(context.Background(), func(tx kv.Tx) error {
			c, err := tx.Cursor(kv.HeaderCanonical)
			if err != nil {
				return err
			}
			defer c.Close()
			k, _, err := c.Last()
			if err != nil {
				return err
			}
			if len(k) >= 8 {
				be.trieHead = binary.BigEndian.Uint64(k[:8])
			}
			return nil
		}); e != nil {
			be.Close()
			return nil, fmt.Errorf("trie head: %w", e)
		}
	}

	if codesDir != "" {
		cr, err := ethel.NewCodesFreezerReader(codesDir)
		if err != nil {
			be.Close()
			return nil, fmt.Errorf("codes-freezer: %w", err)
		}
		be.codes = cr
	}

	be.tipSeen = be.findTip()
	return be, nil
}

func (b *freezerBackend) Close() {
	if b.hc != nil {
		b.hc.Close()
	}
	if b.bc != nil {
		b.bc.Close()
	}
	if b.wit != nil {
		b.wit.Close()
	}
	if b.codeDB != nil {
		b.codeDB.Close()
	}
	if b.trieDB != nil {
		b.trieDB.Close()
	}
	if b.codes != nil {
		b.codes.Close()
	}
}

// AccountProof extracts an EIP-1186 proof for addr (+ slots) from the trie DB at
// its canonical head, reusing the production ProofRetainer + FlatDBTrieLoader (the
// same machinery that computes header.Root). Returns JSON serve.AccountProofResponse.
func (b *freezerBackend) AccountProof(addr types.Address, slots []types.Hash) ([]byte, error) {
	if b.trieDB == nil {
		return nil, serve.ErrNotSupported
	}
	tx, err := b.trieDB.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	addrHash, err := types.HashData(addr[:])
	if err != nil {
		return nil, err
	}
	var acc account.StateAccount
	enc, err := tx.GetOne(kv.HashedAccounts, addrHash[:])
	if err != nil {
		return nil, err
	}
	if len(enc) > 0 {
		if err := acc.DecodeForStorage(enc); err != nil {
			return nil, fmt.Errorf("decode account: %w", err)
		}
	}
	rl := trie.NewRetainList(0)
	pr, err := trie.NewProofRetainer(addr, &acc, slots, rl)
	if err != nil {
		return nil, err
	}
	loader := trie.NewFlatDBTrieLoader("account-proof", rl, nil, nil, false)
	loader.SetProofRetainer(pr)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return nil, fmt.Errorf("CalcTrieRoot: %w", err)
	}
	res, err := pr.ProofResult()
	if err != nil {
		return nil, fmt.Errorf("ProofResult: %w", err)
	}
	return json.Marshal(serve.AccountProofResponse{Block: b.trieHead, Root: root, Proof: res})
}

// findTip locates the highest readable header by scanning down from the headerc
// upper bound (the last segment may be partial). Called once at startup.
func (b *freezerBackend) findTip() uint64 {
	max := b.hc.MaxBlock()
	for n := max; n > 0; n-- {
		if _, err := b.hc.ReadHeader(n - 1); err == nil {
			return n - 1
		}
	}
	return 0
}

// Head advances the cached tip upward (the producer appends live) and returns it
// with the latest finalized anchor.
func (b *freezerBackend) Head() (uint64, types.Hash, uint64, error) {
	b.mu.Lock()
	for {
		if _, err := b.hc.ReadHeader(b.tipSeen + 1); err != nil {
			break
		}
		b.tipSeen++
	}
	tip := b.tipSeen
	b.mu.Unlock()

	h, err := b.hc.ReadHeader(tip)
	if err != nil {
		return 0, types.Hash{}, 0, err
	}
	return tip, h.Hash(), b.latestAnchor(tip), nil
}

// latestAnchor returns the highest anchor height ≤ tip with a proof file present.
func (b *freezerBackend) latestAnchor(tip uint64) uint64 {
	if b.anchorDir == "" {
		return 0
	}
	ents, err := os.ReadDir(b.anchorDir)
	if err != nil {
		return 0
	}
	best := uint64(0)
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "anchor-") || !strings.HasSuffix(name, ".bin") {
			continue
		}
		numStr := strings.TrimSuffix(strings.TrimPrefix(name, "anchor-"), ".bin")
		n, perr := strconv.ParseUint(numStr, 10, 64)
		if perr != nil || n > tip {
			continue
		}
		if n > best {
			best = n
		}
	}
	return best
}

func (b *freezerBackend) HeaderRLP(n uint64) ([]byte, error) {
	h, err := b.hc.ReadHeader(n)
	if err != nil {
		return nil, err
	}
	// headerc is lossy (drops ParentHash) — reconstruct parentHash as the stored
	// canonical hash of block n-1 (chain property). Genesis has zero parentHash.
	var parent types.Hash
	if n > 0 {
		ph, perr := b.hc.ReadHeader(n - 1)
		if perr != nil {
			return nil, fmt.Errorf("parent header %d: %w", n-1, perr)
		}
		parent = ph.Hash()
	}
	return serve.EncodeHeaderRecord(h, parent), nil
}

func (b *freezerBackend) FullHeaderRLP(n uint64) ([]byte, error) {
	h, err := b.hc.ReadHeader(n)
	if err != nil {
		return nil, err
	}
	return serve.HeaderToRLP(h) // all exec fields; ParentHash/Bloom zero (columnar drops them), fine for ②
}

func (b *freezerBackend) BodyRLP(n uint64) ([]byte, error) {
	d, err := b.bc.ReadBody(n)
	if err != nil {
		return nil, err
	}
	return ethel.EncodeBodyBlock(d, b.chainID)
}

func (b *freezerBackend) Witness(n uint64) ([]byte, error) {
	return b.wit.Retrieve(n)
}

func (b *freezerBackend) Anchor(n uint64) ([]byte, error) {
	if b.anchorDir == "" {
		return nil, fmt.Errorf("anchor serving disabled")
	}
	return os.ReadFile(filepath.Join(b.anchorDir, fmt.Sprintf("anchor-%010d.bin", n)))
}

// CodeCompressed serves the codes-freezer's already-zstd-framed blob for codeHash
// directly (no decompress+recompress) — the serve.GetCodeZ passthrough fast path.
// Returns (nil,nil) when the freezer lacks it (the caller falls back to compressing
// the raw Code(hash)).
func (b *freezerBackend) CodeCompressed(hash types.Hash) ([]byte, error) {
	if b.codes == nil {
		return nil, nil
	}
	var key types.Address
	copy(key[:], hash[:20])
	return b.codes.LookupCompressedByAddress(key)
}

// AccountMultiproof builds ONE merged account-trie multiproof covering all addrs
// via a single CalcTrieRoot pass with a WitnessRetainer (the shared upper trie is
// captured once, deduped). Returns JSON serve.AccountMultiproofResponse.
func (b *freezerBackend) AccountMultiproof(addrs []types.Address) ([]byte, error) {
	if b.trieDB == nil {
		return nil, serve.ErrNotSupported
	}
	tx, err := b.trieDB.BeginRo(context.Background())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rl := trie.NewRetainList(0)
	wr := trie.NewWitnessRetainer(rl)
	for _, a := range addrs {
		ah, err := types.HashData(a[:])
		if err != nil {
			return nil, err
		}
		wr.AddHashedKey(ah[:])
	}
	loader := trie.NewFlatDBTrieLoader("account-multiproof", rl, nil, nil, false)
	loader.SetWitnessRetainer(wr)
	root, err := loader.CalcTrieRoot(tx, nil)
	if err != nil {
		return nil, fmt.Errorf("CalcTrieRoot: %w", err)
	}
	return json.Marshal(serve.AccountMultiproofResponse{
		Block: b.trieHead, Root: root, Addrs: addrs, ProofNodes: wr.Nodes(),
	})
}

// Code serves bytecode by keccak codeHash (content-addressed) for layer-② replay.
// Source order: codes-freezer (keyed by codeHash[:20] — code-import2fz truncates
// the codeHash-keyed Code/Bytecodes table key to 20 B), then MDBX kv.Code. The
// client verifies keccak256(code)==codeHash, so the 20-byte index is collision-safe.
func (b *freezerBackend) Code(hash types.Hash) ([]byte, error) {
	if b.codes != nil {
		var key types.Address
		copy(key[:], hash[:20])
		if c, err := b.codes.LookupByAddress(key); err == nil && len(c) > 0 {
			return c, nil
		}
	}
	if b.codeDB != nil {
		tx, err := b.codeDB.BeginRo(context.Background())
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		c, err := tx.GetOne(kv.Code, hash[:])
		if err != nil {
			return nil, err
		}
		if len(c) > 0 {
			return c, nil
		}
	}
	if b.codes == nil && b.codeDB == nil {
		return nil, fmt.Errorf("code serving disabled (set --codes or --chaindata)")
	}
	return nil, fmt.Errorf("code %x not found", hash[:6])
}
