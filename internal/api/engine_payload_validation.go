package api

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/rlp"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	vmcore "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/lib/common/fixedgas"
	"github.com/n42blockchain/N42/lib/txpool/txpoolcfg"
	"github.com/n42blockchain/N42/params"
)

var (
	errBlobTxMissingHashes       = errors.New("blob transaction missing blob hashes")
	errBlobTxTooManyBlobs        = errors.New("blob transaction has too many blobs")
	errBlobTxFeeCapTooLow        = errors.New("max fee per blob gas less than block blob gas fee")
	errSetCodeTxEmptyAuthList    = errors.New("EIP-7702 transaction with empty auth list")
	errBlobTxContractCreation    = errors.New("input string too short for common.Address, decoding into (types.BlobTx).To")
	errSetCodeTxContractCreation = errors.New("input string too short for common.Address, decoding into (types.SetCodeTx).To")
	errTxGasLimitTooHigh         = errors.New("transaction gas limit too high")
	errFloorDataGasTooLow        = errors.New("insufficient gas for floor data gas cost")
	errInitCodeSizeExceeded      = errors.New("max initcode size exceeded")
)

func validateExecutionPayloadTransactions(txs []hexutil.Bytes, cfg *params.ChainConfig, blockNumber, timestamp, baseFee, excessBlobGas, blockGasLimit uint64) error {
	return validateExecutionPayloadTransactionsWithBaseFee(txs, cfg, blockNumber, timestamp, uint256.NewInt(baseFee), excessBlobGas, blockGasLimit)
}

func validateExecutionPayloadTransactionsWithBaseFee(txs []hexutil.Bytes, cfg *params.ChainConfig, blockNumber, timestamp uint64, baseFee *uint256.Int, excessBlobGas, blockGasLimit uint64) error {
	rules := &params.Rules{ChainID: new(big.Int)}
	var blobBaseFee *uint256.Int
	if cfg != nil {
		rules = cfg.RulesWithTimestamp(blockNumber, timestamp)
		if rules.IsCancun {
			blobBaseFee = cfg.CalcBlobFee(excessBlobGas, timestamp)
		}
	}
	baseFeeValue := new(uint256.Int)
	if baseFee != nil {
		baseFeeValue.Set(baseFee)
	}

	for _, txBytes := range txs {
		if len(txBytes) == 0 {
			continue
		}
		tx, err := transaction.DecodeEthereumTransaction(txBytes)
		if err != nil {
			return err
		}
		if err := validateExecutionPayloadTransaction(tx, rules, baseFeeValue, blobBaseFee, blockGasLimit); err != nil {
			return err
		}
	}
	return nil
}

func validateExecutionPayloadHeader(header, parent *block.Header, cfg *params.ChainConfig) error {
	if header == nil {
		return errors.New("missing execution payload header")
	}
	if len(header.Extra) > 32 {
		return fmt.Errorf("invalid extraData: length %d exceeds 32 bytes", len(header.Extra))
	}
	if header.GasUsed > header.GasLimit {
		return fmt.Errorf("invalid gasUsed: have %d, gasLimit %d", header.GasUsed, header.GasLimit)
	}
	if header.GasLimit < params.MinGasLimit {
		// Match geth/misc gas-limit validation wording so EEST's exception mapper
		// classifies the payload as INVALID_GASLIMIT instead of an undefined error.
		return fmt.Errorf("invalid gas limit below %d", params.MinGasLimit)
	}
	if header.GasLimit > params.MaxGasLimit {
		return fmt.Errorf("invalid gas limit: have %v, max %v", header.GasLimit, params.MaxGasLimit)
	}
	if parent == nil {
		return nil
	}
	parentNumber, err := requireHeaderNumber(parent, "parent header number unavailable")
	if err != nil {
		return err
	}
	headerNumber, err := requireHeaderNumber(header, "header number unavailable")
	if err != nil {
		return err
	}
	if headerNumber.Uint64() != parentNumber.Uint64()+1 {
		return fmt.Errorf("invalid block number: have %d, want %d", headerNumber.Uint64(), parentNumber.Uint64()+1)
	}
	blockNumber := headerNumber.Uint64()
	if cfg != nil && cfg.IsLondon(blockNumber) {
		if err := misc.VerifyEip1559Header(cfg, parent, header); err != nil {
			return err
		}
	} else if err := misc.VerifyGaslimit(parent.GasLimit, header.GasLimit); err != nil {
		return err
	}
	if header.Time <= parent.Time {
		return fmt.Errorf("invalid timestamp: have %d, parent %d", header.Time, parent.Time)
	}
	if cfg != nil {
		rules := cfg.RulesWithTimestamp(blockNumber, header.Time)
		if rules != nil && rules.IsCancun {
			return validateExecutionPayloadBlobHeader(header, parent, cfg)
		}
	}
	return nil
}

func validateExecutionPayloadBlobHeader(header, parent *block.Header, cfg *params.ChainConfig) error {
	if header == nil || parent == nil || cfg == nil {
		return nil
	}
	gasPerBlob := cfg.BlobGasPerBlob(header.Time)
	maxBlobGas := cfg.BlobMaxGasPerBlock(header.Time)
	var hdrBlobGasUsed, hdrExcessBlobGas uint64
	if header.BlobGasUsed != nil {
		hdrBlobGasUsed = *header.BlobGasUsed
	}
	if header.ExcessBlobGas != nil {
		hdrExcessBlobGas = *header.ExcessBlobGas
	}
	if hdrBlobGasUsed > maxBlobGas {
		return fmt.Errorf("blob gas used %d exceeds maximum %d", hdrBlobGasUsed, maxBlobGas)
	}
	if hdrBlobGasUsed%gasPerBlob != 0 {
		return fmt.Errorf("blob gas used %d is not a multiple of %d", hdrBlobGasUsed, gasPerBlob)
	}
	var parentExcess, parentUsed uint64
	if parent.ExcessBlobGas != nil {
		parentExcess = *parent.ExcessBlobGas
	}
	if parent.BlobGasUsed != nil {
		parentUsed = *parent.BlobGasUsed
	}
	expectedExcess := cfg.CalcExcessBlobGasWithBaseFee(parentExcess, parentUsed, parent.BaseFee, header.Time)
	if hdrExcessBlobGas != expectedExcess {
		return fmt.Errorf("incorrect excess blob gas: have %d, want %d", hdrExcessBlobGas, expectedExcess)
	}
	return nil
}

func validateExecutionPayloadBlockRLPSize(blk block.IBlock, rawTxs []hexutil.Bytes, cfg *params.ChainConfig, opts enginePayloadHashOptions) error {
	if blk == nil || cfg == nil {
		return nil
	}
	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil {
		return nil
	}
	rules := cfg.RulesWithTimestamp(uint64FromUint256OrZero(header.Number), header.Time)
	// EIP-7934 is an Ethereum Osaka rule; Fusaka and Glamsterdam imply IsOsaka.
	if rules == nil || !rules.IsOsaka {
		return nil
	}
	size, err := executionPayloadBlockRLPSize(blk, rawTxs, cfg, opts)
	if err != nil {
		return err
	}
	return vmcore.ValidateRLPBlockSize(size, true)
}

func executionPayloadBlockRLPSize(blk block.IBlock, rawTxs []hexutil.Bytes, cfg *params.ChainConfig, opts enginePayloadHashOptions) (uint64, error) {
	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil {
		return 0, fmt.Errorf("unexpected execution payload header type %T", blk.Header())
	}
	encodedTxs := make([]interface{}, 0, len(rawTxs))
	for _, tx := range rawTxs {
		encodedTxs = append(encodedTxs, blockRLPTransaction(tx))
	}
	blockRLP := []interface{}{
		enginePayloadRLPHeader(header, cfg, opts),
		encodedTxs,
		[]interface{}{},
	}
	if includeWithdrawalsInPayloadBlockBody(header, cfg, opts) {
		if opts.withdrawals != nil {
			blockRLP = append(blockRLP, opts.withdrawals)
		} else {
			blockRLP = append(blockRLP, []*Withdrawal{})
		}
	}
	var buf bytes.Buffer
	if err := rlp.Encode(&buf, blockRLP); err != nil {
		return 0, err
	}
	return uint64(buf.Len()), nil
}

func blockRLPTransaction(tx hexutil.Bytes) interface{} {
	// Legacy transactions are already raw RLP lists inside the payload.
	// Typed transactions (EIP-2718 and descendants) are opaque byte strings in
	// the block body and must be RLP-wrapped by the outer block encoder.
	if len(tx) > 0 && tx[0] >= 0xc0 {
		return rawRLP(tx)
	}
	return []byte(tx)
}

func includeWithdrawalsInPayloadBlockBody(header *block.Header, cfg *params.ChainConfig, opts enginePayloadHashOptions) bool {
	if opts.includeWithdrawals {
		return true
	}
	if cfg == nil {
		return false
	}
	return cfg.IsShanghaiAt(uint64FromUint256OrZero(header.Number), header.Time)
}

func enginePayloadRLPHeader(header *block.Header, cfg *params.ChainConfig, opts enginePayloadHashOptions) *ethRPCHeader {
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
	if includeWithdrawalsInPayloadBlockBody(header, cfg, opts) {
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
	return &ethRPCHeader{
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
	}
}

func validateExecutionPayloadTransaction(tx *transaction.Transaction, rules *params.Rules, baseFee, blobBaseFee *uint256.Int, blockGasLimit uint64) error {
	if tx == nil {
		return nil
	}
	if err := validateTransactionInitCodeSize(tx, rules); err != nil {
		return err
	}
	isContractCreation := tx.To() == nil
	if rules.IsOsaka && tx.Gas() > fixedgas.MaxTxnGasLimit {
		return errTxGasLimitTooHigh
	}

	switch tx.Type() {
	case transaction.LegacyTxType:
	case transaction.AccessListTxType:
		if !rules.IsBerlin {
			return internalcore.ErrTxTypeNotSupported
		}
	case transaction.DynamicFeeTxType:
		if !rules.IsLondon {
			return internalcore.ErrTxTypeNotSupported
		}
	case transaction.BlobTxType:
		if !rules.IsCancun {
			return internalcore.ErrTxTypeNotSupported
		}
		if isContractCreation {
			return errBlobTxContractCreation
		}
		if len(tx.BlobHashes()) == 0 {
			return errBlobTxMissingHashes
		}
		if uint64(len(tx.BlobHashes())) > maxBlobsPerTx(rules) {
			return errBlobTxTooManyBlobs
		}
		if blobFeeCap := tx.BlobFeeCap(); blobFeeCap == nil || blobBaseFee == nil || blobFeeCap.Lt(blobBaseFee) {
			return errBlobTxFeeCapTooLow
		}
	case transaction.SetCodeTxType:
		if !rules.IsPrague {
			return internalcore.ErrTxTypeNotSupported
		}
		if isContractCreation {
			return errSetCodeTxContractCreation
		}
		if len(tx.AuthList()) == 0 {
			return errSetCodeTxEmptyAuthList
		}
	default:
		return internalcore.ErrTxTypeNotSupported
	}

	if tx.Gas() > blockGasLimit {
		return internalcore.ErrGasLimitReached
	}
	if tx.GasFeeCapIntCmp(tx.GasTipCap()) < 0 {
		return internalcore.ErrTipAboveFeeCap
	}
	if rules.IsLondon {
		if _, err := tx.EffectiveGasTip(baseFee); err != nil {
			return err
		}
	}

	accessListLen, storageKeysLen := accessListStats(tx.AccessList())
	intrinsicGas, floorGas, reason := txpoolcfg.CalcIntrinsicGas(
		uint64(len(tx.Data())),
		countNonZeroBytes(tx.Data()),
		uint64(len(tx.AuthList())),
		accessListLen,
		storageKeysLen,
		isContractCreation,
		rules.IsHomestead,
		rules.IsIstanbul,
		rules.IsShanghai,
		rules.IsPrague,
		rules.IsGlamsterdam,
	)
	if reason != txpoolcfg.Success {
		return fmt.Errorf("intrinsic gas calculation failed: %s", reason)
	}
	// Intrinsic gas validation takes precedence when both the standard
	// intrinsic-gas requirement and the Prague floor-gas requirement fail.
	// This preserves the consensus error ordering used by the execution tests.
	if tx.Gas() < intrinsicGas {
		return internalcore.ErrIntrinsicGas
	}
	if rules.IsPrague && floorGas > intrinsicGas {
		if tx.Gas() < floorGas {
			return errFloorDataGasTooLow
		}
	}
	return nil
}

// validateWireBlockBody applies to a block that arrived as wire RLP (devp2p,
// RLP import) the body checks engine_newPayload runs before execution: every
// transaction's shape for the active fork (validateExecutionPayloadTransaction)
// and, from Cancun, the header's blobGasUsed against the blobs the body
// carries. The shared execution core does not repeat these, so without this a
// wire block with an oversized creation initcode, a malformed EIP-7702 or
// blob transaction, or a wrong blobGasUsed would be accepted.
func validateWireBlockBody(blk *block.Block, cfg *params.ChainConfig) error {
	if blk == nil || cfg == nil {
		return nil
	}
	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil || header.Number == nil {
		return nil
	}
	number := header.Number.Uint64()
	rules := cfg.RulesWithTimestamp(number, header.Time)
	baseFee := new(uint256.Int)
	if header.BaseFee != nil {
		baseFee.Set(header.BaseFee)
	}
	var excessBlobGas uint64
	if header.ExcessBlobGas != nil {
		excessBlobGas = *header.ExcessBlobGas
	}
	var blobBaseFee *uint256.Int
	if rules.IsCancun {
		blobBaseFee = cfg.CalcBlobFee(excessBlobGas, header.Time)
	}
	var blobs uint64
	for i, tx := range blk.Transactions() {
		if err := validateExecutionPayloadTransaction(tx, rules, baseFee, blobBaseFee, header.GasLimit); err != nil {
			return fmt.Errorf("invalid transaction %d: %w", i, err)
		}
		blobs += uint64(len(tx.BlobHashes()))
	}
	if !rules.IsCancun {
		return nil
	}
	if blobs > cfg.BlobMaxBlobsPerBlock(header.Time) {
		return fmt.Errorf("too many blobs in block: have %d, max %d", blobs, cfg.BlobMaxBlobsPerBlock(header.Time))
	}
	want := blobs * cfg.BlobGasPerBlob(header.Time)
	if header.BlobGasUsed == nil || *header.BlobGasUsed != want {
		var have uint64
		if header.BlobGasUsed != nil {
			have = *header.BlobGasUsed
		}
		return fmt.Errorf("blob gas used mismatch (header %d, calculated %d)", have, want)
	}
	return nil
}

// validateWireBlockForkFields checks what a wire block body must agree with
// in its header, which a payload gets from its versioned shape instead: no
// blob fields before Cancun, and from Shanghai a withdrawalsRoot that commits
// to the withdrawals the body carries.
func validateWireBlockForkFields(blk *block.Block, withdrawals []*Withdrawal, cfg *params.ChainConfig) error {
	if blk == nil || cfg == nil {
		return nil
	}
	header, ok := blk.Header().(*block.Header)
	if !ok || header == nil || header.Number == nil {
		return nil
	}
	number := header.Number.Uint64()
	if !cfg.IsCancunAt(number, header.Time) && (header.BlobGasUsed != nil || header.ExcessBlobGas != nil || header.ParentBeaconRoot != nil) {
		return errors.New("blob header fields present before Cancun")
	}
	if cfg.IsShanghaiAt(number, header.Time) {
		if header.WithdrawalsHash == nil {
			return errors.New("missing withdrawalsRoot")
		}
		if got := withdrawalsRoot(withdrawals); got != *header.WithdrawalsHash {
			return fmt.Errorf("invalid withdrawalsRoot: have %s, want %s", header.WithdrawalsHash.Hex(), got.Hex())
		}
	}
	return nil
}

func validateTransactionInitCodeSize(tx *transaction.Transaction, rules *params.Rules) error {
	if tx == nil || rules == nil || !rules.IsShanghai || tx.To() != nil {
		return nil
	}
	// EIP-7954 (Amsterdam) raises the initcode cap to 128 KiB; the
	// execution layer already enforces the raised limit, so keeping
	// the Shanghai cap here would reject valid Glamsterdam transactions.
	maxInitCode := uint64(fixedgas.MaxInitCodeSize)
	if rules.IsGlamsterdam {
		maxInitCode = params.MaxInitCodeSizeEIP7954
	}
	if uint64(len(tx.Data())) > maxInitCode {
		return errInitCodeSizeExceeded
	}
	return nil
}

func countNonZeroBytes(data []byte) uint64 {
	var count uint64
	for _, b := range data {
		if b != 0 {
			count++
		}
	}
	return count
}

func accessListStats(accessList transaction.AccessList) (uint64, uint64) {
	var addresses uint64
	var storageKeys uint64
	for _, tuple := range accessList {
		addresses++
		storageKeys += uint64(len(tuple.StorageKeys))
	}
	return addresses, storageKeys
}

func maxBlobsPerTx(rules *params.Rules) uint64 {
	switch {
	case rules.IsOsaka:
		return transaction.MaxBlobsPerBlock
	case rules.IsPectra || rules.IsPrague:
		return 9
	default:
		return transaction.MaxBlobsPerBlock
	}
}
