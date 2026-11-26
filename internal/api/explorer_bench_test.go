package api

// explorer_bench_test.go measures the JSON-RPC read paths a block explorer
// (Blockscout) drives during catch-up, against a real executed chain whose
// blocks carry explorerTxsPerBlock transactions each (the profile-1 shape:
// ~1.2k transactions a block, a sixth of them contract calls emitting a
// Transfer-shaped LOG3).
//
// "cold" benchmarks open a fresh BlockChain over the same database for each
// iteration, so the block/receipt caches start empty: that is the catch-up
// case, where the explorer reads blocks no one has read before.

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/crypto"
	internalcore "github.com/n42blockchain/N42/internal"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

const (
	explorerTxsPerBlock = 1200
	explorerBlocks      = 3
	explorerCallEvery   = 6 // every 6th tx calls the log emitter
)

type explorerFixture struct {
	DB      kv.RwDB
	Chain   *internalcore.BlockChain
	Config  *params.ChainConfig
	Genesis *block.Block
	Engine  apiXFakerEngine
	Blocks  []*block.Block
	Senders []types.Address
}

var (
	explorerFixtureOnce  sync.Once
	explorerFixtureValue *explorerFixture
)

// explorerEmitterRuntime emits LOG3(mem[0:32], topic, CALLER, ADDRESS):
//
//	30        ADDRESS
//	33        CALLER
//	7f <32B>  PUSH32 topic (keccak("Transfer(address,address,uint256)"))
//	60 20     PUSH1 0x20
//	60 00     PUSH1 0x00
//	a3        LOG3
//	00        STOP
func explorerEmitterRuntime() []byte {
	topic := crypto.Keccak256([]byte("Transfer(address,address,uint256)"))
	code := []byte{0x30, 0x33, 0x7f}
	code = append(code, topic...)
	return append(code, 0x60, 0x20, 0x60, 0x00, 0xa3, 0x00)
}

func explorerGetFixture(tb testing.TB) *explorerFixture {
	tb.Helper()
	explorerFixtureOnce.Do(func() { explorerFixtureValue = explorerBuildFixture(tb) })
	if explorerFixtureValue == nil {
		tb.Fatal("explorer fixture failed to build")
	}
	return explorerFixtureValue
}

func explorerBuildFixture(tb testing.TB) *explorerFixture {
	tb.Helper()
	ctx := context.Background()
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	dir, err := os.MkdirTemp("", "explorer-bench-fixture-")
	require.NoError(tb, err)
	db := memdb.New(dir)

	cfg := &params.ChainConfig{
		ChainID:               big.NewInt(1337),
		Consensus:             params.Faker,
		HomesteadBlock:        big.NewInt(0),
		TangerineWhistleBlock: big.NewInt(0),
		SpuriousDragonBlock:   big.NewInt(0),
		ByzantiumBlock:        big.NewInt(0),
		ConstantinopleBlock:   big.NewInt(0),
		PetersburgBlock:       big.NewInt(0),
		IstanbulBlock:         big.NewInt(0),
		BerlinBlock:           big.NewInt(0),
		LondonBlock:           big.NewInt(0),
	}

	keys := make([]*ecdsa.PrivateKey, explorerTxsPerBlock)
	addrs := make([]types.Address, explorerTxsPerBlock)
	alloc := conf.GenesisAlloc{}
	for i := range keys {
		k, err := crypto.GenerateKey()
		require.NoError(tb, err)
		keys[i] = k
		addrs[i] = crypto.PubkeyToAddress(k.PublicKey)
		alloc[addrs[i]] = conf.GenesisAccount{Balance: "0x56bc75e2d63100000"}
	}
	emitter := types.HexToAddress("0x00000000000000000000000000000000000e0e0e")
	alloc[emitter] = conf.GenesisAccount{Balance: "0x0", Code: explorerEmitterRuntime()}

	coinbase := types.HexToAddress("0x1111111111111111111111111111111111111111")
	genesis := &internalcore.GenesisBlock{GenesisConfig: &conf.Genesis{
		Config: cfg, Alloc: alloc, GasLimit: 500_000_000,
		Difficulty: uint256.NewInt(0), Timestamp: 1, BaseFee: uint256.NewInt(7), Coinbase: coinbase,
	}}
	var genesisBlock *block.Block
	require.NoError(tb, db.Update(ctx, func(tx kv.RwTx) error {
		blk, _, werr := genesis.Write(tx)
		genesisBlock = blk
		return werr
	}))

	engine := apiXFakerEngine{}
	bcIface, err := internalcore.NewBlockChain(ctx, genesisBlock, engine, db, nil, cfg)
	require.NoError(tb, err)
	bc := bcIface.(*internalcore.BlockChain)
	signer := transaction.NewLondonSigner(cfg.ChainID)

	fx := &explorerFixture{DB: db, Chain: bc, Config: cfg, Genesis: genesisBlock, Engine: engine, Senders: addrs}
	for b := 0; b < explorerBlocks; b++ {
		txs := make([]*transaction.Transaction, explorerTxsPerBlock)
		for i := range txs {
			to := addrs[(i+1)%len(addrs)]
			value := uint256.NewInt(1000)
			if i%explorerCallEvery == 0 {
				to = emitter
				value = uint256.NewInt(0)
			}
			inner := &transaction.DynamicFeeTx{
				ChainID: uint256.MustFromBig(cfg.ChainID), Nonce: uint64(b),
				GasTipCap: uint256.NewInt(2), GasFeeCap: uint256.NewInt(20),
				Gas: 100_000, To: &to, Value: value,
			}
			signed, serr := transaction.SignTx(transaction.NewTx(inner), signer, keys[i])
			require.NoError(tb, serr)
			txs[i] = signed
		}
		fx.Blocks = append(fx.Blocks, explorerInsert(tb, fx, txs, coinbase))
	}
	return fx
}

func explorerInsert(tb testing.TB, fx *explorerFixture, txs []*transaction.Transaction, coinbase types.Address) *block.Block {
	ctx := context.Background()
	bc, cfg, engine := fx.Chain, fx.Config, fx.Engine
	parent := bc.CurrentBlock()
	ph := parent.Header().(*block.Header)
	header := &block.Header{
		ParentHash: parent.Hash(),
		Number:     new(uint256.Int).AddUint64(ph.Number, 1),
		GasLimit:   ph.GasLimit, Time: ph.Time + 10, Coinbase: coinbase,
		BaseFee: uint256.NewInt(7), Difficulty: uint256.NewInt(1),
	}
	require.NoError(tb, engine.Prepare(bc, header))
	var (
		receipts block.Receipts
		usedGas  uint64
		ibs      *state.IntraBlockState
	)
	require.NoError(tb, fx.DB.View(ctx, func(tx kv.Tx) error {
		ibs = state.New(state.NewPlainStateReader(tx))
		gp := new(common.GasPool)
		gp.AddGas(header.GasLimit)
		for i, txn := range txs {
			ibs.Prepare(txn.Hash(), types.Hash{}, i)
			r, _, aerr := internalcore.ApplyTransaction(cfg, nil, engine, nil, gp, ibs, state.NewNoopWriter(), header, txn, &usedGas, vm2.Config{})
			if aerr != nil {
				return aerr
			}
			receipts = append(receipts, r)
		}
		return nil
	}))
	header.GasUsed = usedGas
	header.Root = ibs.IntermediateRoot()
	header.ReceiptHash = hash.DeriveSha(receipts)
	header.Bloom = block.CreateBloom(receipts)
	built, _, _, err := engine.FinalizeAndAssemble(bc, header, ibs, txs, nil, receipts)
	require.NoError(tb, err)
	n, err := bc.InsertChain([]block.IBlock{built})
	require.NoError(tb, err)
	require.Equal(tb, 1, n)
	return built.(*block.Block)
}

// explorerColdAPI returns an API over a fresh BlockChain (empty caches) on
// the fixture database.
func explorerColdAPI(tb testing.TB, fx *explorerFixture) *API {
	bcIface, err := internalcore.NewBlockChain(context.Background(), fx.Genesis, fx.Engine, fx.DB, nil, fx.Config)
	require.NoError(tb, err)
	return &API{db: fx.DB, bc: bcIface, engine: fx.Engine, chainConfig: fx.Config, txspool: &mockEngineTxPool{}}
}

func explorerWarmAPI(fx *explorerFixture) *API {
	return &API{db: fx.DB, bc: fx.Chain, engine: fx.Engine, chainConfig: fx.Config, txspool: &mockEngineTxPool{}}
}

func explorerBlockNr(fx *explorerFixture, i int) jsonrpc.BlockNumber {
	return jsonrpc.BlockNumber(fx.Blocks[i].Number64().Uint64())
}

func explorerJSONSize(tb testing.TB, v interface{}) int {
	enc, err := json.Marshal(v)
	require.NoError(tb, err)
	return len(enc)
}

// BenchmarkExplorerBlockFullCold: eth_getBlockByNumber(n, true) on a block
// nobody has read yet (catch-up), including JSON encoding.
func BenchmarkExplorerBlockFullCold(b *testing.B) {
	fx := explorerGetFixture(b)
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		bapi := NewBlockChainAPI(explorerColdAPI(b, fx))
		b.StartTimer()
		res, err := bapi.GetBlockByNumber(context.Background(), explorerBlockNr(fx, 1), true)
		require.NoError(b, err)
		explorerJSONSize(b, res)
	}
}

// BenchmarkExplorerBlockReceiptsCold: eth_getBlockReceipts after the block
// was fetched once (Blockscout reads the block, then its receipts).
func BenchmarkExplorerBlockReceiptsCold(b *testing.B) {
	fx := explorerGetFixture(b)
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		bapi := NewBlockChainAPI(explorerColdAPI(b, fx))
		_, err := bapi.GetBlockByNumber(context.Background(), explorerBlockNr(fx, 1), true)
		require.NoError(b, err)
		b.StartTimer()
		res, err := bapi.GetBlockReceipts(context.Background(), jsonrpc.BlockNumberOrHashWithNumber(explorerBlockNr(fx, 1)))
		require.NoError(b, err)
		require.Len(b, res, explorerTxsPerBlock)
		explorerJSONSize(b, res)
	}
}

// BenchmarkExplorerTxReceiptsPerBlock: Blockscout's default receipt path,
// eth_getTransactionReceipt for every transaction of one block (after the
// block was fetched once). One iteration = one whole block.
func BenchmarkExplorerTxReceiptsPerBlock(b *testing.B) {
	fx := explorerGetFixture(b)
	blk := fx.Blocks[1]
	hashes := make([]types.Hash, 0, len(blk.Transactions()))
	for _, tx := range blk.Transactions() {
		hashes = append(hashes, tx.Hash())
	}
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		a := explorerColdAPI(b, fx)
		_, err := NewBlockChainAPI(a).GetBlockByNumber(context.Background(), explorerBlockNr(fx, 1), true)
		require.NoError(b, err)
		txAPI := NewTransactionAPI(a, new(AddrLocker))
		b.StartTimer()
		for _, h := range hashes {
			res, err := txAPI.GetTransactionReceipt(context.Background(), avmtypes.FromastHash(h))
			require.NoError(b, err)
			require.NotNil(b, res)
			explorerJSONSize(b, res)
		}
	}
}

// BenchmarkExplorerBalanceAtHeight: eth_getBalance(addr, n) for the block's
// touched addresses (coin-balance fetcher). One iteration = one address.
func BenchmarkExplorerBalanceAtHeight(b *testing.B) {
	fx := explorerGetFixture(b)
	bapi := NewBlockChainAPI(explorerWarmAPI(fx))
	at := jsonrpc.BlockNumberOrHashWithNumber(explorerBlockNr(fx, 1))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		addr := fx.Senders[i%len(fx.Senders)]
		_, err := bapi.GetBalance(context.Background(), *avmtypes.FromastAddress(&addr), at)
		require.NoError(b, err)
	}
}

// TestExplorerTxReceiptMatchesBlockReceipts: the per-transaction receipt
// (served from the cached block and receipts) carries the same JSON as the
// block-receipts entry for every transaction of a heavy block.
func TestExplorerTxReceiptMatchesBlockReceipts(t *testing.T) {
	fx := explorerGetFixture(t)
	a := explorerColdAPI(t, fx)
	bapi := NewBlockChainAPI(a)
	txAPI := NewTransactionAPI(a, new(AddrLocker))
	all, err := bapi.GetBlockReceipts(context.Background(), jsonrpc.BlockNumberOrHashWithNumber(explorerBlockNr(fx, 2)))
	require.NoError(t, err)
	require.Len(t, all, explorerTxsPerBlock)
	for i, tx := range fx.Blocks[2].Transactions() {
		one, err := txAPI.GetTransactionReceipt(context.Background(), avmtypes.FromastHash(tx.Hash()))
		require.NoError(t, err)
		var got, want map[string]interface{}
		enc, _ := json.Marshal(one)
		require.NoError(t, json.Unmarshal(enc, &got))
		enc, _ = json.Marshal(all[i])
		require.NoError(t, json.Unmarshal(enc, &want))
		require.Equal(t, want, got, "tx %d", i)
		if i%explorerCallEvery == 0 {
			require.Len(t, got["logs"], 1)
		}
	}
}

// TestExplorerCachedReceiptsRespectCanonical: a cached receipts entry is not
// served for a hash that no longer owns its height.
func TestExplorerCachedReceiptsRespectCanonical(t *testing.T) {
	fx := explorerGetFixture(t)
	a := explorerColdAPI(t, fx)
	blk := fx.Blocks[0]
	num := blk.Number64().Uint64()
	r, err := a.BlockChain().GetReceipts(blk.Hash())
	require.NoError(t, err)
	require.Len(t, r, explorerTxsPerBlock)

	setCanonical := func(h types.Hash) {
		require.NoError(t, fx.DB.Update(context.Background(), func(tx kv.RwTx) error {
			return rawdb.WriteCanonicalHash(tx, h, num)
		}))
	}
	setCanonical(types.Hash{0xff})
	defer setCanonical(blk.Hash())
	r, err = a.BlockChain().GetReceipts(blk.Hash())
	require.NoError(t, err)
	require.Nil(t, r)
}

// TestExplorerFullBlockTxsMatchTxByHash: every transaction object of
// eth_getBlockByNumber(n, true) equals eth_getTransactionByHash for it, as on
// geth (same block hash, index and effective gasPrice from the header base
// fee).
func TestExplorerFullBlockTxsMatchTxByHash(t *testing.T) {
	fx := explorerGetFixture(t)
	a := explorerColdAPI(t, fx)
	res, err := NewBlockChainAPI(a).GetBlockByNumber(context.Background(), explorerBlockNr(fx, 0), true)
	require.NoError(t, err)
	txs, ok := res["transactions"].([]interface{})
	require.True(t, ok)
	require.Len(t, txs, explorerTxsPerBlock)
	txAPI := NewTransactionAPI(a, new(AddrLocker))
	for i, tx := range fx.Blocks[0].Transactions() {
		one, err := txAPI.GetTransactionByHash(context.Background(), avmtypes.FromastHash(tx.Hash()))
		require.NoError(t, err)
		var want, got map[string]interface{}
		enc, _ := json.Marshal(one)
		require.NoError(t, json.Unmarshal(enc, &want))
		enc, _ = json.Marshal(txs[i])
		require.NoError(t, json.Unmarshal(enc, &got))
		// The full block reports its own "hash" as every tx's blockHash; on
		// this Faker chain eth_getTransactionByHash reports the native hash
		// (they coincide on HotStuff/APoS chains).
		blockHash, _ := json.Marshal(res["hash"])
		require.Equal(t, strings.Trim(string(blockHash), `"`), got["blockHash"])
		delete(want, "blockHash")
		delete(got, "blockHash")
		require.Equal(t, want, got, "tx %d", i)
	}
	// base fee 7 + tip 2 = 9, under the fee cap of 20.
	first, _ := json.Marshal(txs[0])
	require.Contains(t, string(first), `"gasPrice":"0x9"`)
}

// BenchmarkExplorerHTTPBlockBatch: one Blockscout catch-up request (a batch
// of eth_getBlockByNumber(n, true) for every fixture block) through the real
// JSON-RPC HTTP server, block cache warm. One iteration = one batch.
func BenchmarkExplorerHTTPBlockBatch(b *testing.B) {
	fx := explorerGetFixture(b)
	srv := jsonrpc.NewServer()
	require.NoError(b, srv.RegisterName("eth", NewBlockChainAPI(explorerWarmAPI(fx))))
	var reqs []string
	for i := range fx.Blocks {
		reqs = append(reqs, fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"eth_getBlockByNumber","params":["0x%x",true]}`, i, fx.Blocks[i].Number64().Uint64()))
	}
	body := "[" + strings.Join(reqs, ",") + "]"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		require.Equal(b, http.StatusOK, w.Code)
		b.SetBytes(int64(w.Body.Len()))
	}
}

// TestExplorerConcurrentReceiptsOnColdBlock: concurrent per-transaction
// receipt calls on a block nobody has read all succeed (race detector run).
func TestExplorerConcurrentReceiptsOnColdBlock(t *testing.T) {
	fx := explorerGetFixture(t)
	a := explorerColdAPI(t, fx)
	txAPI := NewTransactionAPI(a, new(AddrLocker))
	txs := fx.Blocks[1].Transactions()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := w; i < len(txs); i += 97 {
				res, err := txAPI.GetTransactionReceipt(context.Background(), avmtypes.FromastHash(txs[i].Hash()))
				if err != nil || res == nil || res["transactionIndex"] != hexutil.Uint64(i) {
					errs <- fmt.Errorf("tx %d: %v %v", i, err, res["transactionIndex"])
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}
