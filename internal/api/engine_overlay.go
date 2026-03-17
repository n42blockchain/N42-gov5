package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"sort"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

type engineBuiltPayload struct {
	v1                *ExecutionPayloadV1
	v2                *ExecutionPayloadV2
	v3                *ExecutionPayloadV3
	v4                *ExecutionPayloadV4
	blobsBundle       *BlobsBundleV1
	executionRequests []hexutil.Bytes
}

type engineOverlay struct {
	mu            sync.RWMutex
	head          block.IBlock
	blocksByNum   map[uint64]block.IBlock
	blocksByHash  map[types.Hash]block.IBlock
	hashesByNum   map[uint64]types.Hash
	builtPayloads map[PayloadID]*engineBuiltPayload
}

func newEngineOverlay() *engineOverlay {
	return &engineOverlay{
		blocksByNum:   make(map[uint64]block.IBlock),
		blocksByHash:  make(map[types.Hash]block.IBlock),
		hashesByNum:   make(map[uint64]types.Hash),
		builtPayloads: make(map[PayloadID]*engineBuiltPayload),
	}
}

type ethRPCHeader struct {
	ParentHash       types.Hash       `json:"parentHash"`
	UncleHash        types.Hash       `json:"sha3Uncles"`
	Coinbase         types.Address    `json:"miner"`
	Root             types.Hash       `json:"stateRoot"`
	TxHash           types.Hash       `json:"transactionsRoot"`
	ReceiptHash      types.Hash       `json:"receiptsRoot"`
	Bloom            block.Bloom      `json:"logsBloom"`
	Difficulty       *big.Int         `json:"difficulty"`
	Number           *big.Int         `json:"number"`
	GasLimit         uint64           `json:"gasLimit"`
	GasUsed          uint64           `json:"gasUsed"`
	Time             uint64           `json:"timestamp"`
	Extra            []byte           `json:"extraData"`
	MixDigest        types.Hash       `json:"mixHash"`
	Nonce            block.BlockNonce `json:"nonce"`
	BaseFee          *big.Int         `json:"baseFeePerGas" rlp:"optional"`
	WithdrawalsHash  *types.Hash      `json:"withdrawalsRoot" rlp:"optional"`
	BlobGasUsed      *uint64          `json:"blobGasUsed" rlp:"optional"`
	ExcessBlobGas    *uint64          `json:"excessBlobGas" rlp:"optional"`
	ParentBeaconRoot *types.Hash      `json:"parentBeaconBlockRoot" rlp:"optional"`
	RequestsHash     *types.Hash      `json:"requestsHash" rlp:"optional"`
}

func ethCompatibleHeaderHash(head block.IHeader, cfg *params.ChainConfig) types.Hash {
	header, ok := head.(*block.Header)
	if !ok || header == nil {
		return types.Hash{}
	}
	var (
		baseFee       *big.Int
		withdrawals   *types.Hash
		blobGasUsed   *uint64
		excessBlobGas *uint64
	)
	number := uint64FromUint256OrZero(header.Number)
	if header.BaseFee != nil {
		baseFee = header.BaseFee.ToBig()
	}
	if cfg != nil && cfg.IsShanghai(number) {
		withdrawals = new(types.Hash)
	}
	if cfg != nil && (cfg.IsCancun(number) || cfg.IsPrague(header.Time) || cfg.IsPectra(header.Time) || cfg.IsOsaka(header.Time)) {
		if withdrawals == nil {
			withdrawals = new(types.Hash)
		}
		blobGasUsed = new(uint64)
		excessBlobGas = new(uint64)
		*blobGasUsed = header.BlobGasUsed
		*excessBlobGas = header.ExcessBlobGas
	}
	return hash.RlpHash(&ethRPCHeader{
		ParentHash:      header.ParentHash,
		UncleHash:       hash.EmptyUncleHash,
		Coinbase:        header.Coinbase,
		Root:            header.Root,
		TxHash:          header.TxHash,
		ReceiptHash:     header.ReceiptHash,
		Bloom:           block.BytesToBloom(header.Bloom.Bytes()),
		Difficulty:      uint256ToBigOrZero(header.Difficulty),
		Number:          uint256ToBigOrZero(header.Number),
		GasLimit:        header.GasLimit,
		GasUsed:         header.GasUsed,
		Time:            header.Time,
		Extra:           header.Extra,
		MixDigest:       header.MixDigest,
		Nonce:           block.EncodeNonce(header.Nonce.Uint64()),
		BaseFee:         baseFee,
		WithdrawalsHash: withdrawals,
		BlobGasUsed:     blobGasUsed,
		ExcessBlobGas:   excessBlobGas,
	})
}

func ethCompatibleBlockHash(blk block.IBlock, cfg *params.ChainConfig) types.Hash {
	if blk == nil {
		return types.Hash{}
	}
	return ethCompatibleHeaderHash(blk.Header(), cfg)
}

type enginePayloadHashOptions struct {
	includeWithdrawals bool
	withdrawals        []*Withdrawal
	includeBlobFields  bool
	parentBeaconRoot   *types.Hash
	requestsHash       *types.Hash
}

func ethCompatibleEngineBlockHash(blk block.IBlock, cfg *params.ChainConfig, opts enginePayloadHashOptions) types.Hash {
	if blk == nil {
		return types.Hash{}
	}
	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil {
		return types.Hash{}
	}
	var (
		baseFee         *big.Int
		withdrawalsHash *types.Hash
		blobGasUsed     *uint64
		excessBlobGas   *uint64
	)
	number := uint64FromUint256OrZero(header.Number)
	if header.BaseFee != nil {
		baseFee = header.BaseFee.ToBig()
	}
	if opts.includeWithdrawals || (cfg != nil && cfg.IsShanghai(number)) {
		root := withdrawalsRoot(opts.withdrawals)
		withdrawalsHash = &root
	}
	if opts.includeBlobFields || (cfg != nil && (cfg.IsCancun(number) || cfg.IsPrague(header.Time) || cfg.IsPectra(header.Time) || cfg.IsOsaka(header.Time))) {
		blobGasUsed = new(uint64)
		excessBlobGas = new(uint64)
		*blobGasUsed = header.BlobGasUsed
		*excessBlobGas = header.ExcessBlobGas
	}
	return hash.RlpHash(&ethRPCHeader{
		ParentHash:       header.ParentHash,
		UncleHash:        hash.EmptyUncleHash,
		Coinbase:         header.Coinbase,
		Root:             header.Root,
		TxHash:           header.TxHash,
		ReceiptHash:      header.ReceiptHash,
		Bloom:            block.BytesToBloom(header.Bloom.Bytes()),
		Difficulty:       uint256ToBigOrZero(header.Difficulty),
		Number:           uint256ToBigOrZero(header.Number),
		GasLimit:         header.GasLimit,
		GasUsed:          header.GasUsed,
		Time:             header.Time,
		Extra:            header.Extra,
		MixDigest:        header.MixDigest,
		Nonce:            block.EncodeNonce(header.Nonce.Uint64()),
		BaseFee:          baseFee,
		WithdrawalsHash:  withdrawalsHash,
		BlobGasUsed:      blobGasUsed,
		ExcessBlobGas:    excessBlobGas,
		ParentBeaconRoot: opts.parentBeaconRoot,
		RequestsHash:     opts.requestsHash,
	})
}

func makePayloadID(parentHash types.Hash, timestamp uint64, feeRecipient types.Address) PayloadID {
	return makePayloadIDWithExtras(parentHash, timestamp, feeRecipient)
}

func makePayloadIDWithExtras(parentHash types.Hash, timestamp uint64, feeRecipient types.Address, extras ...[]byte) PayloadID {
	size := len(parentHash) + 8 + len(feeRecipient)
	for _, extra := range extras {
		size += len(extra)
	}
	input := make([]byte, size)
	offset := 0
	copy(input[offset:], parentHash[:])
	offset += len(parentHash)
	binary.BigEndian.PutUint64(input[offset:offset+8], timestamp)
	offset += 8
	copy(input[offset:], feeRecipient[:])
	offset += len(feeRecipient)
	for _, extra := range extras {
		copy(input[offset:], extra)
		offset += len(extra)
	}
	sum := sha256.Sum256(input)
	var id PayloadID
	copy(id[:], sum[:len(id)])
	return id
}

func uint256FromHexUint64(v hexutil.Uint64) *uint256.Int {
	return uint256.NewInt(uint64(v))
}

func uint64FromUint256OrZero(v *uint256.Int) uint64 {
	if v == nil {
		return 0
	}
	return v.Uint64()
}

func decodeTransactions(encoded []hexutil.Bytes) ([]*transaction.Transaction, error) {
	txs := make([]*transaction.Transaction, 0, len(encoded))
	for _, raw := range encoded {
		if len(raw) == 0 {
			continue
		}
		tx := new(transaction.Transaction)
		if err := tx.Unmarshal(raw); err != nil {
			return nil, err
		}
		txs = append(txs, tx)
	}
	return txs, nil
}

func executionPayloadV1ToBlock(payload *ExecutionPayloadV1) (block.IBlock, error) {
	txs, err := decodeTransactions(payload.Transactions)
	if err != nil {
		return nil, err
	}
	header := &block.Header{
		ParentHash:  payload.ParentHash,
		Coinbase:    payload.FeeRecipient,
		Root:        payload.StateRoot,
		TxHash:      hash.DeriveSha(transaction.Transactions(txs)),
		ReceiptHash: payload.ReceiptsRoot,
		Bloom:       block.BytesToBloom(payload.LogsBloom),
		Difficulty:  uint256.NewInt(0),
		Number:      uint256FromHexUint64(payload.BlockNumber),
		GasLimit:    uint64(payload.GasLimit),
		GasUsed:     uint64(payload.GasUsed),
		Time:        uint64(payload.Timestamp),
		MixDigest:   payload.PrevRandao,
		Nonce:       block.EncodeNonce(0),
		Extra:       append([]byte(nil), payload.ExtraData...),
		BaseFee:     uint256FromHexUint64(payload.BaseFeePerGas),
	}
	return block.NewBlock(header, txs), nil
}

func executionPayloadV2ToBlock(payload *ExecutionPayloadV2) (block.IBlock, error) {
	if payload == nil {
		return nil, nil
	}
	return executionPayloadV1ToBlock(&payload.ExecutionPayloadV1)
}

func executionPayloadV3ToBlock(payload *ExecutionPayloadV3) (block.IBlock, error) {
	if payload == nil {
		return nil, nil
	}
	blk, err := executionPayloadV1ToBlock(&ExecutionPayloadV1{
		ParentHash:    payload.ParentHash,
		FeeRecipient:  payload.FeeRecipient,
		StateRoot:     payload.StateRoot,
		ReceiptsRoot:  payload.ReceiptsRoot,
		LogsBloom:     payload.LogsBloom,
		PrevRandao:    payload.PrevRandao,
		BlockNumber:   payload.BlockNumber,
		GasLimit:      payload.GasLimit,
		GasUsed:       payload.GasUsed,
		Timestamp:     payload.Timestamp,
		ExtraData:     payload.ExtraData,
		BaseFeePerGas: payload.BaseFeePerGas,
		BlockHash:     payload.BlockHash,
		Transactions:  payload.Transactions,
	})
	if err != nil || blk == nil {
		return blk, err
	}
	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil {
		return blk, nil
	}
	if payload.BlobGasUsed != nil {
		header.BlobGasUsed = uint64(*payload.BlobGasUsed)
	}
	if payload.ExcessBlobGas != nil {
		header.ExcessBlobGas = uint64(*payload.ExcessBlobGas)
	}
	return blk.WithSeal(header), nil
}

func executionPayloadV4ToBlock(payload *ExecutionPayloadV4) (block.IBlock, error) {
	if payload == nil {
		return nil, nil
	}
	return executionPayloadV3ToBlock(&ExecutionPayloadV3{
		ParentHash:    payload.ParentHash,
		FeeRecipient:  payload.FeeRecipient,
		StateRoot:     payload.StateRoot,
		ReceiptsRoot:  payload.ReceiptsRoot,
		LogsBloom:     payload.LogsBloom,
		PrevRandao:    payload.PrevRandao,
		BlockNumber:   payload.BlockNumber,
		GasLimit:      payload.GasLimit,
		GasUsed:       payload.GasUsed,
		Timestamp:     payload.Timestamp,
		ExtraData:     payload.ExtraData,
		BaseFeePerGas: payload.BaseFeePerGas,
		BlockHash:     payload.BlockHash,
		Transactions:  payload.Transactions,
		Withdrawals:   cloneWithdrawals(payload.Withdrawals),
		BlobGasUsed:   payload.BlobGasUsed,
		ExcessBlobGas: payload.ExcessBlobGas,
	})
}

func blockToExecutionPayloadV1(blk block.IBlock, cfg *params.ChainConfig) *ExecutionPayloadV1 {
	if blk == nil {
		return nil
	}
	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil {
		return nil
	}
	txs := blk.Transactions()
	encodedTxs := make([]hexutil.Bytes, 0, len(txs))
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		raw, err := tx.Marshal()
		if err != nil {
			continue
		}
		encodedTxs = append(encodedTxs, raw)
	}
	return &ExecutionPayloadV1{
		ParentHash:    header.ParentHash,
		FeeRecipient:  header.Coinbase,
		StateRoot:     header.Root,
		ReceiptsRoot:  header.ReceiptHash,
		LogsBloom:     header.Bloom.Bytes(),
		PrevRandao:    header.MixDigest,
		BlockNumber:   hexutil.Uint64(uint64FromUint256OrZero(header.Number)),
		GasLimit:      hexutil.Uint64(header.GasLimit),
		GasUsed:       hexutil.Uint64(header.GasUsed),
		Timestamp:     hexutil.Uint64(header.Time),
		ExtraData:     append([]byte(nil), header.Extra...),
		BaseFeePerGas: hexutil.Uint64(uint64FromUint256OrZero(header.BaseFee)),
		BlockHash:     ethCompatibleBlockHash(blk, cfg),
		Transactions:  encodedTxs,
	}
}

func buildExecutionPayloadV1(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV1, cfg *params.ChainConfig) *ExecutionPayloadV1 {
	var (
		parentStateRoot types.Hash
		gasLimit        uint64 = 30_000_000
		baseFee         uint64
		blockNumber     uint64 = 1
	)
	if parent != nil {
		parentStateRoot = parent.StateRoot()
		gasLimit = parent.GasLimit()
		blockNumber = uint64FromUint256OrZero(parent.Number64()) + 1
		baseFee = uint64FromUint256OrZero(parent.BaseFee64())
	}
	header := &block.Header{
		ParentHash:  parentHash,
		Coinbase:    attrs.SuggestedFeeRecipient,
		Root:        parentStateRoot,
		TxHash:      hash.DeriveSha(transaction.Transactions(nil)),
		ReceiptHash: hash.DeriveSha(block.Receipts(nil)),
		Bloom:       block.BytesToBloom(nil),
		Difficulty:  uint256.NewInt(0),
		Number:      uint256.NewInt(blockNumber),
		GasLimit:    gasLimit,
		GasUsed:     0,
		Time:        uint64(attrs.Timestamp),
		MixDigest:   attrs.PrevRandao,
		Nonce:       block.EncodeNonce(0),
		BaseFee:     uint256.NewInt(baseFee),
	}
	blk := block.NewBlock(header, nil)
	return blockToExecutionPayloadV1(blk, cfg)
}

func buildExecutionPayloadV2(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV2, cfg *params.ChainConfig) *ExecutionPayloadV2 {
	if attrs == nil {
		return nil
	}
	v1 := buildExecutionPayloadV1(parent, parentHash, &attrs.PayloadAttributesV1, cfg)
	if v1 == nil {
		return nil
	}
	payload := &ExecutionPayloadV2{
		ExecutionPayloadV1: *v1,
		Withdrawals:        cloneWithdrawals(attrs.Withdrawals),
	}
	payload.BlockHash = ethCompatibleEngineBlockHash(blockFromExecutionPayloadV1(v1), cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
	})
	return payload
}

func buildExecutionPayloadV3(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV3, cfg *params.ChainConfig) *ExecutionPayloadV3 {
	if attrs == nil {
		return nil
	}
	v2 := buildExecutionPayloadV2(parent, parentHash, &PayloadAttributesV2{
		PayloadAttributesV1: PayloadAttributesV1{
			Timestamp:             attrs.Timestamp,
			PrevRandao:            attrs.PrevRandao,
			SuggestedFeeRecipient: attrs.SuggestedFeeRecipient,
		},
		Withdrawals: cloneWithdrawals(attrs.Withdrawals),
	}, cfg)
	if v2 == nil {
		return nil
	}
	excessBlobGas := uint64(0)
	if parent != nil {
		if parentHeader, ok := parent.Header().(*block.Header); ok && parentHeader != nil {
			excessBlobGas = CalcExcessBlobGas(parentHeader.ExcessBlobGas, parentHeader.BlobGasUsed)
		}
	}
	payload := &ExecutionPayloadV3{
		ParentHash:    v2.ParentHash,
		FeeRecipient:  v2.FeeRecipient,
		StateRoot:     v2.StateRoot,
		ReceiptsRoot:  v2.ReceiptsRoot,
		LogsBloom:     v2.LogsBloom,
		PrevRandao:    v2.PrevRandao,
		BlockNumber:   v2.BlockNumber,
		GasLimit:      v2.GasLimit,
		GasUsed:       v2.GasUsed,
		Timestamp:     v2.Timestamp,
		ExtraData:     v2.ExtraData,
		BaseFeePerGas: v2.BaseFeePerGas,
		BlockHash:     v2.BlockHash,
		Transactions:  cloneHexutilBytesList(v2.Transactions),
		Withdrawals:   cloneWithdrawals(v2.Withdrawals),
		BlobGasUsed:   hexutilUint64Ptr(0),
		ExcessBlobGas: hexutilUint64Ptr(excessBlobGas),
	}
	payload.BlockHash = ethCompatibleEngineBlockHash(blockFromExecutionPayloadV3(payload), cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   attrs.ParentBeaconBlockRoot,
	})
	return payload
}

func buildExecutionPayloadV4(parent block.IBlock, parentHash types.Hash, attrs *PayloadAttributesV4, cfg *params.ChainConfig) *ExecutionPayloadV4 {
	if attrs == nil {
		return nil
	}
	v3 := buildExecutionPayloadV3(parent, parentHash, &PayloadAttributesV3{
		Timestamp:             attrs.Timestamp,
		PrevRandao:            attrs.PrevRandao,
		SuggestedFeeRecipient: attrs.SuggestedFeeRecipient,
		Withdrawals:           cloneWithdrawals(attrs.Withdrawals),
		ParentBeaconBlockRoot: attrs.ParentBeaconBlockRoot,
	}, cfg)
	if v3 == nil {
		return nil
	}
	requestsHash := executionRequestsHash(nil)
	payload := &ExecutionPayloadV4{
		ParentHash:            v3.ParentHash,
		FeeRecipient:          v3.FeeRecipient,
		StateRoot:             v3.StateRoot,
		ReceiptsRoot:          v3.ReceiptsRoot,
		LogsBloom:             v3.LogsBloom,
		PrevRandao:            v3.PrevRandao,
		BlockNumber:           v3.BlockNumber,
		GasLimit:              v3.GasLimit,
		GasUsed:               v3.GasUsed,
		Timestamp:             v3.Timestamp,
		ExtraData:             v3.ExtraData,
		BaseFeePerGas:         v3.BaseFeePerGas,
		BlockHash:             v3.BlockHash,
		Transactions:          cloneHexutilBytesList(v3.Transactions),
		Withdrawals:           cloneWithdrawals(v3.Withdrawals),
		BlobGasUsed:           v3.BlobGasUsed,
		ExcessBlobGas:         v3.ExcessBlobGas,
		DepositRequests:       []DepositRequest{},
		WithdrawalRequests:    []WithdrawalRequest{},
		ConsolidationRequests: []ConsolidationRequest{},
	}
	payload.BlockHash = ethCompatibleEngineBlockHash(blockFromExecutionPayloadV4(payload), cfg, enginePayloadHashOptions{
		includeWithdrawals: true,
		withdrawals:        payload.Withdrawals,
		includeBlobFields:  true,
		parentBeaconRoot:   attrs.ParentBeaconBlockRoot,
		requestsHash:       &requestsHash,
	})
	return payload
}

func blockFromExecutionPayloadV1(payload *ExecutionPayloadV1) block.IBlock {
	blk, _ := executionPayloadV1ToBlock(payload)
	return blk
}

func blockFromExecutionPayloadV3(payload *ExecutionPayloadV3) block.IBlock {
	blk, _ := executionPayloadV3ToBlock(payload)
	return blk
}

func blockFromExecutionPayloadV4(payload *ExecutionPayloadV4) block.IBlock {
	blk, _ := executionPayloadV4ToBlock(payload)
	return blk
}

type withdrawalList []*Withdrawal

func (w withdrawalList) Len() int {
	return len(w)
}

func (w withdrawalList) EncodeIndex(i int, buf *bytes.Buffer) {
	if i < 0 || i >= len(w) || w[i] == nil {
		buf.Reset()
		return
	}
	buf.Reset()
	_ = rlp.Encode(buf, w[i])
}

func withdrawalsRoot(withdrawals []*Withdrawal) types.Hash {
	return hash.DeriveSha(withdrawalList(withdrawals))
}

func executionRequestsHash(requests []hexutil.Bytes) types.Hash {
	sorted := cloneHexutilBytesList(requests)
	sort.SliceStable(sorted, func(i, j int) bool {
		if len(sorted[i]) == 0 {
			return false
		}
		if len(sorted[j]) == 0 {
			return true
		}
		return sorted[i][0] < sorted[j][0]
	})
	aggregate := sha256.New()
	for _, request := range sorted {
		if len(request) <= 1 {
			continue
		}
		sum := sha256.Sum256(request)
		_, _ = aggregate.Write(sum[:])
	}
	var out types.Hash
	_ = out.SetBytes(aggregate.Sum(nil))
	return out
}

func cloneHexutilBytesList(values []hexutil.Bytes) []hexutil.Bytes {
	if values == nil {
		return nil
	}
	cloned := make([]hexutil.Bytes, 0, len(values))
	for _, value := range values {
		if value == nil {
			cloned = append(cloned, nil)
			continue
		}
		copyValue := append(hexutil.Bytes(nil), value...)
		cloned = append(cloned, copyValue)
	}
	return cloned
}

func hexutilUint64Ptr(v uint64) *hexutil.Uint64 {
	n := hexutil.Uint64(v)
	return &n
}

func (o *engineOverlay) builtPayload(id PayloadID) *engineBuiltPayload {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.builtPayloads[id]
}

func (o *engineOverlay) storeBuiltPayload(id PayloadID, payload *engineBuiltPayload) {
	if o == nil || payload == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.builtPayloads[id] = payload
}

func (o *engineOverlay) importBlock(blk block.IBlock, blockHash types.Hash) {
	if o == nil || blk == nil || blk.Number64() == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	number := blk.Number64().Uint64()
	o.head = blk
	o.blocksByNum[number] = blk
	if blockHash == (types.Hash{}) {
		blockHash = ethCompatibleBlockHash(blk, nil)
	}
	o.hashesByNum[number] = blockHash
	o.blocksByHash[blockHash] = blk
}

func (o *engineOverlay) headBlock(fallback block.IBlock) block.IBlock {
	if o == nil {
		return fallback
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	if o.head != nil {
		return o.head
	}
	return fallback
}

func (o *engineOverlay) blockByNumber(number uint64) block.IBlock {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.blocksByNum[number]
}

func (o *engineOverlay) blockByHash(hash types.Hash) block.IBlock {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.blocksByHash[hash]
}

func (o *engineOverlay) hashForBlock(blk block.IBlock, cfg *params.ChainConfig) types.Hash {
	if blk == nil {
		return types.Hash{}
	}
	if o != nil && blk.Number64() != nil {
		o.mu.RLock()
		hashOverride, ok := o.hashesByNum[blk.Number64().Uint64()]
		o.mu.RUnlock()
		if ok {
			return hashOverride
		}
	}
	return ethCompatibleBlockHash(blk, cfg)
}
