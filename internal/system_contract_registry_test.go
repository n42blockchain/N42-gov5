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
