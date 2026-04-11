// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// TransactionArgs is the RPC wire format for eth_* tx submission.
// Carries from/to/gas, legacy and EIP-1559 fee fields, value, data,
// access list, chain id and optional blob parameters. Used by
// eth_sendTransaction, eth_call, eth_estimateGas and eth_signTransaction
// to construct an internal transaction.Transaction after validation.

package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/holiman/uint256"

	avmtypes "github.com/n42blockchain/N42/common/avmtypes"
	avmcommon "github.com/n42blockchain/N42/common/avmutil"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/math"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// TransactionArgs represents
type TransactionArgs struct {
	From                 *avmcommon.Address `json:"from"`
	To                   *avmcommon.Address `json:"to"`
	Gas                  *hexutil.Uint64    `json:"gas"`
	GasPrice             *hexutil.Big       `json:"gasPrice"`
	MaxFeePerGas         *hexutil.Big       `json:"maxFeePerGas"`
	MaxPriorityFeePerGas *hexutil.Big       `json:"maxPriorityFeePerGas"`
	Value                *hexutil.Big       `json:"value"`
	Nonce                *hexutil.Uint64    `json:"nonce"`

	Data  *hexutil.Bytes `json:"data"`
	Input *hexutil.Bytes `json:"input"`

	// Introduced by AccessListTxType transaction.
	AccessList *avmtypes.AccessList `json:"accessList,omitempty"`
	ChainID    *hexutil.Big         `json:"chainId,omitempty"`
}

// RPCAuthorization represents an EIP-7702 authorization for RPC serialization
type RPCAuthorization struct {
	ChainID *hexutil.Big      `json:"chainId"`
	Address avmcommon.Address `json:"address"`
	Nonce   hexutil.Uint64    `json:"nonce"`
	V       *hexutil.Big      `json:"v"`
	R       *hexutil.Big      `json:"r"`
	S       *hexutil.Big      `json:"s"`
	YParity *hexutil.Uint64   `json:"yParity,omitempty"`
}

// RPCTransaction represents a transaction that will serialize to the RPC representation of a transaction
type RPCTransaction struct {
	BlockHash        *avmcommon.Hash      `json:"blockHash"`
	BlockNumber      *hexutil.Big         `json:"blockNumber"`
	From             avmcommon.Address    `json:"from"`
	Gas              hexutil.Uint64       `json:"gas"`
	GasPrice         *hexutil.Big         `json:"gasPrice"`
	GasFeeCap        *hexutil.Big         `json:"maxFeePerGas,omitempty"`
	GasTipCap        *hexutil.Big         `json:"maxPriorityFeePerGas,omitempty"`
	Hash             avmcommon.Hash       `json:"hash"`
	Input            hexutil.Bytes        `json:"input"`
	Nonce            hexutil.Uint64       `json:"nonce"`
	To               *avmcommon.Address   `json:"to"`
	TransactionIndex *hexutil.Uint64      `json:"transactionIndex"`
	Value            *hexutil.Big         `json:"value"`
	Type             hexutil.Uint64       `json:"type"`
	Accesses         *avmtypes.AccessList `json:"accessList,omitempty"`
	ChainID          *hexutil.Big         `json:"chainId,omitempty"`
	V                *hexutil.Big         `json:"v"`
	R                *hexutil.Big         `json:"r"`
	S                *hexutil.Big         `json:"s"`
	// EIP-4844 Blob transaction fields
	MaxFeePerBlobGas    *hexutil.Big     `json:"maxFeePerBlobGas,omitempty"`
	BlobVersionedHashes []avmcommon.Hash `json:"blobVersionedHashes,omitempty"`
	// EIP-7702 SetCode authorization list
	AuthorizationList []RPCAuthorization `json:"authorizationList,omitempty"`
	// YParity for EIP-2718+ typed transactions
	YParity *hexutil.Uint64 `json:"yParity,omitempty"`
}

// from retrieves the transaction sender address.
func (args *TransactionArgs) from() types.Address {
	if args.From == nil {
		return types.Address{}
	}
	from := *avmtypes.ToastAddress(args.From)
	return from
}

// data retrieves the transaction calldata. Input field is preferred.
func (args *TransactionArgs) data() []byte {
	if args.Input != nil {
		return *args.Input
	}
	if args.Data != nil {
		return *args.Data
	}
	return nil
}

// setDefaults fills in default values for unspecified transaction fields.
func (args *TransactionArgs) setDefaults(ctx context.Context, api *API) error {
	if err := args.setFeeDefaults(ctx, api); err != nil {
		return err
	}

	if args.Value == nil {
		args.Value = new(hexutil.Big)
	}
	if args.Nonce == nil {
		nonce := api.TxsPool().Nonce(args.from())
		args.Nonce = (*hexutil.Uint64)(&nonce)
	}
	if args.Data != nil && args.Input != nil && !bytes.Equal(*args.Data, *args.Input) {
		return errors.New(`both "data" and "input" are set and not equal. Please use "input" to pass transaction call data`)
	}
	if args.To == nil && len(args.data()) == 0 {
		return errors.New(`contract creation without any data provided`)
	}
	if args.Gas == nil {
		data := args.data()
		callArgs := TransactionArgs{
			From:                 args.From,
			To:                   args.To,
			GasPrice:             args.GasPrice,
			MaxFeePerGas:         args.MaxFeePerGas,
			MaxPriorityFeePerGas: args.MaxPriorityFeePerGas,
			Value:                args.Value,
			Data:                 (*hexutil.Bytes)(&data),
		}
		pendingBlockNr := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.PendingBlockNumber)
		estimated, err := DoEstimateGas(ctx, api, callArgs, pendingBlockNr, rpcGasCap)
		if err != nil {
			return err
		}
		args.Gas = &estimated
	}
	if args.ChainID == nil {
		id := (*hexutil.Big)(api.GetChainConfig().ChainID)
		args.ChainID = id
	}
	return nil
}

// ToMessage converts TransactionArgs to an EVM message.
func (args *TransactionArgs) ToMessage(globalGasCap uint64, baseFee *big.Int) (transaction.Message, error) {
	// Reject invalid combinations of pre- and post-1559 fee styles
	if args.GasPrice != nil && (args.MaxFeePerGas != nil || args.MaxPriorityFeePerGas != nil) {
		return transaction.Message{}, errors.New("both gasPrice and (maxFeePerGas or maxPriorityFeePerGas) specified")
	}
	// Set sender address or use zero address if none specified.
	addr := args.from()

	// Set default gas & gas price if none were set
	gas := globalGasCap
	if gas == 0 {
		gas = uint64(math.MaxUint64 / 2)
	}
	if args.Gas != nil {
		gas = uint64(*args.Gas)
	}
	if globalGasCap != 0 && globalGasCap < gas {
		log.Warn("Caller gas above allowance, capping", "requested", gas, "cap", globalGasCap)
		gas = globalGasCap
	}
	var (
		gasPrice  *big.Int
		gasFeeCap *big.Int
		gasTipCap *big.Int
	)
	if baseFee == nil {
		// If there's no basefee, then it must be a non-1559 execution
		gasPrice = new(big.Int)
		if args.GasPrice != nil {
			gasPrice = args.GasPrice.ToInt()
		}
		gasFeeCap, gasTipCap = gasPrice, gasPrice
	} else {
		// A basefee is provided, necessitating 1559-type execution
		if args.GasPrice != nil {
			// User specified the legacy gas field, convert to 1559 gas typing
			gasPrice = args.GasPrice.ToInt()
			gasFeeCap, gasTipCap = gasPrice, gasPrice
		} else {
			// User specified 1559 gas fields (or none), use those
			gasFeeCap = new(big.Int)
			if args.MaxFeePerGas != nil {
				gasFeeCap = args.MaxFeePerGas.ToInt()
			}
			gasTipCap = new(big.Int)
			if args.MaxPriorityFeePerGas != nil {
				gasTipCap = args.MaxPriorityFeePerGas.ToInt()
			}
			// Backfill the legacy gasPrice for EVM execution, unless we're all zeroes
			gasPrice = new(big.Int)
			if gasFeeCap.BitLen() > 0 || gasTipCap.BitLen() > 0 {
				gasPrice = math.BigMin(new(big.Int).Add(gasTipCap, baseFee), gasFeeCap)
			}
		}
	}
	value := new(big.Int)
	if args.Value != nil {
		value = args.Value.ToInt()
	}
	data := args.data()
	var accessList transaction.AccessList
	if args.AccessList != nil {
		accessList = avmtypes.ToastAccessList(*args.AccessList)
	}
	val, is1 := uint256.FromBig(value)
	gp, is2 := uint256.FromBig(gasPrice)
	gfc, is3 := uint256.FromBig(gasFeeCap)
	gtc, is4 := uint256.FromBig(gasTipCap)
	if is1 || is2 || is3 || is4 {
		return transaction.Message{}, errors.New("args.Value higher than 2^256-1")
	}
	msg := transaction.NewMessage(addr, avmtypes.ToastAddress(args.To), 0, val, gas, gp, gfc, gtc, nil, nil, data, accessList, false, true)
	return msg, nil

}

// toTransaction assemble Transaction
func (args *TransactionArgs) toTransaction() (*transaction.Transaction, error) {
	var data transaction.TxData
	switch {
	case args.MaxFeePerGas != nil:
		al := transaction.AccessList{}
		if args.AccessList != nil {
			al = avmtypes.ToastAccessList(*args.AccessList)
		}
		dy := &transaction.DynamicFeeTx{
			To:         avmtypes.ToastAddress(args.To),
			Nonce:      uint64(*args.Nonce),
			Gas:        uint64(*args.Gas),
			Data:       args.data(),
			AccessList: al,
		}
		var overflow bool
		dy.GasFeeCap, overflow = uint256.FromBig((*big.Int)(args.MaxFeePerGas))
		if overflow {
			return nil, errors.New("maxFeePerGas overflows uint256")
		}
		dy.ChainID, overflow = uint256.FromBig((*big.Int)(args.ChainID))
		if overflow {
			return nil, errors.New("chainID overflows uint256")
		}
		dy.GasTipCap, overflow = uint256.FromBig((*big.Int)(args.MaxPriorityFeePerGas))
		if overflow {
			return nil, errors.New("maxPriorityFeePerGas overflows uint256")
		}
		dy.Value, overflow = uint256.FromBig((*big.Int)(args.Value))
		if overflow {
			return nil, errors.New("value overflows uint256")
		}
		data = dy
	case args.AccessList != nil:
		alt := &transaction.AccessListTx{
			To:    avmtypes.ToastAddress(args.To),
			Nonce: uint64(*args.Nonce),
			Gas:   uint64(*args.Gas),
			Data:  args.data(),
		}
		alt.AccessList = avmtypes.ToastAccessList(*args.AccessList)
		var overflow bool
		alt.GasPrice, overflow = uint256.FromBig((*big.Int)(args.GasPrice))
		if overflow {
			return nil, errors.New("gasPrice overflows uint256")
		}
		alt.ChainID, overflow = uint256.FromBig((*big.Int)(args.ChainID))
		if overflow {
			return nil, errors.New("chainID overflows uint256")
		}
		alt.Value, overflow = uint256.FromBig((*big.Int)(args.Value))
		if overflow {
			return nil, errors.New("value overflows uint256")
		}
		data = alt
	default:
		lt := &transaction.LegacyTx{
			To:    avmtypes.ToastAddress(args.To),
			Nonce: uint64(*args.Nonce),
			Gas:   uint64(*args.Gas),
			Data:  args.data(),
		}
		var overflow bool
		lt.GasPrice, overflow = uint256.FromBig((*big.Int)(args.GasPrice))
		if overflow {
			return nil, errors.New("gasPrice overflows uint256")
		}
		lt.Value, overflow = uint256.FromBig((*big.Int)(args.Value))
		if overflow {
			return nil, errors.New("value overflows uint256")
		}
		data = lt
	}
	return transaction.NewTx(data), nil
}

// newRPCPendingTransaction returns a pending transaction that will serialize to the RPC representation.
func newRPCPendingTransaction(tx *transaction.Transaction, current block.IHeader) *RPCTransaction {
	return newRPCTransaction(tx, types.Hash{}, 0, 0, big.NewInt(baseFee))
}

// yparity converts a V signature value to a YParity value for EIP-2718+ typed transactions.
func yparity(v *uint256.Int) *hexutil.Uint64 {
	if v == nil {
		return nil
	}
	parity := hexutil.Uint64(v.Uint64())
	return &parity
}

// setDynamicFeeFields sets the common fields shared by all EIP-1559+ typed transactions
// (DynamicFee, Blob, SetCode, PostQuantum): AccessList, ChainID, GasFeeCap, GasTipCap,
// YParity, and effective gas price computation.
func setDynamicFeeFields(result *RPCTransaction, tx *transaction.Transaction, v *uint256.Int, baseFee *big.Int, blockHash types.Hash) {
	al := avmtypes.FromastAccessList(tx.AccessList())
	if al == nil {
		al = avmtypes.AccessList{}
	}
	result.Accesses = &al
	result.ChainID = (*hexutil.Big)(tx.ChainId().ToBig())
	result.YParity = yparity(v)

	gasFeeCap := tx.GasFeeCap()
	gasTipCap := tx.GasTipCap()
	if gasFeeCap != nil {
		gasFeeCapBig := gasFeeCap.ToBig()
		result.GasFeeCap = (*hexutil.Big)(gasFeeCapBig)
		if gasTipCap != nil {
			gasTipCapBig := gasTipCap.ToBig()
			result.GasTipCap = (*hexutil.Big)(gasTipCapBig)
			// compute effective gas price: min(tip + baseFee, gasFeeCap) for mined transactions
			if baseFee != nil && blockHash != (types.Hash{}) {
				price := math.BigMin(new(big.Int).Add(gasTipCapBig, baseFee), gasFeeCapBig)
				result.GasPrice = (*hexutil.Big)(price)
			} else {
				result.GasPrice = (*hexutil.Big)(gasFeeCapBig)
			}
		} else {
			result.GasPrice = (*hexutil.Big)(gasFeeCapBig)
		}
	}
}

func rpcTransactionFrom(tx *transaction.Transaction) avmcommon.Address {
	var zero avmcommon.Address
	if tx == nil {
		return zero
	}
	if from := tx.From(); from != nil {
		return *avmtypes.FromastAddress(from)
	}
	signer := transaction.LatestSignerForChainID(uint256ToBigOrZero(tx.ChainId()))
	from, err := transaction.Sender(signer, tx)
	if err != nil {
		return zero
	}
	return *avmtypes.FromastAddress(&from)
}

// newRPCTransaction returns a transaction that will serialize to the RPC
// representation, with the given location metadata set (if available).
func newRPCTransaction(tx *transaction.Transaction, blockHash types.Hash, blockNumber uint64, index uint64, baseFee *big.Int) *RPCTransaction {

	v, r, s := tx.RawSignatureValues()
	hash := tx.Hash()
	result := &RPCTransaction{
		Type:     hexutil.Uint64(tx.Type()),
		From:     rpcTransactionFrom(tx),
		Gas:      hexutil.Uint64(tx.Gas()),
		GasPrice: (*hexutil.Big)(tx.GasPrice().ToBig()),
		Hash:     avmtypes.FromastHash(hash),
		Input:    hexutil.Bytes(tx.Data()),
		Nonce:    hexutil.Uint64(tx.Nonce()),
		To:       avmtypes.FromastAddress(tx.To()),
		Value:    (*hexutil.Big)(tx.Value().ToBig()),
		V:        (*hexutil.Big)(v.ToBig()),
		R:        (*hexutil.Big)(r.ToBig()),
		S:        (*hexutil.Big)(s.ToBig()),
	}
	if blockHash != (types.Hash{}) {
		hash := avmtypes.FromastHash(blockHash)
		result.BlockHash = &hash
		result.BlockNumber = (*hexutil.Big)(new(big.Int).SetUint64(blockNumber))
		result.TransactionIndex = (*hexutil.Uint64)(&index)
	}
	switch tx.Type() {
	case transaction.LegacyTxType:
		// if a legacy transaction has an EIP-155 chain id, include it explicitly
		if id := tx.ChainId(); id.Sign() != 0 {
			result.ChainID = (*hexutil.Big)(id.ToBig())
		}

	case transaction.AccessListTxType:
		al := avmtypes.FromastAccessList(tx.AccessList())
		if al == nil {
			al = avmtypes.AccessList{}
		}
		result.Accesses = &al
		result.ChainID = (*hexutil.Big)(tx.ChainId().ToBig())
		result.YParity = yparity(v)

	case transaction.DynamicFeeTxType:
		setDynamicFeeFields(result, tx, v, baseFee, blockHash)

	case transaction.BlobTxType:
		setDynamicFeeFields(result, tx, v, baseFee, blockHash)
		if blobFeeCap := tx.BlobFeeCap(); blobFeeCap != nil {
			result.MaxFeePerBlobGas = (*hexutil.Big)(blobFeeCap.ToBig())
		}
		blobHashes := tx.BlobHashes()
		if len(blobHashes) > 0 {
			result.BlobVersionedHashes = make([]avmcommon.Hash, len(blobHashes))
			for i, h := range blobHashes {
				result.BlobVersionedHashes[i] = avmtypes.FromastHash(h)
			}
		}

	case transaction.SetCodeTxType:
		setDynamicFeeFields(result, tx, v, baseFee, blockHash)
		authList := tx.AuthList()
		if len(authList) > 0 {
			result.AuthorizationList = make([]RPCAuthorization, 0, len(authList))
			for _, auth := range authList {
				if auth == nil {
					continue
				}
				rpcAuth := RPCAuthorization{
					ChainID: (*hexutil.Big)(auth.ChainID.ToBig()),
					Address: *avmtypes.FromastAddress(&auth.Address),
					Nonce:   hexutil.Uint64(auth.Nonce),
				}
				if auth.V != nil {
					rpcAuth.V = (*hexutil.Big)(auth.V.ToBig())
					rpcAuth.YParity = yparity(auth.V)
				}
				if auth.R != nil {
					rpcAuth.R = (*hexutil.Big)(auth.R.ToBig())
				}
				if auth.S != nil {
					rpcAuth.S = (*hexutil.Big)(auth.S.ToBig())
				}
				result.AuthorizationList = append(result.AuthorizationList, rpcAuth)
			}
		}

	case transaction.PostQuantumTxType:
		setDynamicFeeFields(result, tx, v, baseFee, blockHash)
	}
	return result
}

// setFeeDefaults fills in default fee values for unspecified tx fields.
func (args *TransactionArgs) setFeeDefaults(ctx context.Context, b *API) error {
	// If both gasPrice and at least one of the EIP-1559 fee parameters are specified, error.
	if args.GasPrice != nil && (args.MaxFeePerGas != nil || args.MaxPriorityFeePerGas != nil) {
		return errors.New("both gasPrice and (maxFeePerGas or maxPriorityFeePerGas) specified")
	}
	// If the tx has completely specified a fee mechanism, no default is needed. This allows users
	// who are not yet synced past London to get defaults for other tx values. See
	// https://github.com/ethereum/go-ethereum/pull/23274 for more information.
	eip1559ParamsSet := args.MaxFeePerGas != nil && args.MaxPriorityFeePerGas != nil
	if (args.GasPrice != nil && !eip1559ParamsSet) || (args.GasPrice == nil && eip1559ParamsSet) {
		// Sanity check the EIP-1559 fee parameters if present.
		if args.GasPrice == nil && args.MaxFeePerGas.ToInt().Cmp(args.MaxPriorityFeePerGas.ToInt()) < 0 {
			return fmt.Errorf("maxFeePerGas (%v) < maxPriorityFeePerGas (%v)", args.MaxFeePerGas, args.MaxPriorityFeePerGas)
		}
		return nil
	}
	// Now attempt to fill in default value depending on whether London is active or not.
	head := b.BlockChain().CurrentBlock()
	if head == nil {
		return errors.New("current block is nil")
	}
	headNumber := head.Number64()
	if headNumber == nil {
		return errors.New("current block number is nil")
	}
	if b.chainConfig.IsLondon(headNumber.Uint64()) {
		// London is active, set maxPriorityFeePerGas and maxFeePerGas.
		headHeader, ok := head.Header().(*block.Header)
		if !ok || headHeader == nil {
			return errors.New("invalid current block header type")
		}
		if err := args.setLondonFeeDefaults(ctx, headHeader, b); err != nil {
			return err
		}
	} else {
		if args.MaxFeePerGas != nil || args.MaxPriorityFeePerGas != nil {
			return errors.New("maxFeePerGas and maxPriorityFeePerGas are not valid before London is active")
		}
		// London not active, set gas price.
		if b == nil || b.gpo == nil {
			return errors.New("gas price oracle is unavailable")
		}
		price, err := b.gpo.SuggestTipCap(ctx, b.chainConfig)
		if err != nil {
			return err
		}
		args.GasPrice = (*hexutil.Big)(price)
	}
	return nil
}

// setLondonFeeDefaults fills in reasonable default fee values for unspecified fields.
func (args *TransactionArgs) setLondonFeeDefaults(ctx context.Context, head *block.Header, b *API) error {
	if head == nil {
		return errors.New("current block header is nil")
	}
	// Set maxPriorityFeePerGas if it is missing.
	if args.MaxPriorityFeePerGas == nil {
		if b == nil || b.gpo == nil {
			return errors.New("gas price oracle is unavailable")
		}
		tip, err := b.gpo.SuggestTipCap(ctx, b.chainConfig)
		if err != nil {
			return err
		}
		args.MaxPriorityFeePerGas = (*hexutil.Big)(tip)
	}
	// Set maxFeePerGas if it is missing.
	if args.MaxFeePerGas == nil {
		if head.BaseFee == nil {
			return errors.New("current block base fee is nil")
		}
		// Set the max fee to be 2 times larger than the previous block's base fee.
		// The additional slack allows the tx to not become invalidated if the base
		// fee is rising.
		val := new(big.Int).Add(
			args.MaxPriorityFeePerGas.ToInt(),
			new(big.Int).Mul(head.BaseFee.ToBig(), big.NewInt(2)),
		)
		args.MaxFeePerGas = (*hexutil.Big)(val)
	}
	// Both EIP-1559 fee parameters are now set; sanity check them.
	if args.MaxFeePerGas.ToInt().Cmp(args.MaxPriorityFeePerGas.ToInt()) < 0 {
		return fmt.Errorf("maxFeePerGas (%v) < maxPriorityFeePerGas (%v)", args.MaxFeePerGas, args.MaxPriorityFeePerGas)
	}
	return nil
}
