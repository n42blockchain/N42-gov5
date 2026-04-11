package internal

import (
	"math/big"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/params"
)

func TestResolvePragueSystemContractRegistryPrePragueIsEmpty(t *testing.T) {
	t.Parallel()

	cfg := &params.ChainConfig{
		ChainID:     big.NewInt(1),
		Consensus:   params.Faker,
		LondonBlock: big.NewInt(0),
	}
	rules := cfg.RulesWithTimestamp(1, 1)

	registry := resolvePragueSystemContractRegistry(rules)
	require.Zero(t, registry.historyStorage)
	require.Nil(t, registry.executionRequests)
}

func TestResolvePragueSystemContractRegistryReturnsCanonicalContracts(t *testing.T) {
	t.Parallel()

	rules := testPragueConfig().RulesWithTimestamp(1, 1)
	registry := resolvePragueSystemContractRegistry(rules)

	require.Equal(t, vm.HistoryStorageAddress, registry.historyStorage)
	require.Equal(t, []executionRequestSystemContract{
		{contract: vm.WithdrawalRequestsAddress, requestType: vm.WithdrawalRequestType},
		{contract: vm.ConsolidationRequestsAddress, requestType: vm.ConsolidationRequestType},
	}, registry.executionRequests)
}

func TestPragueSystemContractDeploymentsReturnCanonicalAlloc(t *testing.T) {
	t.Parallel()

	deployments := PragueSystemContractDeployments()
	require.Len(t, deployments, 3)
	require.Equal(t, vm.WithdrawalRequestsAddress, deployments[0].Address)
	require.Equal(t, vm.WithdrawalRequestQueueCode, deployments[0].Account.Code)
	require.EqualValues(t, 1, deployments[0].Account.Nonce)
	require.Equal(t, vm.ConsolidationRequestsAddress, deployments[1].Address)
	require.Equal(t, vm.ConsolidationRequestQueueCode, deployments[1].Account.Code)
	require.Equal(t, vm.HistoryStorageAddress, deployments[2].Address)
	require.Equal(t, vm.HistoryStorageCode, deployments[2].Account.Code)
}
