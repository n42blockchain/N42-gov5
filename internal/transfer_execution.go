package internal

import (
	"fmt"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	vm "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// transferExecution is a speculative result, not yet installed in canonical
// state. Accounts contains the exact executed dirty set; nil means deleted.
type transferExecution struct {
	receipts block.Receipts
	accounts map[types.Address]*account.StateAccount
	base     *state.AccountSnapshot
}

// speculateTransfers executes restricted paid EOA transfers from the caller's
// current state. It never installs worker writes into that state. A nil result
// with nil error means unsupported: the caller must execute serially. A reader
// error remains fatal. Sender declarations must already have been verified, and
// the caller must establish a transaction boundary without witness/BAL/hooks.
// The opt-in import gate qualifies the reader and observer boundary separately.
func speculateTransfers(config *params.ChainConfig, engine consensus.Engine, header *block.Header, blockHash types.Hash, txs transaction.Transactions, ibs *state.IntraBlockState, workers int) (*transferExecution, error) {
	if config == nil || header == nil || header.Number == nil || engine == nil || engine.Type() != params.HotStuffConsensus || config.UsesParliaRules() || workers < 2 || workers > 32 || len(txs) < 2 || len(txs) > maxAccountPrefetchAddresses {
		return nil, nil
	}
	rules := config.RulesWithTimestamp(header.Number.Uint64(), header.Time)
	if config.IsBAL(header.Time) || uint64(len(txs)) > header.GasLimit/21000 || uint64(len(txs))*21000 != header.GasUsed {
		return nil, nil
	}
	context := NewEVMBlockContext(header, func(uint64) types.Hash { return types.Hash{} }, engine, config, nil)
	fees := map[types.Address]bool{context.Coinbase: true}
	if rules.IsLondon && rules.IsEip1559FeeCollector {
		if config.Eip1559FeeCollector == nil {
			return nil, nil
		}
		fees[*config.Eip1559FeeCollector] = true
	}
	forbidden := map[types.Address]bool{consensus.SystemAddress: true}
	for _, a := range vm.ActivePrecompiles(rules) {
		forbidden[a] = true
	}
	edges := make([][2]types.Address, len(txs))
	seen := make(map[types.Address]bool)
	keys := make([]types.Address, 0, len(txs)+len(fees))
	add := func(a types.Address) {
		if !seen[a] {
			seen[a] = true
			keys = append(keys, a)
		}
	}
	var feeBound uint256.Int
	for i, tx := range txs {
		if tx == nil || tx.From() == nil || tx.To() == nil || tx.Gas() != 21000 || len(tx.Data()) != 0 || len(tx.AccessList()) != 0 || tx.Value().IsZero() || tx.GasPrice().IsZero() || (tx.Type() != transaction.LegacyTxType && tx.Type() != transaction.DynamicFeeTxType) {
			return nil, nil
		}
		edges[i] = [2]types.Address{*tx.From(), *tx.To()}
		for _, a := range edges[i] {
			if fees[a] || forbidden[a] {
				return nil, nil
			}
			add(a)
		}
		// Conservative bound for BOTH fee destinations, even when they alias.
		// Reject wrapping credits before independent lanes can hide overflow.
		var bound uint256.Int
		if _, overflow := bound.MulOverflow(tx.GasFeeCap(), uint256.NewInt(42000)); overflow {
			return nil, nil
		}
		if _, overflow := feeBound.AddOverflow(&feeBound, &bound); overflow {
			return nil, nil
		}
	}
	groups := transferComponents(edges)
	if len(groups) < 2 {
		return nil, nil
	}
	// Fee addresses are appended in deterministic order, including aliases.
	add(context.Coinbase)
	if rules.IsLondon && rules.IsEip1559FeeCollector {
		add(*config.Eip1559FeeCollector)
	}
	base, err := ibs.SnapshotAccounts(keys)
	if err != nil {
		return nil, err
	}
	initialFees := make(map[types.Address]*account.StateAccount, len(fees))
	for _, a := range keys {
		data, err := base.ReadAccountData(a)
		if err != nil {
			return nil, err
		}
		if data != nil && !data.IsEmptyCodeHash() {
			return nil, nil
		}
		if fees[a] {
			initialFees[a] = data
			if data != nil {
				var bound uint256.Int
				if _, overflow := bound.AddOverflow(&data.Balance, &feeBound); overflow {
					return nil, nil
				}
			}
		}
	}
	result := &transferExecution{receipts: make(block.Receipts, len(txs)), accounts: make(map[types.Address]*account.StateAccount, len(keys)), base: base}
	outputs := make([]map[types.Address]*account.StateAccount, len(groups))
	errs := make([]error, len(groups))
	jobs := make(chan int, len(groups))
	var wg sync.WaitGroup
	for w := 0; w < min(workers, len(groups)); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for gi := range jobs {
				s := state.New(base)
				evm := vm.NewEVM(context, evmtypes.TxContext{}, s, config, vm.Config{})
				gp := new(common.GasPool).AddGas(header.GasLimit)
				var gas uint64
				for _, i := range groups[gi] {
					s.Prepare(txs[i].Hash(), blockHash, i)
					r, _, err := ApplyTransactionWithEVM(evm, config, engine, gp, s, state.NewNoopWriter(), header, txs[i], &gas, vm.Config{})
					if err != nil {
						errs[gi] = err
						break
					}
					if r == nil || r.Status != block.ReceiptStatusSuccessful || r.GasUsed != 21000 || len(r.Logs) != 0 {
						errs[gi] = fmt.Errorf("unsupported transfer execution at %d", i)
						break
					}
					result.receipts[i] = r
				}
				if errs[gi] == nil && gas != uint64(len(groups[gi]))*21000 {
					errs[gi] = fmt.Errorf("unsupported block gas accounting")
				}
				if errs[gi] != nil {
					continue
				}
				dirty := s.DirtyAddresses()
				final, err := s.SnapshotAccounts(dirty)
				if err != nil {
					errs[gi] = err
					continue
				}
				out := make(map[types.Address]*account.StateAccount, len(dirty))
				for _, a := range dirty {
					out[a], err = final.ReadAccountData(a)
					if err != nil {
						errs[gi] = err
						break
					}
				}
				outputs[gi] = out
			}
		}()
	}
	for i := range groups {
		jobs <- i
	}
	close(jobs)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			// Re-execute on the original state for canonical error attribution.
			return nil, nil
		}
	}
	for _, out := range outputs {
		for a, data := range out {
			if !seen[a] {
				return nil, nil
			}
			previous, exists := result.accounts[a]
			if !fees[a] {
				if exists {
					return nil, nil
				}
				result.accounts[a] = data
				continue
			}
			if !exists {
				result.accounts[a] = data
				continue
			}
			if data == nil {
				continue // Only an empty, zero-credit fee account can disappear.
			}
			if previous == nil {
				result.accounts[a] = data
				continue
			}
			var delta uint256.Int
			delta.Set(&data.Balance)
			if original := initialFees[a]; original != nil {
				delta.Sub(&delta, &original.Balance)
			}
			if _, overflow := previous.Balance.AddOverflow(&previous.Balance, &delta); overflow {
				return nil, nil
			}
		}
	}
	var gas uint64
	for _, receipt := range result.receipts {
		if receipt == nil {
			return nil, nil
		}
		gas += receipt.GasUsed
		receipt.CumulativeGasUsed = gas
	}
	return result, nil
}
