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

// web3:// Protocol Gateway (ERC-6860 / ERC-4804)
//
// Translates web3:// URLs into EVM CALL messages, enabling fully on-chain
// frontends served via standard HTTP. A browser or client requests:
//
//	GET /{contractAddress}/{path...}
//
// The gateway resolves the contract, calls it with the path as calldata,
// and returns the result as an HTTP response with appropriate Content-Type.
//
// This enables dApps where HTML/CSS/JS/images are stored entirely on-chain
// (via the ContentStore precompile or contract bytecode) and served without
// any centralized infrastructure.
package api

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

// Web3Gateway serves on-chain content via HTTP by translating paths
// into EVM calls per ERC-6860.
type Web3Gateway struct {
	api    *API
	cfg    *conf.Web3GatewayCfg
	server *http.Server
	ln     net.Listener
}

// NewWeb3Gateway creates a new web3:// protocol gateway.
func NewWeb3Gateway(api *API, cfg *conf.Web3GatewayCfg) *Web3Gateway {
	return &Web3Gateway{api: api, cfg: cfg}
}

// Start begins serving HTTP requests.
func (gw *Web3Gateway) Start() error {
	addr := fmt.Sprintf("%s:%d", gw.cfg.Host, gw.cfg.Port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("web3 gateway: listen %s: %w", addr, err)
	}
	gw.ln = ln

	mux := http.NewServeMux()
	mux.HandleFunc("/", gw.handleRequest)

	gw.server = &http.Server{
		Handler:        mux,
		ReadTimeout:    15 * time.Second,
		WriteTimeout:   30 * time.Second,
		IdleTimeout:    60 * time.Second,
		MaxHeaderBytes: 1 << 16, // 64KB max header
	}

	go func() {
		log.Info("web3:// gateway started", "addr", addr)
		if err := gw.server.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Error("web3 gateway serve error", "err", err)
		}
	}()
	return nil
}

// Stop gracefully shuts down the gateway.
func (gw *Web3Gateway) Stop() error {
	if gw.server == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	log.Info("web3:// gateway stopped")
	return gw.server.Shutdown(ctx)
}

// handleRequest processes a web3:// style HTTP request.
// URL format: /{contractAddress}/{path...}
// The contract is called with the path encoded as calldata.
func (gw *Web3Gateway) handleRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "N42 web3:// Gateway (ERC-6860)\nUsage: /{contractAddress}/{path}\n")
		return
	}

	// Split into contract address and resource path
	parts := strings.SplitN(path, "/", 2)
	addrStr := parts[0]
	resourcePath := ""
	if len(parts) > 1 {
		resourcePath = parts[1]
	}

	// Parse contract address
	if !strings.HasPrefix(addrStr, "0x") {
		addrStr = "0x" + addrStr
	}
	if len(addrStr) != 42 {
		http.Error(w, "invalid contract address", http.StatusBadRequest)
		return
	}
	contractAddr := types.HexToAddress(addrStr)

	// Build calldata from resource path
	calldata := buildCalldata(resourcePath)

	// Execute eth_call
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	result, err := gw.executeCall(ctx, contractAddr, calldata)
	if err != nil {
		log.Debug("web3 gateway call failed", "contract", contractAddr.Hex(), "err", err)
		http.Error(w, "contract call failed", http.StatusInternalServerError)
		return
	}

	// Determine content type from path
	contentType := inferContentType(resourcePath)

	// Set response headers
	w.Header().Set("Content-Type", contentType)
	if gw.cfg.CacheSeconds > 0 {
		w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", gw.cfg.CacheSeconds))
	}
	w.Header().Set("X-Web3-Contract", contractAddr.Hex())
	w.WriteHeader(http.StatusOK)
	w.Write(result)
}

// executeCall performs a real eth_call to the target contract.
func (gw *Web3Gateway) executeCall(ctx context.Context, to types.Address, calldata []byte) ([]byte, error) {
	tx, err := gw.api.db.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	blockNrOrHash := jsonrpc.BlockNumberOrHashWithNumber(jsonrpc.LatestBlockNumber)
	ibs := gw.api.State(tx, blockNrOrHash)
	if ibs == nil {
		return nil, fmt.Errorf("state not available")
	}

	// Verify contract exists
	code := ibs.GetCode(to)
	if len(code) == 0 {
		return nil, fmt.Errorf("no code at address %s", to.Hex())
	}

	// For root path with no calldata, return contract bytecode directly
	// (common pattern for on-chain frontends using SSTORE2/bytecode storage)
	if len(calldata) == 0 {
		return code, nil
	}

	// Execute real EVM call via the API's DoCall infrastructure
	result, err := gw.api.doWeb3Call(ctx, tx, to, calldata, blockNrOrHash)
	if err != nil {
		return nil, err
	}
	return result, nil
}

// buildCalldata encodes a resource path into EVM calldata.
// For simple paths, this is the UTF-8 encoded path.
// For paths ending in known extensions, the path is used as-is.
func buildCalldata(path string) []byte {
	if path == "" {
		return nil
	}
	return []byte(path)
}

// inferContentType determines the MIME type from a resource path.
func inferContentType(path string) string {
	if path == "" {
		return "text/html; charset=utf-8"
	}
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".js", ".mjs":
		return "application/javascript; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".ico":
		return "image/x-icon"
	case ".woff":
		return "font/woff"
	case ".woff2":
		return "font/woff2"
	case ".txt":
		return "text/plain; charset=utf-8"
	case ".xml":
		return "application/xml"
	case ".wasm":
		return "application/wasm"
	default:
		return "application/octet-stream"
	}
}

