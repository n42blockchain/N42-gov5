package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/miner/builder"
	vm2 "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/layered"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"

	"github.com/holiman/uint256"
)

func (e *EngineAPIV1) canonicalHead() block.IBlock {
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil {
		return nil
	}
	return e.api.api.BlockChain().CurrentBlock()
}

func (e *EngineAPIV1) canonicalHeadHash() types.Hash {
	return ethCompatibleBlockHash(e.canonicalHead(), e.chainConfig())
}

func (e *EngineAPIV1) validatePayloadExecution(blk block.IBlock, parentHash types.Hash, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes) error {
	_, _, err := e.executePayloadOnParentState(blk, parentHash, parentBeaconRoot, expectedRequests)
	return err
}

type expectedExecutionPayloadOutputs struct {
	stateRoot    types.Hash
	receiptsRoot types.Hash
	logsBloom    []byte
}

func captureExpectedExecutionPayloadOutputs(header *block.Header) *expectedExecutionPayloadOutputs {
	if header == nil {
		return nil
	}
	return &expectedExecutionPayloadOutputs{
		stateRoot:    header.Root,
		receiptsRoot: header.ReceiptHash,
		logsBloom:    append([]byte(nil), header.Bloom.Bytes()...),
	}
}

func validateExecutedPayloadOutputs(expected *expectedExecutionPayloadOutputs, header *block.Header) error {
	if expected == nil || header == nil {
		return nil
	}
	if header.Root != expected.stateRoot {
		return fmt.Errorf("state root mismatch")
	}
	if header.ReceiptHash != expected.receiptsRoot {
		return fmt.Errorf("receipts root mismatch")
	}
	if !bytes.Equal(header.Bloom.Bytes(), expected.logsBloom) {
		return fmt.Errorf("logs bloom mismatch")
	}
	return nil
}

func (e *EngineAPIV1) executePayloadOnParentState(blk block.IBlock, parentHash types.Hash, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes) (*engineStateOverlay, block.Receipts, error) {
	return e.executePayloadOnParentStateWithWithdrawals(blk, parentHash, parentBeaconRoot, expectedRequests, nil)
}

func (e *EngineAPIV1) executePayloadOnParentStateAndValidateWithWithdrawals(blk block.IBlock, parentHash types.Hash, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes, withdrawals []*Withdrawal) (*engineStateOverlay, block.Receipts, error) {
	expected := captureExpectedExecutionPayloadOutputs(blockHeader(blk))
	overlayState, receipts, err := e.executePayloadOnParentStateWithWithdrawals(blk, parentHash, parentBeaconRoot, expectedRequests, withdrawals)
	if err != nil {
		return nil, nil, err
	}
	if err := validateExecutedPayloadOutputs(expected, blockHeader(blk)); err != nil {
		return nil, nil, err
	}
	return overlayState, receipts, nil
}

type statefulPayloadBuildResult struct {
	block             *block.Block
	receipts          block.Receipts
	executionRequests []hexutil.Bytes
}

func clonePayloadBodyWithdrawals(withdrawals []*Withdrawal) []*Withdrawal {
	return cloneWithdrawals(withdrawals)
}

func applyExecutionWithdrawals(ibs *state.IntraBlockState, withdrawals []*Withdrawal) {
	if ibs == nil || len(withdrawals) == 0 {
		return
	}
	gwei := uint256.NewInt(params.GWei)
	for _, withdrawal := range withdrawals {
		if withdrawal == nil || withdrawal.Amount == 0 {
			continue
		}
		amount := new(uint256.Int).Mul(uint256.NewInt(uint64(withdrawal.Amount)), gwei)
		if amount.IsZero() {
			continue
		}
		ibs.AddBalance(withdrawal.Address, amount)
	}
}

func finalizeExecutionStateChanges(cfg *params.ChainConfig, header *block.Header, ibs *state.IntraBlockState) error {
	if cfg == nil || header == nil || ibs == nil {
		return nil
	}
	number := uint64FromUint256OrZero(header.Number)
	rules := cfg.RulesWithTimestamp(number, header.Time)
	if rules == nil {
		return nil
	}
	return ibs.FinalizeTx(rules, state.NewNoopWriter())
}

func prepareStatefulPayloadHeader(cfg *params.ChainConfig, parentHeader, header *block.Header, withdrawals []*Withdrawal, parentBeaconRoot *types.Hash) {
	if header == nil || cfg == nil {
		return
	}
	number := uint64FromUint256OrZero(header.Number)
	if cfg.IsShanghaiAt(number, header.Time) {
		root := withdrawalsRoot(withdrawals)
		header.WithdrawalsHash = &root
	}
	if cfg.IsCancunAt(number, header.Time) {
		blobGasUsed := uint64(0)
		header.BlobGasUsed = &blobGasUsed
		var parentExcess, parentUsed uint64
		if parentHeader != nil {
			if parentHeader.ExcessBlobGas != nil {
				parentExcess = *parentHeader.ExcessBlobGas
			}
			if parentHeader.BlobGasUsed != nil {
				parentUsed = *parentHeader.BlobGasUsed
			}
		}
		var parentBaseFee *uint256.Int
		if parentHeader != nil {
			parentBaseFee = parentHeader.BaseFee
		}
		excessBlobGas := cfg.CalcExcessBlobGasWithBaseFee(parentExcess, parentUsed, parentBaseFee, header.Time)
		header.ExcessBlobGas = &excessBlobGas
		if parentBeaconRoot != nil {
			root := *parentBeaconRoot
			header.ParentBeaconRoot = &root
		}
	}
}

func setStatefulPayloadBlobGasUsed(header *block.Header, txs []*transaction.Transaction) {
	if header == nil || header.BlobGasUsed == nil {
		return
	}
	var blobGasUsed uint64
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		blobGasUsed += tx.BlobGas()
	}
	*header.BlobGasUsed = blobGasUsed
}

func setStatefulPayloadRequestsHash(cfg *params.ChainConfig, header *block.Header, requests []hexutil.Bytes) {
	if header == nil || cfg == nil {
		return
	}
	if cfg.IsPrague(header.Time) || cfg.IsPectra(header.Time) || cfg.IsOsaka(header.Time) {
		root := executionRequestsHash(requests)
		header.RequestsHash = &root
	}
}

func (e *EngineAPIV1) executePayloadOnParentStateWithWithdrawals(blk block.IBlock, parentHash types.Hash, parentBeaconRoot *types.Hash, expectedRequests []hexutil.Bytes, withdrawals []*Withdrawal) (*engineStateOverlay, block.Receipts, error) {
	// The (nil,nil,nil) "no-error, no-overlay" return is an intentional sentinel
	// for the N42 lightweight / validate-only mode: when no execution engine is
	// wired (e.api.api.engine == nil) deep state execution is skipped and the
	// payload is accepted on the strength of the structural checks the caller
	// already ran. Do NOT flip these to fail-closed — that path is load-bearing
	// (and exercised by the engine_api tests). The legitimate missing-parent
	// case is short-circuited to ACCEPTED by the live callers before reaching
	// here, so VALID is never reported for an unresolved-parent block.
	if blk == nil || parentHash == (types.Hash{}) {
		return nil, nil, nil
	}
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil || e.api.api.engine == nil {
		return nil, nil, nil
	}
	concreteBlock, ok := blk.(*block.Block)
	if !ok {
		return nil, nil, fmt.Errorf("unexpected execution payload block type %T", blk)
	}
	parentHeader := e.parentHeader(parentHash)
	header := blockHeader(concreteBlock)
	if parentHeader == nil || parentHeader.Number == nil || header == nil {
		return nil, nil, nil
	}
	db := e.api.api.BlockChain().DB()
	if db == nil {
		return nil, nil, nil
	}
	parentState := e.parentStateOverlay(parentHash)
	var nextState *engineStateOverlay
	var executedReceipts block.Receipts
	err := withParentState(db, parentHeader.Number.Uint64(), parentState, func(tx kv.Tx, stateReader state.StateReader, ibs *state.IntraBlockState) error {
		ibs.BeginWriteCodes()
		blockHashFunc := internalcore.GetHashFn(header, func(_ types.Hash, number uint64) *block.Header {
			if parentHeader.Number.Uint64() == number {
				return parentHeader
			}
			canonicalHash, err := rawdb.ReadCanonicalHash(tx, number)
			if err != nil || canonicalHash == (types.Hash{}) {
				return nil
			}
			return rawdb.ReadHeader(tx, canonicalHash, number)
		})
		gasPool := new(common.GasPool)
		gasPool.AddGas(concreteBlock.GasLimit())
		stateWriter := state.NewNoopWriter()
		usedGas := uint64(0)
		receipts := make(block.Receipts, 0, len(concreteBlock.Transactions()))
		cfg := e.chainConfig()
		if err := internalcore.ProcessExecutionBlockStart(parentBeaconRoot, cfg, ibs, header, e.api.api.engine); err != nil {
			return err
		}

		for i, txn := range concreteBlock.Transactions() {
			ibs.Prepare(txn.Hash(), concreteBlock.Hash(), i)
			receipt, _, err := internalcore.ApplyTransaction(cfg, blockHashFunc, e.api.api.engine, nil, gasPool, ibs, stateWriter, header, txn, &usedGas, vm2.Config{})
			if err != nil {
				return err
			}
			if receipt != nil {
				receipts = append(receipts, receipt)
			}
		}
		if usedGas != header.GasUsed {
			return fmt.Errorf("gas used by execution: %d, in header: %d", usedGas, header.GasUsed)
		}
		if err := finalizeExecutionStateChanges(cfg, header, ibs); err != nil {
			return err
		}
		applyExecutionWithdrawals(ibs, withdrawals)
		actualRequests, err := internalcore.ProcessExecutionBlockEnd(receipts, cfg, ibs, header, e.api.api.engine)
		if err != nil {
			return err
		}
		var rules *params.Rules
		if cfg != nil {
			rules = cfg.RulesWithTimestamp(uint64FromUint256OrZero(header.Number), header.Time)
		}
		if rules != nil && rules.IsPrague {
			if executionRequestsHash(actualRequests) != executionRequestsHash(expectedRequests) {
				return fmt.Errorf("invalid requests hash")
			}
			requestsHash := executionRequestsHash(actualRequests)
			header.RequestsHash = &requestsHash
		}
		header.Root = ibs.IntermediateRoot()
		header.ReceiptHash = ethel.EthReceiptHash(receipts)
		header.Bloom = block.CreateBloom(receipts)
		concreteBlock.WithSeal(header)
		accounts, storage := ibs.DirtyAccountData()
		nextState = mergeEngineStateOverlay(parentState, parentHeader.Number.Uint64(), accounts, storage, ibs.CodeHashes())
		executedReceipts = cloneReceipts(receipts)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return nextState, executedReceipts, nil
}

func (e *EngineAPIV1) buildExecutionPayloadV1Stateful(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV1) *ExecutionPayloadV1 {
	result := e.buildExecutionPayloadStateful(parent, parentHash, attrs, nil, nil)
	if result == nil || result.block == nil {
		return nil
	}
	return blockToExecutionPayloadV1(result.block, e.chainConfig())
}

func (e *EngineAPIV1) buildExecutionPayloadStateful(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV1, withdrawals []*Withdrawal, parentBeaconRoot *types.Hash) *statefulPayloadBuildResult {
	if e == nil || e.api == nil || e.api.api == nil || e.api.api.BlockChain() == nil || e.api.api.engine == nil || e.api.api.txspool == nil {
		return nil
	}
	if parent == nil || attrs == nil {
		return nil
	}
	parentHeader := blockHeader(parent)
	if parentHeader == nil || parentHeader.Number == nil {
		return nil
	}
	db := e.api.api.BlockChain().DB()
	if db == nil {
		return nil
	}
	var result *statefulPayloadBuildResult
	err := withParentState(db, parentHeader.Number.Uint64(), e.parentStateOverlay(parentHash), func(tx kv.Tx, _ state.StateReader, ibs *state.IntraBlockState) error {
		header := buildStatefulPayloadHeader(parentHeader, parentHash, attrs, e.chainConfig())
		prepareStatefulPayloadHeader(e.chainConfig(), parentHeader, header, withdrawals, parentBeaconRoot)
		if err := internalcore.ProcessExecutionBlockStart(parentBeaconRoot, e.chainConfig(), ibs, header, e.api.api.engine); err != nil {
			return err
		}
		txs, receipts, err := e.executePayloadTransactions(tx, parentHeader, header, ibs)
		if err != nil {
			return err
		}
		setStatefulPayloadBlobGasUsed(header, txs)
		applyExecutionWithdrawals(ibs, withdrawals)
		if err := finalizeExecutionStateChanges(e.chainConfig(), header, ibs); err != nil {
			return err
		}
		actualRequests, err := internalcore.ProcessExecutionBlockEnd(receipts, e.chainConfig(), ibs, header, e.api.api.engine)
		if err != nil {
			return err
		}
		setStatefulPayloadRequestsHash(e.chainConfig(), header, actualRequests)
		_, _, err = e.api.api.engine.Finalize(e.api.api.BlockChain(), header, ibs, txs, nil)
		if err != nil {
			return err
		}
		if header.Root == (types.Hash{}) {
			header.Root = ibs.IntermediateRoot()
		}
		if err := populateEthereumExecutionPayloadHeader(header, txs, receipts); err != nil {
			return err
		}
		builtBlock, ok := block.NewBlock(header, txs).(*block.Block)
		if !ok {
			return fmt.Errorf("unexpected built payload block type %T", block.NewBlock(header, txs))
		}
		result = &statefulPayloadBuildResult{
			block:             builtBlock,
			receipts:          cloneReceipts(receipts),
			executionRequests: cloneHexutilBytesList(actualRequests),
		}
		return nil
	})
	if err != nil {
		log.Warn("stateful engine payload build failed", "err", err)
		return nil
	}
	return result
}

func populateEthereumExecutionPayloadHeader(header *block.Header, txs []*transaction.Transaction, receipts block.Receipts) error {
	if header == nil {
		return nil
	}
	_, txRoot, err := encodeEthereumTransactions(txs)
	if err != nil {
		return err
	}
	header.TxHash = txRoot
	header.ReceiptHash = ethel.EthReceiptHash(receipts)
	header.Bloom = block.CreateBloom(receipts)
	return nil
}

func buildStatefulPayloadHeader(parentHeader *block.Header, parentHash types.Hash, attrs *PayloadAttributesV1, cfg *params.ChainConfig) *block.Header {
	header := &block.Header{
		ParentHash: parentHash,
		UncleHash:  hash.EmptyUncleHash,
		Coinbase:   attrs.SuggestedFeeRecipient,
		Number:     uint256.NewInt(1),
		GasLimit:   30_000_000,
		Time:       uint64(attrs.Timestamp),
		Difficulty: uint256.NewInt(0),
		MixDigest:  attrs.PrevRandao,
		Nonce:      block.EncodeNonce(0),
		BaseFee:    uint256.NewInt(0),
	}
	if parentHeader == nil {
		return header
	}
	header.Number = uint256.NewInt(0).Add(parentHeader.Number, uint256.NewInt(1))
	header.GasLimit = parentHeader.GasLimit
	if parentHeader.BaseFee != nil {
		header.BaseFee = uint256.NewInt(parentHeader.BaseFee.Uint64())
	}
	if cfg != nil {
		header.BaseFee, _ = uint256.FromBig(misc.CalcBaseFee(cfg, parentHeader))
	}
	return header
}

func (e *EngineAPIV1) executePayloadTransactions(tx kv.Tx, parentHeader, header *block.Header, ibs *state.IntraBlockState) ([]*transaction.Transaction, block.Receipts, error) {
	pending := e.api.api.txspool.Pending(false)
	if len(pending) == 0 {
		return []*transaction.Transaction{}, block.Receipts{}, nil
	}
	getHeader := func(_ types.Hash, number uint64) *block.Header {
		if parentHeader != nil && parentHeader.Number != nil && parentHeader.Number.Uint64() == number {
			return parentHeader
		}
		canonicalHash, err := rawdb.ReadCanonicalHash(tx, number)
		if err != nil || canonicalHash == (types.Hash{}) {
			return nil
		}
		return rawdb.ReadHeader(tx, canonicalHash, number)
	}
	blockHashFunc := internalcore.GetHashFn(header, getHeader)
	gasPool := new(common.GasPool).AddGas(header.GasLimit)
	stateWriter := state.NewNoopWriter()
	vmConfig := vm2.Config{}
	txs := make([]*transaction.Transaction, 0)
	receipts := make(block.Receipts, 0)
	txSet := builder.NewTxByPriceAndNonce(pending, header.BaseFee)

	commitTx := func(txn *transaction.Transaction) error {
		ibs.Prepare(txn.Hash(), types.Hash{}, len(txs))
		gasSnapshot := gasPool.Gas()
		usedGasSnapshot := header.GasUsed
		snapshot := ibs.Snapshot()
		coinbase := header.Coinbase
		receipt, _, err := internalcore.ApplyTransaction(e.chainConfig(), blockHashFunc, e.api.api.engine, &coinbase, gasPool, ibs, stateWriter, header, txn, &header.GasUsed, vmConfig)
		if err != nil {
			ibs.RevertToSnapshot(snapshot)
			gasPool = new(common.GasPool).AddGas(gasSnapshot)
			header.GasUsed = usedGasSnapshot
			return err
		}
		txs = append(txs, txn)
		receipts = append(receipts, receipt)
		return nil
	}

	for gasPool.Gas() >= params.TxGas {
		txn := txSet.Peek()
		if txn == nil {
			break
		}
		err := commitTx(txn)
		switch {
		case err == nil:
			txSet.Shift()
		case errors.Is(err, internalcore.ErrGasLimitReached):
			txSet.Pop()
		case errors.Is(err, internalcore.ErrNonceTooHigh):
			txSet.Pop()
		case errors.Is(err, internalcore.ErrNonceTooLow):
			txSet.Shift()
		default:
			txSet.Shift()
		}
	}
	return txs, receipts, nil
}

func (e *EngineAPIV1) parentStateOverlay(parentHash types.Hash) *engineStateOverlay {
	if overlay := e.overlay(); overlay != nil {
		return overlay.stateOverlayByHash(parentHash)
	}
	return nil
}

type overlaySnapshotStateReader struct {
	base  state.StateReader
	state *engineStateOverlay
}

func (r *overlaySnapshotStateReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if r.state != nil && r.state.accounts != nil {
		if data, ok := r.state.accounts[address]; ok {
			if data == nil {
				return nil, nil
			}
			acc := new(account.StateAccount)
			if err := acc.DecodeForStorageV2(data); err != nil {
				return nil, err
			}
			return acc, nil
		}
	}
	return r.base.ReadAccountData(address)
}

func (r *overlaySnapshotStateReader) ReadAccountStorage(address types.Address, key *types.Hash) ([]byte, error) {
	if r.state != nil && r.state.storage != nil {
		if slots, ok := r.state.storage[address]; ok {
			if value, exists := slots[*key]; exists {
				if value == nil {
					return nil, nil
				}
				return append([]byte(nil), value...), nil
			}
		}
	}
	return r.base.ReadAccountStorage(address, key)
}

func (r *overlaySnapshotStateReader) ReadAccountCode(address types.Address, codeHash types.Hash) ([]byte, error) {
	if r.state != nil && len(r.state.codes) > 0 {
		if code, ok := r.state.codes[codeHash]; ok {
			return append([]byte(nil), code...), nil
		}
	}
	return r.base.ReadAccountCode(address, codeHash)
}

func (r *overlaySnapshotStateReader) ReadAccountCodeSize(address types.Address, codeHash types.Hash) (int, error) {
	if r.state != nil && len(r.state.codes) > 0 {
		if code, ok := r.state.codes[codeHash]; ok {
			return len(code), nil
		}
	}
	return r.base.ReadAccountCodeSize(address, codeHash)
}

func newOverlayStateView(db kv.RwDB, tx kv.Tx, blockNumber uint64, overlayState *engineStateOverlay) (state.StateReader, *state.IntraBlockState, error) {
	var stateReader state.StateReader = state.NewPlainState(tx, blockNumber+1)
	if cache := layered.ExtractCache(db); cache != nil {
		stateReader = state.NewCachedStateReader(stateReader, cache)
	}
	if overlayState != nil {
		stateReader = &overlaySnapshotStateReader{
			base:  stateReader,
			state: cloneEngineStateOverlay(overlayState),
		}
	}
	return stateReader, state.New(stateReader), nil
}

func applyEngineStateOverlayToTx(tx kv.RwTx, overlayState *engineStateOverlay) error {
	if tx == nil || overlayState == nil {
		return nil
	}
	for codeHash, code := range overlayState.codes {
		if code == nil {
			continue
		}
		if err := tx.Put(modules.Code, codeHash[:], code); err != nil {
			return err
		}
	}
	for addr, accountData := range overlayState.accounts {
		if accountData == nil {
			if err := tx.Delete(modules.Account, addr[:]); err != nil {
				return err
			}
			if err := deleteAccountStorageFromTx(tx, addr); err != nil {
				return err
			}
			continue
		}
		if err := tx.Put(modules.Account, addr[:], accountData); err != nil {
			return err
		}
		// Phase D: PlainContractCode is no longer maintained. Account row
		// carries CodeHash inline; bytecode is content-addressed in
		// modules.Code (codeHash → bytecode).
		var decoded account.StateAccount
		if err := decoded.DecodeForStorageV2(accountData); err != nil {
			return err
		}
		if !decoded.IsEmptyCodeHash() {
			if code, ok := overlayState.codes[decoded.CodeHash]; ok && code != nil {
				if err := tx.Put(modules.Code, decoded.CodeHash[:], code); err != nil {
					return err
				}
			}
		}
	}
	for addr, slots := range overlayState.storage {
		for slot, value := range slots {
			key := modules.PlainGenerateCompositeStorageKey(addr.Bytes(), slot.Bytes())
			if value == nil || len(value) == 0 {
				if err := tx.Delete(modules.Storage, key); err != nil {
					return err
				}
				continue
			}
			if err := tx.Put(modules.Storage, key, value); err != nil {
				return err
			}
		}
	}
	return nil
}

func deleteAccountStorageFromTx(tx kv.RwTx, addr types.Address) error {
	cursor, err := tx.Cursor(modules.Storage)
	if err != nil {
		return err
	}
	defer cursor.Close()
	prefix := addr[:]
	var keys [][]byte
	for key, _, err := cursor.Seek(prefix); key != nil; key, _, err = cursor.Next() {
		if err != nil {
			return err
		}
		if len(key) < len(prefix) || !bytes.Equal(key[:len(prefix)], prefix) {
			break
		}
		keys = append(keys, append([]byte(nil), key...))
	}
	for _, key := range keys {
		if err := tx.Delete(modules.Storage, key); err != nil {
			return err
		}
	}
	return nil
}

func withParentState(db kv.RwDB, parentNumber uint64, overlayState *engineStateOverlay, fn func(tx kv.Tx, stateReader state.StateReader, ibs *state.IntraBlockState) error) error {
	tx, err := db.BeginRw(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	baseNumber := parentNumber
	if overlayState != nil && overlayState.hasBaseBlockNumber {
		baseNumber = overlayState.baseBlockNumber
	}
	rewound, err := rewindEngineValidationState(tx, baseNumber+1)
	if err != nil {
		return err
	}
	if err := applyEngineStateOverlayToTx(tx, overlayState); err != nil {
		return err
	}
	var stateReader state.StateReader
	if rewound || overlayState != nil {
		if err := tx.ClearBucket(kv.TrieOfAccounts); err != nil {
			return fmt.Errorf("clear parent account trie: %w", err)
		}
		if err := tx.ClearBucket(kv.TrieOfStorage); err != nil {
			return fmt.Errorf("clear parent storage trie: %w", err)
		}
		if err := ethel.RebuildHashedState(tx); err != nil {
			return fmt.Errorf("rebuild parent hashed state: %w", err)
		}
		stateReader = state.NewPlainStateReader(tx)
	} else {
		stateReader = state.NewPlainState(tx, parentNumber+1)
		if cache := layered.ExtractCache(db); cache != nil {
			stateReader = state.NewCachedStateReader(stateReader, cache)
		}
	}
	ibs := state.New(stateReader)
	ibs.BeginWriteCodes()
	ethel.SetupStateRootComputer(tx, ibs)
	if err := ethel.InitHashState(tx); err != nil {
		return err
	}
	return fn(tx, stateReader, ibs)
}

func withCanonicalParentState(db kv.RwDB, parentNumber uint64, fn func(tx kv.Tx, stateReader state.StateReader, ibs *state.IntraBlockState) error) error {
	return withParentState(db, parentNumber, nil, fn)
}

func mergeEngineStateOverlay(parent *engineStateOverlay, baseBlockNumber uint64, accounts map[types.Address][]byte, storage map[types.Address]map[types.Hash][]byte, codes map[types.Hash][]byte) *engineStateOverlay {
	merged := cloneEngineStateOverlay(parent)
	if merged == nil {
		merged = &engineStateOverlay{
			accounts:           make(map[types.Address][]byte),
			storage:            make(map[types.Address]map[types.Hash][]byte),
			codes:              make(map[types.Hash][]byte),
			baseBlockNumber:    baseBlockNumber,
			hasBaseBlockNumber: true,
		}
	}
	if merged.accounts == nil {
		merged.accounts = make(map[types.Address][]byte)
	}
	if merged.storage == nil {
		merged.storage = make(map[types.Address]map[types.Hash][]byte)
	}
	if merged.codes == nil {
		merged.codes = make(map[types.Hash][]byte)
	}
	for addr, accountData := range accounts {
		if accountData == nil {
			merged.accounts[addr] = nil
			continue
		}
		merged.accounts[addr] = append([]byte(nil), accountData...)
	}
	for addr, slots := range storage {
		mergedSlots := merged.storage[addr]
		if mergedSlots == nil {
			mergedSlots = make(map[types.Hash][]byte, len(slots))
			merged.storage[addr] = mergedSlots
		}
		for slot, value := range slots {
			if value == nil {
				mergedSlots[slot] = nil
				continue
			}
			mergedSlots[slot] = append([]byte(nil), value...)
		}
	}
	for codeHash, code := range codes {
		merged.codes[codeHash] = append([]byte(nil), code...)
	}
	return merged
}
