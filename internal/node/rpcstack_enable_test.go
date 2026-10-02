package node

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPServerEnableDisableRPC(t *testing.T) {
	h := newHTTPServer()
	require.False(t, h.rpcAllowed())

	require.NoError(t, h.enableRPC(nil, httpConfig{}))
	require.True(t, h.rpcAllowed())

	err := h.enableRPC(nil, httpConfig{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already enabled")

	require.True(t, h.disableRPC())
	require.False(t, h.rpcAllowed())
	require.False(t, h.disableRPC()) // second call: nothing to disable
}

func TestHTTPServerEnableDisableWS(t *testing.T) {
	h := newHTTPServer()
	require.False(t, h.wsAllowed())

	require.NoError(t, h.enableWS(nil, wsConfig{}))
	require.True(t, h.wsAllowed())

	err := h.enableWS(nil, wsConfig{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already enabled")

	require.True(t, h.disableWS())
	require.False(t, h.wsAllowed())
	require.False(t, h.disableWS())
}

func TestHTTPServerStopWSAlsoStopsHTTPWhenOnlyWS(t *testing.T) {
	h := newHTTPServer()
	require.NoError(t, h.enableWS(nil, wsConfig{}))
	// doStop is a no-op without a real listener, but stopWS exercises the
	// disableWS + "not rpcAllowed -> doStop" branch regardless.
	h.stopWS()
	require.False(t, h.wsAllowed())
}

func TestHTTPServerStopNoListenerIsNoop(t *testing.T) {
	h := newHTTPServer()
	require.NotPanics(t, func() { h.stop() })
}

func TestRegisterApisFromWhitelistSkipsUnlistedModules(t *testing.T) {
	// exposeAll=false and no modules whitelisted -> nothing registered, no error.
	require.NoError(t, RegisterApisFromWhitelist(nil, nil, nil, false))
}
