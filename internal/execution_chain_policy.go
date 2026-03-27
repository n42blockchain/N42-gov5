package internal

import (
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm/evmtypes"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

type executionChainPolicy struct {
	useBorTransfers         bool
	useBorSystemCallContext bool
}

func newExecutionChainPolicy(chainCfg *params.ChainConfig, consensusType params.ConsensusType) executionChainPolicy {
	return executionChainPolicy{
		useBorTransfers:         consensusType.UsesBorTransferMode(),
		useBorSystemCallContext: chainCfg.UsesBorSystemCallContext(),
	}
}

func (p executionChainPolicy) transferFunc() evmtypes.TransferFunc {
	if p.useBorTransfers {
		return BorTransfer
	}
	return Transfer
}

func (p executionChainPolicy) systemCallContext(header *block.Header, msg Message) (*types.Address, evmtypes.TxContext) {
	if p.useBorSystemCallContext {
		return &header.Coinbase, evmtypes.TxContext{}
	}
	return &state.SystemAddress, NewEVMTxContext(msg)
}

func (p executionChainPolicy) shouldIgnoreSystemCallError() bool {
	return p.useBorSystemCallContext
}
