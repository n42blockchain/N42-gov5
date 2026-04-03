// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"
	verkle "github.com/ethereum/go-verkle"
	"github.com/urfave/cli/v2"
	"golang.org/x/crypto/sha3"
	"lukechampine.com/blake3"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/replay"
	"github.com/n42blockchain/N42/lib/bmt"
	bmtstore "github.com/n42blockchain/N42/lib/bmt/store"
	libcommit "github.com/n42blockchain/N42/lib/commitment"
	"github.com/n42blockchain/N42/lib/jmt"
	jmtstore "github.com/n42blockchain/N42/lib/jmt/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
)

func init() {
	rootCmd = append(rootCmd, benchStateCommand)
}

var benchStateCommand = &cli.Command{
	Name:  "bench-state",
	Usage: "Benchmark 7 state commitment trees from a LeafJournal file",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "journal", Usage: "Path to leaves.journal", Required: true},
		&cli.Uint64Flag{Name: "blocks", Usage: "Number of blocks (0=all)", Value: 100000},
		&cli.StringFlag{Name: "output", Usage: "Output JSON file", Value: "bench_state_report.json"},
		&cli.IntFlag{Name: "proof-samples", Usage: "Random keys for proof benchmark", Value: 1000},
		&cli.Uint64Flag{Name: "snapshot-interval", Usage: "Blocks between growth snapshots", Value: 10000},
		&cli.StringFlag{Name: "only", Usage: "Run only one tree: jmt, mpt, bmt, verkle (default: all)"},
	},
	Action: runBenchState,
}

// --- Report Structures ---

type BenchReport struct {
	JournalPath      string           `json:"journal_path"`
	BlocksProcessed  uint64           `json:"blocks_processed"`
	EntriesProcessed uint64           `json:"entries_processed"`
	ElapsedSec       float64          `json:"elapsed_sec"`
	JMT              JMTMetrics       `json:"jmt"`
	MPT              MPTMetrics       `json:"mpt"`
	BMT              BMTMetrics       `json:"bmt"`
	Verkle           VerkleMetrics    `json:"verkle"`
	JMTPath          SimMetrics       `json:"jmt_path"`
	BinaryPath       SimMetrics       `json:"binary_path"`
	KZG              KZGMetrics       `json:"kzg"`
	Snapshots        []GrowthSnapshot `json:"growth_snapshots"`
}

type BMTMetrics struct {
	StorageBytes    int64   `json:"storage_bytes"`
	TotalNodes      int     `json:"total_nodes"`
	AvgNodeSize     float64 `json:"avg_node_size"`
	RootTimeAvgUs   float64 `json:"root_time_avg_us"`
	BlocksPerSec    float64 `json:"blocks_per_sec"`
	ProofSizeAvg    float64 `json:"proof_size_avg"`
	ProofSizeP50    int     `json:"proof_size_p50"`
	ProofSizeP99    int     `json:"proof_size_p99"`
	ProofDepthAvg   float64 `json:"proof_depth_avg"`
	ProofTimeAvgUs  float64 `json:"proof_gen_time_avg_us"`
	VerifyTimeAvgUs float64 `json:"proof_verify_time_avg_us"`
}

type JMTMetrics struct {
	StorageBytes       int64   `json:"storage_bytes"`
	TotalNodes         int     `json:"total_nodes"`
	AvgNodeSize        float64 `json:"avg_node_size"`
	RootTimeAvgUs      float64 `json:"root_time_avg_us"`
	BlocksPerSec       float64 `json:"blocks_per_sec"`
	ProofSizeAvg       float64 `json:"proof_size_avg"`
	ProofSizeP50       int     `json:"proof_size_p50"`
	ProofSizeP99       int     `json:"proof_size_p99"`
	ProofTimeAvgUs     float64 `json:"proof_gen_time_avg_us"`
	ProofDepthAvg      float64 `json:"proof_depth_avg"`
	VerifyTimeAvgUs    float64 `json:"proof_verify_time_avg_us"`
	HistProofSupported bool    `json:"hist_proof_supported"`
	HistStorageBytes   int64   `json:"hist_storage_bytes"`
}

type MPTMetrics struct {
	StorageBytes   int64   `json:"storage_bytes"`
	HistNodes      int     `json:"hist_nodes"`
	HistBytes      int64   `json:"hist_bytes"`
	TotalBranches  int     `json:"total_branches"`
	AvgBranchSize  float64 `json:"avg_branch_size"`
	RootTimeAvgUs  float64 `json:"root_time_avg_us"`
	BlocksPerSec   float64 `json:"blocks_per_sec"`
	ProofSupported bool    `json:"proof_supported"`
	ProofSizeAvg   float64 `json:"proof_size_avg,omitempty"`
	ProofSizeP50   int     `json:"proof_size_p50,omitempty"`
	ProofSizeP99   int     `json:"proof_size_p99,omitempty"`
	ProofDepthAvg  float64 `json:"proof_depth_avg,omitempty"`
	ProofTimeAvgUs float64 `json:"proof_gen_time_avg_us,omitempty"`
}

type VerkleMetrics struct {
	UniqueKeys    int     `json:"unique_keys"`
	HistNodes     int64   `json:"hist_nodes"`
	HistBytes     int64   `json:"hist_bytes"`
	RootTimeAvgUs float64 `json:"root_time_avg_us"`
	BlocksPerSec  float64 `json:"blocks_per_sec"`
	ProofSizeEst  int     `json:"proof_size_est"`
	ProofDepthEst float64 `json:"proof_depth_est"`
}

type SimMetrics struct {
	TotalNodes    int64   `json:"total_nodes"`
	StorageBytes  int64   `json:"storage_bytes"`
	LiveLeaves    int     `json:"live_leaves"`
	RootTimeAvgUs float64 `json:"root_time_avg_us"`
	BlocksPerSec  float64 `json:"blocks_per_sec"`
	ProofSizeEst  int     `json:"proof_size_est"`
	ProofDepthEst float64 `json:"proof_depth_est"`
}

type KZGMetrics struct {
	LiveLeaves      int     `json:"live_leaves"`
	StorageBytes    int64   `json:"storage_bytes"`
	RootTimeAvgUs   float64 `json:"root_time_avg_us"`
	BlocksPerSec    float64 `json:"blocks_per_sec"`
	ProofSizeEst    int     `json:"proof_size_est"`
	ProofDepthEst   float64 `json:"proof_depth_est"`
	CryptoTimeEstUs float64 `json:"crypto_time_est_us"`
}

type GrowthSnapshot struct {
	BlockNum       uint64 `json:"block_num"`
	JMTBytes       int64  `json:"jmt_bytes"`
	JMTNodes       int    `json:"jmt_nodes"`
	MPTBytes       int64  `json:"mpt_bytes"`
	MPTBranches    int    `json:"mpt_branches"`
	BMTBytes       int64  `json:"bmt_bytes"`
	BMTNodes       int    `json:"bmt_nodes"`
	VerkleKeys     int    `json:"verkle_keys"`
	VerkleHistNodes int64 `json:"verkle_hist_nodes"`
	VerkleHistBytes int64 `json:"verkle_hist_bytes"`
	JMTPathNodes   int64  `json:"jmt_path_nodes"`
	JMTPathBytes   int64  `json:"jmt_path_bytes"`
	BinaryNodes    int64  `json:"binary_nodes"`
	BinaryBytes    int64  `json:"binary_bytes"`
	KZGLeaves      int    `json:"kzg_leaves"`
	KZGBytes       int64  `json:"kzg_bytes"`
}

// --- MPT Bench Context ---

type benchBranchStore struct {
	data           map[string][]byte
	histNodes      int   // cumulative: every Put is a new historical node
	histBytes      int64 // cumulative: total bytes across all versions
}

func (s *benchBranchStore) Get(prefix []byte) ([]byte, error) {
	if d, ok := s.data[string(prefix)]; ok {
		return d, nil
	}
	return nil, nil
}

func (s *benchBranchStore) Put(prefix, data []byte) {
	s.histNodes++
	s.histBytes += int64(len(prefix)) + int64(len(data))
	s.data[string(prefix)] = append([]byte{}, data...)
}

func (s *benchBranchStore) CurrentBytes() int64 {
	var total int64
	for k, v := range s.data {
		total += int64(len(k)) + int64(len(v))
	}
	return total
}

type benchContext struct {
	branches *benchBranchStore
	state    map[string][]byte // current state: key → value
}

func (c *benchContext) Branch(prefix []byte) ([]byte, uint64, error) {
	d, _ := c.branches.Get(prefix)
	return d, 0, nil
}

func (c *benchContext) PutBranch(prefix, data, prevData []byte) error {
	c.branches.Put(prefix, data)
	return nil
}

func (c *benchContext) Account(plainKey []byte) (*libcommit.Update, error) {
	v, ok := c.state[string(plainKey)]
	if !ok || len(v) == 0 {
		return &libcommit.Update{Flags: libcommit.DeleteUpdate}, nil
	}
	u := &libcommit.Update{
		Flags: libcommit.BalanceUpdate | libcommit.NonceUpdate,
		Nonce: 1,
	}
	u.Balance.SetBytes(v)
	return u, nil
}

func (c *benchContext) Storage(plainKey []byte) (*libcommit.Update, error) {
	return &libcommit.Update{Flags: libcommit.DeleteUpdate}, nil
}

func (c *benchContext) TxNum() uint64 { return 0 }

// --- Simulator: PathTree16 (16-ary path-addressed Merkle) ---

type PathTree16 struct {
	leaves     map[[32]byte]struct{}
	totalNodes int64 // cumulative historical nodes created
	totalBytes int64 // cumulative historical bytes
	timeUs     int64
}

func newPathTree16() *PathTree16 {
	return &PathTree16{leaves: make(map[[32]byte]struct{})}
}

func (t *PathTree16) putBatch(entries []bmt.BatchEntry) {
	start := time.Now()
	for _, e := range entries {
		t.leaves[e.Key] = struct{}{}
	}
	depth := t.depth()
	for _, e := range entries {
		t.totalNodes += int64(depth) + 1
		t.totalBytes += int64(depth)*513 + int64(33+len(e.Value))
	}
	var buf [8]byte
	buf[0] = byte(len(t.leaves))
	_ = blake3.Sum256(buf[:])
	d := time.Since(start).Microseconds()
	if d < 1 {
		d = 1
	}
	t.timeUs += d
}

func (t *PathTree16) depth() int {
	n := len(t.leaves)
	if n <= 1 {
		return 1
	}
	// log16(n) = log2(n) / 4
	return int(math.Ceil(math.Log2(float64(n)) / 4.0))
}

func (t *PathTree16) proofSizeEst() int {
	// Each level contributes 15 sibling hashes (15 * 32 = 480B)
	return t.depth() * 15 * 32
}

// --- Simulator: PathTree2 (binary path-addressed Merkle) ---

type PathTree2 struct {
	leaves     map[[32]byte]struct{}
	totalNodes int64
	totalBytes int64
	timeUs     int64
}

func newPathTree2() *PathTree2 {
	return &PathTree2{leaves: make(map[[32]byte]struct{})}
}

func (t *PathTree2) putBatch(entries []bmt.BatchEntry) {
	start := time.Now()
	for _, e := range entries {
		t.leaves[e.Key] = struct{}{}
	}
	depth := t.depth()
	for _, e := range entries {
		t.totalNodes += int64(depth) + 1
		t.totalBytes += int64(depth)*65 + int64(33+len(e.Value))
	}
	var buf [8]byte
	buf[0] = byte(len(t.leaves))
	_ = blake3.Sum256(buf[:])
	d := time.Since(start).Microseconds()
	if d < 1 {
		d = 1
	}
	t.timeUs += d
}

func (t *PathTree2) depth() int {
	n := len(t.leaves)
	if n <= 1 {
		return 1
	}
	return int(math.Ceil(math.Log2(float64(n))))
}

func (t *PathTree2) proofSizeEst() int {
	// Each level: 1 sibling hash = 32B
	return t.depth() * 32
}

// --- Simulator: KZGTree (KZG 4096-ary polynomial commitment) ---

type KZGTree struct {
	leaves     map[[32]byte]struct{}
	totalNodes int64
	totalBytes int64
	timeUs     int64
}

func newKZGTree() *KZGTree {
	return &KZGTree{leaves: make(map[[32]byte]struct{})}
}

func (t *KZGTree) putBatch(entries []bmt.BatchEntry) {
	start := time.Now()
	for _, e := range entries {
		t.leaves[e.Key] = struct{}{}
	}
	depth := t.depth()
	for _, e := range entries {
		t.totalNodes += int64(depth) + 1
		t.totalBytes += int64(depth)*48 + int64(48+len(e.Value))
	}
	t.timeUs += time.Since(start).Microseconds()
	t.timeUs += int64(len(entries)) * 400 // simulated EC-mul overhead
}

func (t *KZGTree) depth() int {
	n := len(t.leaves)
	if n <= 1 {
		return 1
	}
	// log4096(n) = log2(n) / 12
	d := math.Ceil(math.Log2(float64(n)) / 12.0)
	if d < 1 {
		return 1
	}
	return int(d)
}

func (t *KZGTree) proofSizeEst() int {
	// Per level: 1 KZG opening proof (48B commitment + 48B proof) = 96B
	return t.depth() * 96
}

func (t *KZGTree) cryptoTimeEstUs() float64 {
	return float64(t.depth()) * 400.0 // 3 EC-mul per level
}

// --- Main ---

func runBenchState(cliCtx *cli.Context) error {
	journalPath := cliCtx.String("journal")
	maxBlocks := cliCtx.Uint64("blocks")
	outputPath := cliCtx.String("output")
	proofSamples := cliCtx.Int("proof-samples")
	snapshotInterval := cliCtx.Uint64("snapshot-interval")
	onlyTree := cliCtx.String("only") // "", "jmt", "mpt", "bmt", "verkle"

	runJMT := onlyTree == "" || onlyTree == "jmt"
	runMPT := onlyTree == "" || onlyTree == "mpt"
	runBMT := onlyTree == "" || onlyTree == "bmt"
	runVerkle := onlyTree == "" || onlyTree == "verkle"
	runSim := onlyTree == "" // simulators only when running all

	if onlyTree != "" {
		fmt.Printf("Single-tree mode: %s only\n", onlyTree)
	}

	reader, err := replay.NewLeafJournalReader(journalPath)
	if err != nil {
		return fmt.Errorf("open journal: %w", err)
	}
	defer reader.Close()

	// --- JMT setup (MDBX-backed) ---
	modules.N42Init()
	benchBaseDir := "D:/tree"
	os.MkdirAll(benchBaseDir, 0755)

	var jmtTree *jmt.Tree
	var jmtDB kv.RwDB
	var jmtTx kv.RwTx
	if runJMT {
		jmtTmpDir, _ := os.MkdirTemp(benchBaseDir, "bench-jmt-*")
		defer os.RemoveAll(jmtTmpDir)
		jmtTableCfg := kv.TableCfg{jmtstore.JMTNodeTable: {}}
		var err2 error
		jmtDB, err2 = mdbx.NewMDBX(log2.New()).
			Path(jmtTmpDir).
			WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return jmtTableCfg }).
			MapSize(64 * datasize.GB).
			GrowthStep(1 * datasize.GB).
			Open(context.Background())
		if err2 != nil {
			return fmt.Errorf("open jmt temp db: %w", err2)
		}
		defer jmtDB.Close()
		jmtTx, err2 = jmtDB.BeginRw(context.Background())
		if err2 != nil {
			return fmt.Errorf("begin jmt tx: %w", err2)
		}
		defer jmtTx.Rollback()
		jmtMDBXStore := jmtstore.NewMDBXStore(jmtTx, jmtstore.JMTNodeTable)
		jmtTree = jmt.New(jmtMDBXStore)
	}

	// --- MPT setup ---
	var mptBranches *benchBranchStore
	var mptState map[string][]byte
	var hph *libcommit.HexPatriciaHashed
	keccak := sha3.NewLegacyKeccak256()
	if runMPT {
		mptBranches = &benchBranchStore{data: make(map[string][]byte)}
		mptState = make(map[string][]byte)
		mptCtx := &benchContext{branches: mptBranches, state: mptState}
		hph = libcommit.NewHexPatriciaHashed(20, mptCtx)
	}

	// Tracking (incremental counters avoid full MDBX table scans)
	var (
		blocksProcessed  uint64
		entriesProcessed uint64
		jmtTotalUs       int64
		mptTotalUs       int64
		bmtTotalUs       int64
		jmtNodeCount     int
		jmtByteCount     int64 // real serialized bytes (key+value)
		bmtNodeCount     int
		bmtByteCount     int64
		liveKeys         []jmt.Hash
		snapshots        []GrowthSnapshot
	)

	// --- BMT setup (MDBX-backed) ---
	var bmtTree *bmt.Tree
	var bmtDB kv.RwDB
	var bmtCountStore *countingBMTStore
	if runBMT {
		bmtTmpDir, _ := os.MkdirTemp(benchBaseDir, "bench-bmt-*")
		defer os.RemoveAll(bmtTmpDir)
		bmtTableCfg := kv.TableCfg{bmtstore.BMTNodeTable: {}}
		var err2 error
		bmtDB, err2 = mdbx.NewMDBX(log2.New()).
			Path(bmtTmpDir).
			WithTableCfg(func(_ kv.TableCfg) kv.TableCfg { return bmtTableCfg }).
			MapSize(64 * datasize.GB).
			GrowthStep(1 * datasize.GB).
			Open(context.Background())
		if err2 != nil {
			return fmt.Errorf("open bmt temp db: %w", err2)
		}
		defer bmtDB.Close()
		bmtTx, err2 := bmtDB.BeginRw(context.Background())
		if err2 != nil {
			return fmt.Errorf("begin bmt tx: %w", err2)
		}
		defer bmtTx.Rollback()
		bmtRawStore := bmtstore.NewMDBXStore(bmtTx, bmtstore.BMTNodeTable)
		bmtCountStore = &countingBMTStore{inner: bmtRawStore, nodeCount: &bmtNodeCount, byteCount: &bmtByteCount}
		bmtTree = bmt.New(bmtRawStore)
	}

	// --- Verkle setup (in-memory, go-verkle) ---
	var verkleRoot verkle.VerkleNode
	var verkleTotalUs int64
	var verkleHistNodes int64
	var verkleHistBytes int64
	var verkleSeen map[[64]byte]struct{}
	verkleEnabled := runVerkle
	if runVerkle {
		verkleRoot = verkle.New()
		verkleSeen = make(map[[64]byte]struct{})
	}

	// --- Simulator setup ---
	var pathTree16 *PathTree16
	var pathTree2 *PathTree2
	var kzgTree *KZGTree
	if runSim {
		pathTree16 = newPathTree16()
		pathTree2 = newPathTree2()
		kzgTree = newKZGTree()
	}

	start := time.Now()
	fmt.Printf("Processing journal: %s\n", journalPath)

	for {
		if maxBlocks > 0 && blocksProcessed >= maxBlocks {
			break
		}
		block, err := reader.ReadBlock()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read block: %w", err)
		}

		entries := block.Entries
		entriesProcessed += uint64(len(entries))

		// Skip all tree ops for empty blocks (only read journal + increment counter)
		if len(entries) > 0 {
			// --- JMT ---
			if runJMT {
				jmtEntries := make([]jmt.BatchEntry, len(entries))
				for i, e := range entries {
					var h jmt.Hash
					copy(h[:], e.Key[:])
					jmtEntries[i] = jmt.BatchEntry{KeyHash: h, Value: e.Value}
				}
				sort.Slice(jmtEntries, func(i, j int) bool {
					return bytes.Compare(jmtEntries[i].KeyHash[:], jmtEntries[j].KeyHash[:]) < 0
				})
				t0 := time.Now()
				if _, err := jmtTree.BatchUpdate(jmtEntries); err != nil {
					return fmt.Errorf("jmt batch block %d: %w", block.BlockNum, err)
				}
				jmtNodeCount += jmtTree.DirtyCount()
				jmtByteCount += jmtTree.DirtyBytes()
				if err := jmtTree.Flush(); err != nil {
					return fmt.Errorf("jmt flush block %d: %w", block.BlockNum, err)
				}
				jmtTotalUs += time.Since(t0).Microseconds()
			}

			// --- BMT (MDBX-backed) ---
			if runBMT {
				bmtEntries := make([]bmt.BatchEntry, len(entries))
				for i, e := range entries {
					bmtEntries[i] = bmt.BatchEntry{Key: e.Key, Value: e.Value}
				}
				sort.Slice(bmtEntries, func(i, j int) bool {
					return bytes.Compare(bmtEntries[i].Key[:], bmtEntries[j].Key[:]) < 0
				})
				t0b := time.Now()
				if err := bmtTree.PutBatch(bmtEntries); err != nil {
					return fmt.Errorf("bmt batch block %d: %w", block.BlockNum, err)
				}
				if err := bmtTree.FlushTo(bmtCountStore); err != nil {
					return fmt.Errorf("bmt flush block %d: %w", block.BlockNum, err)
				}
				bmtTotalUs += time.Since(t0b).Microseconds()
			}

			// --- MPT ---
			if runMPT {
				updates := libcommit.NewUpdates(libcommit.ModeUpdate, "", func(key []byte) []byte {
					keccak.Reset()
					keccak.Write(key)
					hash := keccak.Sum(nil)
					nibbles := make([]byte, 64)
					for i, b := range hash {
						nibbles[i*2] = b >> 4
						nibbles[i*2+1] = b & 0x0f
					}
					return nibbles
				})
				for _, e := range entries {
					pk := string(e.Key[:20])
					if len(e.Value) == 0 {
						delete(mptState, pk)
						updates.TouchPlainKey(pk, nil, updates.TouchAccount)
					} else {
						var acct account.StateAccount
						acct.Nonce = 1
						acct.Balance.SetBytes(e.Value)
						acct.CodeHash = types.Hash{0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c, 0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0, 0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b, 0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70}
						enc := acct.MarshalV2()
						mptState[pk] = enc
						updates.TouchPlainKey(pk, enc, updates.TouchAccount)
					}
				}
				t1 := time.Now()
				if updates.Size() > 0 {
					if _, err := hph.Process(context.Background(), updates, "bench", nil, libcommit.WarmupConfig{}); err != nil {
						return fmt.Errorf("mpt process block %d: %w", block.BlockNum, err)
					}
				}
				mptTotalUs += time.Since(t1).Microseconds()
			}

			// --- Verkle (go-verkle) ---
			if verkleEnabled {
				t0v := time.Now()
				var vval [32]byte
				panicked := false
				for _, e := range entries {
					vkey := toVerkleKey(e.Key)
					vval = [32]byte{}
					if len(e.Value) > 0 {
						copy(vval[:], e.Value)
					}
					func() {
						defer func() {
							if r := recover(); r != nil {
								panicked = true
							}
						}()
						verkleRoot.Insert(vkey[:], vval[:], nil)
					}()
					if panicked {
						break
					}
				}
				if panicked {
					verkleEnabled = false
					fmt.Printf("  [warn] Verkle disabled at %d blocks\n", blocksProcessed)
				} else {
					func() {
						defer func() {
							if r := recover(); r != nil {
								verkleEnabled = false
								fmt.Printf("  [warn] Verkle Commit() failed at %d blocks\n", blocksProcessed)
							}
						}()
						verkleRoot.Commit()
					}()
				}
				verkleTotalUs += time.Since(t0v).Microseconds()
			}

			// --- Simulators ---
			if runSim {
				simEntries := make([]bmt.BatchEntry, len(entries))
				for i, e := range entries {
					simEntries[i] = bmt.BatchEntry{Key: e.Key, Value: e.Value}
				}
				pathTree16.putBatch(simEntries)
				pathTree2.putBatch(simEntries)
				kzgTree.putBatch(simEntries)
			}

			// Collect live keys for proof sampling
			for _, e := range entries {
				if len(e.Value) > 0 {
					var h jmt.Hash
					copy(h[:], e.Key[:])
					if len(liveKeys) < proofSamples*10 {
						liveKeys = append(liveKeys, h)
					} else {
						idx := rand.Intn(len(liveKeys))
						liveKeys[idx] = h
					}
				}
			}
		}

		// MDBX commit disabled — 128GB RAM can hold the full write tx
		// Commit was causing hangs on Windows due to msync on large mmap files

		// Growth snapshot
		blocksProcessed++
		if blocksProcessed%100000 == 0 && blocksProcessed%snapshotInterval != 0 {
			fmt.Printf("  %d blocks | %.0f blk/s\n", blocksProcessed, float64(blocksProcessed)/time.Since(start).Seconds())
		}
		if blocksProcessed%snapshotInterval == 0 {
			// Verkle: real serialization at snapshot intervals only
			if verkleEnabled {
				func() {
					defer func() { recover() }()
					nodes, err := verkleRoot.(*verkle.InternalNode).BatchSerialize()
					if err == nil {
						for _, sn := range nodes {
							if _, seen := verkleSeen[sn.CommitmentBytes]; !seen {
								verkleSeen[sn.CommitmentBytes] = struct{}{}
								verkleHistNodes++
								verkleHistBytes += int64(len(sn.SerializedBytes))
							}
						}
					}
				}()
			}
			jmtEstBytes := jmtByteCount
			snap := GrowthSnapshot{
				BlockNum: blocksProcessed,
			}
			if runJMT {
				snap.JMTBytes = jmtEstBytes
				snap.JMTNodes = jmtNodeCount
			}
			if runMPT {
				snap.MPTBytes = mptBranches.histBytes
				snap.MPTBranches = mptBranches.histNodes
			}
			if runBMT {
				snap.BMTBytes = bmtByteCount
				snap.BMTNodes = bmtNodeCount
			}
			if verkleEnabled {
				snap.VerkleHistNodes = verkleHistNodes
				snap.VerkleHistBytes = verkleHistBytes
			}
			if runSim {
				snap.VerkleKeys = len(pathTree16.leaves)
				snap.JMTPathNodes = pathTree16.totalNodes
				snap.JMTPathBytes = pathTree16.totalBytes
				snap.BinaryNodes = pathTree2.totalNodes
				snap.BinaryBytes = pathTree2.totalBytes
				snap.KZGLeaves = len(kzgTree.leaves)
				snap.KZGBytes = kzgTree.totalBytes
			}
			snapshots = append(snapshots, snap)
			elapsed := time.Since(start).Seconds()
			// Print progress line based on which tree is running
			switch onlyTree {
			case "jmt":
				fmt.Printf("  %d blocks | JMT: %d nodes %.1f MB | %.0f blk/s\n",
					blocksProcessed, snap.JMTNodes, float64(snap.JMTBytes)/1e6, float64(blocksProcessed)/elapsed)
			case "mpt":
				fmt.Printf("  %d blocks | MPT: %d br %.1f MB | %.0f blk/s\n",
					blocksProcessed, snap.MPTBranches, float64(snap.MPTBytes)/1e6, float64(blocksProcessed)/elapsed)
			case "bmt":
				fmt.Printf("  %d blocks | BMT: %d nodes %.1f MB | %.0f blk/s\n",
					blocksProcessed, snap.BMTNodes, float64(snap.BMTBytes)/1e6, float64(blocksProcessed)/elapsed)
			case "verkle":
				fmt.Printf("  %d blocks | Verkle: %d hist nodes %.1f MB | %.0f blk/s\n",
					blocksProcessed, snap.VerkleHistNodes, float64(snap.VerkleHistBytes)/1e6, float64(blocksProcessed)/elapsed)
			default:
				fmt.Printf("  %d blocks | JMT: %d nodes %.1f MB | MPT: %d br %.1f MB | BMT: %d nodes %.1f MB | Verkle | PT16: %d | PT2: %d | KZG: %d | %.0f blk/s\n",
					blocksProcessed,
					snap.JMTNodes, float64(snap.JMTBytes)/1e6,
					snap.MPTBranches, float64(snap.MPTBytes)/1e6,
					snap.BMTNodes, float64(snap.BMTBytes)/1e6,
					len(pathTree16.leaves), len(pathTree2.leaves), len(kzgTree.leaves),
					float64(blocksProcessed)/elapsed)
			}
		}
	}

	elapsed := time.Since(start)

	// --- Proof Benchmarks ---
	sampleCount := proofSamples
	if sampleCount > len(liveKeys) {
		sampleCount = len(liveKeys)
	}
	rand.Shuffle(len(liveKeys), func(i, j int) { liveKeys[i], liveKeys[j] = liveKeys[j], liveKeys[i] })

	// --- JMT Proof Benchmark ---
	var proofSizes []int
	var proofDepths []int
	var proofGenUs int64
	var proofVerifyUs int64
	if runJMT {
		fmt.Printf("\nJMT proof benchmark: sampling %d keys...\n", sampleCount)
		for i := 0; i < sampleCount; i++ {
			t0 := time.Now()
			proof, err := jmtTree.GetProof(liveKeys[i])
			genUs := time.Since(t0).Microseconds()
			proofGenUs += genUs
			if err != nil || proof == nil {
				continue
			}
			pSize := 0
			for _, pe := range proof.Path {
				pSize += len(pe.NodeData)
			}
			proofSizes = append(proofSizes, pSize)
			proofDepths = append(proofDepths, len(proof.Path))
			t1 := time.Now()
			_, _ = jmt.VerifyProof(jmtTree.Root(), proof, jmt.DefaultHasher())
			proofVerifyUs += time.Since(t1).Microseconds()
		}
	}

	// --- MPT Proof Benchmark ---
	var mptProofSizes []int
	var mptProofDepths []int
	var mptProofGenUs int64
	if runMPT {
		fmt.Printf("MPT proof estimation from %d branches (hist: %d)...\n", len(mptBranches.data), mptBranches.histNodes)
		if len(mptBranches.data) > 0 {
			mptSampleCount := sampleCount
			if mptSampleCount > len(liveKeys) {
				mptSampleCount = len(liveKeys)
			}
			for i := 0; i < mptSampleCount; i++ {
				t0 := time.Now()
				key := liveKeys[i]
				keccak.Reset()
				keccak.Write(key[:20])
				hash := keccak.Sum(nil)
				nibbles := make([]byte, 0, 64)
				for _, b := range hash {
					nibbles = append(nibbles, b>>4, b&0x0f)
				}
				depth := 0
				proofSize := 0
				for prefix, branchData := range mptBranches.data {
					prefixBytes := []byte(prefix)
					if len(prefixBytes) <= len(nibbles) {
						match := true
						for j := 0; j < len(prefixBytes); j++ {
							if prefixBytes[j] != nibbles[j] {
								match = false
								break
							}
						}
						if match {
							depth++
							proofSize += len(branchData)
						}
					}
				}
				mptProofGenUs += time.Since(t0).Microseconds()
				if depth > 0 {
					mptProofSizes = append(mptProofSizes, proofSize)
					mptProofDepths = append(mptProofDepths, depth)
				}
			}
		}
	}

	// --- BMT Proof Benchmark ---
	var bmtProofSizes []int
	var bmtProofDepths []int
	var bmtProofGenUs int64
	var bmtProofVerifyUs int64
	if runBMT {
		fmt.Printf("BMT proof benchmark: sampling %d keys...\n", sampleCount)
		for i := 0; i < sampleCount; i++ {
			t0 := time.Now()
			proof, err := bmtTree.GetProof(types.Hash(liveKeys[i]))
			genUs := time.Since(t0).Microseconds()
			bmtProofGenUs += genUs
			if err != nil || proof == nil {
				continue
			}
			pSize := len(proof.Siblings) * 32
			bmtProofSizes = append(bmtProofSizes, pSize)
			bmtProofDepths = append(bmtProofDepths, proof.Depth)
			t1 := time.Now()
			bmt.VerifyProof(bmtTree.Root(), proof)
			bmtProofVerifyUs += time.Since(t1).Microseconds()
		}
	}

	// Final Verkle serialization
	if verkleEnabled {
		func() {
			defer func() { recover() }()
			nodes, err := verkleRoot.(*verkle.InternalNode).BatchSerialize()
			if err == nil {
				for _, sn := range nodes {
					if _, seen := verkleSeen[sn.CommitmentBytes]; !seen {
						verkleSeen[sn.CommitmentBytes] = struct{}{}
						verkleHistNodes++
						verkleHistBytes += int64(len(sn.SerializedBytes))
					}
				}
			}
		}()
	}

	// Final stats from incremental counters
	jmtFinalNodes := jmtNodeCount
	jmtFinalBytes := jmtByteCount
	bmtFinalNodes := bmtNodeCount
	bmtFinalBytes := bmtByteCount

	// --- Build Report ---
	report := BenchReport{
		JournalPath:      journalPath,
		BlocksProcessed:  blocksProcessed,
		EntriesProcessed: entriesProcessed,
		ElapsedSec:       elapsed.Seconds(),
		Snapshots:        snapshots,
	}

	// JMT metrics
	if runJMT {
		report.JMT = JMTMetrics{
			StorageBytes:       jmtFinalBytes,
			TotalNodes:         jmtFinalNodes,
			AvgNodeSize:        safeDiv(float64(jmtFinalBytes), float64(jmtFinalNodes)),
			RootTimeAvgUs:      safeDiv(float64(jmtTotalUs), float64(blocksProcessed)),
			BlocksPerSec:       safeDiv(float64(blocksProcessed), float64(jmtTotalUs)/1e6),
			HistProofSupported: true,
			HistStorageBytes:   jmtFinalBytes,
		}
		if len(proofSizes) > 0 {
			sort.Ints(proofSizes)
			sort.Ints(proofDepths)
			report.JMT.ProofSizeAvg = avg(proofSizes)
			report.JMT.ProofSizeP50 = proofSizes[len(proofSizes)/2]
			report.JMT.ProofSizeP99 = proofSizes[len(proofSizes)*99/100]
			report.JMT.ProofDepthAvg = avg(proofDepths)
			report.JMT.ProofTimeAvgUs = safeDiv(float64(proofGenUs), float64(len(proofSizes)))
			report.JMT.VerifyTimeAvgUs = safeDiv(float64(proofVerifyUs), float64(len(proofSizes)))
		}
	}

	// MPT metrics
	if runMPT {
		report.MPT = MPTMetrics{
			StorageBytes:   mptBranches.histBytes,
			HistNodes:      mptBranches.histNodes,
			HistBytes:      mptBranches.histBytes,
			TotalBranches:  mptBranches.histNodes,
			AvgBranchSize:  safeDiv(float64(mptBranches.histBytes), float64(mptBranches.histNodes)),
			RootTimeAvgUs:  safeDiv(float64(mptTotalUs), float64(blocksProcessed)),
			BlocksPerSec:   safeDiv(float64(blocksProcessed), float64(mptTotalUs)/1e6),
			ProofSupported: len(mptProofSizes) > 0,
		}
		if len(mptProofSizes) > 0 {
			sort.Ints(mptProofSizes)
			sort.Ints(mptProofDepths)
			report.MPT.ProofSizeAvg = avg(mptProofSizes)
			report.MPT.ProofSizeP50 = mptProofSizes[len(mptProofSizes)/2]
			report.MPT.ProofSizeP99 = mptProofSizes[len(mptProofSizes)*99/100]
			report.MPT.ProofDepthAvg = avg(mptProofDepths)
			report.MPT.ProofTimeAvgUs = safeDiv(float64(mptProofGenUs), float64(len(mptProofSizes)))
		}
	}

	// BMT metrics
	if runBMT {
		report.BMT = BMTMetrics{
			StorageBytes:  bmtFinalBytes,
			TotalNodes:    bmtFinalNodes,
			AvgNodeSize:   safeDiv(float64(bmtFinalBytes), float64(bmtFinalNodes)),
			RootTimeAvgUs: safeDiv(float64(bmtTotalUs), float64(blocksProcessed)),
			BlocksPerSec:  safeDiv(float64(blocksProcessed), float64(bmtTotalUs)/1e6),
		}
		if len(bmtProofSizes) > 0 {
			sort.Ints(bmtProofSizes)
			sort.Ints(bmtProofDepths)
			report.BMT.ProofSizeAvg = avg(bmtProofSizes)
			report.BMT.ProofSizeP50 = bmtProofSizes[len(bmtProofSizes)/2]
			report.BMT.ProofSizeP99 = bmtProofSizes[len(bmtProofSizes)*99/100]
			report.BMT.ProofDepthAvg = avg(bmtProofDepths)
			report.BMT.ProofTimeAvgUs = safeDiv(float64(bmtProofGenUs), float64(len(bmtProofSizes)))
			report.BMT.VerifyTimeAvgUs = safeDiv(float64(bmtProofVerifyUs), float64(len(bmtProofSizes)))
		}
	}

	// Verkle metrics
	if runVerkle {
		uniqueKeys := 0
		if runSim && pathTree16 != nil {
			uniqueKeys = len(pathTree16.leaves)
		}
		report.Verkle = VerkleMetrics{
			UniqueKeys:    uniqueKeys,
			HistNodes:     verkleHistNodes,
			HistBytes:     verkleHistBytes,
			RootTimeAvgUs: safeDiv(float64(verkleTotalUs), float64(blocksProcessed)),
			BlocksPerSec:  safeDiv(float64(blocksProcessed), float64(verkleTotalUs)/1e6),
			ProofSizeEst:  verkleProofSizeEst(uniqueKeys),
			ProofDepthEst: verkleDepthEst(uniqueKeys),
		}
	}

	// Simulator metrics
	if runSim {
		report.JMTPath = SimMetrics{
			TotalNodes:    pathTree16.totalNodes,
			StorageBytes:  pathTree16.totalBytes,
			LiveLeaves:    len(pathTree16.leaves),
			RootTimeAvgUs: safeDiv(float64(pathTree16.timeUs), float64(blocksProcessed)),
			BlocksPerSec:  safeDiv(float64(blocksProcessed), float64(pathTree16.timeUs)/1e6),
			ProofSizeEst:  pathTree16.proofSizeEst(),
			ProofDepthEst: float64(pathTree16.depth()),
		}
		report.BinaryPath = SimMetrics{
			TotalNodes:    pathTree2.totalNodes,
			StorageBytes:  pathTree2.totalBytes,
			LiveLeaves:    len(pathTree2.leaves),
			RootTimeAvgUs: safeDiv(float64(pathTree2.timeUs), float64(blocksProcessed)),
			BlocksPerSec:  safeDiv(float64(blocksProcessed), float64(pathTree2.timeUs)/1e6),
			ProofSizeEst:  pathTree2.proofSizeEst(),
			ProofDepthEst: float64(pathTree2.depth()),
		}
		report.KZG = KZGMetrics{
			LiveLeaves:      len(kzgTree.leaves),
			StorageBytes:    kzgTree.totalBytes,
			RootTimeAvgUs:   safeDiv(float64(kzgTree.timeUs), float64(blocksProcessed)),
			BlocksPerSec:    safeDiv(float64(blocksProcessed), float64(kzgTree.timeUs)/1e6),
			ProofSizeEst:    kzgTree.proofSizeEst(),
			ProofDepthEst:   float64(kzgTree.depth()),
			CryptoTimeEstUs: kzgTree.cryptoTimeEstUs(),
		}
	}

	// --- Output ---
	printTable(report)

	jsonData, _ := json.MarshalIndent(report, "", "  ")
	os.WriteFile(outputPath, jsonData, 0644)
	fmt.Printf("\nReport written to %s\n", outputPath)

	return nil
}

// --- Helpers ---

// countingStore wraps bmt.NodeStore to track incremental node/byte counts.
type countingBMTStore struct {
	inner     bmt.NodeStore
	nodeCount *int
	byteCount *int64
}

func (s *countingBMTStore) Get(hash bmt.Hash) (bmt.NodeValue, error) { return s.inner.Get(hash) }
func (s *countingBMTStore) Put(hash bmt.Hash, value bmt.NodeValue) error {
	*s.nodeCount++
	*s.byteCount += int64(32 + len(value))
	return s.inner.Put(hash, value)
}

func safeDiv(a, b float64) float64 {
	if b == 0 {
		return 0
	}
	return a / b
}

func avg(vals []int) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0
	for _, v := range vals {
		sum += v
	}
	return float64(sum) / float64(len(vals))
}

// toVerkleKey converts a journal key to a Verkle-safe 32-byte key.
// go-verkle panics when two stems (key[:31]) share bytes 0..29 but differ
// at byte 30, because tree depth exceeds StemSize-1. Non-hashed journal keys
// (raw addresses / storage slots) often have long zero-padding that triggers
// this. Blake3 re-hash ensures uniform stem distribution.
func toVerkleKey(key [32]byte) [32]byte {
	var buf [33]byte
	buf[0] = 0x56 // 'V' domain separator
	copy(buf[1:], key[:])
	return blake3.Sum256(buf[:])
}

func verkleDepthEst(n int) float64 {
	if n <= 256 {
		return 1
	}
	return math.Ceil(math.Log(float64(n)) / math.Log(256))
}

func verkleProofSizeEst(n int) int {
	// IPA proof: 8 * 2 * 32B (CL+CR) + 32B (final eval) = 544B
	// + depth * 32B (commitments by path)
	// + ~3B depth/extension status
	depth := int(verkleDepthEst(n))
	return 544 + depth*32 + depth
}

func printTable(r BenchReport) {
	fmt.Println()
	// Two-part table: real implementations + simulators
	fmt.Println("═══════════════════════ Real Implementations ═══════════════════════════════════════════════════════════")
	fmt.Println("╔══════════════════════╦══════════════╦══════════════╦══════════════╦══════════════╗")
	fmt.Println("║ Metric               ║  JMT(16)CA   ║  MPT(16)CA   ║  BMT(2)CA    ║  Verkle(256) ║")
	fmt.Println("╠══════════════════════╬══════════════╬══════════════╬══════════════╬══════════════╣")
	fmt.Printf("║ Blocks               ║ %12d ║ %12d ║ %12d ║ %12d ║\n",
		r.BlocksProcessed, r.BlocksProcessed, r.BlocksProcessed, r.BlocksProcessed)
	fmt.Printf("║ Entries              ║ %12d ║ %12d ║ %12d ║ %12d ║\n",
		r.EntriesProcessed, r.EntriesProcessed, r.EntriesProcessed, r.EntriesProcessed)
	fmt.Printf("║ Hist nodes           ║ %12d ║ %12d ║ %12d ║ %12d ║\n",
		r.JMT.TotalNodes, r.MPT.HistNodes, r.BMT.TotalNodes, r.Verkle.HistNodes)
	fmt.Printf("║ Hist storage (MB)    ║ %12.1f ║ %12.1f ║ %12.1f ║ %12.1f ║\n",
		float64(r.JMT.StorageBytes)/1e6, float64(r.MPT.HistBytes)/1e6, float64(r.BMT.StorageBytes)/1e6, float64(r.Verkle.HistBytes)/1e6)
	fmt.Printf("║ Avg node size (B)    ║ %12.1f ║ %12.1f ║ %12.1f ║ %12s ║\n",
		r.JMT.AvgNodeSize, r.MPT.AvgBranchSize, r.BMT.AvgNodeSize, "97/288")
	fmt.Printf("║ Root time (us/blk)   ║ %12.1f ║ %12.1f ║ %12.1f ║ %12.1f ║\n",
		r.JMT.RootTimeAvgUs, r.MPT.RootTimeAvgUs, r.BMT.RootTimeAvgUs, r.Verkle.RootTimeAvgUs)
	fmt.Printf("║ Throughput (blk/s)   ║ %12.0f ║ %12.0f ║ %12.0f ║ %12.0f ║\n",
		r.JMT.BlocksPerSec, r.MPT.BlocksPerSec, r.BMT.BlocksPerSec, r.Verkle.BlocksPerSec)
	fmt.Println("╠══════════════════════╬══════════════╬══════════════╬══════════════╬══════════════╣")
	fmt.Printf("║ Proof size avg (B)   ║ %12.0f ║ %12.0f ║ %12.0f ║ %12d ║\n",
		r.JMT.ProofSizeAvg, r.MPT.ProofSizeAvg, r.BMT.ProofSizeAvg, r.Verkle.ProofSizeEst)
	fmt.Printf("║ Proof size p50       ║ %12d ║ %12d ║ %12d ║          --  ║\n",
		r.JMT.ProofSizeP50, r.MPT.ProofSizeP50, r.BMT.ProofSizeP50)
	fmt.Printf("║ Proof size p99       ║ %12d ║ %12d ║ %12d ║          --  ║\n",
		r.JMT.ProofSizeP99, r.MPT.ProofSizeP99, r.BMT.ProofSizeP99)
	fmt.Printf("║ Proof depth avg      ║ %12.1f ║ %12.1f ║ %12.1f ║ %12.1f ║\n",
		r.JMT.ProofDepthAvg, r.MPT.ProofDepthAvg, r.BMT.ProofDepthAvg, r.Verkle.ProofDepthEst)
	fmt.Printf("║ Proof gen (us)       ║ %12.1f ║ %12.1f ║ %12.1f ║          --  ║\n",
		r.JMT.ProofTimeAvgUs, r.MPT.ProofTimeAvgUs, r.BMT.ProofTimeAvgUs)
	fmt.Printf("║ Proof verify (us)    ║ %12.1f ║          --  ║ %12.1f ║          --  ║\n",
		r.JMT.VerifyTimeAvgUs, r.BMT.VerifyTimeAvgUs)
	fmt.Printf("║ Historical proof     ║          yes ║      planned ║          yes ║          yes ║\n")
	fmt.Printf("║ Full hist (MB)       ║ %12.1f ║ %12.1f ║ %12.1f ║ %12.1f ║\n",
		float64(r.JMT.HistStorageBytes)/1e6, float64(r.MPT.HistBytes)/1e6, float64(r.BMT.StorageBytes)/1e6, float64(r.Verkle.HistBytes)/1e6)
	fmt.Println("╚══════════════════════╩══════════════╩══════════════╩══════════════╩══════════════╝")

	fmt.Println()
	fmt.Println("════════════════════════ Simulated Trees ═══════════════════════════════════════════")
	fmt.Println("╔══════════════════════╦══════════════╦══════════════╦══════════════╗")
	fmt.Println("║ Metric               ║ JMT(16)Path  ║ Binary(2)P   ║  KZG(4096)   ║")
	fmt.Println("╠══════════════════════╬══════════════╬══════════════╬══════════════╣")
	fmt.Printf("║ Live leaves          ║ %12d ║ %12d ║ %12d ║\n",
		r.JMTPath.LiveLeaves, r.BinaryPath.LiveLeaves, r.KZG.LiveLeaves)
	fmt.Printf("║ Hist nodes (total)   ║ %12d ║ %12d ║          --  ║\n",
		r.JMTPath.TotalNodes, r.BinaryPath.TotalNodes)
	fmt.Printf("║ Hist storage (MB)    ║ %12.1f ║ %12.1f ║ %12.1f ║\n",
		float64(r.JMTPath.StorageBytes)/1e6, float64(r.BinaryPath.StorageBytes)/1e6, float64(r.KZG.StorageBytes)/1e6)
	fmt.Printf("║ Root time (us/blk)   ║ %12.1f ║ %12.1f ║ %12.1f ║\n",
		r.JMTPath.RootTimeAvgUs, r.BinaryPath.RootTimeAvgUs, r.KZG.RootTimeAvgUs)
	fmt.Printf("║ Throughput (blk/s)   ║ %12.0f ║ %12.0f ║ %12.0f ║\n",
		r.JMTPath.BlocksPerSec, r.BinaryPath.BlocksPerSec, r.KZG.BlocksPerSec)
	fmt.Printf("║ Proof size est (B)   ║ %12d ║ %12d ║ %12d ║\n",
		r.JMTPath.ProofSizeEst, r.BinaryPath.ProofSizeEst, r.KZG.ProofSizeEst)
	fmt.Printf("║ Proof depth est      ║ %12.1f ║ %12.1f ║ %12.1f ║\n",
		r.JMTPath.ProofDepthEst, r.BinaryPath.ProofDepthEst, r.KZG.ProofDepthEst)
	fmt.Printf("║ Crypto overhead (us) ║          --  ║          --  ║ %12.1f ║\n",
		r.KZG.CryptoTimeEstUs)
	fmt.Println("╚══════════════════════╩══════════════╩══════════════╩══════════════╝")

	fmt.Printf("\nTotal elapsed: %.1fs\n", r.ElapsedSec)
}
