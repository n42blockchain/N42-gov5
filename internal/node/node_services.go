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
// Start helpers for the tx generator, MCP server, web3 gateway and ingest server.

package node

import (
	"fmt"
	"runtime"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/internal/api"
	ddngateway "github.com/n42blockchain/N42/internal/ddn/gateway"
	"github.com/n42blockchain/N42/internal/ingest"
	"github.com/n42blockchain/N42/internal/mcp"
	"github.com/n42blockchain/N42/internal/txgen"
	"github.com/n42blockchain/N42/log"
)

// startTxGenerator initializes and starts the transaction generator for development testing.
func (n *Node) startTxGenerator() {
	if !n.runtimePlan.startTxGenerator {
		return
	}
	txgenConfig := &txgen.Config{
		Enabled:        n.config.DevCfg.TxGenEnabled,
		MaxTxsPerBlock: n.config.DevCfg.TxGenMaxPerBlock,
		Interval:       n.config.DevCfg.TxGenInterval,
		GasPrice:       uint64(n.config.DevCfg.TxGenGasPrice),
		GasLimit:       21000,
		Value:          1000,
		// 0.1 ETH per test account: enough for millions of 1000-wei transfers,
		// and small enough that a coinbase living off hotstuff.devBlockReward
		// (1 ETH/block, one leader turn in 7) can fund all ten accounts within
		// its first few reward blocks instead of tens of minutes.
		FaucetAmount: 100000000000000000,
		CoinbaseKey:  n.config.DevCfg.TxGenKey,
	}

	chainID, _ := uint256.FromBig(n.config.ChainCfg.ChainID)

	// Use etherbase (coinbase) as the faucet source
	coinbase := n.etherbase

	n.txGenerator = txgen.New(n.ctx, txgenConfig, n.txspool, chainID, coinbase, n.accman)

	n.txGenerator.FundAccounts()
	n.txGenerator.Start()
}

func (n *Node) startMCPServer() {
	if !n.runtimePlan.startMCPServer {
		return
	}
	mcpBackend := &mcpNodeBackend{node: n}
	n.mcpServer = mcp.NewServer(mcpBackend, n.config.MCPCfg.AllowedTools)
	if n.ddnGateway != nil {
		ddngateway.RegisterMCPTools(n.mcpServer, n.ddnGateway)
	}
	host := n.config.MCPCfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	addr := fmt.Sprintf("%s:%d", host, n.config.MCPCfg.Port)
	if err := n.mcpServer.Start(addr); err != nil {
		log.Error("MCP server failed to start", "err", err)
	}
}

func (n *Node) startWeb3Gateway() {
	if !n.runtimePlan.startWeb3Gateway {
		return
	}
	n.web3Gateway = api.NewWeb3Gateway(n.api, &n.config.Web3GatewayCfg)
	if err := n.web3Gateway.Start(); err != nil {
		log.Error("web3:// gateway failed to start", "err", err)
	}
}

func (n *Node) startIngestServer() {
	if !n.runtimePlan.startIngestServer {
		return
	}
	ingestPool := &ingestPoolAdapter{pool: n.txspool}
	n.ingestServer = ingest.NewServer(
		n.config.IngestCfg.Addr,
		ingestPool,
		n.config.IngestCfg.SoftTarget,
		n.config.IngestCfg.HardCap,
	)
	if n.config.IngestCfg.HintOnly {
		// Recovery across three quarters of this node's CPU budget, as the
		// import's own sender recovery sizes itself; the endpoint is fed
		// ahead of the block, so it competes with nothing on the critical
		// path except itself.
		workers := runtime.GOMAXPROCS(0)
		if workers > 2 {
			workers -= workers / 4
		}
		// The import verifies with the signer of the block's fork rules
		// (MakeSignerWithTimestamp), and the sender cache is keyed by that
		// signer: recover into the same one, following the chain head.
		cfg := n.blockChain.Config()
		bc := n.blockChain
		n.ingestServer.EnableHintOnly(func() transaction.Signer {
			h := bc.CurrentBlock()
			if h == nil || h.Number64() == nil {
				return transaction.LatestSignerForChainID(cfg.ChainID)
			}
			return transaction.MakeSignerWithTimestamp(cfg, h.Number64().ToBig(), h.Time())
		}, workers)
	}
	n.enableFrameIngest()
	if err := n.ingestServer.Start(); err != nil {
		log.Error("Ingest server failed to start", "err", err)
	} else {
		log.Info("Ingest server enabled", "addr", n.config.IngestCfg.Addr, "hintOnly", n.config.IngestCfg.HintOnly)
	}
}
