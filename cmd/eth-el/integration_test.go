// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package main

import (
	"context"
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

	"github.com/c2h5oh/datasize"
	"github.com/golang-jwt/jwt/v4"
	"github.com/urfave/cli/v2"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/internal/ethel/engineapi"
)

// pickFreePort asks the OS for a free TCP port and closes the listener
// before returning it. There is a tiny race window where another
// process could grab the port before the Node re-binds, which is
// acceptable for a single-shot test.
func pickFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pick port: %v", err)
	}
	defer l.Close()
	_, portStr, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("split addr: %v", err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return p
}

// TestEthEL_StartStopLifecycle runs the full Node.Start / Node.Stop
// path against a temp datadir with bootstrap + catchup disabled. It
// verifies end-to-end:
//
//   - Node.Start opens chaindata MDBX and both freezer handles,
//   - the engineAPI factory is invoked and binds its HTTP listener,
//   - JWT auth actually gates requests (403 → 200 round trip),
//   - Node.Stop walks services in reverse and closes storage cleanly,
//     and Stop is idempotent.
//
// Bootstrap and catchup are disabled because their real bodies read
// from freezer tables we do not populate in the test — the goal here
// is to exercise the Node orchestration + engineAPI service, not the
// executor. The executor and rebuild paths have their own unit tests.
func TestEthEL_StartStopLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("opens real MDBX; skipped in -short mode")
	}

	datadir := t.TempDir()
	port := pickFreePort(t)
	jwtPath := filepath.Join(datadir, "jwt.hex")

	cfg := conf.DefaultEthELCfg()
	cfg.DataDir = datadir
	cfg.Storage.MapSize = 1 * datasize.GB // tiny to keep test fast on Windows
	cfg.Bootstrap.Enabled = false
	cfg.EngineAPI.Enabled = true
	cfg.EngineAPI.Host = "127.0.0.1"
	cfg.EngineAPI.Port = port
	cfg.EngineAPI.JWTSecretPath = jwtPath

	node, err := ethel.NewNode(cfg)
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}

	node.RegisterFactory(func(n *ethel.Node) ethel.Service {
		return engineapi.New(cfg.EngineAPI, n.ChainConfig(), n.Engine(), n.RwDB(), n.OutFreezer())
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// Node.Start runs the whole service chain synchronously in the
	// calling goroutine. The live sentinel runs synchronously too
	// (runLive blocks on ctx.Done), so Start never returns until
	// shutdown — run it in a goroutine.
	startDone := make(chan error, 1)
	go func() { startDone <- node.Start(ctx) }()

	// Poll for the engineAPI listener to come up instead of sleeping
	// a fixed interval. This keeps the test stable on slow CI hosts.
	url := "http://127.0.0.1:" + strconv.Itoa(port)
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-startDone
			t.Fatal("engineAPI listener did not come up in 10s")
		}
		time.Sleep(50 * time.Millisecond)
	}

	// 1. Unauthenticated POST → 403 from the JWT middleware.
	resp, err := http.Post(url, "application/json",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"engine_exchangeCapabilities","params":[[]]}`))
	if err != nil {
		t.Fatalf("unauth POST: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unauth POST status: got %d, want 403", resp.StatusCode)
	}

	// 2. Load the auto-generated JWT secret and issue an authed request.
	//    The engineAPI writes the secret to disk on first start, so by
	//    the time the listener is up the file must exist.
	secretHex, err := os.ReadFile(jwtPath)
	if err != nil {
		t.Fatalf("read jwt: %v", err)
	}
	secret, err := hex.DecodeString(strings.TrimSpace(string(secretHex)))
	if err != nil {
		t.Fatalf("decode jwt: %v", err)
	}
	tokenStr, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(time.Now()),
	}).SignedString(secret)
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, url,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"engine_exchangeCapabilities","params":[[]]}`))
	req.Header.Set("Authorization", "Bearer "+tokenStr)
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("authed POST: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authed POST: status=%d body=%q, want 200", resp.StatusCode, body)
	}

	// 3. Initiate shutdown. Cancelling the context makes the live
	//    sentinel return, which unblocks node.start and makes Start
	//    return nil.
	cancel()
	select {
	case err := <-startDone:
		if err != nil && err != context.Canceled {
			t.Fatalf("Start returned %v after cancel", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Start did not return after cancel")
	}

	// 4. Explicit Stop, then a second Stop to confirm idempotency.
	if err := node.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := node.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

// TestEthEL_FlagsCoverConfig pins that every flag the tests and the
// production wiring expect is actually registered in flags(). This is
// a compile-time-ish smoke test: when someone renames or removes a
// flag, this test fails immediately instead of the heavier
// TestEthEL_StartStopLifecycle silently mis-reading the option.
func TestEthEL_FlagsCoverConfig(t *testing.T) {
	flagList := flags()
	required := []string{
		"datadir",
		"network",
		"genesis",
		"bootstrap.enabled",
		"bootstrap.manifest",
		"catchup.manifest",
		"catchup.commit-interval",
		"engine.enabled",
		"engine.port",
		"engine.jwt",
		"torrent.enabled",
		"torrent.listen",
		"eldevp2p.enode-file",
	}
	for _, name := range required {
		if !hasFlag(flagList, name) {
			t.Errorf("flag %q missing from flags()", name)
		}
	}
}

func TestLoadCustomGenesis(t *testing.T) {
	path := filepath.Join(t.TempDir(), "genesis.json")
	input := `{
	  "config": {"chainId": 4242, "terminalTotalDifficultyPassed": true},
	  "timestamp": "0x2a",
	  "gasLimit": "0x1c9c380",
	  "difficulty": "0x0",
	  "alloc": {}
	}`
	if err := os.WriteFile(path, []byte(input), 0o600); err != nil {
		t.Fatal(err)
	}
	genesis, err := loadCustomGenesis(path)
	if err != nil {
		t.Fatalf("loadCustomGenesis: %v", err)
	}
	if genesis.Config == nil || genesis.Config.ChainID == nil || genesis.Config.ChainID.Uint64() != 4242 {
		t.Fatalf("chain config = %#v, want chain ID 4242", genesis.Config)
	}
	if genesis.Timestamp != 42 {
		t.Fatalf("timestamp = %d, want 42", genesis.Timestamp)
	}

	cfg := conf.DefaultEthELCfg()
	cfg.DataDir = t.TempDir()
	cfg.Genesis = genesis
	node, err := ethel.NewNode(cfg)
	if err != nil {
		t.Fatalf("NewNode(custom genesis): %v", err)
	}
	if got := node.ChainConfig().ChainID.Uint64(); got != 4242 {
		t.Fatalf("node chain ID = %d, want 4242", got)
	}
}

func hasFlag(flagList []cli.Flag, name string) bool {
	for _, f := range flagList {
		for _, n := range f.Names() {
			if n == name {
				return true
			}
		}
	}
	return false
}
