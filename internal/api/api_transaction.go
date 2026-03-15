package api

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/accounts"
	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// TransactionAPI exposes methods for reading and creating transaction data.
type TransactionAPI struct {
	api       *API
	nonceLock *AddrLocker
}

// NewTransactionAPI creates a new RPC service with methods for interacting with transactions.
func NewTransactionAPI(api *API, nonceLock *AddrLocker) *TransactionAPI {
	return &TransactionAPI{api, nonceLock}
}

// GetTransactionCount returns the number of transactions the given address has sent for the given block number.
func (s *TransactionAPI) GetTransactionCount(ctx context.Context, address avmcommon.Address, blockNrOrHash jsonrpc.BlockNumberOrHash) (*hexutil.Uint64, error) {
	if blockNr, ok := blockNrOrHash.Number(); ok && blockNr == jsonrpc.PendingBlockNumber {
		nonce := s.api.TxsPool().Nonce(*avmtypes.ToastAddress(&address))
		return (*hexutil.Uint64)(&nonce), nil
	}

	tx, err := s.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	state := s.api.State(tx, blockNrOrHash)
	if state == nil {
		return nil, nil
	}
	nonce := state.GetNonce(*avmtypes.ToastAddress(&address))
	return (*hexutil.Uint64)(&nonce), nil

}

func (s *TransactionAPI) SendRawTransaction(ctx context.Context, input hexutil.Bytes) (avmcommon.Hash, error) {
	if len(input) == 0 {
		return avmcommon.Hash{}, errors.New("empty transaction data")
	}
	tx := new(avmtypes.Transaction)
	err := tx.UnmarshalBinary(input)
	if err != nil {
		return avmcommon.Hash{}, err
	}
	currentBlock := s.api.BlockChain().CurrentBlock()
	if currentBlock == nil {
		return avmcommon.Hash{}, errors.New("no current block available")
	}
	header := currentBlock.Header()
	if header == nil {
		return avmcommon.Hash{}, errors.New("no header available")
	}
	metaTx, err := tx.ToastTransaction(s.api.GetChainConfig(), uint256ToBigOrZero(header.Number64()))
	if err != nil {
		return avmcommon.Hash{}, err
	}
	return SubmitTransaction(context.Background(), s.api, metaTx)
}

// MaxBatchSize is the maximum number of transactions allowed in a single batch request.
const MaxBatchSize = 200

func (s *TransactionAPI) BatchRawTransaction(ctx context.Context, inputs []hexutil.Bytes) ([]avmcommon.Hash, error) {
	if len(inputs) == 0 {
		return []avmcommon.Hash{}, nil
	}
	if len(inputs) > MaxBatchSize {
		return nil, fmt.Errorf("batch size %d exceeds maximum allowed %d", len(inputs), MaxBatchSize)
	}

	currentBlock := s.api.BlockChain().CurrentBlock()
	if currentBlock == nil {
		return nil, errors.New("no current block available")
	}
	header := currentBlock.Header()
	if header == nil {
		return nil, errors.New("no header available")
	}

	hs := make([]avmcommon.Hash, len(inputs))
	for i, t := range inputs {
		if len(t) == 0 {
			hs[i] = avmcommon.Hash{}
			return hs, errors.New("empty transaction data")
		}
		tx := new(avmtypes.Transaction)
		err := tx.UnmarshalBinary(t)
		if err != nil {
			hs[i] = avmcommon.Hash{}
			return hs, err
		}
		metaTx, err := tx.ToastTransaction(s.api.GetChainConfig(), uint256ToBigOrZero(header.Number64()))
		if err != nil {
			hs[i] = avmcommon.Hash{}
			return hs, err
		}

		if hs[i], err = SubmitTransaction(context.Background(), s.api, metaTx); err != nil {
			return hs, err
		}
	}
	return hs, nil
}

// GetTransactionReceipt returns the transaction receipt for the given transaction hash.
func (s *TransactionAPI) GetTransactionReceipt(ctx context.Context, hash avmcommon.Hash) (map[string]interface{}, error) {
	var (
		tx          *transaction.Transaction
		blockHash   = avmtypes.ToastHash(avmcommon.Hash{})
		blockNumber uint64
		index       uint64
		err         error
	)
	if dbErr := s.api.Database().View(ctx, func(t kv.Tx) error {
		tx, blockHash, blockNumber, index, err = rawdb.ReadTransactionByHash(t, avmtypes.ToastHash(hash))
		if err != nil {
			return err
		}
		if tx == nil {
			log.Tracef("rawdb.ReadTransactionByHash, txhash = %v not found\n", hash)
		}
		return nil
	}); dbErr != nil {
		return nil, dbErr
	}
	if tx == nil {
		return nil, nil
	}
	receipts, err := s.api.BlockChain().GetReceipts(blockHash)

	if err != nil {
		return nil, err
	}
	if len(receipts) <= int(index) {
		return nil, nil
	}
	receipt := receipts[index]

	from := tx.From()
	fields := map[string]interface{}{
		"blockHash":         avmtypes.FromastHash(blockHash),
		"blockNumber":       hexutil.Uint64(blockNumber),
		"transactionHash":   hash,
		"transactionIndex":  hexutil.Uint64(index),
		"from":              avmtypes.FromastAddress(from),
		"to":                avmtypes.FromastAddress(tx.To()),
		"gasUsed":           hexutil.Uint64(receipt.GasUsed),
		"cumulativeGasUsed": hexutil.Uint64(receipt.CumulativeGasUsed),
		"contractAddress":   nil,
		"logsBloom":         receipt.Bloom,
		"type":              hexutil.Uint(tx.Type()),
	}
	// Assign the effective gas price paid
	header, err := s.api.BlockChain().GetHeaderByHash(blockHash)
	if err != nil {
		return nil, err
	}
	if header == nil {
		return nil, errors.New("header not found for receipt")
	}
	headerBaseFee := header.BaseFee64()
	if headerBaseFee == nil {
		headerBaseFee = new(uint256.Int)
	}
	gasPrice := new(big.Int).Add(headerBaseFee.ToBig(), tx.EffectiveGasTipValue(headerBaseFee).ToBig())
	fields["effectiveGasPrice"] = (*hexutil.Big)(gasPrice)
	// Add blob gas fields for EIP-4844 blob transactions
	if tx.Type() == transaction.BlobTxType {
		fields["blobGasUsed"] = hexutil.Uint64(tx.BlobGas())
		fields["blobGasPrice"] = (*hexutil.Big)(big.NewInt(1)) // minimum blob gas price
	}
	// Assign receipt status or post state.
	if len(receipt.PostState) > 0 {
		fields["root"] = hexutil.Bytes(receipt.PostState)
	} else {
		fields["status"] = hexutil.Uint(receipt.Status)
	}
	if receipt.Logs == nil {
		fields["logs"] = []*avmtypes.Log{}
	} else {
		fields["logs"] = avmtypes.FromastLogs(receipt.Logs)
	}
	// If the ContractAddress is 20 0x0 bytes, assume it is not a contract creation
	if !receipt.ContractAddress.IsNull() {
		fields["contractAddress"] = avmtypes.FromastAddress(&receipt.ContractAddress)
	}

	return fields, nil
}

// GetBlockTransactionCountByHash returns the number of transactions in the block with the given hash.
func (s *TransactionAPI) GetBlockTransactionCountByHash(ctx context.Context, blockHash avmcommon.Hash) *hexutil.Uint {
	if block, _ := s.api.BlockChain().GetBlockByHash(avmtypes.ToastHash(blockHash)); block != nil {
		n := hexutil.Uint(len(block.Transactions()))
		return &n
	}
	return nil
}

// GetTransactionByHash returns the transaction for the given hash
func (s *TransactionAPI) GetTransactionByHash(ctx context.Context, hash avmcommon.Hash) (*RPCTransaction, error) {
	var (
		tx          *transaction.Transaction
		blockHash   = avmtypes.ToastHash(avmcommon.Hash{})
		blockNumber uint64
		index       uint64
		err         error
	)
	if err := s.api.Database().View(ctx, func(t kv.Tx) error {
		tx, blockHash, blockNumber, index, err = rawdb.ReadTransactionByHash(t, avmtypes.ToastHash(hash))
		return err
	}); err != nil {
		return nil, err
	}

	if tx != nil {
		header := s.api.BlockChain().GetHeaderByNumber(uint256.NewInt(blockNumber))
		if header == nil {
			return nil, nil
		}
		headerBaseFee := header.BaseFee64()
		if headerBaseFee == nil {
			headerBaseFee = new(uint256.Int)
		}
		return newRPCTransaction(tx, blockHash, blockNumber, index, headerBaseFee.ToBig()), nil
	}

	if tx := s.api.TxsPool().GetTx(avmtypes.ToastHash(hash)); tx != nil {
		currentBlock := s.api.BlockChain().CurrentBlock()
		if currentBlock == nil {
			return nil, nil
		}
		return newRPCPendingTransaction(tx, currentBlock.Header()), nil
	}

	return nil, nil
}

// GetTransactionByBlockHashAndIndex returns the transaction for the given block hash and index.
func (s *TransactionAPI) GetTransactionByBlockHashAndIndex(ctx context.Context, blockHash avmcommon.Hash, index hexutil.Uint) *RPCTransaction {
	blk, _ := s.api.BlockChain().GetBlockByHash(avmtypes.ToastHash(blockHash))
	if blk == nil {
		return nil
	}
	txs := blk.Transactions()
	if int(index) >= len(txs) {
		return nil
	}
	hdr := blk.Header()
	if hdr == nil {
		return nil
	}
	headerBaseFee := hdr.BaseFee64()
	if headerBaseFee == nil {
		headerBaseFee = new(uint256.Int)
	}
	return newRPCTransaction(txs[index], avmtypes.ToastHash(blockHash), uint256ToUint64OrZero(blk.Number64()), uint64(index), headerBaseFee.ToBig())
}

// SubmitTransaction submits a transaction to the transaction pool.
func SubmitTransaction(ctx context.Context, api *API, tx *transaction.Transaction) (avmcommon.Hash, error) {
	if err := checkTxFee(*tx.GasPrice(), tx.Gas(), baseFee); err != nil {
		return avmcommon.Hash{}, err
	}
	if err := api.TxsPool().AddLocal(tx); err != nil {
		return avmcommon.Hash{}, err
	}
	hash := tx.Hash()
	return avmtypes.FromastHash(hash), nil
}

// SendTransaction creates a transaction for the given argument, signs it and submits it to the transaction pool.
func (s *TransactionAPI) SendTransaction(ctx context.Context, args TransactionArgs) (avmcommon.Hash, error) {
	// Look up the wallet containing the requested signer
	account := accounts.Account{Address: args.from()}

	wallet, err := s.api.accountManager.Find(account)
	if err != nil {
		return avmcommon.Hash{}, err
	}

	if args.Nonce == nil {
		s.nonceLock.LockAddr(args.from())
		defer s.nonceLock.UnlockAddr(args.from())
	}

	if err := args.setDefaults(ctx, s.api); err != nil {
		return avmcommon.Hash{}, err
	}
	tx, err := args.toTransaction()
	if err != nil {
		return avmcommon.Hash{}, err
	}

	signed, err := wallet.SignTx(account, tx, s.api.GetChainConfig().ChainID)
	if err != nil {
		return avmcommon.Hash{}, err
	}
	signed.SetFrom(args.from())
	return SubmitTransaction(ctx, s.api, signed)
}

// checkTxFee validates the transaction fee to prevent unreasonably high gas prices.
// maxGasPriceGwei is the maximum acceptable gas price (1000 Gwei).
const maxGasPriceGwei = 1000_000_000_000 // 1000 Gwei in Wei

func checkTxFee(gasPrice uint256.Int, gas uint64, cap float64) error {
	if gasPrice.IsZero() {
		return nil
	}
	maxGasPrice := uint256.NewInt(maxGasPriceGwei)
	if gasPrice.Cmp(maxGasPrice) > 0 {
		return fmt.Errorf("gas price %s exceeds maximum allowed %s", gasPrice.String(), maxGasPrice.String())
	}
	return nil
}

// toHexSlice creates a slice of hex-strings based on []byte.
func toHexSlice(b [][]byte) []string {
	r := make([]string, len(b))
	for i := range b {
		r[i] = hexutil.Encode(b[i])
	}
	return r
}
