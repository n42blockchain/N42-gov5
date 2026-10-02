package node

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsWebsocket(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	require.False(t, isWebsocket(req))

	req.Header.Set("Upgrade", "WebSocket")
	req.Header.Set("Connection", "Upgrade")
	require.True(t, isWebsocket(req))

	req2 := httptest.NewRequest("GET", "/", nil)
	req2.Header.Set("Upgrade", "websocket")
	req2.Header.Set("Connection", "keep-alive, Upgrade")
	require.True(t, isWebsocket(req2))

	req3 := httptest.NewRequest("GET", "/", nil)
	req3.Header.Set("Upgrade", "websocket")
	require.False(t, isWebsocket(req3)) // missing Connection: upgrade
}

func TestCheckPath(t *testing.T) {
	root := httptest.NewRequest("GET", "/", nil)
	require.True(t, checkPath(root, ""))

	other := httptest.NewRequest("GET", "/foo", nil)
	require.False(t, checkPath(other, ""))

	prefixed := httptest.NewRequest("GET", "/ws/sub", nil)
	require.True(t, checkPath(prefixed, "/ws"))
	require.False(t, checkPath(prefixed, "/wsxxx"))

	short := httptest.NewRequest("GET", "/w", nil)
	require.False(t, checkPath(short, "/ws"))
}

func TestValidatePrefix(t *testing.T) {
	require.NoError(t, validatePrefix("http", ""))
	require.NoError(t, validatePrefix("http", "/ws"))

	err := validatePrefix("http", "ws")
	require.Error(t, err)
	require.Contains(t, err.Error(), "leading")

	err = validatePrefix("http", "/ws?x=1")
	require.Error(t, err)
	require.Contains(t, err.Error(), "metadata-characters")

	err = validatePrefix("http", "/ws#frag")
	require.Error(t, err)
}
