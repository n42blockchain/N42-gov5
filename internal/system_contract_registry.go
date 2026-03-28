package internal

import (
	"github.com/n42blockchain/N42/common/types"
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
