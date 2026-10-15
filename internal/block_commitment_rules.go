// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"errors"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/params"
)

// ConfigureBlockCommitmentRules selects the existing process-local root/fork
// rules before any producer or validator starts. Exactly one chain may own these
// globals per process. Metadata-only validators call it without constructing an
// execution BlockChain; ordinary NewBlockChain startup uses the same path.
func ConfigureBlockCommitmentRules(config *params.ChainConfig) error {
	// Both QMDB and Ethereum MPT use standard MPT roots over RLP transactions.
	// Other native schemes retain proto roots for historical hash continuity.
	block.UseEthereumTxRoot = config != nil && (config.StateScheme == string(params.StateCommitmentPresetQMDB) || config.StateScheme == string(params.StateCommitmentPresetEthereumMPT))
	block.TxRootBlake3Time = txRootBlake3Time(config)
	block.DeferredExecutionTime = deferredExecutionTime(config)
	if block.DeferredExecutionTime != 0 && !nativeQMDBChain(config) {
		return errors.New("deferredExecutionTime is set but the chain is not the native QMDB chain: the stored execution results and the applied marker it relies on exist only there")
	}
	return nil
}
