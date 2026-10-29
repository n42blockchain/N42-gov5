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

func resolvePragueSystemContractRegistry(rules *params.Rules) pragueSystemContractRegistry {
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
	if rules.IsGlamsterdam {
		// EIP-8282: the builder deposit and exit queues are dequeued after
		// the Prague ones, in request-type order.
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
