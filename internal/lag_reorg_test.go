// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"context"
	"crypto/ecdsa"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// lagTestEngine is the coreT faker with the root step every engine's
// Finalize runs (the deferred write stores the root it leaves behind).
type lagTestEngine struct{ coreTFakerEngine }

func (lagTestEngine) Finalize(_ consensus.ChainHeaderReader, _ block.IHeader, ibs *state.IntraBlockState, _ []*transaction.Transaction, _ []block.IHeader) ([]*block.Reward, map[types.Address]*uint256.Int, error) {
	ibs.IntermediateRoot()
	return nil, nil, nil
}

// lagChain is a native QMDB chain under deferred execution from genesis,
// built and imported at depth 2: every header carries the executed result
// of the block two below it, and the builder never executes (it reads the
// stored results, as a lag-mode leader does).
type lagChain struct {
	t      *testing.T
	db     kv.RwDB
	bc     *BlockChain
	cfg    *params.ChainConfig
	keys   []*ecdsa.PrivateKey
	addrs  []types.Address
	signer transaction.Signer
}

func newLagChain(t *testing.T) *lagChain {
	t.Helper()
	return newLagChainKeys(t, nil)
}

// newLagChainKeys funds keys (three fresh ones when nil) at genesis; two
// chains built from the same keys share their genesis.
func newLagChainKeys(t *testing.T, keys []*ecdsa.PrivateKey) *lagChain {
	t.Helper()
	return newLagChainEngine(t, keys, lagTestEngine{})
}

// newLagChainEngine is newLagChainKeys under engine (the HotStuff-ruled
// importBenchEngine runs the commit-only canonical rule the ordered writer
// needs).
func newLagChainEngine(t *testing.T, keys []*ecdsa.PrivateKey, engine consensus.Engine) *lagChain {
	t.Helper()
	ctx := context.Background()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db := memdb.New(t.TempDir())
	cfg := &params.ChainConfig{
		ChainID: big.NewInt(1337), Consensus: params.Faker,
		HomesteadBlock: big.NewInt(0), TangerineWhistleBlock: big.NewInt(0), SpuriousDragonBlock: big.NewInt(0),
		ByzantiumBlock: big.NewInt(0), ConstantinopleBlock: big.NewInt(0), PetersburgBlock: big.NewInt(0),
		IstanbulBlock: big.NewInt(0), BerlinBlock: big.NewInt(0), LondonBlock: big.NewInt(0),
		StateScheme:           string(params.StateCommitmentPresetQMDB),
		DeferredExecutionTime: big.NewInt(1),
	}
	lc := &lagChain{t: t, db: db, cfg: cfg, signer: transaction.NewLondonSigner(cfg.ChainID)}
	alloc := conf.GenesisAlloc{}
	if keys == nil {
		for i := 0; i < 3; i++ {
			k, err := crypto.GenerateKey()
			require.NoError(t, err)
			keys = append(keys, k)
		}
	}
	for i, k := range keys {
		lc.keys = append(lc.keys, k)
		lc.addrs = append(lc.addrs, crypto.PubkeyToAddress(k.PublicKey))
		alloc[lc.addrs[i]] = conf.GenesisAccount{Balance: "0x56bc75e2d63100000"}
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
	prev := block.DeferredExecutionTime
	t.Cleanup(func() { block.DeferredExecutionTime = prev })
	bci, err := NewBlockChain(ctx, gb, engine, db, nil, cfg)
	require.NoError(t, err)
	lc.bc = bci.(*BlockChain)
	rc := commitment.NewQMDBRootComputer()
	rtx, err := db.BeginRo(ctx)
	require.NoError(t, err)
	rc.SetCold(rtx)
	require.NoError(t, rc.LoadFrom(rtx))
	rtx.Rollback()
	rc.SetCold(nil)
	rc.EnableUndoRecording()
	lc.bc.SetQMDBRootComputer(rc)
	return lc
}

func (lc *lagChain) transfer(from int, nonce uint64, to types.Address, value uint64) *transaction.Transaction {
	tx, err := transaction.SignTx(transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID: uint256.MustFromBig(lc.cfg.ChainID), Nonce: nonce, GasTipCap: uint256.NewInt(1),
		GasFeeCap: uint256.NewInt(2), Gas: 21000, To: &to, Value: uint256.NewInt(value),
	}), lc.signer, lc.keys[from])
	require.NoError(lc.t, err)
	return tx
}

// build assembles a child of parent without executing it: the header's
// execution fields are this node's stored result of the block two below
// (LaggedExecutedResult), exactly what the lag-mode leader stamps.
func (lc *lagChain) build(parent *block.Header, extra byte, txs ...*transaction.Transaction) *block.Block {
	hdr := &block.Header{ParentHash: parent.Hash(), Number: new(uint256.Int).AddUint64(parent.Number, 1),
		GasLimit: parent.GasLimit, Time: parent.Time + 10, BaseFee: uint256.NewInt(1), Difficulty: uint256.NewInt(1),
		Coinbase: types.HexToAddress("0x1111111111111111111111111111111111111111"), Extra: []byte{extra}}
	tx, err := lc.db.BeginRo(context.Background())
	require.NoError(lc.t, err)
	defer tx.Rollback()
	res, _, err := lc.bc.LaggedExecutedResult(tx, hdr, nil)
	require.NoError(lc.t, err)
	hdr.Root, hdr.ReceiptHash, hdr.Bloom, hdr.GasUsed = res.Root, res.ReceiptHash, res.Bloom, res.GasUsed
	b := block.NewBlockFromReceipt(hdr, txs, nil, nil, nil).(*block.Block)
	lc.bc.RememberAvailable(b) // a lag-mode leader builds on it before it executes
	return b
}

func (lc *lagChain) insert(blks ...*block.Block) {
	lc.t.Helper()
	for _, b := range blks {
		_, err := lc.bc.InsertChainAuthorized([]block.IBlock{b})
		require.NoError(lc.t, err, "insert %d", b.Number64().Uint64())
		require.True(lc.t, lc.bc.blockStored(b.Hash(), b.Number64().Uint64()), "block %d not stored", b.Number64().Uint64())
	}
}

func (lc *lagChain) balance(addr types.Address) *uint256.Int {
	var bal *uint256.Int
	require.NoError(lc.t, lc.db.View(context.Background(), func(tx kv.Tx) error {
		ibs := state.New(state.NewPlainStateReader(tx))
		bal = ibs.GetBalance(addr).Clone()
		return nil
	}))
	return bal
}

func (lc *lagChain) result(h types.Hash) rawdb.ExecutedResult {
	var r rawdb.ExecutedResult
	require.NoError(lc.t, lc.db.View(context.Background(), func(tx kv.Tx) error {
		var ok bool
		var err error
		r, ok, err = rawdb.ReadExecutedResult(tx, h)
		require.True(lc.t, ok, "no executed result for %x", h[:4])
		return err
	}))
	return r
}

// TestLagTwoChainReorgRollsBackExecution (P2a item c): a depth-2 chain A
// (1..4) is imported, then a fork B from block 2 (B3..B5) with different
// transfers. After the switch the state holds B's transfers and none of
// A3/A4's, B5's header (carrying B3's result) was accepted, so B3 executed
// on block 2's post-state, and a header carrying A3's result instead is
// refused as a lagged equivocation.
func TestLagTwoChainReorgRollsBackExecution(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	lc := newLagChain(t)
	g := lc.bc.genesisBlock.Header().(*block.Header)
	a0, a1, a2 := lc.addrs[0], lc.addrs[1], lc.addrs[2]

	b1 := lc.build(g, 0, lc.transfer(0, 0, a1, 100))
	lc.insert(b1)
	b2 := lc.build(b1.Header().(*block.Header), 0, lc.transfer(0, 1, a1, 100))
	lc.insert(b2)
	a3 := lc.build(b2.Header().(*block.Header), 0xA, lc.transfer(0, 2, a1, 1000))
	lc.insert(a3)
	a4 := lc.build(a3.Header().(*block.Header), 0xA, lc.transfer(0, 3, a1, 1000))
	lc.insert(a4)
	// Header 4 carried block 2's result; block 3 carries block 1's.
	require.Equal(t, lc.result(b2.Hash()).Root, a4.Header().(*block.Header).Root)
	require.Equal(t, lc.result(b1.Hash()).Root, a3.Header().(*block.Header).Root)
	bal1A := lc.balance(a1)

	// Fork from block 2: transfers to a2 instead.
	fb3 := lc.build(b2.Header().(*block.Header), 0xB, lc.transfer(0, 2, a2, 7))
	fb4 := lc.build(fb3.Header().(*block.Header), 0xB, lc.transfer(0, 3, a2, 7))
	lc.insert(fb3, fb4)
	fb5 := lc.build(fb4.Header().(*block.Header), 0xB, lc.transfer(0, 4, a2, 7))
	lc.insert(fb5)

	require.NotEqual(t, lc.result(a3.Hash()).Root, lc.result(fb3.Hash()).Root, "the forks must execute differently")
	require.Equal(t, lc.result(fb3.Hash()).Root, fb5.Header().(*block.Header).Root)
	// State: B's three transfers to a2, A's two transfers to a1 rolled back.
	got := new(uint256.Int).Sub(lc.balance(a2), lc.balanceAtGenesis())
	require.Equal(t, uint64(21), got.Uint64())
	require.True(t, lc.balance(a1).Lt(bal1A), "A3/A4's transfers to a1 must be rolled back")
	_ = a0

	// A header on B5 that reports A3 (the other fork's block 3) instead of
	// B4's two-below result is a lagged equivocation and is refused.
	bad := lc.build(fb5.Header().(*block.Header), 0xC)
	badHdr := bad.Header().(*block.Header)
	ar := lc.result(a3.Hash())
	badHdr.Root, badHdr.ReceiptHash, badHdr.Bloom, badHdr.GasUsed = ar.Root, ar.ReceiptHash, ar.Bloom, ar.GasUsed
	badBlk := block.NewBlockFromReceipt(badHdr, nil, nil, nil, nil)
	before := LaggedEquivocations()
	_, err := lc.bc.InsertChainAuthorized([]block.IBlock{badBlk})
	require.Error(t, err)
	require.Greater(t, LaggedEquivocations(), before)
}

func (lc *lagChain) balanceAtGenesis() *uint256.Int {
	v, _ := uint256.FromHex("0x56bc75e2d63100000")
	return v
}

// TestLagExecutorImportsInParentOrder: available blocks handed to the lag
// executor out of order (as pushes and own seals can arrive) are imported in
// parent order on its goroutine, each import is reported to consensus
// through the hook, and the executed results lag the queue by nothing once
// it drains.
func TestLagExecutorImportsInParentOrder(t *testing.T) {
	defer block.SetDeferredDepthForTest(2, 0)()
	lc := newLagChain(t)
	g := lc.bc.genesisBlock.Header().(*block.Header)
	a1 := lc.addrs[1]
	b1 := lc.build(g, 0, lc.transfer(0, 0, a1, 1))
	b2 := lc.build(b1.Header().(*block.Header), 0, lc.transfer(0, 1, a1, 1))

	var mu sync.Mutex
	var order []types.Hash
	lc.bc.SetLagImportedHook(func(h, _ types.Hash) {
		mu.Lock()
		order = append(order, h)
		mu.Unlock()
	})
	waitImported := func(want int) {
		deadline := time.Now().Add(10 * time.Second)
		for {
			mu.Lock()
			n := len(order)
			mu.Unlock()
			if n >= want {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("lag executor imported %d of %d", n, want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	// Out of order: the child first.
	lc.bc.EnqueueLagImport(b2)
	lc.bc.EnqueueLagImport(b1)
	waitImported(2)
	// Block 3 can be built only now: its header reports block 1.
	b3 := lc.build(b2.Header().(*block.Header), 0, lc.transfer(0, 2, a1, 1))
	lc.bc.EnqueueLagImport(b3)
	waitImported(3)
	require.Equal(t, []types.Hash{b1.Hash(), b2.Hash(), b3.Hash()}, order)
	imported, last, queued := lc.bc.LagStats()
	require.Equal(t, uint64(3), imported)
	require.Equal(t, uint64(3), last)
	require.Equal(t, 0, queued)
	require.False(t, lc.bc.LagImportQueued(b3.Hash()))
	require.Equal(t, lc.result(b1.Hash()).Root, b3.Header().(*block.Header).Root)
}
