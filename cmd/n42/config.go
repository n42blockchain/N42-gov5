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

package main

import (
	"math/big"
	"time"

	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/params"
)

// DefaultConfig 包含节点的默认配置
// 这些默认值经过优化，适合大多数用户直接启动节点
var DefaultConfig = conf.Config{
	// 节点配置
	NodeCfg: conf.NodeConfig{
		NodePrivate: "",
		DataDir:     DefaultDataDir, // ./n42data

		// HTTP RPC - 默认关闭，启用时监听本地
		HTTP:     false,
		HTTPHost: "127.0.0.1",
		HTTPPort: "8545",
		HTTPApi:  "eth,web3,net", // 默认开放安全的 API

		// WebSocket RPC - 默认关闭
		WS:     false,
		WSHost: "127.0.0.1",
		WSPort: "8546",
		WSApi:  "eth,web3,net",

		// IPC
		IPCPath: DefaultIPCPath, // n42.ipc

		// 挖矿 - 默认关闭
		Miner: false,

		// 网络
		Chain: "mainnet",
	},

	// 网络配置
	NetworkCfg: conf.NetWorkConfig{
		Bootstrapped: true,
	},

	// 日志配置 - 默认输出到控制台，级别为 info
	// 日志轮转策略: 按大小切分 + 按数量/时间清理 + 可选压缩
	LoggerCfg: conf.LoggerConfig{
		LogFile:      "",     // 空表示只输出到控制台
		Level:        "info", // 生产环境使用 info
		MaxSize:      100,    // 单文件 100MB，超过自动切分
		MaxBackups:   10,     // 保留 10 个旧文件
		MaxAge:       30,     // 保留 30 天
		Compress:     true,   // 压缩旧文件，节省约 90% 空间
		TotalSizeCap: 0,      // 总大小限制 (0=不限)
		LocalTime:    true,   // 使用本地时间
		Console:      true,   // 同时输出到控制台
		JSONFormat:   true,   // 文件使用 JSON 格式
	},

	// 性能分析 - 默认关闭
	PprofCfg: conf.PprofConfig{
		MaxCpu:     0, // 0 表示使用所有 CPU
		Port:       DefaultPprofPort,
		TraceMutex: false,
		TraceBlock: false,
		Pprof:      false,
	},

	// 数据库配置
	DatabaseCfg: conf.DatabaseConfig{
		DBType:     "lmdb",
		DBPath:     "chaindata",
		DBName:     "n42",
		SubDB:      []string{"chain"},
		Debug:      false,
		IsMem:      false,
		MaxDB:      100,
		MaxReaders: 1000,
	},

	// 指标配置 - 默认关闭
	MetricsCfg: conf.MetricsConfig{
		Enable: false,
		Port:   DefaultMetricsPort,
		HTTP:   "127.0.0.1",
	},

	// P2P 配置
	P2PCfg: &conf.P2PConfig{
		TCPPort:      DefaultP2PTCPPort,
		UDPPort:      DefaultP2PUDPPort,
		MaxPeers:     DefaultMaxPeers,
		MinSyncPeers: DefaultMinSyncPeers,
		StaticPeerID: true,
		NoDiscovery:  false,
		P2PLimit: &conf.P2PLimit{
			BlockBatchLimit:            64,
			BlockBatchLimitBurstFactor: 2,
			BlockBatchLimiterPeriod:    5,
		},
	},

	// Gas 价格预言机
	GPO: conf.FullNodeGPO,

	// 挖矿配置
	Miner: conf.MinerConfig{
		GasCeil:  30000000,
		GasPrice: big.NewInt(params.GWei),
		Recommit: 4 * time.Second,
	},

	// 开发配置
	DevCfg: conf.DefaultDevConfig(),
}
