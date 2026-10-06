package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	"github.com/n42blockchain/N42/conf"
)

// nodeTAuthRPCConfig builds the minimal private-chain config with the
// JWT-authenticated RPC server enabled on an ephemeral loopback port.
func nodeTAuthRPCConfig(dataDir string) *conf.Config {
	cfg := nodeTMinimalConfig(dataDir)
	cfg.NodeCfg.AuthRPC = true
	cfg.NodeCfg.AuthAddr = "127.0.0.1"
	cfg.NodeCfg.AuthPort = 0
	return cfg
}

func nodeTAuthRPCPost(t *testing.T, addr, token string) *http.Response {
	t.Helper()
	body, err := json.Marshal(map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "engine_exchangeCapabilities",
		"params":  []interface{}{[]string{}},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, "http://"+addr, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("http post: %v", err)
	}
	return resp
}

func nodeTSignJWT(t *testing.T, secret []byte) string {
	t.Helper()
	claims := jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now())}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(secret)
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}

func TestNewNodeAuthRPCValidAndInvalidToken(t *testing.T) {
	dir := t.TempDir()
	cfg := nodeTAuthRPCConfig(dir)

	n, err := NewNode(nodeTNewContext(), cfg)
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer n.Close()

	if err := n.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	addr := n.httpAuth.listenAddr()
	if addr == "" {
		t.Fatal("expected auth HTTP RPC server to report a bound address")
	}

	secretHex, err := os.ReadFile(filepath.Join(dir, datadirJWTKey))
	if err != nil {
		t.Fatalf("read persisted jwt secret: %v", err)
	}
	trimmed := bytes.TrimPrefix(bytes.TrimSpace(secretHex), []byte("0x"))
	secret, err := hex.DecodeString(string(trimmed))
	if err != nil {
		t.Fatalf("decode jwt secret: %v", err)
	}

	// Valid token: the auth endpoint accepts the request (status 200 — the
	// JSON body may still carry a method-not-found error, that's the RPC
	// layer, not the JWT middleware).
	validResp := nodeTAuthRPCPost(t, addr, nodeTSignJWT(t, secret))
	defer validResp.Body.Close()
	if validResp.StatusCode != http.StatusOK {
		t.Fatalf("valid token: expected 200, got %d", validResp.StatusCode)
	}

	// Invalid token: wrong signing secret must be rejected by the JWT
	// middleware before reaching the RPC layer.
	invalidResp := nodeTAuthRPCPost(t, addr, nodeTSignJWT(t, []byte("wrong-secret-wrong-secret-wrong")))
	defer invalidResp.Body.Close()
	if invalidResp.StatusCode == http.StatusOK {
		t.Fatalf("invalid token: expected rejection, got 200")
	}

	// No token at all must also be rejected.
	noTokenResp := nodeTAuthRPCPost(t, addr, "")
	defer noTokenResp.Body.Close()
	if noTokenResp.StatusCode == http.StatusOK {
		t.Fatalf("missing token: expected rejection, got 200")
	}
}
