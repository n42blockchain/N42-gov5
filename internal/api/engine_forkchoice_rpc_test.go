package api

import (
	"context"
	"math/big"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/n42blockchain/N42/params"
)

type preciseChainStub struct {
	current        *block.Block
	config         *params.ChainConfig
	db             kv.RwDB
	blocksByHash   map[types.Hash]*block.Block
	blocksByNumber map[uint64]*block.Block
	receiptsByHash map[types.Hash]block.Receipts
}

type persistedForkchoiceChainStub struct {
	*preciseChainStub
	tags map[jsonrpc.BlockNumber]block.IBlock
}

func (m *persistedForkchoiceChainStub) ForkchoiceTaggedBlock(number jsonrpc.BlockNumber) block.IBlock {
	return m.tags[number]
}

func newPreciseChainStub(cfg *params.ChainConfig, db kv.RwDB, blocks ...*block.Block) *preciseChainStub {
	stub := &preciseChainStub{
		config:         cfg,
		db:             db,
		blocksByHash:   make(map[types.Hash]*block.Block),
		blocksByNumber: make(map[uint64]*block.Block),
		receiptsByHash: make(map[types.Hash]block.Receipts),
	}
	for _, blk := range blocks {
		if blk == nil {
			continue
		}
		stub.current = blk
		stub.blocksByNumber[blk.Number64().Uint64()] = blk
		engineHash := ethCompatibleBlockHash(blk, cfg)
		stub.blocksByHash[engineHash] = blk
		stub.blocksByHash[blk.Hash()] = blk
	}
	return stub
}

func (m *preciseChainStub) Config() *params.ChainConfig { return m.config }
func (m *preciseChainStub) CurrentBlock() block.IBlock  { return m.current }
func (m *preciseChainStub) GetHeader(hash types.Hash, number *uint256.Int) block.IHeader {
	blk := m.blocksByHash[hash]
	if blk == nil || number == nil || blk.Number64().Uint64() != number.Uint64() {
		return nil
	}
	return blk.Header()
}
func (m *preciseChainStub) GetHeaderByNumber(number *uint256.Int) block.IHeader {
	if number == nil {
		return nil
	}
	blk := m.blocksByNumber[number.Uint64()]
	if blk == nil {
		return nil
	}
	return blk.Header()
}
func (m *preciseChainStub) GetHeaderByHash(hash types.Hash) (block.IHeader, error) {
	blk := m.blocksByHash[hash]
	if blk == nil {
		return nil, nil
	}
	return blk.Header(), nil
}
func (m *preciseChainStub) GetTd(types.Hash, *uint256.Int) *uint256.Int { return nil }
func (m *preciseChainStub) GetBlockByNumber(number *uint256.Int) (block.IBlock, error) {
	if number == nil {
		return nil, nil
	}
	blk := m.blocksByNumber[number.Uint64()]
	if blk == nil {
		return nil, nil
	}
	return blk, nil
}
func (m *preciseChainStub) GetDepositInfo(types.Address) (*uint256.Int, *uint256.Int) {
	return nil, nil
}
func (m *preciseChainStub) GetAccountRewardUnpaid(types.Address) (*uint256.Int, error) {
	return nil, nil
}
func (m *preciseChainStub) InsertHeader([]block.IHeader) (int, error) { return 0, nil }
func (m *preciseChainStub) GetBlockByHash(h types.Hash) (block.IBlock, error) {
	blk := m.blocksByHash[h]
	if blk == nil {
		return nil, nil
	}
	return blk, nil
}
func (m *preciseChainStub) Blocks() []block.IBlock                           { return nil }
func (m *preciseChainStub) Start() error                                     { return nil }
func (m *preciseChainStub) GenesisBlock() block.IBlock                       { return nil }
func (m *preciseChainStub) NewBlockHandler([]byte, peer.ID) error            { return nil }
func (m *preciseChainStub) InsertChain([]block.IBlock) (int, error)          { return 0, nil }
func (m *preciseChainStub) InsertBlock([]block.IBlock, bool) (int, error)    { return 0, nil }
func (m *preciseChainStub) SetEngine(interface{})                            {}
func (m *preciseChainStub) GetBlocksFromHash(types.Hash, int) []block.IBlock { return nil }
func (m *preciseChainStub) SealedBlock(block.IBlock) error                   { return nil }
func (m *preciseChainStub) Engine() interface{}                              { return nil }
func (m *preciseChainStub) GetReceipts(hash types.Hash) (block.Receipts, error) {
	return m.receiptsByHash[hash], nil
}
func (m *preciseChainStub) GetLogs(types.Hash) ([][]*block.Log, error) { return nil, nil }
func (m *preciseChainStub) SetHead(uint64) error                       { return nil }
func (m *preciseChainStub) AddFutureBlock(block.IBlock) error          { return nil }
func (m *preciseChainStub) GetBlock(hash types.Hash, number uint64) block.IBlock {
	blk := m.blocksByHash[hash]
	if blk == nil || blk.Number64().Uint64() != number {
		return nil
	}
	return blk
}
func (m *preciseChainStub) StateAt(kv.Tx, uint64) interface{} { return nil }
func (m *preciseChainStub) HasBlock(hash types.Hash, number uint64) bool {
	blk := m.blocksByHash[hash]
	return blk != nil && blk.Number64().Uint64() == number
}
func (m *preciseChainStub) DB() kv.RwDB           { return m.db }
func (m *preciseChainStub) Quit() <-chan struct{} { return nil }
func (m *preciseChainStub) EarliestBlock() uint64 { return 0 }
func (m *preciseChainStub) Close() error          { return nil }
func (m *preciseChainStub) WriteBlockWithState(block.IBlock, []*block.Receipt, interface{}, map[types.Address]*uint256.Int) error {
	return nil
}

func makeTestBlock(parentHash types.Hash, number, timestamp uint64) *block.Block {
	header := &block.Header{
		ParentHash: parentHash,
		UncleHash:  hash.EmptyUncleHash,
		Number:     uint256.NewInt(number),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       timestamp,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	blk, _ := block.NewBlock(header, nil).(*block.Block)
	return blk
}

func requireEngineErrorCode(t *testing.T, err error, want int) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil")
	}
	coded, ok := err.(interface{ ErrorCode() int })
	if !ok {
		t.Fatalf("error = %T, want error with code", err)
	}
	if got := coded.ErrorCode(); got != want {
		t.Fatalf("error code = %d, want %d", got, want)
	}
}

func TestForkchoiceUpdatedV1RejectsInvalidForkchoiceState(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	valid := makeTestBlock(ethCompatibleBlockHash(genesis, cfg), 1, 2)
	orphanParent := types.HexToHash("0x1234")
	orphan := makeTestBlock(orphanParent, 1, 2)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	validHash := ethCompatibleBlockHash(valid, cfg)
	orphanHash := ethCompatibleBlockHash(orphan, cfg)
	api.engineOverlay.stageBlock(valid, validHash, nil, nil, false)
	api.engineOverlay.stageBlock(orphan, orphanHash, nil, nil, false)

	_, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: orphanHash,
		SafeBlockHash: ethCompatibleBlockHash(genesis, cfg),
	}, nil)
	requireEngineErrorCode(t, err, -38002)

	api.engineOverlay.importBlock(valid, validHash, nil)
	unknownHash := types.HexToHash("0x9999")
	_, err = engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: validHash,
		SafeBlockHash: unknownHash,
	}, nil)
	requireEngineErrorCode(t, err, -38002)

	_, err = engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash:      validHash,
		FinalizedBlockHash: unknownHash,
	}, nil)
	requireEngineErrorCode(t, err, -38002)
}

func TestForkchoiceUpdatedV1ReturnsSyncingForUnknownHeadWithKnownSafeFinalized(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	valid := makeTestBlock(ethCompatibleBlockHash(genesis, cfg), 1, 2)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	validHash := ethCompatibleBlockHash(valid, cfg)
	api.engineOverlay.importBlock(valid, validHash, nil)

	unknownHead := types.HexToHash("0x9999")
	resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash:      unknownHead,
		SafeBlockHash:      validHash,
		FinalizedBlockHash: validHash,
	}, nil)
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV1() error = %v", err)
	}
	if resp.PayloadStatus.Status != PayloadStatusSyncing {
		t.Fatalf("ForkchoiceUpdatedV1().Status = %q, want %q", resp.PayloadStatus.Status, PayloadStatusSyncing)
	}
}

func TestForkchoiceUpdatedV1ReturnsSyncingForUnknownHeadWithMatchingSafeFinalized(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	unknownHead := types.HexToHash("0x9999")
	resp, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash:      unknownHead,
		SafeBlockHash:      unknownHead,
		FinalizedBlockHash: unknownHead,
	}, nil)
	if err != nil {
		t.Fatalf("ForkchoiceUpdatedV1() error = %v", err)
	}
	if resp.PayloadStatus.Status != PayloadStatusSyncing {
		t.Fatalf("ForkchoiceUpdatedV1().Status = %q, want %q", resp.PayloadStatus.Status, PayloadStatusSyncing)
	}
}

func TestForkchoiceUpdatedV1RejectsInvalidPayloadAttributes(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 5)
	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	engine := NewEngineAPIV1(NewBlockChainAPI(api))

	_, err := engine.ForkchoiceUpdatedV1(context.Background(), &ForkchoiceStateV1{
		HeadBlockHash: ethCompatibleBlockHash(genesis, cfg),
	}, &PayloadAttributesV1{
		Timestamp:             5,
		SuggestedFeeRecipient: types.Address{},
	})
	requireEngineErrorCode(t, err, -38003)
}

func TestBlockChainAPIGetBlockByNumberSupportsSafeAndFinalizedTags(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	genesisHash := ethCompatibleBlockHash(genesis, cfg)
	block1 := makeTestBlock(genesisHash, 1, 2)
	block2 := makeTestBlock(ethCompatibleBlockHash(block1, cfg), 2, 3)
	block1Hash := ethCompatibleBlockHash(block1, cfg)
	block2Hash := ethCompatibleBlockHash(block2, cfg)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	api.engineOverlay.importBlock(block1, block1Hash, nil)
	api.engineOverlay.importBlock(block2, block2Hash, nil)
	api.engineOverlay.setForkchoiceHashes(block1Hash, genesisHash)

	rpcAPI := NewBlockChainAPI(api)
	safeBlock, err := rpcAPI.GetBlockByNumber(context.Background(), jsonrpc.SafeBlockNumber, false)
	if err != nil {
		t.Fatalf("GetBlockByNumber(safe) error = %v", err)
	}
	if got := safeBlock["hash"]; got != avmtypes.FromastHash(block1Hash) {
		t.Fatalf("safe block hash = %v, want %v", got, avmtypes.FromastHash(block1Hash))
	}

	finalizedBlock, err := rpcAPI.GetBlockByNumber(context.Background(), jsonrpc.FinalizedBlockNumber, false)
	if err != nil {
		t.Fatalf("GetBlockByNumber(finalized) error = %v", err)
	}
	if got := finalizedBlock["hash"]; got != avmtypes.FromastHash(genesisHash) {
		t.Fatalf("finalized block hash = %v, want %v", got, avmtypes.FromastHash(genesisHash))
	}
}

func TestBlockChainAPIGetBlockByNumberFallsBackToPersistedForkchoiceTags(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	genesisHash := ethCompatibleBlockHash(genesis, cfg)
	block1 := makeTestBlock(genesisHash, 1, 2)
	chain := &persistedForkchoiceChainStub{
		preciseChainStub: newPreciseChainStub(cfg, nil, genesis, block1),
		tags: map[jsonrpc.BlockNumber]block.IBlock{
			jsonrpc.SafeBlockNumber:      block1,
			jsonrpc.FinalizedBlockNumber: genesis,
		},
	}
	api := &API{
		bc:            chain,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}

	rpcAPI := NewBlockChainAPI(api)
	safeBlock, err := rpcAPI.GetBlockByNumber(context.Background(), jsonrpc.SafeBlockNumber, false)
	if err != nil {
		t.Fatalf("GetBlockByNumber(safe) error = %v", err)
	}
	if got := safeBlock["hash"]; got != avmtypes.FromastHash(ethCompatibleBlockHash(block1, cfg)) {
		t.Fatalf("safe block hash = %v", got)
	}
	finalizedBlock, err := rpcAPI.GetBlockByNumber(context.Background(), jsonrpc.FinalizedBlockNumber, false)
	if err != nil {
		t.Fatalf("GetBlockByNumber(finalized) error = %v", err)
	}
	if got := finalizedBlock["hash"]; got != avmtypes.FromastHash(genesisHash) {
		t.Fatalf("finalized block hash = %v", got)
	}
}

func TestBlockChainAPIGetBlockByNumberReturnsNilWhenSafeAndFinalizedUnset(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}

	rpcAPI := NewBlockChainAPI(api)
	safeBlock, err := rpcAPI.GetBlockByNumber(context.Background(), jsonrpc.SafeBlockNumber, false)
	if err != nil {
		t.Fatalf("GetBlockByNumber(safe) error = %v", err)
	}
	if safeBlock != nil {
		t.Fatalf("GetBlockByNumber(safe) = %v, want nil", safeBlock)
	}

	finalizedBlock, err := rpcAPI.GetBlockByNumber(context.Background(), jsonrpc.FinalizedBlockNumber, false)
	if err != nil {
		t.Fatalf("GetBlockByNumber(finalized) error = %v", err)
	}
	if finalizedBlock != nil {
		t.Fatalf("GetBlockByNumber(finalized) = %v, want nil", finalizedBlock)
	}
}

func TestGetTransactionReceiptReturnsOverlayImportedReceipt(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	db := memdb.NewTestDB(t)
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	genesisHash := ethCompatibleBlockHash(genesis, cfg)

	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	signer := transaction.NewLondonSigner(big.NewInt(1))
	recipient := types.HexToAddress("0x1111111111111111111111111111111111111111")
	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(1),
		Gas:       21_000,
		To:        &recipient,
		Value:     uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, signer, senderKey)
	if err != nil {
		t.Fatalf("SignTx() error = %v", err)
	}

	header := &block.Header{
		ParentHash: genesisHash,
		UncleHash:  hash.EmptyUncleHash,
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		GasUsed:    21_000,
		Time:       2,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	imported := block.NewBlock(header, []*transaction.Transaction{signedTx})
	importedHash := ethCompatibleBlockHash(imported, cfg)
	receipt := &block.Receipt{
		Status:            block.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21_000,
		GasUsed:           21_000,
		TxHash:            signedTx.Hash(),
	}

	api := &API{
		bc:            newPreciseChainStub(cfg, db, genesis),
		db:            db,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	api.engineOverlay.importBlock(imported, importedHash, block.Receipts{receipt})

	resp, err := NewTransactionAPI(api, new(AddrLocker)).GetTransactionReceipt(context.Background(), avmtypes.FromastHash(signedTx.Hash()))
	if err != nil {
		t.Fatalf("GetTransactionReceipt() error = %v", err)
	}
	if resp == nil {
		t.Fatal("GetTransactionReceipt() = nil")
	}
	if got := resp["blockHash"]; got != avmtypes.FromastHash(importedHash) {
		t.Fatalf("receipt blockHash = %v, want %v", got, avmtypes.FromastHash(importedHash))
	}
	if got := resp["transactionHash"]; got != avmtypes.FromastHash(signedTx.Hash()) {
		t.Fatalf("receipt transactionHash = %v, want %v", got, avmtypes.FromastHash(signedTx.Hash()))
	}
	if got := resp["from"]; got != rpcTransactionFrom(signedTx) {
		t.Fatalf("receipt from = %v, want %v", got, rpcTransactionFrom(signedTx))
	}
	logs, ok := resp["logs"].([]*avmtypes.Log)
	if !ok || logs == nil || len(logs) != 0 {
		t.Fatalf("receipt logs = %#v, want non-nil empty list", resp["logs"])
	}
}

func TestGetTransactionReceiptDoesNotExposeStagedBlock(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	db := memdb.NewTestDB(t)
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	genesisHash := ethCompatibleBlockHash(genesis, cfg)

	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	signer := transaction.NewLondonSigner(big.NewInt(1))
	recipient := types.HexToAddress("0x2222222222222222222222222222222222222222")
	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(1),
		Gas:       21_000,
		To:        &recipient,
		Value:     uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, signer, senderKey)
	if err != nil {
		t.Fatalf("SignTx() error = %v", err)
	}

	header := &block.Header{
		ParentHash: genesisHash,
		UncleHash:  hash.EmptyUncleHash,
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		GasUsed:    21_000,
		Time:       2,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	staged := block.NewBlock(header, []*transaction.Transaction{signedTx})
	stagedHash := ethCompatibleBlockHash(staged, cfg)
	receipt := &block.Receipt{
		Status:            block.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21_000,
		GasUsed:           21_000,
		TxHash:            signedTx.Hash(),
	}

	api := &API{
		bc:            newPreciseChainStub(cfg, db, genesis),
		db:            db,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	api.engineOverlay.stageBlock(staged, stagedHash, nil, block.Receipts{receipt}, false)

	resp, err := NewTransactionAPI(api, new(AddrLocker)).GetTransactionReceipt(context.Background(), avmtypes.FromastHash(signedTx.Hash()))
	if err != nil {
		t.Fatalf("GetTransactionReceipt() error = %v", err)
	}
	if resp != nil {
		t.Fatalf("GetTransactionReceipt() = %v, want nil for staged payload", resp)
	}
}

func TestGetBlockByHashDoesNotExposeStagedBlock(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	db := memdb.NewTestDB(t)
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	genesisHash := ethCompatibleBlockHash(genesis, cfg)

	header := &block.Header{
		ParentHash: genesisHash,
		UncleHash:  hash.EmptyUncleHash,
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       2,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	staged := block.NewBlock(header, nil)
	stagedHash := ethCompatibleBlockHash(staged, cfg)

	api := &API{
		bc:            newPreciseChainStub(cfg, db, genesis),
		db:            db,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	api.engineOverlay.stageBlock(staged, stagedHash, nil, nil, false)

	resp, err := NewBlockChainAPI(api).GetBlockByHash(context.Background(), avmtypes.FromastHash(stagedHash), false)
	if err != nil {
		t.Fatalf("GetBlockByHash() error = %v", err)
	}
	if resp != nil {
		t.Fatalf("GetBlockByHash() = %v, want nil for staged payload", resp)
	}
}

func TestGetTransactionReceiptTracksCanonicalReorgs(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	db := memdb.NewTestDB(t)
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	genesisHash := ethCompatibleBlockHash(genesis, cfg)
	parent := makeTestBlock(genesisHash, 1, 2)
	parentHash := ethCompatibleBlockHash(parent, cfg)

	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	signer := transaction.NewLondonSigner(big.NewInt(1))
	recipient := types.HexToAddress("0x4444444444444444444444444444444444444444")
	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(0),
		GasFeeCap: uint256.NewInt(1),
		Gas:       21_000,
		To:        &recipient,
		Value:     uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, signer, senderKey)
	if err != nil {
		t.Fatalf("SignTx() error = %v", err)
	}

	txHeader := &block.Header{
		ParentHash: parentHash,
		UncleHash:  hash.EmptyUncleHash,
		Number:     uint256.NewInt(2),
		GasLimit:   30_000_000,
		GasUsed:    21_000,
		Time:       3,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	txBlock := block.NewBlock(txHeader, []*transaction.Transaction{signedTx})
	txBlockHash := ethCompatibleBlockHash(txBlock, cfg)
	receipt := &block.Receipt{
		Status:            block.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21_000,
		GasUsed:           21_000,
		TxHash:            signedTx.Hash(),
	}

	altHeader := &block.Header{
		ParentHash: parentHash,
		UncleHash:  hash.EmptyUncleHash,
		Number:     uint256.NewInt(2),
		GasLimit:   30_000_000,
		GasUsed:    0,
		Time:       3,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
		Extra:      []byte{0x01},
	}
	altBlock := block.NewBlock(altHeader, nil)
	altBlockHash := ethCompatibleBlockHash(altBlock, cfg)

	api := &API{
		bc:            newPreciseChainStub(cfg, db, genesis),
		db:            db,
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}
	api.engineOverlay.importBlock(parent, parentHash, nil)
	api.engineOverlay.stageBlock(txBlock, txBlockHash, nil, block.Receipts{receipt}, false)
	api.engineOverlay.importBlock(txBlock, txBlockHash, block.Receipts{receipt})

	txAPI := NewTransactionAPI(api, new(AddrLocker))
	resp, err := txAPI.GetTransactionReceipt(context.Background(), avmtypes.FromastHash(signedTx.Hash()))
	if err != nil {
		t.Fatalf("GetTransactionReceipt() error = %v", err)
	}
	if resp == nil {
		t.Fatal("GetTransactionReceipt() = nil after canonical import")
	}
	if got := resp["blockHash"]; got != avmtypes.FromastHash(txBlockHash) {
		t.Fatalf("receipt blockHash = %v, want %v", got, avmtypes.FromastHash(txBlockHash))
	}

	api.engineOverlay.stageBlock(altBlock, altBlockHash, nil, nil, false)
	api.engineOverlay.importBlock(altBlock, altBlockHash, nil)

	resp, err = txAPI.GetTransactionReceipt(context.Background(), avmtypes.FromastHash(signedTx.Hash()))
	if err != nil {
		t.Fatalf("GetTransactionReceipt() after reorg-out error = %v", err)
	}
	if resp != nil {
		t.Fatalf("GetTransactionReceipt() = %v, want nil after reorg-out", resp)
	}

	api.engineOverlay.importBlock(txBlock, txBlockHash, block.Receipts{receipt})

	resp, err = txAPI.GetTransactionReceipt(context.Background(), avmtypes.FromastHash(signedTx.Hash()))
	if err != nil {
		t.Fatalf("GetTransactionReceipt() after reorg-back error = %v", err)
	}
	if resp == nil {
		t.Fatal("GetTransactionReceipt() = nil after reorg-back")
	}
	if got := resp["blockHash"]; got != avmtypes.FromastHash(txBlockHash) {
		t.Fatalf("receipt blockHash after reorg-back = %v, want %v", got, avmtypes.FromastHash(txBlockHash))
	}
}

func TestGetBlockByNumberFullTxRecoversSenderWhenUnset(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	genesis := makeTestBlock(types.Hash{}, 0, 1)
	genesisHash := ethCompatibleBlockHash(genesis, cfg)

	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	signer := transaction.NewLondonSigner(big.NewInt(1))
	recipient := types.HexToAddress("0x3333333333333333333333333333333333333333")
	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		Nonce:     0,
		GasTipCap: uint256.NewInt(2),
		GasFeeCap: uint256.NewInt(10),
		Gas:       21_000,
		To:        &recipient,
		Value:     uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, signer, senderKey)
	if err != nil {
		t.Fatalf("SignTx() error = %v", err)
	}
	if signedTx.From() != nil {
		t.Fatalf("signedTx.From() unexpectedly set = %v", signedTx.From())
	}
	sender, err := transaction.Sender(signer, signedTx)
	if err != nil {
		t.Fatalf("Sender() error = %v", err)
	}

	header := &block.Header{
		ParentHash: genesisHash,
		UncleHash:  hash.EmptyUncleHash,
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		GasUsed:    21_000,
		Time:       2,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}
	mined := block.NewBlock(header, []*transaction.Transaction{signedTx})
	minedBlock, ok := mined.(*block.Block)
	if !ok {
		t.Fatalf("block.NewBlock() type = %T, want *block.Block", mined)
	}

	api := &API{
		bc:            newPreciseChainStub(cfg, nil, genesis, minedBlock),
		chainConfig:   cfg,
		engineOverlay: newEngineOverlay(),
	}

	resp, err := NewBlockChainAPI(api).GetBlockByNumber(context.Background(), 1, true)
	if err != nil {
		t.Fatalf("GetBlockByNumber(fullTx) error = %v", err)
	}
	if resp == nil {
		t.Fatal("GetBlockByNumber(fullTx) = nil")
	}
	txs, ok := resp["transactions"].([]interface{})
	if !ok || len(txs) != 1 {
		t.Fatalf("transactions = %T %#v, want single RPC transaction", resp["transactions"], resp["transactions"])
	}
	rpcTx, ok := txs[0].(*RPCTransaction)
	if !ok {
		t.Fatalf("transactions[0] = %T, want *RPCTransaction", txs[0])
	}
	if rpcTx.From != *avmtypes.FromastAddress(&sender) {
		t.Fatalf("rpcTx.From = %v, want %v", rpcTx.From, *avmtypes.FromastAddress(&sender))
	}
}

func TestGetBlockReceiptsRecoversSenderWhenUnset(t *testing.T) {
	cfg := &params.ChainConfig{ChainID: big.NewInt(1)}
	senderKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	signer := transaction.NewLondonSigner(big.NewInt(1))
	recipient := types.HexToAddress("0x4444444444444444444444444444444444444444")
	tx := transaction.NewTx(&transaction.DynamicFeeTx{
		ChainID:   uint256.NewInt(1),
		GasTipCap: uint256.NewInt(2),
		GasFeeCap: uint256.NewInt(10),
		Gas:       21_000,
		To:        &recipient,
		Value:     uint256.NewInt(1),
	})
	signedTx, err := transaction.SignTx(tx, signer, senderKey)
	if err != nil {
		t.Fatalf("SignTx() error = %v", err)
	}
	if signedTx.From() != nil {
		t.Fatalf("signedTx.From() unexpectedly set = %v", signedTx.From())
	}

	blk := block.NewBlock(&block.Header{
		UncleHash:  hash.EmptyUncleHash,
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		GasUsed:    21_000,
		Time:       2,
		BaseFee:    uint256.NewInt(1),
		Difficulty: uint256.NewInt(0),
	}, []*transaction.Transaction{signedTx}).(*block.Block)
	chain := newPreciseChainStub(cfg, nil, blk)
	chain.receiptsByHash[blk.Hash()] = block.Receipts{{
		Status:            block.ReceiptStatusSuccessful,
		CumulativeGasUsed: 21_000,
		GasUsed:           21_000,
	}}
	backend := &API{bc: chain, chainConfig: cfg, engineOverlay: newEngineOverlay()}

	receipts, err := NewBlockChainAPI(backend).GetBlockReceipts(
		context.Background(), jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber),
	)
	if err != nil {
		t.Fatalf("GetBlockReceipts() error = %v", err)
	}
	if len(receipts) != 1 {
		t.Fatalf("GetBlockReceipts() count = %d, want 1", len(receipts))
	}
	if receipts[0].From != rpcTransactionFrom(signedTx) {
		t.Fatalf("receipt from = %v, want %v", receipts[0].From, rpcTransactionFrom(signedTx))
	}
}
