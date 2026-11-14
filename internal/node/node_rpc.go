// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// RPC server wiring: API list, in-process and HTTP/WS endpoints, JWT secret.

package node

import (
	"crypto/rand"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path"
	"strconv"
	"strings"

	pkgerrors "github.com/pkg/errors"

	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/internal/api"
	"github.com/n42blockchain/N42/internal/api/consensusrest"
	"github.com/n42blockchain/N42/internal/api/graphql"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// getAPIs return two sets of APIs, both the ones that do not require
// authentication, and the complete set
func (n *Node) getAPIs() (unauthenticated, all []jsonrpc.API) {
	for _, api := range n.rpcAPIs {
		if !api.Authenticated {
			unauthenticated = append(unauthenticated, api)
		}
	}
	return unauthenticated, n.rpcAPIs
}

func (n *Node) startInProc() error {
	for _, api := range n.rpcAPIs {
		if err := n.inprocHandler.RegisterName(api.Namespace, api.Service); err != nil {
			return err
		}
	}
	return nil
}

func (n *Node) stopInProc() {
	n.inprocHandler.Stop()
}

// obtainJWTSecret loads the jwt-secret, either from the provided config,
// or from the default location. If neither of those are present, it generates
// a new secret and stores to the default location.
func (n *Node) obtainJWTSecret(cliParam string) ([]byte, error) {
	fileName := cliParam
	if len(fileName) == 0 {
		// no path provided, use default
		fileName = path.Join(n.config.NodeCfg.DataDir, datadirJWTKey)
	}
	// try reading from file
	if data, err := os.ReadFile(fileName); err == nil {
		jwtSecret, err := hexutil.Decode(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, pkgerrors.Wrap(err, fmt.Sprintf("failed to decode hex (%s) string", strings.TrimSpace(string(data))))
		}
		if len(jwtSecret) == 32 {
			log.Info("Loaded JWT secret file", "path", fileName, "crc32", fmt.Sprintf("%#x", crc32.ChecksumIEEE(jwtSecret)))
			return jwtSecret, nil
		}
		log.Error("Invalid JWT secret", "path", fileName, "length", len(jwtSecret))
		return nil, errors.New("invalid JWT secret")
	}
	// Need to generate one
	jwtSecret := make([]byte, 32)
	if _, err := rand.Read(jwtSecret); err != nil {
		return nil, fmt.Errorf("failed to generate JWT secret: %w", err)
	}

	if err := os.WriteFile(fileName, []byte(hexutil.Encode(jwtSecret)), 0600); err != nil {
		return nil, err
	}
	log.Info("Generated JWT secret", "path", fileName)
	return jwtSecret, nil
}

func (n *Node) startRPC() error {
	openAPIs, allAPIs := n.getAPIs()

	if err := n.startInProc(); err != nil {
		return err
	}

	// IPC endpoint: constructed at NewNode but never started, so --ipcpath was a
	// silent no-op (clients hitting <datadir>/n42.ipc got connection refused).
	// Serve the full API set over the local socket; empty IPCPath disables it.
	if n.config.NodeCfg.IPCPath != "" {
		if err := n.ipc.start(n.rpcAPIs); err != nil {
			log.Warn("IPC endpoint failed to start", "err", err)
		}
	}

	// Create rate limiter if configured
	var rl *jsonrpc.RateLimiter
	if n.config.NodeCfg.HTTPRateLimit > 0 {
		burst := n.config.NodeCfg.HTTPRateLimitBurst
		if burst <= 0 {
			burst = n.config.NodeCfg.HTTPRateLimit * 2
		}
		cfg := jsonrpc.DefaultRateLimitConfig()
		cfg.RequestsPerSecond = n.config.NodeCfg.HTTPRateLimit
		cfg.BurstSize = burst
		rl = jsonrpc.NewRateLimiter(cfg)
		n.rateLimiter = rl
		log.Info("RPC rate limiting enabled", "rps", n.config.NodeCfg.HTTPRateLimit, "burst", burst)
	}

	// Set health provider on all HTTP servers
	hp := &nodeHealthProvider{node: n}
	n.http.healthProvider = hp
	n.httpAuth.healthProvider = hp

	if n.config.NodeCfg.HTTP {
		httpModules := resolveHTTPModules(n.config.NodeCfg.HTTPApi, n.rpcAPIs, resolveConsensusRuntimePlan(n.engine))
		config := httpConfig{
			CorsAllowedOrigins: utils.SplitAndTrim(n.config.NodeCfg.HTTPCors),
			Vhosts:             []string{"*"},
			Modules:            httpModules,
			prefix:             "",
			rateLimiter:        rl,
		}
		port, _ := strconv.Atoi(n.config.NodeCfg.HTTPPort)
		if err := n.http.setListenAddr(n.config.NodeCfg.HTTPHost, port); err != nil {
			return err
		}
		// Public HTTP must only ever see unauthenticated APIs. Passing the full
		// set here made the API Authenticated flag a no-op for this endpoint:
		// the JWT-only namespaces (engine, coprocessor, hotstuff admin) were
		// servable to anyone who put them in --http.api.
		if err := n.http.enableRPC(openAPIs, config); err != nil {
			return err
		}
		if err := n.http.start(); err != nil {
			return err
		}

		// GraphQL API
		if n.config.NodeCfg.GraphQL.Enabled {
			gqlHandler := graphql.New(n.api)
			endpoint := n.config.NodeCfg.GraphQL.EffectiveEndpoint()
			n.http.mux.Handle(endpoint, gqlHandler)
			log.Info("GraphQL endpoint enabled", "endpoint", endpoint)
		}

		// Consensus REST + explorer page, on the same HTTP port, enabled when
		// "n42" is present in --http.api (alongside the n42_* JSON-RPC methods).
		// Pool config (for committee/verify routes) comes from N42_BLS_POOL_* env.
		for _, m := range httpModules {
			if m == "n42" {
				restSrv := consensusrest.NewServer(n.db, consensusrest.ConfigFromEnv())
				n.http.mux.Handle("/n42/consensus/v1/", restSrv.Handler())
				n.http.mux.Handle("/n42/explorer", consensusrest.ExplorerHandler())
				log.Info("Consensus REST + explorer enabled", "rest", "/n42/consensus/v1/", "explorer", "/n42/explorer")
				break
			}
		}
	}

	// Configure WebSocket.
	if n.config.NodeCfg.WS {
		port, _ := strconv.Atoi(n.config.NodeCfg.WSPort)
		if err := n.ws.setListenAddr(n.config.NodeCfg.WSHost, port); err != nil {
			return err
		}
		config := wsConfig{
			Modules:     utils.SplitAndTrim(n.config.NodeCfg.WSApi),
			Origins:     utils.SplitAndTrim(n.config.NodeCfg.WSOrigins),
			prefix:      "",
			jwtSecret:   []byte{},
			rateLimiter: n.rateLimiter, // share the HTTP per-IP limiter; bounds WS upgrades
		}
		if err := n.ws.enableWS(openAPIs, config); err != nil {
			return err
		}
		if err := n.ws.start(); err != nil {
			return err
		}
	}

	// Configure authenticated API
	if len(openAPIs) != len(allAPIs) && n.config.NodeCfg.AuthRPC {
		jwtSecret, err := n.obtainJWTSecret(n.config.NodeCfg.JWTSecret)
		if err != nil {
			return err
		}
		config := httpConfig{
			CorsAllowedOrigins: utils.SplitAndTrim(n.config.NodeCfg.HTTPCors),
			Vhosts:             []string{"*"},
			Modules:            authenticatedModules(allAPIs),
			prefix:             "",
			jwtSecret:          jwtSecret,
		}

		if err := n.httpAuth.setListenAddr(n.config.NodeCfg.AuthAddr, n.config.NodeCfg.AuthPort); err != nil {
			return err
		}
		if err := n.httpAuth.enableRPC(n.rpcAPIs, config); err != nil {
			return err
		}
		if err := n.httpAuth.start(); err != nil {
			return err
		}
	}

	return nil
}

func (n *Node) stopRPC() {
	n.http.stop()
	n.httpAuth.stop()
	n.ws.stop()
	n.ipc.stop()
	n.stopInProc()
	if n.rateLimiter != nil {
		n.rateLimiter.Stop()
	}
}
