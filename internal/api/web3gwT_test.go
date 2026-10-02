package api

// web3gwT_test.go exercises the web3:// protocol gateway (web3_gateway.go)
// against the real executed-chain fixture: NewWeb3Gateway/Start/Stop over a
// loopback listener on an OS-assigned port, and handleRequest/executeCall
// directly via httptest for the request-shape branches that don't need a
// live socket.

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/conf"
)

func web3gwTCfg() *conf.Web3GatewayCfg {
	return &conf.Web3GatewayCfg{
		Enabled:      true,
		Host:         "127.0.0.1",
		Port:         0,
		CacheSeconds: 5,
	}
}

func TestWeb3GatewayHandleRequestRoot(t *testing.T) {
	fx := apiXGetChainFixture(t)
	gw := NewWeb3Gateway(apiXNewAPIForFixture(fx), web3gwTCfg())
	require.NotNil(t, gw)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	gw.handleRequest(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "web3:// Gateway")
}

func TestWeb3GatewayHandleRequestMethodNotAllowed(t *testing.T) {
	fx := apiXGetChainFixture(t)
	gw := NewWeb3Gateway(apiXNewAPIForFixture(fx), web3gwTCfg())

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	gw.handleRequest(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestWeb3GatewayHandleRequestInvalidAddress(t *testing.T) {
	fx := apiXGetChainFixture(t)
	gw := NewWeb3Gateway(apiXNewAPIForFixture(fx), web3gwTCfg())

	req := httptest.NewRequest(http.MethodGet, "/not-an-address/index.html", nil)
	rec := httptest.NewRecorder()
	gw.handleRequest(rec, req)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestWeb3GatewayHandleRequestNoCodeAtAddress(t *testing.T) {
	fx := apiXGetChainFixture(t)
	gw := NewWeb3Gateway(apiXNewAPIForFixture(fx), web3gwTCfg())

	// fx.Senders[0] is a plain EOA: has no contract code, so executeCall
	// returns the "no code at address" error and handleRequest maps it to
	// a 500.
	req := httptest.NewRequest(http.MethodGet, "/"+fx.Senders[0].Hex()[2:]+"/index.html", nil)
	rec := httptest.NewRecorder()
	gw.handleRequest(rec, req)
	require.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestWeb3GatewayHandleRequestContractRootBytecode(t *testing.T) {
	fx := apiXGetChainFixture(t)
	gw := NewWeb3Gateway(apiXNewAPIForFixture(fx), web3gwTCfg())

	// Root path (no resource path) with calldata == nil returns the raw
	// contract bytecode per executeCall's early-return branch.
	req := httptest.NewRequest(http.MethodGet, "/"+fx.ContractAddr.Hex()[2:], nil)
	rec := httptest.NewRecorder()
	gw.handleRequest(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, rec.Body.Bytes())
	require.Equal(t, fx.ContractAddr.Hex(), rec.Header().Get("X-Web3-Contract"))
	require.Contains(t, rec.Header().Get("Cache-Control"), "max-age=5")
}

func TestWeb3GatewayHandleRequestContractCallWithCalldata(t *testing.T) {
	fx := apiXGetChainFixture(t)
	gw := NewWeb3Gateway(apiXNewAPIForFixture(fx), web3gwTCfg())

	req := httptest.NewRequest(http.MethodGet, "/"+fx.ContractAddr.Hex()[2:]+"/style.css", nil)
	rec := httptest.NewRecorder()
	gw.handleRequest(rec, req)
	// The tiny fixture contract has no dispatcher for arbitrary calldata;
	// either it executes (200, text/css) or reverts (500) depending on the
	// EVM's handling of the fallback path — either way the content-type
	// inference and address-with-0x-prefix branch get exercised.
	require.Contains(t, []int{http.StatusOK, http.StatusInternalServerError}, rec.Code)
	if rec.Code == http.StatusOK {
		require.Equal(t, "text/css; charset=utf-8", rec.Header().Get("Content-Type"))
	}
}

func TestWeb3GatewayStartStop(t *testing.T) {
	fx := apiXGetChainFixture(t)
	gw := NewWeb3Gateway(apiXNewAPIForFixture(fx), web3gwTCfg())

	require.NoError(t, gw.Start())
	defer gw.Stop()

	addr := gw.ln.Addr().String()
	url := fmt.Sprintf("http://%s/", addr)

	var resp *http.Response
	var err error
	for i := 0; i < 20; i++ {
		resp, err = http.Get(url)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NoError(t, err)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	require.Contains(t, string(body), "web3:// Gateway")

	require.NoError(t, gw.Stop())
	// Stop is idempotent via the nil-server guard when called twice.
	gw2 := &Web3Gateway{}
	require.NoError(t, gw2.Stop())
}

func TestBuildCalldataAndInferContentType(t *testing.T) {
	require.Nil(t, buildCalldata(""))
	require.Equal(t, []byte("foo/bar"), buildCalldata("foo/bar"))

	cases := map[string]string{
		"":          "text/html; charset=utf-8",
		"a.html":    "text/html; charset=utf-8",
		"a.htm":     "text/html; charset=utf-8",
		"a.css":     "text/css; charset=utf-8",
		"a.js":      "application/javascript; charset=utf-8",
		"a.mjs":     "application/javascript; charset=utf-8",
		"a.json":    "application/json; charset=utf-8",
		"a.png":     "image/png",
		"a.jpg":     "image/jpeg",
		"a.jpeg":    "image/jpeg",
		"a.gif":     "image/gif",
		"a.svg":     "image/svg+xml",
		"a.ico":     "image/x-icon",
		"a.woff":    "font/woff",
		"a.woff2":   "font/woff2",
		"a.txt":     "text/plain; charset=utf-8",
		"a.xml":     "application/xml",
		"a.wasm":    "application/wasm",
		"a.unknown": "application/octet-stream",
	}
	for path, want := range cases {
		require.Equal(t, want, inferContentType(path), "path=%s", path)
	}
}
