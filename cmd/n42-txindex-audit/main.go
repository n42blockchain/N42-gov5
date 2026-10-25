// n42-txindex-audit checks a stopped performance owner's physical lookup tiers.
// It opens a fresh, read-only MDBX handle and never creates segments.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gofrs/flock"
	"github.com/holiman/uint256"

	blockpkg "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/txindexer"
	"github.com/n42blockchain/N42/internal/txlookup"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/params"
)

type result struct {
	Kind              string        `json:"kind"`
	Genesis           string        `json:"genesis"`
	ChainIdentity     string        `json:"chain_identity"`
	Head              uint64        `json:"head"`
	HeadHash          string        `json:"head_hash"`
	Watermark         uint64        `json:"watermark"`
	SegmentStart      uint64        `json:"segment_start"`
	SegmentEnd        uint64        `json:"segment_end"`
	Segments          int           `json:"segments"`
	BaselineTx        uint64        `json:"baseline_tx"`
	SegmentTx         uint64        `json:"segment_tx"`
	TailTx            uint64        `json:"tail_tx"`
	TailBlocks        uint64        `json:"tail_blocks"`
	CanonicalDigest   string        `json:"canonical_digest"`
	TransactionDigest string        `json:"transaction_digest"`
	Blocks            []blockResult `json:"blocks"`
}

type blockResult struct {
	Number       uint64 `json:"number"`
	Hash         string `json:"hash"`
	StateRoot    string `json:"state_root"`
	ReceiptsRoot string `json:"receipts_root"`
	GasUsed      uint64 `json:"gas_used"`
	Transactions int    `json:"transactions"`
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	mode := flag.String("mode", "physical", "physical read-only audit or resolver fresh-OpenExplicit audit")
	dataDir := flag.String("data-dir", "", "Stopped owner's absolute data directory")
	wantHead := flag.Uint64("expect-head", 0, "Exact committed head number")
	wantGenesis := flag.String("expect-genesis-hash", "", "Externally pinned canonical genesis hash")
	wantHeadHash := flag.String("expect-head-hash", "", "Externally pinned committed head hash")
	wantChainID := flag.String("expect-chain-id", "", "Externally pinned decimal chain ID")
	wantIdentity := flag.String("expect-chain-identity", "", "Optional identity digest from a prior physical audit")
	minSegments := flag.Int("min-segments", 2, "Minimum physically published segments")
	minTailBlocks := flag.Uint64("min-tail-blocks", 1, "Minimum residual tail blocks")
	wantTxPerBlock := flag.Uint64("expect-tx-per-block", 0, "Exact transactions in every non-genesis block")
	wantTotalTx := flag.Uint64("expect-total-tx", 0, "Exact transactions across all audited blocks")
	flag.Parse()
	if flag.NArg() != 0 || !filepath.IsAbs(*dataDir) || *wantHead != 25 || *wantChainID == "" || *minSegments < 2 || *minTailBlocks == 0 || *wantTxPerBlock == 0 || *wantTotalTx == 0 {
		return fmt.Errorf("absolute --data-dir, --expect-head 25, positive min-tail-blocks/expect-tx-per-block/expect-total-tx and min-segments >=2 required")
	}
	expectedGenesis, err := parseHash(*wantGenesis)
	if err != nil {
		return fmt.Errorf("expect-genesis-hash: %w", err)
	}
	expectedHeadHash, err := parseHash(*wantHeadHash)
	if err != nil {
		return fmt.Errorf("expect-head-hash: %w", err)
	}
	var expectedIdentity types.Hash
	if *wantIdentity != "" {
		expectedIdentity, err = parseHash(*wantIdentity)
		if err != nil {
			return fmt.Errorf("expect-chain-identity: %w", err)
		}
	}
	if *mode == "resolver" {
		return runResolver(ctx, *dataDir, *wantHead, *wantTxPerBlock, *wantTotalTx, *wantChainID, expectedGenesis, expectedHeadHash, expectedIdentity)
	}
	if *mode != "physical" {
		return fmt.Errorf("mode must be physical or resolver")
	}
	lock := flock.New(filepath.Join(*dataDir, "LOCK"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("owner data directory is in use")
	}
	defer lock.Unlock()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbx.NewMDBX(log.New()).Path(filepath.Join(*dataDir, kv.ChainDB.String())).WithTableCfg(func(kv.TableCfg) kv.TableCfg {
		return modules.N42TableCfg
	}).Readonly().Open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	var genesis, headHash types.Hash
	var watermark uint64
	var identity types.Hash
	var config *params.ChainConfig
	err = db.View(ctx, func(tx kv.Tx) error {
		var err error
		genesis, err = rawdb.ReadCanonicalHash(tx, 0)
		if err != nil || genesis == (types.Hash{}) {
			return fmt.Errorf("canonical genesis unavailable: %w", err)
		}
		config, err = rawdb.ReadChainConfig(tx, genesis)
		if err != nil || config == nil || config.ChainID == nil {
			return fmt.Errorf("persisted chain config unavailable: %w", err)
		}
		if config.ChainID.String() != *wantChainID {
			return fmt.Errorf("persisted chain ID differs from external pin")
		}
		identity, err = txlookup.CanonicalChainIdentity(config.ChainID, genesis)
		if err != nil {
			return err
		}
		mark, err := tx.GetOne(modules.ChainConfig, []byte("txindex-tail-start"))
		if err != nil {
			return err
		}
		if len(mark) != 8 {
			return fmt.Errorf("missing or malformed tail watermark")
		}
		watermark = binary.BigEndian.Uint64(mark)
		if watermark != 2 {
			return fmt.Errorf("watermark %d differs from OFF block 1/ON block 2 boundary", watermark)
		}
		headHash = rawdb.ReadHotStuffCommittedHead(tx)
		if headHash == (types.Hash{}) {
			return fmt.Errorf("durable committed head unavailable")
		}
		if rawdb.ReadHeadBlockHash(tx) != headHash {
			return fmt.Errorf("full block head differs from committed head")
		}
		appliedNumber, appliedHash, applied, err := rawdb.ReadQMDBApplied(tx)
		if err != nil || !applied || appliedNumber != *wantHead || types.Hash(appliedHash) != headHash {
			return fmt.Errorf("QMDB applied head differs from committed head: %w", err)
		}
		number := rawdb.ReadHeaderNumber(tx, headHash)
		if number == nil || *number != *wantHead {
			return fmt.Errorf("durable head number differs from %d", *wantHead)
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, *wantHead)
		if err != nil || canonical != headHash {
			return fmt.Errorf("durable head is not canonical: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if genesis != expectedGenesis || headHash != expectedHeadHash || (expectedIdentity != (types.Hash{}) && identity != expectedIdentity) {
		return fmt.Errorf("external genesis/head/chain identity pin differs from physical database")
	}
	if err := internal.ConfigureBlockCommitmentRules(config); err != nil {
		return err
	}
	canonical := func(n uint64) (types.Hash, error) {
		var h types.Hash
		err := db.View(ctx, func(tx kv.Tx) error { var err error; h, err = rawdb.ReadCanonicalHash(tx, n); return err })
		return h, err
	}
	service, err := txlookup.NewAnchoredService(filepath.Join(*dataDir, "txindex"), identity, canonical, *wantHead)
	if err != nil {
		return err
	}
	defer service.Close()
	start, end := service.Coverage()
	if service.SegmentCount() < *minSegments || start != watermark || end <= start || end > *wantHead || *wantHead-end+1 < *minTailBlocks {
		return fmt.Errorf("physical coverage [%d,%d), %d segments, head %d, watermark %d misses required two-seal/residual-tail shape", start, end, service.SegmentCount(), *wantHead, watermark)
	}
	answer := result{Kind: "gov5-owner-txindex-physical-allhash-audit-v1", Genesis: genesis.Hex(), ChainIdentity: identity.Hex(), Head: *wantHead, HeadHash: headHash.Hex(), Watermark: watermark, SegmentStart: start, SegmentEnd: end, Segments: service.SegmentCount(), TailBlocks: *wantHead - end + 1}
	canonicalDigest, transactionDigest := sha256.New(), sha256.New()
	tail := txlookup.NewTail()
	type tailBlock struct {
		number uint64
		hashes []types.Hash
	}
	var tailBlocks []tailBlock
	var previous types.Hash
	for n := uint64(0); n <= *wantHead; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := db.View(ctx, func(tx kv.Tx) error {
			block, senders, err := rawdb.CanonicalBlockByNumberWithSenders(tx, n)
			if err != nil || block == nil {
				return fmt.Errorf("canonical block %d unavailable: %w", n, err)
			}
			canonicalHash, err := rawdb.ReadCanonicalHash(tx, n)
			if err != nil || canonicalHash != block.Hash() || block.Number64() == nil || block.Number64().Uint64() != n {
				return fmt.Errorf("canonical block %d hash/number mismatch: %w", n, err)
			}
			if n > 0 && block.ParentHash() != previous {
				return fmt.Errorf("canonical parent mismatch at %d", n)
			}
			previous = block.Hash()
			var ordinal [8]byte
			binary.BigEndian.PutUint64(ordinal[:], n)
			canonicalDigest.Write(ordinal[:])
			canonicalDigest.Write(previous[:])
			txs := block.Transactions()
			if n > 0 && uint64(len(txs)) != *wantTxPerBlock {
				return fmt.Errorf("block %d has %d transactions, want %d", n, len(txs), *wantTxPerBlock)
			}
			header := block.Header().(*blockpkg.Header)
			if blockpkg.BodyTxRoot(block.Body().(*blockpkg.Body), header.Time) != header.TxHash {
				return fmt.Errorf("block %d body transaction root mismatch", n)
			}
			if n > 0 {
				receipts, err := rawdb.ReadReceiptsForExecutionWithError(tx, block, senders)
				if err != nil || len(receipts) != len(txs) {
					return fmt.Errorf("block %d receipts %d != transactions %d: %w", n, len(receipts), len(txs), err)
				}
				executed := internal.ExecutedResultFor(config, n, header.Root, receipts)
				if executed.ReceiptHash != header.ReceiptHash || executed.GasUsed != header.GasUsed || executed.Bloom != header.Bloom {
					return fmt.Errorf("block %d receipt root/gas/bloom mismatch", n)
				}
			}
			answer.Blocks = append(answer.Blocks, blockResult{Number: n, Hash: block.Hash().Hex(), StateRoot: header.Root.Hex(), ReceiptsRoot: header.ReceiptHash.Hex(), GasUsed: header.GasUsed, Transactions: len(txs)})
			hashes := make([]types.Hash, len(txs))
			for i, txn := range txs {
				hashes[i] = txn.Hash()
			}
			if n >= end {
				tail.Add(n, hashes)
				tailBlocks = append(tailBlocks, tailBlock{n, hashes})
			}
			service.SetPositionVerifier(func(blockNumber, txIndex uint64, hash types.Hash) (bool, error) {
				return blockNumber == n && txIndex < uint64(len(hashes)) && hashes[txIndex] == hash, nil
			})
			for index, hash := range hashes {
				physical, err := tx.GetOne(modules.TxLookup, hash[:])
				if err != nil {
					return err
				}
				if n < watermark {
					if len(physical) == 0 || new(uint256.Int).SetBytes(physical).Uint64() != n {
						return fmt.Errorf("baseline table lookup failed at %d", n)
					}
					answer.BaselineTx++
				} else {
					if len(physical) != 0 {
						return fmt.Errorf("post-watermark physical TxLookup row at %d", n)
					}
					if n < end {
						got, err := service.Lookup(nil, hash)
						if err != nil || got == nil || *got != n {
							return fmt.Errorf("segment lookup failed at %d: %w", n, err)
						}
						answer.SegmentTx++
					} else {
						answer.TailTx++
					}
				}
				transactionDigest.Write(ordinal[:])
				binary.BigEndian.PutUint64(ordinal[:], uint64(index))
				transactionDigest.Write(ordinal[:])
				binary.BigEndian.PutUint64(ordinal[:], n)
				transactionDigest.Write(hash[:])
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	// Query after the entire residual tail has been rebuilt, so later Add calls
	// cannot hide an earlier block's missing or overwritten hash.
	for _, b := range tailBlocks {
		for _, hash := range b.hashes {
			if got, ok := tail.Lookup(hash); !ok || got != b.number {
				return fmt.Errorf("fresh tail lookup failed at %d", b.number)
			}
		}
	}
	answer.CanonicalDigest = hex.EncodeToString(canonicalDigest.Sum(nil))
	answer.TransactionDigest = hex.EncodeToString(transactionDigest.Sum(nil))
	if answer.BaselineTx+answer.SegmentTx+answer.TailTx != *wantTotalTx {
		return fmt.Errorf("total transactions %d != %d", answer.BaselineTx+answer.SegmentTx+answer.TailTx, *wantTotalTx)
	}
	if answer.BaselineTx == 0 || answer.SegmentTx == 0 || answer.TailTx == 0 {
		return fmt.Errorf("lookup tier not exercised: %+v", answer)
	}
	return json.NewEncoder(os.Stdout).Encode(answer)
}

func parseHash(value string) (types.Hash, error) {
	if len(value) != 66 || value[:2] != "0x" {
		return types.Hash{}, fmt.Errorf("64 hex digits with 0x prefix required")
	}
	raw, err := hex.DecodeString(value[2:])
	if err != nil {
		return types.Hash{}, err
	}
	h := types.BytesToHash(raw)
	if h == (types.Hash{}) {
		return h, fmt.Errorf("zero hash forbidden")
	}
	return h, nil
}

// diskChain is intentionally separate from the producer's BlockChain and
// caches the fixed 26-block audit corpus. The resolver's position verifier
// probes other blocks on RecSplit false positives; a two-entry cache would
// re-decode 163k transactions thousands of times. This cache stays bounded
// by the required head 25 and belongs only to the short-lived audit process.
type diskChain struct {
	db            kv.RwDB
	config        *params.ChainConfig
	genesis       *blockpkg.Block
	head          *blockpkg.Block
	mu            sync.Mutex
	currentNumber uint64
	current       *blockpkg.Block
	blocks        map[uint64]*blockpkg.Block
}

func (c *diskChain) Config() *params.ChainConfig   { return c.config }
func (c *diskChain) GenesisBlock() blockpkg.IBlock { return c.genesis }
func (c *diskChain) CurrentBlock() blockpkg.IBlock { return c.head }
func (c *diskChain) remember(n uint64, b *blockpkg.Block) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentNumber, c.current = n, b
	if c.blocks == nil {
		c.blocks = make(map[uint64]*blockpkg.Block, 26)
	}
	c.blocks[n] = b
}
func (c *diskChain) GetBlockByNumber(number *uint256.Int) (blockpkg.IBlock, error) {
	n := number.Uint64()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current != nil && c.currentNumber == n {
		return c.current, nil
	}
	if b := c.blocks[n]; b != nil {
		return b, nil
	}
	var b *blockpkg.Block
	err := c.db.View(context.Background(), func(tx kv.Tx) error { var err error; b, err = rawdb.ReadBlockByNumber(tx, n); return err })
	if err == nil && b != nil {
		c.blocks[n] = b
	}
	return b, err
}

func runResolver(ctx context.Context, dataDir string, wantHead, wantTxPerBlock, wantTotalTx uint64, wantChainID string, wantGenesis, wantHeadHash, wantIdentity types.Hash) error {
	lock := flock.New(filepath.Join(dataDir, "LOCK"))
	locked, err := lock.TryLock()
	if err != nil {
		return err
	}
	if !locked {
		return fmt.Errorf("owner data directory is in use")
	}
	defer lock.Unlock()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbx.NewMDBX(log.New()).Path(filepath.Join(dataDir, kv.ChainDB.String())).WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).Open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	chain := &diskChain{db: db, blocks: make(map[uint64]*blockpkg.Block, 26)}
	var actualIdentity types.Hash
	err = db.View(ctx, func(tx kv.Tx) error {
		var err error
		chain.genesis, err = rawdb.ReadBlockByNumber(tx, 0)
		if err != nil || chain.genesis == nil || chain.genesis.Hash() != wantGenesis {
			return fmt.Errorf("resolver genesis differs: %w", err)
		}
		chain.config, err = rawdb.ReadChainConfig(tx, wantGenesis)
		if err != nil || chain.config == nil {
			return fmt.Errorf("resolver chain config unavailable: %w", err)
		}
		if chain.config.ChainID == nil || chain.config.ChainID.String() != wantChainID {
			return fmt.Errorf("resolver chain ID differs from external pin")
		}
		identity, err := txlookup.CanonicalChainIdentity(chain.config.ChainID, wantGenesis)
		if err != nil || (wantIdentity != (types.Hash{}) && identity != wantIdentity) {
			return fmt.Errorf("resolver chain identity differs: %w", err)
		}
		actualIdentity = identity
		chain.head, err = rawdb.ReadBlockByNumber(tx, wantHead)
		if err != nil || chain.head == nil || chain.head.Hash() != wantHeadHash {
			return fmt.Errorf("resolver canonical head differs: %w", err)
		}
		if rawdb.ReadHeadBlockHash(tx) != wantHeadHash || rawdb.ReadHotStuffCommittedHead(tx) != wantHeadHash {
			return fmt.Errorf("resolver durable head markers differ")
		}
		appliedNumber, appliedHash, applied, err := rawdb.ReadQMDBApplied(tx)
		if err != nil || !applied || appliedNumber != wantHead || types.Hash(appliedHash) != wantHeadHash {
			return fmt.Errorf("resolver applied marker differs: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := internal.ConfigureBlockCommitmentRules(chain.config); err != nil {
		return err
	}
	x, err := txindexer.OpenExplicit(ctx, chain, db, filepath.Join(dataDir, "txindex"), true)
	if err != nil {
		return err
	}
	defer x.Stop()
	digest := sha256.New()
	var checked uint64
	for n := uint64(1); n <= wantHead; n++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := db.View(ctx, func(tx kv.Tx) error {
			b, err := rawdb.ReadBlockByNumber(tx, n)
			if err != nil || b == nil {
				return fmt.Errorf("resolver canonical block %d unavailable: %w", n, err)
			}
			txs := b.Transactions()
			if uint64(len(txs)) != wantTxPerBlock {
				return fmt.Errorf("resolver block %d tx count %d differs from %d", n, len(txs), wantTxPerBlock)
			}
			chain.remember(n, b)
			var number [8]byte
			binary.BigEndian.PutUint64(number[:], n)
			for index, txn := range txs {
				h := txn.Hash()
				got, err := rawdb.ReadTxLookupEntry(tx, h)
				if err != nil || got == nil || *got != n {
					return fmt.Errorf("real resolver hash lookup failed at block %d index %d: %w", n, index, err)
				}
				digest.Write(number[:])
				binary.BigEndian.PutUint64(number[:], uint64(index))
				digest.Write(number[:])
				binary.BigEndian.PutUint64(number[:], n)
				digest.Write(h[:])
				checked++
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if checked != wantTotalTx {
		return fmt.Errorf("resolver checked %d transactions, want %d", checked, wantTotalTx)
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Kind                string `json:"kind"`
		Genesis             string `json:"genesis"`
		Head                uint64 `json:"head"`
		HeadHash            string `json:"head_hash"`
		ChainIdentity       string `json:"chain_identity"`
		CheckedTransactions uint64 `json:"checked_transactions"`
		TransactionDigest   string `json:"transaction_digest"`
	}{"gov5-owner-txindex-real-resolver-allhash-audit-v1", wantGenesis.Hex(), wantHead, wantHeadHash.Hex(), actualIdentity.Hex(), checked, hex.EncodeToString(digest.Sum(nil))})
}
