package api

// bridgeApiT_test.go covers BridgeAPI's remaining branches in bridge_api.go:
// Status/RouteInfo against a real bridge.ZKRouter (not just the "no router
// configured" guard already covered elsewhere), plus bridgeStatusName's full
// switch.

import (
	"context"
	"testing"

	"github.com/holiman/uint256"
	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/bridge"
)

// bridgeApiTFakeDispatcher is a minimal in-process bridge.HyperlaneDispatcher
// so ZKRouter.Send succeeds on a Hyperlane-routed destination without any
// real Hyperlane Mailbox wiring.
type bridgeApiTFakeDispatcher struct{}

func (bridgeApiTFakeDispatcher) Dispatch(ctx context.Context, destDomain uint32, recipientAddr [32]byte, body []byte) (types.Hash, error) {
	return types.HexToHash("0x1234"), nil
}

func (bridgeApiTFakeDispatcher) QuoteDispatch(ctx context.Context, destDomain uint32, body []byte) (*uint256.Int, error) {
	return uint256.NewInt(1), nil
}

func TestBridgeAPIStatusAndRouteInfoWithRealRouter(t *testing.T) {
	router := bridge.NewZKRouter(nil, bridgeApiTFakeDispatcher{}, nil, nil, nil)
	defer router.Close()

	api := NewBridgeAPI(router)
	require.NotNil(t, api)
	require.Len(t, api.APIs(), 1)
	require.Equal(t, "bridge", api.APIs()[0].Namespace)

	recipient := types.HexToAddress("0x5000000000000000000000000000000000000005")
	txHash, err := api.Send(context.Background(), bridge.DomainArbitrum, recipient, uint256.NewInt(7))
	require.NoError(t, err)
	require.NotEqual(t, types.Hash{}, txHash)

	status, err := api.Status(context.Background(), txHash)
	require.NoError(t, err)
	require.NotNil(t, status)
	require.Equal(t, "pending", status.Status)
	require.Equal(t, uint8(bridge.StatusPending), status.Code)

	// RouteInfo against the default route table (Arbitrum is pre-seeded as
	// a Hyperlane route by defaultRouteTable).
	info, err := api.RouteInfo(context.Background(), bridge.DomainArbitrum)
	require.NoError(t, err)
	require.NotNil(t, info)
	require.Equal(t, "Hyperlane", info.RouteType)
	require.Equal(t, "Arbitrum", info.Name)

	// Unknown destination chain: no route configured.
	_, err = api.RouteInfo(context.Background(), 0xFFFFFFFE)
	require.Error(t, err)

	_, err = api.LatestVerifiedBlock(context.Background(), bridge.DomainArbitrum)
	// Either succeeds (0, nil light client state) or fails cleanly; either
	// way the router != nil branch is exercised.
	_ = err
}

func TestBridgeAPISendAndStatusGuardsWithNilRouter(t *testing.T) {
	api := NewBridgeAPI(nil)

	_, err := api.Send(context.Background(), 1, types.Address{}, uint256.NewInt(1))
	require.Error(t, err)

	_, err = api.Status(context.Background(), types.Hash{})
	require.Error(t, err)

	_, err = api.LatestVerifiedBlock(context.Background(), 1)
	require.Error(t, err)

	_, err = api.RouteInfo(context.Background(), 1)
	require.Error(t, err)

	_, err = api.Send(context.Background(), 1, types.Address{}, nil)
	require.Error(t, err)

	_, err = api.Send(context.Background(), 1, types.Address{}, uint256.NewInt(0))
	require.Error(t, err)
}

func TestBridgeStatusNameCoversAllStatuses(t *testing.T) {
	cases := map[bridge.BridgeStatus]string{
		bridge.StatusPending:   "pending",
		bridge.StatusProving:   "proving",
		bridge.StatusSubmitted: "submitted",
		bridge.StatusVerified:  "verified",
		bridge.StatusCompleted: "completed",
		bridge.StatusFailed:    "failed",
		bridge.BridgeStatus(99): "unknown",
	}
	for status, want := range cases {
		require.Equal(t, want, bridgeStatusName(status))
	}
}
