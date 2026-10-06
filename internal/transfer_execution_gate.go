package internal

import (
	"os"
	"reflect"
	"strconv"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus"
	vm "github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// Opt-in while full block and fleet qualification is in progress. Default off.
var transferExecutionWorkers = parseTransferExecutionWorkers(os.Getenv("N42_TRANSFER_COMPONENT_WORKERS"))

func parseTransferExecutionWorkers(value string) int {
	n, err := strconv.Atoi(value)
	if err != nil || n < 2 || n > 32 {
		return 0
	}
	return n
}

// tryTransferExecution is called only after declared sender verification and
// block-start operations. It keeps unqualified readers/observers on the serial
// path. Errors are fatal, including any failure after merge has begun.
func tryTransferExecution(config *params.ChainConfig, engine consensus.Engine, header *block.Header, blockHash types.Hash, txs transaction.Transactions, ibs *state.IntraBlockState, cfg vm.Config, workers int) (block.Receipts, bool, error) {
	if workers < 2 || !reflect.DeepEqual(cfg, vm.Config{}) || !ibs.CanMergeEOAAccounts() {
		return nil, false, nil
	}
	switch ibs.GetStateReader().(type) {
	case *state.PlainStateReader, *blockAccountPrefetchReader:
		// These readers are only accessed on the owner during capture. Workers
		// receive AccountSnapshot, never their MDBX transaction or cursor.
	default:
		return nil, false, nil
	}
	candidate, err := speculateTransfers(config, engine, header, blockHash, txs, ibs, workers)
	if err != nil || candidate == nil {
		return nil, false, err
	}
	installed, err := ibs.MergeEOAAccounts(candidate.base, candidate.accounts, config.RulesWithTimestamp(header.Number.Uint64(), header.Time))
	if err != nil || !installed {
		return nil, false, err
	}
	// Preserve the context left by the last serial transaction for block-end
	// operations and any logs they attach to the state's current transaction.
	last := len(txs) - 1
	ibs.Prepare(txs[last].Hash(), blockHash, last)
	return candidate.receipts, true, nil
}
