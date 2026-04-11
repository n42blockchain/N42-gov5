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
	"github.com/n42blockchain/N42/internal/consensus/misc"
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

type engineOverlayTxLookup struct {
	tx          *transaction.Transaction
	blockHash   types.Hash
	blockNumber uint64
	index       uint64
}

type engineStateOverlay struct {
	accounts map[types.Address][]byte
	storage  map[types.Address]map[types.Hash][]byte
	codes    map[types.Hash][]byte
}

const (
	maxOverlayBlocks   = 1024 // max imported blocks to retain
	maxOverlayPayloads = 128  // max built payloads to retain
)

type engineOverlay struct {
	mu                  sync.RWMutex
	head                block.IBlock
	safeHash            types.Hash
	finalizedHash       types.Hash
	blocksByNum         map[uint64]block.IBlock
	blocksByHash        map[types.Hash]block.IBlock
	hashesByNum         map[uint64]types.Hash
	payloadBodiesByHash map[types.Hash]*ExecutionPayloadBodyV1
	validatedByHash     map[types.Hash]bool
	rejectedByHash      map[types.Hash]*types.Hash
	stateByHash         map[types.Hash]*engineStateOverlay
	receiptsByHash      map[types.Hash]block.Receipts
	txLookupByHash      map[types.Hash]*engineOverlayTxLookup
	builtPayloads       map[PayloadID]*engineBuiltPayload
}

type ethereumRawTransactions [][]byte

func newEngineOverlay() *engineOverlay {
	return &engineOverlay{
		blocksByNum:         make(map[uint64]block.IBlock),
		blocksByHash:        make(map[types.Hash]block.IBlock),
		hashesByNum:         make(map[uint64]types.Hash),
		payloadBodiesByHash: make(map[types.Hash]*ExecutionPayloadBodyV1),
		validatedByHash:     make(map[types.Hash]bool),
		rejectedByHash:      make(map[types.Hash]*types.Hash),
		stateByHash:         make(map[types.Hash]*engineStateOverlay),
		receiptsByHash:      make(map[types.Hash]block.Receipts),
		txLookupByHash:      make(map[types.Hash]*engineOverlayTxLookup),
		builtPayloads:       make(map[PayloadID]*engineBuiltPayload),
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
		beaconRoot    *types.Hash
		requestsHash  *types.Hash
	)
	number := uint64FromUint256OrZero(header.Number)
	if header.BaseFee != nil {
		baseFee = header.BaseFee.ToBig()
	}
	if cfg != nil && cfg.IsShanghaiAt(number, header.Time) {
		root := withdrawalsRoot(nil)
		if header.WithdrawalsHash != nil {
			root = *header.WithdrawalsHash
		}
		withdrawals = &root
	}
	if cfg != nil && cfg.IsCancunAt(number, header.Time) {
		if withdrawals == nil {
			root := withdrawalsRoot(nil)
			if header.WithdrawalsHash != nil {
				root = *header.WithdrawalsHash
			}
			withdrawals = &root
		}
		blobGasUsed = new(uint64)
		excessBlobGas = new(uint64)
		if header.BlobGasUsed != nil {
			*blobGasUsed = *header.BlobGasUsed
		}
		if header.ExcessBlobGas != nil {
			*excessBlobGas = *header.ExcessBlobGas
		}
		beaconRoot = new(types.Hash)
		if header.ParentBeaconRoot != nil {
			*beaconRoot = *header.ParentBeaconRoot
		}
	}
	if cfg != nil && (cfg.IsPrague(header.Time) || cfg.IsPectra(header.Time) || cfg.IsOsaka(header.Time)) {
		root := executionRequestsHash(nil)
		if header.RequestsHash != nil {
			root = *header.RequestsHash
		}
		requestsHash = &root
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
		WithdrawalsHash:  withdrawals,
		BlobGasUsed:      blobGasUsed,
		ExcessBlobGas:    excessBlobGas,
		ParentBeaconRoot: beaconRoot,
		RequestsHash:     requestsHash,
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
		requestsHash    *types.Hash
	)
	number := uint64FromUint256OrZero(header.Number)
	if header.BaseFee != nil {
		baseFee = header.BaseFee.ToBig()
	}
	if opts.includeWithdrawals || (cfg != nil && cfg.IsShanghaiAt(number, header.Time)) {
		root := withdrawalsRoot(opts.withdrawals)
		withdrawalsHash = &root
	}
	if opts.includeBlobFields || (cfg != nil && cfg.IsCancunAt(number, header.Time)) {
		blobGasUsed = new(uint64)
		excessBlobGas = new(uint64)
		if header.BlobGasUsed != nil {
			*blobGasUsed = *header.BlobGasUsed
		}
		if header.ExcessBlobGas != nil {
			*excessBlobGas = *header.ExcessBlobGas
		}
	}
	if opts.requestsHash != nil {
		requestsHash = opts.requestsHash
	} else if cfg != nil && (cfg.IsPrague(header.Time) || cfg.IsPectra(header.Time) || cfg.IsOsaka(header.Time)) {
		root := executionRequestsHash(nil)
		requestsHash = &root
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
		RequestsHash:     requestsHash,
	})
}

func makePayloadID(parentHash types.Hash, timestamp uint64, feeRecipient types.Address, prevRandao types.Hash) PayloadID {
	return makePayloadIDWithExtras(parentHash, timestamp, feeRecipient, prevRandao)
}

func makePayloadIDWithExtras(parentHash types.Hash, timestamp uint64, feeRecipient types.Address, prevRandao types.Hash, extras ...[]byte) PayloadID {
	size := len(parentHash) + 8 + len(feeRecipient) + len(prevRandao)
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
	copy(input[offset:], prevRandao[:])
	offset += len(prevRandao)
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

func (txs ethereumRawTransactions) Len() int { return len(txs) }

func (txs ethereumRawTransactions) EncodeIndex(i int, w *bytes.Buffer) {
	w.Write(txs[i])
}

func encodeEthereumTransactions(txs []*transaction.Transaction) ([]hexutil.Bytes, types.Hash, error) {
	if len(txs) == 0 {
		return make([]hexutil.Bytes, 0), deriveEthereumListHash(ethereumRawTransactions(nil)), nil
	}
	rawTxs := make(ethereumRawTransactions, 0, len(txs))
	encodedTxs := make([]hexutil.Bytes, 0, len(txs))
	for _, tx := range txs {
		if tx == nil {
			continue
		}
		raw, err := transaction.EncodeEthereumTransaction(tx)
		if err != nil {
			return nil, types.Hash{}, err
		}
		rawTxs = append(rawTxs, raw)
		encodedTxs = append(encodedTxs, hexutil.Bytes(raw))
	}
	return encodedTxs, deriveEthereumListHash(rawTxs), nil
}

func decodeTransactions(encoded []hexutil.Bytes) ([]*transaction.Transaction, error) {
	txs := make([]*transaction.Transaction, 0, len(encoded))
	for _, raw := range encoded {
		if len(raw) == 0 {
			continue
		}
		tx, err := transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			return nil, err
		}
		txs = append(txs, tx)
	}
	return txs, nil
}

func applyExecutionPayloadForkFields(blk block.IBlock, withdrawals []*Withdrawal, blobGasUsed, excessBlobGas *hexutil.Uint64, parentBeaconBlockRoot *types.Hash, executionRequests []hexutil.Bytes) block.IBlock {
	if blk == nil {
		return nil
	}
	header := blockHeader(blk)
	if header == nil {
		return blk
	}
	header = block.CopyHeader(header)
	if withdrawals != nil {
		root := withdrawalsRoot(withdrawals)
		header.WithdrawalsHash = &root
	}
	if blobGasUsed != nil {
		v := uint64(*blobGasUsed)
		header.BlobGasUsed = &v
	}
	if excessBlobGas != nil {
		v := uint64(*excessBlobGas)
		header.ExcessBlobGas = &v
	}
	if parentBeaconBlockRoot != nil {
		root := *parentBeaconBlockRoot
		header.ParentBeaconRoot = &root
	}
	if executionRequests != nil {
		root := executionRequestsHash(executionRequests)
		header.RequestsHash = &root
	}
	return blk.WithSeal(header)
}

func executionPayloadV1ToBlock(payload *ExecutionPayloadV1) (block.IBlock, error) {
	txs, err := decodeTransactions(payload.Transactions)
	if err != nil {
		return nil, err
	}
	_, txRoot, err := encodeEthereumTransactions(txs)
	if err != nil {
		return nil, err
	}
	header := &block.Header{
		ParentHash:  payload.ParentHash,
		UncleHash:   hash.EmptyUncleHash,
		Coinbase:    payload.FeeRecipient,
		Root:        payload.StateRoot,
		TxHash:      txRoot,
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
	blk, err := executionPayloadV1ToBlock(&payload.ExecutionPayloadV1)
	if err != nil || blk == nil {
		return blk, err
	}
	return applyExecutionPayloadForkFields(blk, payload.Withdrawals, payload.BlobGasUsed, payload.ExcessBlobGas, nil, nil), nil
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
	return applyExecutionPayloadForkFields(blk, payload.Withdrawals, payload.BlobGasUsed, payload.ExcessBlobGas, nil, nil), nil
}

func executionPayloadV4ToBlock(payload *ExecutionPayloadV4) (block.IBlock, error) {
	if payload == nil {
		return nil, nil
	}
	blk, err := executionPayloadV3ToBlock(&ExecutionPayloadV3{
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
	if err != nil || blk == nil {
		return blk, err
	}
	return applyExecutionPayloadForkFields(blk, payload.Withdrawals, payload.BlobGasUsed, payload.ExcessBlobGas, nil, nil), nil
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
	encodedTxs, _, err := encodeEthereumTransactions(txs)
	if err != nil {
		encodedTxs = nil
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
	_, emptyTxRoot, err := encodeEthereumTransactions(nil)
	if err != nil {
		emptyTxRoot = types.Hash{}
	}
	if parent != nil {
		parentStateRoot = parent.StateRoot()
		gasLimit = parent.GasLimit()
		blockNumber = uint64FromUint256OrZero(parent.Number64()) + 1
		baseFee = uint64FromUint256OrZero(parent.BaseFee64())
		if parentHeader := blockHeader(parent); parentHeader != nil && cfg != nil {
			baseFee = misc.CalcBaseFee(cfg, parentHeader).Uint64()
		}
	}
	header := &block.Header{
		ParentHash:  parentHash,
		UncleHash:   hash.EmptyUncleHash,
		Coinbase:    attrs.SuggestedFeeRecipient,
		Root:        parentStateRoot,
		TxHash:      emptyTxRoot,
		ReceiptHash: hash.DeriveShaV2(block.Receipts(nil)),
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
			var pExcess, pUsed uint64
			if parentHeader.ExcessBlobGas != nil {
				pExcess = *parentHeader.ExcessBlobGas
			}
			if parentHeader.BlobGasUsed != nil {
				pUsed = *parentHeader.BlobGasUsed
			}
			if cfg != nil {
				excessBlobGas = cfg.CalcExcessBlobGasWithBaseFee(pExcess, pUsed, parentHeader.BaseFee, uint64(attrs.Timestamp))
			} else {
				excessBlobGas = CalcExcessBlobGas(pExcess, pUsed)
			}
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
	return deriveEthereumListHash(withdrawalList(withdrawals))
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

	// Evict oldest payloads if limit exceeded to prevent unbounded memory growth
	if len(o.builtPayloads) > maxOverlayPayloads {
		count := 0
		for pid := range o.builtPayloads {
			if pid != id {
				delete(o.builtPayloads, pid)
				count++
				if len(o.builtPayloads) <= maxOverlayPayloads/2 {
					break
				}
			}
		}
	}
}

func cloneReceipt(receipt *block.Receipt) *block.Receipt {
	if receipt == nil {
		return nil
	}
	cloned := *receipt
	cloned.PostState = append([]byte(nil), receipt.PostState...)
	if receipt.BlockNumber != nil {
		cloned.BlockNumber = uint256.NewInt(receipt.BlockNumber.Uint64())
	}
	if receipt.Logs != nil {
		cloned.Logs = append([]*block.Log(nil), receipt.Logs...)
	}
	return &cloned
}

func cloneReceipts(receipts block.Receipts) block.Receipts {
	if receipts == nil {
		return nil
	}
	cloned := make(block.Receipts, 0, len(receipts))
	for _, receipt := range receipts {
		cloned = append(cloned, cloneReceipt(receipt))
	}
	return cloned
}

func cloneHashPtr(hash *types.Hash) *types.Hash {
	if hash == nil {
		return nil
	}
	cloned := *hash
	return &cloned
}

func (o *engineOverlay) storeBlockDataLocked(blk block.IBlock, blockHash types.Hash, overlayState *engineStateOverlay, receipts block.Receipts, validated bool, exposeTransactions bool, body *ExecutionPayloadBodyV1) {
	if blk == nil {
		return
	}
	o.blocksByHash[blockHash] = blk
	if validated {
		o.validatedByHash[blockHash] = true
	}
	if overlayState != nil {
		o.stateByHash[blockHash] = cloneEngineStateOverlay(overlayState)
	}
	if body != nil {
		o.payloadBodiesByHash[blockHash] = cloneExecutionPayloadBody(body)
	} else if _, exists := o.payloadBodiesByHash[blockHash]; !exists {
		o.payloadBodiesByHash[blockHash] = payloadBodyFromBlock(blk, nil)
	}
	if receipts == nil {
		return
	}
	clonedReceipts := cloneReceipts(receipts)
	o.receiptsByHash[blockHash] = clonedReceipts
	blockNumber := uint64FromUint256OrZero(blk.Number64())
	for i, tx := range blk.Transactions() {
		if tx == nil {
			continue
		}
		if i < len(clonedReceipts) && clonedReceipts[i] != nil {
			clonedReceipts[i].TxHash = tx.Hash()
			clonedReceipts[i].BlockHash = blockHash
			clonedReceipts[i].BlockNumber = uint256.NewInt(blockNumber)
			clonedReceipts[i].TransactionIndex = uint(i)
		}
		if exposeTransactions {
			o.txLookupByHash[tx.Hash()] = &engineOverlayTxLookup{
				tx:          tx,
				blockHash:   blockHash,
				blockNumber: blockNumber,
				index:       uint64(i),
			}
		}
	}
}

func (o *engineOverlay) stageBlock(blk block.IBlock, blockHash types.Hash, overlayState *engineStateOverlay, receipts block.Receipts, validated bool) {
	o.stageBlockWithBody(blk, blockHash, overlayState, receipts, validated, nil)
}

func (o *engineOverlay) stageBlockWithBody(blk block.IBlock, blockHash types.Hash, overlayState *engineStateOverlay, receipts block.Receipts, validated bool, body *ExecutionPayloadBodyV1) {
	if o == nil || blk == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if blockHash == (types.Hash{}) {
		blockHash = ethCompatibleBlockHash(blk, nil)
	}
	o.storeBlockDataLocked(blk, blockHash, overlayState, receipts, validated, false, body)
	if blk.Number64() != nil && len(o.blocksByHash) > maxOverlayBlocks {
		o.evictOldBlocks(blk.Number64().Uint64())
	}
}

func (o *engineOverlay) stageRejectedBlockWithBody(blk block.IBlock, blockHash types.Hash, latestValidHash *types.Hash, body *ExecutionPayloadBodyV1) {
	if o == nil || blk == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if blockHash == (types.Hash{}) {
		blockHash = ethCompatibleBlockHash(blk, nil)
	}
	o.storeBlockDataLocked(blk, blockHash, nil, nil, false, false, body)
	o.rejectedByHash[blockHash] = cloneHashPtr(latestValidHash)
	if blk.Number64() != nil && len(o.blocksByHash) > maxOverlayBlocks {
		o.evictOldBlocks(blk.Number64().Uint64())
	}
}

func (o *engineOverlay) importBlock(blk block.IBlock, blockHash types.Hash, receipts block.Receipts) {
	o.importBlockWithBody(blk, blockHash, receipts, nil)
}

func (o *engineOverlay) importBlockWithBody(blk block.IBlock, blockHash types.Hash, receipts block.Receipts, body *ExecutionPayloadBodyV1) {
	if o == nil || blk == nil || blk.Number64() == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	number := blk.Number64().Uint64()
	o.head = blk
	if blockHash == (types.Hash{}) {
		blockHash = ethCompatibleBlockHash(blk, nil)
	}
	o.storeBlockDataLocked(blk, blockHash, nil, receipts, true, false, body)
	o.rebuildCanonicalChainLocked(blk, blockHash)

	// Evict oldest blocks if limit exceeded to prevent unbounded memory growth
	if len(o.blocksByHash) > maxOverlayBlocks {
		o.evictOldBlocks(number)
	}
}

func (o *engineOverlay) rebuildCanonicalChainLocked(head block.IBlock, headHash types.Hash) {
	o.blocksByNum = make(map[uint64]block.IBlock)
	o.hashesByNum = make(map[uint64]types.Hash)
	o.txLookupByHash = make(map[types.Hash]*engineOverlayTxLookup)
	for depth := 0; depth <= maxOverlayBlocks && head != nil && head.Number64() != nil; depth++ {
		number := head.Number64().Uint64()
		if _, exists := o.hashesByNum[number]; exists {
			break
		}
		o.blocksByNum[number] = head
		o.hashesByNum[number] = headHash
		receipts := o.receiptsByHash[headHash]
		for i, tx := range head.Transactions() {
			if tx == nil {
				continue
			}
			if i >= len(receipts) || receipts[i] == nil {
				continue
			}
			o.txLookupByHash[tx.Hash()] = &engineOverlayTxLookup{
				tx:          tx,
				blockHash:   headHash,
				blockNumber: number,
				index:       uint64(i),
			}
		}
		header := blockHeader(head)
		if header == nil {
			break
		}
		parentHash := header.ParentHash
		parent := o.blocksByHash[parentHash]
		if parent == nil {
			break
		}
		head = parent
		headHash = parentHash
	}
}

func (o *engineOverlay) setForkchoiceHashes(safeHash, finalizedHash types.Hash) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.safeHash = safeHash
	o.finalizedHash = finalizedHash
}

// evictOldBlocks removes blocks older than the cutoff to bound memory usage.
// Caller must hold o.mu write lock.
func (o *engineOverlay) evictOldBlocks(currentNum uint64) {
	cutoff := uint64(0)
	if currentNum > maxOverlayBlocks {
		cutoff = currentNum - maxOverlayBlocks
	}
	for h, blk := range o.blocksByHash {
		if blk == nil || blk.Number64() == nil || blk.Number64().Uint64() >= cutoff {
			continue
		}
		delete(o.blocksByHash, h)
		delete(o.validatedByHash, h)
		delete(o.rejectedByHash, h)
		delete(o.stateByHash, h)
		delete(o.receiptsByHash, h)
		delete(o.payloadBodiesByHash, h)
		for txHash, lookup := range o.txLookupByHash {
			if lookup != nil && lookup.blockHash == h {
				delete(o.txLookupByHash, txHash)
			}
		}
	}
	for num := range o.blocksByNum {
		if num < cutoff {
			delete(o.blocksByNum, num)
			delete(o.hashesByNum, num)
		}
	}
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

func (o *engineOverlay) canonicalBlockByHash(hash types.Hash) block.IBlock {
	if o == nil || hash == (types.Hash{}) {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	blk := o.blocksByHash[hash]
	if blk == nil || blk.Number64() == nil {
		return nil
	}
	if o.hashesByNum[blk.Number64().Uint64()] != hash {
		return nil
	}
	return blk
}

func (o *engineOverlay) blockValidated(hash types.Hash) bool {
	if o == nil || hash == (types.Hash{}) {
		return false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.validatedByHash[hash]
}

func (o *engineOverlay) rejectedLatestValidHash(hash types.Hash) (*types.Hash, bool) {
	if o == nil || hash == (types.Hash{}) {
		return nil, false
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	latestValidHash, ok := o.rejectedByHash[hash]
	return cloneHashPtr(latestValidHash), ok
}

func (o *engineOverlay) payloadBodyByHash(hash types.Hash) *ExecutionPayloadBodyV1 {
	if o == nil || hash == (types.Hash{}) {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return cloneExecutionPayloadBody(o.payloadBodiesByHash[hash])
}

func (o *engineOverlay) payloadBodyByNumber(number uint64) *ExecutionPayloadBodyV1 {
	if o == nil {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	hash, ok := o.hashesByNum[number]
	if !ok {
		return nil
	}
	return cloneExecutionPayloadBody(o.payloadBodiesByHash[hash])
}

func (o *engineOverlay) stateOverlayByHash(hash types.Hash) *engineStateOverlay {
	if o == nil || hash == (types.Hash{}) {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return cloneEngineStateOverlay(o.stateByHash[hash])
}

func (o *engineOverlay) receiptsByBlockHash(hash types.Hash) block.Receipts {
	if o == nil || hash == (types.Hash{}) {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return cloneReceipts(o.receiptsByHash[hash])
}

func (o *engineOverlay) txLookup(hash types.Hash) *engineOverlayTxLookup {
	if o == nil || hash == (types.Hash{}) {
		return nil
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	lookup := o.txLookupByHash[hash]
	if lookup == nil {
		return nil
	}
	cloned := *lookup
	return &cloned
}

func (o *engineOverlay) forkchoiceHashes() (types.Hash, types.Hash) {
	if o == nil {
		return types.Hash{}, types.Hash{}
	}
	o.mu.RLock()
	defer o.mu.RUnlock()
	return o.safeHash, o.finalizedHash
}

func (o *engineOverlay) hashForBlock(blk block.IBlock, cfg *params.ChainConfig) types.Hash {
	if blk == nil {
		return types.Hash{}
	}
	if o != nil && blk.Number64() != nil {
		o.mu.RLock()
		canonicalBlk, ok := o.blocksByNum[blk.Number64().Uint64()]
		hashOverride, hashOK := o.hashesByNum[blk.Number64().Uint64()]
		o.mu.RUnlock()
		if ok && hashOK && canonicalBlk == blk {
			return hashOverride
		}
	}
	return ethCompatibleBlockHash(blk, cfg)
}

func cloneOverlayCodeMap(src map[types.Hash][]byte) map[types.Hash][]byte {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[types.Hash][]byte, len(src))
	for codeHash, code := range src {
		dst[codeHash] = append([]byte(nil), code...)
	}
	return dst
}

func cloneEngineStateOverlay(src *engineStateOverlay) *engineStateOverlay {
	if src == nil {
		return nil
	}
	dst := &engineStateOverlay{
		accounts: make(map[types.Address][]byte, len(src.accounts)),
		storage:  make(map[types.Address]map[types.Hash][]byte, len(src.storage)),
		codes:    cloneOverlayCodeMap(src.codes),
	}
	for addr, accountData := range src.accounts {
		if accountData == nil {
			dst.accounts[addr] = nil
			continue
		}
		dst.accounts[addr] = append([]byte(nil), accountData...)
	}
	for addr, slots := range src.storage {
		copiedSlots := make(map[types.Hash][]byte, len(slots))
		for slot, value := range slots {
			if value == nil {
				copiedSlots[slot] = nil
				continue
			}
			copiedSlots[slot] = append([]byte(nil), value...)
		}
		dst.storage[addr] = copiedSlots
	}
	return dst
}
