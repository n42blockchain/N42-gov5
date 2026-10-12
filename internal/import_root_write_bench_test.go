// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// P4 (docs/QS_MULTI_MILLION_DESIGN.md): an offline model of a follower's
// full import of the fleet's block shape -- 163,200 plain transfers in 1,020
// gateway frames, 8,000 senders over 22,857 hash-derived recipients, 200k
// filler accounts -- through the lag executor on a native QMDB chain at
// deferred depth 2, with reads from the tree (N42_STATE_READ_QMDB=1) and the
// plain Account table frozen (N42_STATE_WRITE_QMDB_ONLY=1), on a durable
// on-disk MDBX. It reports, per block, the state root (and its parts: the
// dirty-set collection, the snapshot publication, the lock wait, the op
// build, ApplyOps, the upper fold) and the write (per table, and the MDBX
// commit).
//
// Off unless N42_BENCH_IMPORT=1. Sizes: N42_BENCH_TXS, N42_BENCH_SENDERS,
// N42_BENCH_RECIPIENTS, N42_BENCH_FILLER, N42_BENCH_BLOCKS; the database
// goes under N42_BENCH_DIR (default the test's temp dir). Run it with the
// fleet's environment (N42_HISTORY_INDEX_DEFERRED=1 N42_TXINDEX_TAIL=1
// N42_QMDB_EVICT_LAG_BLOCKS=4 N42_WRITE_PROBE=1).

package internal

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/c2h5oh/datasize"
	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/frame"
	"github.com/n42blockchain/N42/internal/frame/frametest"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	"github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// importBenchEngine is the lag test engine under HotStuff's canonical rule:
// an import never moves the head (CommitToCanonical does, later and off the
// import), as on the fleet.
type importBenchEngine struct{ lagTestEngine }

func (importBenchEngine) Type() params.ConsensusType { return params.HotStuffConsensus }

type importBenchRow struct {
	n                        uint64
	ahead                    bool
	root, process, valid, wr time.Duration
	publish                  time.Duration
	wait                     time.Duration // the hand-off's wait for the previous write
	recov, exec              time.Duration
	ph                       ProcessPhases
	at                       AheadTimes
	cr                       commitment.ComputeRootPhases
	w                        blockWritePhases
}

// signImportBenchFrames signs one block's transfers (sender i%senders at its
// next nonce, a hash-derived hot recipient) and packs them into frames.
func signImportBenchFrames(t *testing.T, gw *ecdsa.PrivateKey, cfg *params.ChainConfig, senders []*ecdsa.PrivateKey,
	recipients []types.Address, nonces []uint64, blockIdx, nTxs, perFrame int, seq *uint64) (raws [][]byte, hashes []types.Hash, from [][]types.Address) {
	signer := transaction.LatestSignerForChainID(cfg.ChainID)
	chainID := uint256.MustFromBig(cfg.ChainID)
	enc := make([][]byte, nTxs)
	var wg sync.WaitGroup
	const workers = 4
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < nTxs; i += workers {
				s := i % len(senders)
				to := recipients[((blockIdx*nTxs+i)*7919)%len(recipients)]
				tx, err := transaction.SignNewTx(senders[s], signer, &transaction.DynamicFeeTx{
					ChainID: chainID, Nonce: nonces[s] + uint64(i/len(senders)),
					GasTipCap: uint256.NewInt(1), GasFeeCap: uint256.NewInt(1e10), Gas: 21000,
					To: &to, Value: uint256.NewInt(1 + uint64(i%1000)),
				})
				if err != nil {
					panic(err)
				}
				e, err := tx.EthEncoded()
				if err != nil {
					panic(err)
				}
				enc[i] = e
			}
		}(w)
	}
	wg.Wait()
	for i := 0; i < nTxs; i++ {
		nonces[i%len(senders)]++
	}
	for lo := 0; lo < nTxs; lo += perFrame {
		hi := min(lo+perFrame, nTxs)
		*seq++
		raw, h, err := frame.Encode(gw, *seq, frame.TxTypeEthereum, enc[lo:hi])
		require.NoError(t, err)
		raws = append(raws, raw)
		hashes = append(hashes, h)
		addrs := make([]types.Address, 0, hi-lo)
		for i := lo; i < hi; i++ {
			addrs = append(addrs, crypto.PubkeyToAddress(senders[i%len(senders)].PublicKey))
		}
		from = append(from, addrs)
	}
	return raws, hashes, from
}

func TestQMDBImportRootWriteBench(t *testing.T) {
	if os.Getenv("N42_BENCH_IMPORT") != "1" {
		t.Skip("N42_BENCH_IMPORT=1 runs the import benchmark")
	}
	nTxs := benchEnvInt("N42_BENCH_TXS", 163200)
	nSenders := benchEnvInt("N42_BENCH_SENDERS", 8000)
	nRecipients := benchEnvInt("N42_BENCH_RECIPIENTS", 22857)
	nFiller := benchEnvInt("N42_BENCH_FILLER", 200000)
	nBlocks := benchEnvInt("N42_BENCH_BLOCKS", 8)
	admitPct := benchEnvInt("N42_BENCH_ADMIT_SENDERS_PCT", 100)
	const perFrame = 160

	defer SetFrameBlocksForTest(true)()
	defer block.SetVoteOnAvailabilityForTest(true)()
	defer block.SetDeferredDepthForTest(2, 0)()
	defer commitment.SetQMDBStateReadModeForTest(commitment.QMDBReadOn)()
	defer SetLagPipelineForTest(true)()
	prevGas := lagChainGasLimit
	lagChainGasLimit = uint64(nTxs) * 21000 * 2
	defer func() { lagChainGasLimit = prevGas }()

	ctx := context.Background()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	dir := t.TempDir()
	if d := os.Getenv("N42_BENCH_DIR"); d != "" {
		dir = fmt.Sprintf("%s/import-%d", d, time.Now().UnixNano())
		require.NoError(t, os.MkdirAll(dir, 0o755))
		defer os.RemoveAll(dir)
	}
	// The node's chaindata options (internal/node OpenDatabase), durable.
	db, err := mdbx.NewMDBX(log.New()).WriteMergeThreshold(4 * 8192).Path(dir).Label(kv.ChainDB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).
		MapSize(64 * datasize.GB).Open(ctx)
	require.NoError(t, err)
	defer db.Close()

	cfg := &params.ChainConfig{
		ChainID: big.NewInt(1337), Consensus: params.Faker,
		HomesteadBlock: big.NewInt(0), TangerineWhistleBlock: big.NewInt(0), SpuriousDragonBlock: big.NewInt(0),
		ByzantiumBlock: big.NewInt(0), ConstantinopleBlock: big.NewInt(0), PetersburgBlock: big.NewInt(0),
		IstanbulBlock: big.NewInt(0), BerlinBlock: big.NewInt(0), LondonBlock: big.NewInt(0),
		StateScheme:           string(params.StateCommitmentPresetQMDB),
		DeferredExecutionTime: big.NewInt(1),
	}
	// N42_BENCH_PRAGUE=1 runs the fleet's fork set: the Prague system calls
	// at every block end (and their contracts in the genesis).
	prague := os.Getenv("N42_BENCH_PRAGUE") == "1"
	if prague {
		cfg.ShanghaiTime, cfg.CancunTime, cfg.PragueTime = big.NewInt(0), big.NewInt(0), big.NewInt(0)
	}
	keys := frametest.Keys(t, nSenders+1, 97)
	gw, senders := keys[0], keys[1:]
	recipients := make([]types.Address, nRecipients)
	for i := range recipients {
		recipients[i] = types.BytesToAddress(crypto.Keccak256([]byte("p4-bench-recipient"), []byte(strconv.Itoa(i))))
	}
	alloc := conf.GenesisAlloc{}
	for _, k := range senders {
		alloc[crypto.PubkeyToAddress(k.PublicKey)] = conf.GenesisAccount{Balance: "0x56bc75e2d63100000"}
	}
	for _, a := range recipients {
		alloc[a] = conf.GenesisAccount{Balance: "0x1"}
	}
	if prague {
		for _, d := range PragueSystemContractDeployments() {
			alloc[d.Address] = d.Account
		}
	}
	for i := 0; i < nFiller; i++ {
		a := types.BytesToAddress(crypto.Keccak256([]byte("p4-bench-filler"), []byte(strconv.Itoa(i))))
		alloc[a] = conf.GenesisAccount{Balance: "0x" + strconv.FormatUint(uint64(i+1), 16)}
	}
	genesis := &GenesisBlock{GenesisConfig: &conf.Genesis{Config: cfg, Alloc: alloc, GasLimit: lagChainGasLimit,
		Difficulty: uint256.NewInt(0), Timestamp: 1, BaseFee: uint256.NewInt(1),
		Coinbase: types.HexToAddress("0x1111111111111111111111111111111111111111")}}
	var gb *block.Block
	require.NoError(t, db.Update(ctx, func(tx kv.RwTx) error {
		b, _, err := genesis.Write(tx)
		gb = b
		return err
	}))
	prevDET := block.DeferredExecutionTime
	defer func() { block.DeferredExecutionTime = prevDET }()
	bci, err := NewBlockChain(ctx, gb, importBenchEngine{}, db, nil, cfg)
	require.NoError(t, err)
	bc := bci.(*BlockChain)
	rc := commitment.NewQMDBRootComputer()
	rtx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	rc.SetCold(rtx)
	require.NoError(t, rc.LoadFrom(rtx))
	rtx.Rollback()
	rc.SetCold(nil)
	rc.EnableUndoRecording()
	bc.SetQMDBRootComputer(rc)
	// N42_STATE_READ_QMDB=1 / N42_STATE_WRITE_QMDB_ONLY=1, as internal/node.
	modules.SetLatestAccountSource(commitment.NewQMDBLatestAccountSource(rc, db))
	modules.SetPlainAccountWriteSkipped(true)
	defer modules.SetPlainAccountWriteSkipped(false)
	defer modules.SetLatestAccountSource(nil)
	bc.SetParallelEVM(true)
	fstore := store.NewWithKeep(1<<30, 16)
	bc.SetFrameStore(fstore)

	// Sign every block up front (not measured).
	tSign := time.Now()
	type signed struct {
		raws   [][]byte
		hashes []types.Hash
		from   [][]types.Address
	}
	var seq uint64
	nonces := make([]uint64, len(senders))
	blocks := make([]signed, nBlocks)
	for i := range blocks {
		blocks[i].raws, blocks[i].hashes, blocks[i].from = signImportBenchFrames(t, gw, cfg, senders, recipients, nonces, i, nTxs, perFrame, &seq)
	}
	t.Logf("signed %d blocks of %d txs in %v", nBlocks, nTxs, time.Since(tSign))

	var mu sync.Mutex
	rows := map[uint64]*importBenchRow{}
	row := func(n uint64) *importBenchRow {
		r := rows[n]
		if r == nil {
			r = &importBenchRow{n: n}
			rows[n] = r
		}
		return r
	}
	blockWritePhasesHook = func(n uint64, ph blockWritePhases) {
		mu.Lock()
		row(n).w = ph
		mu.Unlock()
	}
	importPhasesHook = func(n uint64, ph ProcessPhases, process, validate, write time.Duration, ahead *AheadTimes) {
		mu.Lock()
		r := row(n)
		r.root, r.process, r.valid, r.wr, r.ahead = ph.Finalize, process, validate, write, ahead != nil
		r.ph = ph
		if ahead != nil {
			r.at = *ahead
		}
		r.recov, r.exec = ph.Recover, ph.Exec
		r.publish = time.Duration(lagPublishNanos.Swap(0))
		// This block's root: the next one has not started (the import
		// goroutine is still in this one).
		if cr := commitment.LastComputeRootPhases(); cr != nil {
			r.cr = *cr
		}
		if handed, _, wait := bc.LagWriteStats(); handed > 0 {
			r.wait = wait
		}
		mu.Unlock()
	}
	defer func() { blockWritePhasesHook, importPhasesHook = nil, nil }()

	waitLag := func(n uint64) {
		deadline := time.Now().Add(10 * time.Minute)
		for {
			if _, last, _ := bc.LagStats(); last >= n {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("lag executor never reached %d", n)
			}
			time.Sleep(time.Millisecond)
		}
	}

	importSigner := transaction.MakeSignerWithTimestamp(cfg, big.NewInt(1), gb.Header().(*block.Header).Time+10)
	parent := gb.Header().(*block.Header)
	var wireHashes []types.Hash
	tStart := time.Now()
	for i := range blocks {
		n := uint64(i + 1)
		// Senders recovered at admission, as the fleet's frame recoverer
		// does (N42_FRAME_RECOVER_WORKERS): the import only attaches them.
		// N42_BENCH_ADMIT_SENDERS_PCT (default 100) leaves the rest of the
		// frames unrecovered, as when admission recovery runs behind ingest
		// (S99: 8-14k frames pending).
		for j := range blocks[i].raws {
			require.True(t, fstore.Put(blocks[i].hashes[j], blocks[i].raws[j]))
			if j*100 < admitPct*len(blocks[i].raws) {
				require.True(t, fstore.SetSenders(blocks[i].hashes[j], &store.Senders{Signer: importSigner, Addrs: blocks[i].from[j]}))
			}
		}
		// The leader gate: block n carries the result of n-2.
		if n >= 3 {
			waitLag(n - 2)
		}
		hdr := &block.Header{ParentHash: parent.Hash(), Number: parent.Number.Clone(), GasLimit: parent.GasLimit,
			Time: parent.Time + 10, BaseFee: parent.BaseFee.Clone(), Difficulty: parentDifficulty(parent),
			Coinbase: types.HexToAddress("0x1111111111111111111111111111111111111111")}
		hdr.Number.AddUint64(hdr.Number, 1)
		if bf := misc.CalcBaseFee(cfg, parent); bf != nil {
			hdr.BaseFee.SetFromBig(bf)
		}
		rtx, err := db.BeginRo(ctx)
		require.NoError(t, err)
		res, _, err := bc.LaggedExecutedResult(rtx, hdr, nil)
		rtx.Rollback()
		require.NoError(t, err)
		hdr.Root, hdr.ReceiptHash, hdr.Bloom, hdr.GasUsed = res.Root, res.ReceiptHash, res.Bloom, res.GasUsed
		hdr.SetFrameTxRoot(block.FrameTxRoot(blocks[i].hashes))
		w := overWire(t, block.NewLaggedBlock(hdr, blocks[i].hashes, blocks[i].raws))
		checked, retry, err := bc.CheckDeferredBlock(w)
		require.True(t, checked)
		require.False(t, retry)
		require.NoError(t, err, "availability check of block %d", n)
		wireHashes = append(wireHashes, w.Hash())
		parent = w.Header().(*block.Header)
	}
	waitLag(uint64(nBlocks))
	wall := time.Since(tStart)
	for i, h := range wireHashes {
		res := (&lagChain{t: t, db: db}).result(h)
		require.Equal(t, uint64(nTxs)*21000, res.GasUsed, "block %d did not execute every transfer", i+1)
		// The roots depend on the transfers only (not on the signatures),
		// so two builds compare them run against run.
		t.Logf("block %d state root %x", i+1, res.Root)
	}

	ms := func(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
	var b strings.Builder
	// write is the block's write on whichever goroutine ran it (the ordered
	// writer's, N42_LAG_WRITE_ASYNC); hand is what the import itself spent
	// on it (all of it when synchronous; the wait for the previous write and
	// the capture of the QMDB rows when handed off), so the next block's root
	// waits root+hand: the critical path.
	fmt.Fprintf(&b, "\n%3s %5s %7s %7s %6s %6s | %6s %6s %5s %6s %6s %5s | %7s %6s %6s %6s %6s %6s %6s %6s %6s %6s %6s\n",
		"n", "ahead", "root", "write", "hand", "wait", "other", "publ", "lock", "ops", "apply", "fold",
		"begin", "rcpt", "block", "ce", "state", "chgSet", "chgHis", "qflush", "qmeta", "commit", "post")
	var keysN []uint64
	for n := range rows {
		keysN = append(keysN, n)
	}
	sort.Slice(keysN, func(i, j int) bool { return keysN[i] < keysN[j] })
	type agg struct{ v []float64 }
	cols := map[string]*agg{}
	add := func(k string, v float64) {
		if cols[k] == nil {
			cols[k] = &agg{}
		}
		cols[k].v = append(cols[k].v, v)
	}
	for _, n := range keysN {
		r := rows[n]
		cr := r.cr
		other := r.root - r.publish - cr.LockWait - cr.Collect - cr.Apply - cr.Fold
		fmt.Fprintf(&b, "%3d %5v %7.1f %7.1f %6.1f %6.1f | %6.1f %6.1f %5.1f %6.1f %6.1f %5.1f | %7.1f %6.1f %6.1f %6.1f %6.1f %6.1f %6.1f %6.1f %6.1f %6.1f %6.1f\n",
			n, r.ahead, ms(r.root), ms(r.w.Total), ms(r.wr), ms(r.wait), ms(other), ms(r.publish), ms(cr.LockWait), ms(cr.Collect), ms(cr.Apply), ms(cr.Fold),
			ms(r.w.Begin), ms(r.w.Receipts), ms(r.w.Block), ms(r.w.Evidence), ms(r.w.State), ms(r.w.ChgTruncate+r.w.ChgSets),
			ms(r.w.ChgHistory), ms(r.w.QMDBFlush), ms(r.w.QMDBMeta), ms(r.w.Commit), ms(r.w.Post))
		if n <= 2 {
			continue // warm-up
		}
		add("root", ms(r.root))
		add("recov", ms(r.recov))
		add("exec", ms(r.exec))
		add("write", ms(r.w.Total))
		add("handoff", ms(r.wr))
		add("handoff.wait", ms(r.wait))
		add("critical", ms(r.root+r.wr))
		add("root.other", ms(other))
		add("root.publish", ms(r.publish))
		add("root.lock", ms(cr.LockWait))
		add("root.ops", ms(cr.Collect))
		add("root.apply", ms(cr.Apply))
		add("root.fold", ms(cr.Fold))
		add("w.begin", ms(r.w.Begin))
		add("w.receipts", ms(r.w.Receipts))
		add("w.block", ms(r.w.Block))
		add("w.state", ms(r.w.State))
		add("w.chgsets", ms(r.w.ChgTruncate+r.w.ChgSets))
		add("w.history", ms(r.w.ChgHistory))
		add("w.qflush", ms(r.w.QMDBFlush))
		add("w.qmeta", ms(r.w.QMDBMeta))
		add("w.commit", ms(r.w.Commit))
		add("w.post", ms(r.w.Post))
		add("root+write", ms(r.root+r.w.Total))
		if r.ahead {
			// The ahead run (P10): its preamble, recovery, setup, wave,
			// collect (and its parts) and whatever the phases leave out.
			ph := r.ph
			add("a.run", ms(r.at.Run))
			add("a.wait", ms(r.at.Wait))
			add("a.pre", ms(r.at.Pre))
			add("a.setup", ms(ph.Setup))
			add("a.collect", ms(ph.Collect))
			add("a.c.rcpt", ms(ph.CollectReceipts))
			add("a.c.apply", ms(ph.CollectApply))
			add("a.c.pref", ms(ph.CollectPrefetch))
			add("a.c.fees", ms(ph.CollectFees))
			add("a.gap", ms(r.at.Run-r.at.Pre-ph.Recover-ph.Setup-ph.Exec-ph.Collect))
		}
	}
	names := make([]string, 0, len(cols))
	for k := range cols {
		names = append(names, k)
	}
	sort.Strings(names)
	fmt.Fprintf(&b, "medians over blocks 3..%d:\n", nBlocks)
	for _, k := range names {
		v := cols[k].v
		sort.Float64s(v)
		fmt.Fprintf(&b, "  %-14s %7.1f ms\n", k, v[len(v)/2])
	}
	launched, used, discarded, _ := bc.LagPipelineStats()
	fmt.Fprintf(&b, "ahead launched %d used %d discarded %d; wall %v for %d blocks\n", launched, used, discarded, wall, nBlocks)
	t.Log(b.String())
}
