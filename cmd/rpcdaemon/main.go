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

// rpcdaemon is a standalone RPC server that connects to a core N42 node
// via gRPC, providing JSON-RPC query capabilities without running a full
// blockchain node.
//
// This enables horizontal scaling: multiple rpcdaemon instances can connect
// to the same core node, distributing read-heavy RPC workloads.
//
// Usage:
//
//	rpcdaemon --private.api.addr=localhost:9090 --http.addr=0.0.0.0 --http.port=20012
//	rpcdaemon --datadir=/path/to/chaindata  (local mode, direct MDBX access)
package main

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	gointerfaces "github.com/n42blockchain/N42/lib/gointerfaces"
	"github.com/n42blockchain/N42/lib/gointerfaces/remote"
	"github.com/n42blockchain/N42/lib/kv/remotedb"
	"github.com/n42blockchain/N42/lib/kv/remotedbserver"
	log3 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
	"github.com/urfave/cli/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

var (
	version = "0.1.0"

	privateAPIFlag = &cli.StringFlag{
		Name:  "private.api.addr",
		Value: "127.0.0.1:9090",
		Usage: "Core node's gRPC address for KV access",
	}
	httpAddrFlag = &cli.StringFlag{
		Name:  "http.addr",
		Value: "127.0.0.1",
		Usage: "HTTP-RPC server listening interface",
	}
	httpPortFlag = &cli.IntFlag{
		Name:  "http.port",
		Value: 20012,
		Usage: "HTTP-RPC server listening port",
	}
)

func main() {
	app := &cli.App{
		Name:    "rpcdaemon",
		Usage:   "Standalone RPC daemon for N42 blockchain node",
		Version: version,
		Flags: []cli.Flag{
			privateAPIFlag,
			httpAddrFlag,
			httpPortFlag,
		},
		Action: runRPCDaemon,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runRPCDaemon(cliCtx *cli.Context) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	privateAddr := cliCtx.String("private.api.addr")
	httpAddr := cliCtx.String("http.addr")
	httpPort := cliCtx.Int("http.port")

	log.Info("RPCDaemon starting",
		"private.api", privateAddr,
		"http", fmt.Sprintf("%s:%d", httpAddr, httpPort),
		"version", version,
	)

	// Connect to core node's gRPC KV service
	log.Info("Connecting to core node", "addr", privateAddr)
	conn, err := grpc.DialContext(ctx, privateAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(256*1024*1024)),
	)
	if err != nil {
		return fmt.Errorf("failed to connect to core node at %s: %w", privateAddr, err)
	}
	defer conn.Close()

	// Create remote KV client
	kvClient := remote.NewKVClient(conn)
	apiVersion := gointerfaces.VersionFromProto(remotedbserver.KvServiceAPIVersion)
	remoteDB, err := remotedb.NewRemote(apiVersion, log3.New(), kvClient).Open()
	if err != nil {
		return fmt.Errorf("failed to open remote DB: %w", err)
	}
	defer remoteDB.Close()

	// Verify connectivity by opening a read transaction
	tx, err := remoteDB.BeginRo(ctx)
	if err != nil {
		return fmt.Errorf("failed to open read transaction: %w", err)
	}
	tx.Rollback()
	log.Info("Connected to core node successfully")

	// Set up JSON-RPC server
	rpcServer := jsonrpc.NewServer()

	// Register read-only APIs (eth_*, debug_*, net_*, web3_*)
	// In a full implementation, these would use remoteDB for state queries.
	// For now, register basic methods that demonstrate the architecture.
	rpcServer.RegisterName("web3", &Web3API{version: version})
	rpcServer.RegisterName("net", &NetAPI{})

	// HTTP handler
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "N42 RPCDaemon v%s\n", version)
			return
		}
		rpcServer.ServeHTTP(w, r)
	})

	listenAddr := fmt.Sprintf("%s:%d", httpAddr, httpPort)
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", listenAddr, err)
	}

	httpServer := &http.Server{
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start HTTP server
	go func() {
		log.Info("RPCDaemon HTTP server started", "addr", listenAddr)
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Error("RPCDaemon HTTP server error", "err", err)
		}
	}()

	// Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info("RPCDaemon shutting down", "signal", sig)

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	return httpServer.Shutdown(shutdownCtx)
}

// Web3API provides web3 namespace methods.
type Web3API struct {
	version string
}

func (api *Web3API) ClientVersion() string {
	return fmt.Sprintf("N42-RPCDaemon/v%s", api.version)
}

// NetAPI provides net namespace methods.
type NetAPI struct{}

func (api *NetAPI) Version() string {
	return "1" // Chain ID placeholder
}

func (api *NetAPI) Listening() bool {
	return true
}
