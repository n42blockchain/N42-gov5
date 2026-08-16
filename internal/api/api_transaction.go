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
	"github.com/n42blockchain/N42/common/block"
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
	tx, err := transaction.DecodeEthereumTransaction(input)
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
	signer := transaction.MakeSignerWithTimestamp(s.api.GetChainConfig(), uint256ToBigOrZero(header.Number64()), currentBlock.Time())
	from, err := transaction.Sender(signer, tx)
	if err != nil {
		return avmcommon.Hash{}, err
	}
	tx.SetFrom(from)
	return SubmitTransaction(context.Background(), s.api, tx)
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

	// Decode and fee-check everything first, then admit the survivors to the
	// pool as ONE batch. The old loop called SubmitTransaction per entry —
	// each paying its own pool round — and aborted the whole batch on the
	// first bad entry, so a batch endpoint submitted no faster than the
	// single endpoint. A batch also engages the pool's parallel sender
	// pre-warm, which a size-1 add cannot.
	hs := make([]avmcommon.Hash, len(inputs))
	txs := make([]*transaction.Transaction, 0, len(inputs))
	slot := make([]int, 0, len(inputs))
	var firstErr error
	for i, t := range inputs {
		if len(t) == 0 {
			if firstErr == nil {
				firstErr = errors.New("empty transaction data")
			}
			continue
		}
		metaTx, err := transaction.DecodeEthereumTransaction(t)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		signer := transaction.MakeSignerWithTimestamp(s.api.GetChainConfig(), uint256ToBigOrZero(header.Number64()), currentBlock.Time())
		from, err := transaction.Sender(signer, metaTx)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		metaTx.SetFrom(from)
		if err := checkTxFee(*metaTx.GasPrice(), metaTx.Gas(), baseFee); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		txs = append(txs, metaTx)
		slot = append(slot, i)
	}
	if len(txs) > 0 {
		errs := s.api.TxsPool().AddLocals(txs)
		for j, err := range errs {
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			hs[slot[j]] = avmtypes.FromastHash(txs[j].Hash())
		}
	}
	return hs, firstErr
}

// GetTransactionReceipt returns the transaction receipt for the given transaction hash.
func (s *TransactionAPI) GetTransactionReceipt(ctx context.Context, hash avmcommon.Hash) (map[string]interface{}, error) {
	var (
		tx          *transaction.Transaction
		blockHash   = avmtypes.ToastHash(avmcommon.Hash{})
		blockNumber uint64
		index       uint64
		receipt     *block.Receipt
		err         error
	)
	if s.api != nil && s.api.engineOverlay != nil {
		if lookup := s.api.engineOverlay.txLookup(avmtypes.ToastHash(hash)); lookup != nil {
			tx = lookup.tx
			blockHash = lookup.blockHash
			blockNumber = lookup.blockNumber
			index = lookup.index
			receipts := s.api.engineOverlay.receiptsByBlockHash(blockHash)
			if len(receipts) > int(index) {
				receipt = receipts[index]
			}
		}
	}
	if dbErr := s.api.Database().View(ctx, func(t kv.Tx) error {
		if tx != nil {
			return nil
		}
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
	// Cold tier: a tx aged out of the MDBX TxLookup table is resolved via the
	// txlookup RecSplit segment index → block → (tx, index). Same self-verifying
	// path as GetTransactionByHash; the receipt is then read from that block.
	if tx == nil && s.api != nil && s.api.txIndex != nil {
		want := avmtypes.ToastHash(hash)
		if blkNum, _ := s.api.txIndex.Lookup(nil, want); blkNum != nil {
			if blk, _ := s.api.BlockChain().GetBlockByNumber(uint256.NewInt(*blkNum)); blk != nil {
				for i, btx := range blk.Transactions() {
					if btx.Hash() == want {
						tx = btx
						blockNumber = *blkNum
						index = uint64(i)
						blockHash = blk.Hash()
						break
					}
				}
			}
		}
	}
	if tx == nil {
		return nil, nil
	}
	if receipt == nil {
		receipts, err := s.api.BlockChain().GetReceipts(blockHash)
		if err != nil {
			return nil, err
		}
		if len(receipts) <= int(index) {
			return nil, nil
		}
		receipt = receipts[index]
	}

	from := rpcTransactionFrom(tx)
	fields := map[string]interface{}{
		"blockHash":         avmtypes.FromastHash(blockHash),
		"blockNumber":       hexutil.Uint64(blockNumber),
		"transactionHash":   hash,
		"transactionIndex":  hexutil.Uint64(index),
		"from":              from,
		"to":                avmtypes.FromastAddress(tx.To()),
		"gasUsed":           hexutil.Uint64(receipt.GasUsed),
		"cumulativeGasUsed": hexutil.Uint64(receipt.CumulativeGasUsed),
		"contractAddress":   nil,
		"logsBloom":         receipt.Bloom,
		"type":              hexutil.Uint(tx.Type()),
	}
	// Assign the effective gas price paid
	var header block.IHeader
	if blk, _ := s.getBlockByHash(blockHash); blk != nil {
		header = blk.Header()
	} else {
		header, err = s.api.BlockChain().GetHeaderByHash(blockHash)
		if err != nil {
			return nil, err
		}
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
		var excessBlobGas uint64
		if bh, ok := header.(*block.Header); ok && bh != nil && bh.ExcessBlobGas != nil {
			excessBlobGas = *bh.ExcessBlobGas
		}
		fields["blobGasPrice"] = (*hexutil.Big)(transaction.CalcBlobFee(excessBlobGas).ToBig()) // real fee from excessBlobGas (matches eth_getBlockReceipts)
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
	if block, _ := s.getBlockByHash(avmtypes.ToastHash(blockHash)); block != nil {
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
	if s.api != nil && s.api.engineOverlay != nil {
		if lookup := s.api.engineOverlay.txLookup(avmtypes.ToastHash(hash)); lookup != nil {
			blk, _ := s.getBlockByHash(lookup.blockHash)
			if blk == nil || blk.Header() == nil {
				return nil, nil
			}
			headerBaseFee := blk.Header().BaseFee64()
			if headerBaseFee == nil {
				headerBaseFee = new(uint256.Int)
			}
			return newRPCTransaction(lookup.tx, lookup.blockHash, lookup.blockNumber, lookup.index, headerBaseFee.ToBig()), nil
		}
	}
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

	// Cold tier: a historical tx no longer in the MDBX TxLookup table is
	// resolved via the txlookup RecSplit segment index → block number → the
	// real block, returning the FULL tx (with signature). Preferred over the F2
	// ledger view below (which has no r/s/v). The Service's verifier already
	// confirmed the candidate block actually holds this hash.
	if s.api != nil && s.api.txIndex != nil {
		if blkNum, _ := s.api.txIndex.Lookup(nil, avmtypes.ToastHash(hash)); blkNum != nil {
			if blk, _ := s.api.BlockChain().GetBlockByNumber(uint256.NewInt(*blkNum)); blk != nil {
				if rpcTx := newRPCTransactionFromBlockHash(blk, avmtypes.ToastHash(hash), s.api.GetChainConfig(), nil); rpcTx != nil {
					return rpcTx, nil
				}
			}
		}
	}

	// F2 ledger fallback (F1.5): the full tx isn't in the index/store, but the
	// MPHF tx-hash index can resolve it to (block,index) → serve the F2 ledger
	// view, echoing the queried hash. r/s/v stay empty (F2 has no signature).
	if t := s.f2TxByHash(avmtypes.ToastHash(hash)); t != nil {
		return t, nil
	}

	return nil, nil
}

// GetTransactionByBlockHashAndIndex returns the transaction for the given block hash and index.
func (s *TransactionAPI) GetTransactionByBlockHashAndIndex(ctx context.Context, blockHash avmcommon.Hash, index hexutil.Uint) *RPCTransaction {
	blk, _ := s.getBlockByHash(avmtypes.ToastHash(blockHash))
	if blk == nil {
		return nil
	}
	txs := blk.Transactions()
	if int(index) >= len(txs) {
		// Header present but body txs absent (cold body) → serve from F2.
		return f2TxByNumberAndIndex(uint256ToUint64OrZero(blk.Number64()), uint64(index), avmtypes.ToastHash(blockHash))
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
	if api == nil || api.TxsPool() == nil {
		return avmcommon.Hash{}, errors.New("transaction pool unavailable")
	}
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
