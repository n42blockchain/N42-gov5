package internal

import (
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

type executionRequestSystemContract struct {
	contract    types.Address
	requestType byte
}

type pragueSystemContractRegistry struct {
	historyStorage    types.Address
	executionRequests []executionRequestSystemContract
}

type SystemContractDeployment struct {
	Address types.Address
	Account conf.GenesisAccount
}

// resolvePragueSystemContractRegistry lists the system contracts a block at
// time calls. chainConfig decides the chain-specific parts (EIP-8282 is
// skipped on N42 native chains); it may be nil, which keeps the Ethereum set.
func resolvePragueSystemContractRegistry(chainConfig *params.ChainConfig, rules *params.Rules, time uint64) pragueSystemContractRegistry {
	if rules == nil || !rules.IsPrague {
		return pragueSystemContractRegistry{}
	}
	registry := pragueSystemContractRegistry{
		historyStorage: vm.HistoryStorageAddress,
		executionRequests: []executionRequestSystemContract{
			{contract: vm.WithdrawalRequestsAddress, requestType: vm.WithdrawalRequestType},
			{contract: vm.ConsolidationRequestsAddress, requestType: vm.ConsolidationRequestType},
		},
	}
	if rules.IsGlamsterdam && (chainConfig == nil || chainConfig.UsesEIP8282BuilderRequests(time)) {
		// EIP-8282: the builder deposit and exit queues are dequeued after
		// the Prague ones, in request-type order. N42 native chains skip
		// them (params.ChainConfig.UsesEIP8282BuilderRequests).
		registry.executionRequests = append(registry.executionRequests,
			executionRequestSystemContract{contract: vm.BuilderDepositRequestsAddress, requestType: vm.BuilderDepositRequestType},
			executionRequestSystemContract{contract: vm.BuilderExitRequestsAddress, requestType: vm.BuilderExitRequestType},
		)
	}
	return registry
}

func PragueSystemContractDeployments() []SystemContractDeployment {
	return []SystemContractDeployment{
		{
			Address: vm.WithdrawalRequestsAddress,
			Account: conf.GenesisAccount{
				Balance: "0x0",
				Nonce:   1,
				Code:    vm.WithdrawalRequestQueueCode,
			},
		},
		{
			Address: vm.ConsolidationRequestsAddress,
			Account: conf.GenesisAccount{
				Balance: "0x0",
				Nonce:   1,
				Code:    vm.ConsolidationRequestQueueCode,
			},
		},
		{
			Address: vm.HistoryStorageAddress,
			Account: conf.GenesisAccount{
				Balance: "0x0",
				Nonce:   1,
				Code:    vm.HistoryStorageCode,
			},
		},
	}
}
