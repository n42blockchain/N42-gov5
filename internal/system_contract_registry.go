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
	return pragueSystemContractRegistry{
		historyStorage: vm.HistoryStorageAddress,
		executionRequests: []executionRequestSystemContract{
			{contract: vm.WithdrawalRequestsAddress, requestType: vm.WithdrawalRequestType},
			{contract: vm.ConsolidationRequestsAddress, requestType: vm.ConsolidationRequestType},
		},
	}
}

func PragueSystemContractDeployments() []SystemContractDeployment {
	return []SystemContractDeployment{
		{
			Address: vm.WithdrawalRequestsAddress,
			Account: conf.GenesisAccount{
				Balance: "0x0",
				Nonce:   1,
				Code:    vm.ExecutionRequestQueueCode,
			},
		},
		{
			Address: vm.ConsolidationRequestsAddress,
			Account: conf.GenesisAccount{
				Balance: "0x0",
				Nonce:   1,
				Code:    vm.ExecutionRequestQueueCode,
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
