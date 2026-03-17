package api

import (
	"crypto/sha256"
	"encoding/binary"
	"math/big"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

type engineBuiltPayload struct {
	v1 *ExecutionPayloadV1
	v2 *ExecutionPayloadV2
}

type engineOverlay struct {
	mu            sync.RWMutex
	head          block.IBlock
	blocksByNum   map[uint64]block.IBlock
	builtPayloads map[PayloadID]*engineBuiltPayload
}

func newEngineOverlay() *engineOverlay {
	return &engineOverlay{
		blocksByNum:   make(map[uint64]block.IBlock),
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

func makePayloadID(parentHash types.Hash, timestamp uint64, feeRecipient types.Address) PayloadID {
	var input [8 + len(parentHash) + len(feeRecipient)]byte
	copy(input[:], parentHash[:])
	binary.BigEndian.PutUint64(input[len(parentHash):len(parentHash)+8], timestamp)
	copy(input[len(parentHash)+8:], feeRecipient[:])
	sum := sha256.Sum256(input[:])
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

func (o *engineOverlay) importBlock(blk block.IBlock) {
	if o == nil || blk == nil || blk.Number64() == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.head = blk
	o.blocksByNum[blk.Number64().Uint64()] = blk
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
