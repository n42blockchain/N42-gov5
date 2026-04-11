// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

package evmsdk

import (
	"context"
	"fmt"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/hexutil"

	common2 "github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/consensus/apos"
	"github.com/n42blockchain/N42/internal/consensus/misc"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/ethdb/olddb"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

func verify(ctx context.Context, msg *state.EntireCode) (types.Hash, error) {
	codeMap := make(map[types.Hash][]byte)
	for _, pair := range msg.Codes {
		codeMap[pair.Hash] = pair.Code
	}

	readCodeF := func(hash types.Hash) ([]byte, error) {
		if code, ok := codeMap[hash]; ok {
			return code, nil
		}
		return nil, nil
	}

	hashMap := make(map[uint64]types.Hash)
	for _, h := range msg.Headers {
		hashMap[h.Number.Uint64()] = h.Hash()
	}
	getNumberHash := func(n uint64) types.Hash {
		if hash, ok := hashMap[n]; ok {
			return hash
		}
		return types.Hash{}
	}

	var txs transaction.Transactions
	for _, tByte := range msg.Entire.Transactions {
		tmp := &transaction.Transaction{}
		if err := tmp.Unmarshal(tByte); err != nil {
			return types.Hash{}, fmt.Errorf("unmarshal transaction failed: %w", err)
		}
		txs = append(txs, tmp)
	}

	body := &block.Body{
		Txs: txs,
	}

	blk := block.NewBlockFromStorage(msg.Entire.Header.Hash(), msg.Entire.Header, body)
	blockNumber, err := requireBlockNumber(blk.Number64())
	if err != nil {
		return types.Hash{}, err
	}
	batch := olddb.NewHashBatch(nil, ctx.Done(), "")
	defer batch.Rollback()
	old := make(map[string][]byte, len(msg.Entire.Snap.Items))
	for _, v := range msg.Entire.Snap.Items {
		old[string(v.Key)] = v.Value
	}
	stateReader := olddb.NewStateReader(old, nil, batch, blockNumber)
	stateReader.SetReadCodeF(readCodeF)
	ibs := state.New(stateReader)
	ibs.SetSnapshot(msg.Entire.Snap)
	ibs.SetHeight(blockNumber)
	ibs.SetGetOneFun(batch.GetOne)

	root, err := checkBlock2(getNumberHash, blk, ibs, msg.CoinBase, msg.Rewards)
	if err != nil {
		return types.Hash{}, fmt.Errorf("check block failed: %w", err)
	}
	return root, nil
}

func checkBlock2(getHashF func(n uint64) types.Hash, blk *block.Block, ibs *state.IntraBlockState, coinbase types.Address, rewards []*block.Reward) (types.Hash, error) {
	header := blk.Header().(*block.Header)
	blockNumber, err := requireBlockNumber(blk.Number64())
	if err != nil {
		return types.Hash{}, err
	}
	chainConfig := params.MainnetChainConfig
	if chainConfig.DAOForkSupport && chainConfig.DAOForkBlock != nil && chainConfig.DAOForkBlock.Cmp(new(uint256.Int).SetUint64(blockNumber).ToBig()) == 0 {
		misc.ApplyDAOHardFork(ibs)
	}
	noop := state.NewNoopWriter()

	usedGas := new(uint64)
	gp := new(common2.GasPool)
	gp.AddGas(blk.GasLimit())
	cfg := vm.Config{}
	engine := apos.NewFaker()
	for i, tx := range blk.Transactions() {
		// Security: handle nil To() for contract creation transactions
		toAddr := "nil"
		if tx.To() != nil {
			toAddr = tx.To().Hex()
		}
		simpleLog("apply transaction", "transactionIndex", i, "from", tx.From().Hex(), "to", toAddr, "value", tx.Value().Uint64(), "data", hexutil.Encode(tx.Data()), "gas", tx.Gas(), "gasPrice", tx.GasPrice().Uint64())
		//
		ibs.Prepare(tx.Hash(), blk.Hash(), i)
		_, _, err := internal.ApplyTransaction(chainConfig, getHashF, engine, &coinbase, gp, ibs, noop, header, tx, usedGas, cfg)
		if err != nil {
			return types.Hash{}, err
		}
	}

	if !cfg.StatelessExec && *usedGas != header.GasUsed {
		return types.Hash{}, fmt.Errorf("gas used by execution: %d, in header: %d", *usedGas, header.GasUsed)
	}

	if len(rewards) > 0 {
		for _, reward := range rewards {
			if reward.Amount != nil && !reward.Amount.IsZero() {
				if !ibs.Exist(reward.Address) {
					ibs.CreateAccount(reward.Address, false)
				}
				ibs.AddBalance(reward.Address, reward.Amount)
			}
		}
		ibs.SoftFinalise()
	}

	return ibs.IntermediateRoot(), nil
}

func requireBlockNumber(v *uint256.Int) (uint64, error) {
	if v == nil {
		return 0, fmt.Errorf("block number unavailable")
	}
	return v.Uint64(), nil
}
