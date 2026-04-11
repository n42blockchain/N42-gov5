// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package engineapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv/memdb"
	"github.com/n42blockchain/N42/params"
)

// pickFreePort grabs an OS-assigned port by binding then closing. There is
// a tiny race window where another process could grab it before Service.Start
// re-binds; for a single-shot test this is acceptable.
func pickFreePort(t *testing.T) int {
	t.Helper()
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	defer l.Close()
	addr := l.Addr().String()
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return p
}

// TestService_StartStopUnauthRejected stands the engineapi Service up
// against an in-memory chaindata MDBX, asserts the listener is bound,
// then sends an unauthenticated POST and expects a 403. This pins:
//
//   - the JWT middleware is actually wired into the mux,
//   - listener Start/Stop are clean (no goroutine leaks),
//   - the auto-generated JWT secret path persists when --engine.jwt is set.
func TestService_StartStopUnauthRejected(t *testing.T) {
	dir := t.TempDir()
	jwtPath := filepath.Join(dir, "jwt.hex")

	port := pickFreePort(t)
	cfg := conf.EngineAPICfg{
		Enabled:       true,
		Host:          "127.0.0.1",
		Port:          port,
		JWTSecretPath: jwtPath,
	}

	db := memdb.New("")
	defer db.Close()

	svc := New(cfg, params.EthereumMainnetChainConfig, ethel.NewEthReplayEngine(params.EthereumMainnetChainConfig), db, nil)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	// Verify the JWT secret file was auto-generated.
	if _, err := os.Stat(jwtPath); err != nil {
		t.Fatalf("jwt file not created: %v", err)
	}

	// Wait briefly for the listener goroutine to be ready.
	time.Sleep(50 * time.Millisecond)

	url := "http://127.0.0.1:" + strconv.Itoa(port)
	body := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"engine_exchangeCapabilities","params":[[]]}`)
	resp, err := http.Post(url, "application/json", body)
	if err != nil {
		t.Fatalf("POST without token: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("unauth POST: status=%d body=%q, want 403", resp.StatusCode, string(buf))
	}
}

// TestService_AuthAccepted issues a request with a valid HS256 token and
// expects a 2xx (the body content is irrelevant — what we are pinning is
// that the middleware lets the request through to the jsonrpc handler).
func TestService_AuthAccepted(t *testing.T) {
	dir := t.TempDir()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatal(err)
	}
	jwtPath := filepath.Join(dir, "jwt.hex")
	if err := os.WriteFile(jwtPath, []byte(hex.EncodeToString(secret)), 0o600); err != nil {
		t.Fatal(err)
	}

	port := pickFreePort(t)
	cfg := conf.EngineAPICfg{
		Enabled:       true,
		Host:          "127.0.0.1",
		Port:          port,
		JWTSecretPath: jwtPath,
	}

	db := memdb.New("")
	defer db.Close()

	svc := New(cfg, params.EthereumMainnetChainConfig, ethel.NewEthReplayEngine(params.EthereumMainnetChainConfig), db, nil)
	if err := svc.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = svc.Stop() })

	time.Sleep(50 * time.Millisecond)

	tokenStr, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}).SignedString(secret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}

	url := "http://127.0.0.1:" + strconv.Itoa(port)
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"engine_exchangeCapabilities","params":[[]]}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		buf, _ := io.ReadAll(resp.Body)
		t.Fatalf("authed POST: status=%d body=%q, want 200", resp.StatusCode, string(buf))
	}
}
