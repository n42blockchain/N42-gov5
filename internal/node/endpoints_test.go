package node

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestCheckModuleAvailability(t *testing.T) {
	apis := []jsonrpc.API{
		{Namespace: "eth"},
		{Namespace: "eth"}, // duplicate namespace must be deduped
		{Namespace: "net"},
	}

	bad, available := checkModuleAvailability([]string{"eth", "net", "web3"}, apis)
	require.ElementsMatch(t, []string{"web3"}, bad)
	require.ElementsMatch(t, []string{"eth", "net"}, available)
}

func TestCheckModuleAvailabilityAllowsJSONRPCApiImplicitly(t *testing.T) {
	bad, available := checkModuleAvailability([]string{jsonrpc.JSONRPCApi}, nil)
	require.Empty(t, bad)
	require.Empty(t, available)
}

func TestCheckModuleAvailabilityNoModulesRequested(t *testing.T) {
	apis := []jsonrpc.API{{Namespace: "eth"}}
	bad, available := checkModuleAvailability(nil, apis)
	require.Empty(t, bad)
	require.Equal(t, []string{"eth"}, available)
}
