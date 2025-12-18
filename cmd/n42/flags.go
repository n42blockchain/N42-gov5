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
	"github.com/urfave/cli/v2"
)

// =============================================================================
// 默认值常量
// =============================================================================

const (
	// 默认端口
	DefaultHTTPPort    = 8545
	DefaultWSPort      = 8546
	DefaultP2PTCPPort  = 30303
	DefaultP2PUDPPort  = 30303
	DefaultPprofPort   = 6060
	DefaultMetricsPort = 6061
	DefaultAuthRPCPort = 8551

	// 默认目录
	DefaultDataDir = "./n42data"
	DefaultIPCPath = "n42.ipc"
	DefaultLogFile = "n42.log"

	// 默认网络参数
	DefaultMaxPeers     = 50
	DefaultMinSyncPeers = 3
)

// =============================================================================
// 快速启动预设标志
// =============================================================================
//
// 这些标志提供便捷的启动方式，补充 cmd.go 中的详细参数。
//
// 最简单的启动方式：
//   n42                          # 启动主网全节点
//   n42 --testnet                # 启动测试网节点
//   n42 --http                   # 启用 RPC（默认 127.0.0.1:8545）
//
// 常用组合：
//   n42 --http --http.addr 0.0.0.0    # 对外开放 RPC
//   n42 --ws --ws.addr 0.0.0.0        # 对外开放 WebSocket
//   n42 --data.dir /data/n42          # 指定数据目录
//

// QuickStartFlags 提供快速启动的便捷参数
var QuickStartFlags = []cli.Flag{
	// 网络快捷选择
	&cli.BoolFlag{
		Name:     "testnet",
		Usage:    "启动测试网节点 (等同于 --chain testnet)",
		Category: "QUICK START",
		Action: func(ctx *cli.Context, b bool) error {
			if b {
				DefaultConfig.NodeCfg.Chain = "testnet"
			}
			return nil
		},
	},
	&cli.BoolFlag{
		Name:     "dev",
		Usage:    "启动开发者模式 (本地单节点，无需同步)",
		Category: "QUICK START",
		Action: func(ctx *cli.Context, b bool) error {
			if b {
				DefaultConfig.NodeCfg.Chain = "private"
				DefaultConfig.P2PCfg.NoDiscovery = true
				DefaultConfig.P2PCfg.MaxPeers = 0
			}
			return nil
		},
	},

	// 快速端口设置
	&cli.IntFlag{
		Name:     "port",
		Usage:    "P2P 网络监听端口 (同时设置 TCP 和 UDP)",
		Value:    DefaultP2PTCPPort,
		Category: "QUICK START",
		Action: func(ctx *cli.Context, port int) error {
			DefaultConfig.P2PCfg.TCPPort = port
			DefaultConfig.P2PCfg.UDPPort = port
			return nil
		},
	},

	// 常用别名
	&cli.BoolFlag{
		Name:     "mine",
		Usage:    "启用挖矿/验证 (等同于 --engine.miner)",
		Category: "QUICK START",
		Destination: &DefaultConfig.NodeCfg.Miner,
	},
	&cli.StringFlag{
		Name:     "etherbase",
		Usage:    "挖矿奖励接收地址 (等同于 --engine.etherbase)",
		Category: "QUICK START",
		Destination: &DefaultConfig.Miner.Etherbase,
	},

	// 同步模式
	&cli.StringFlag{
		Name:     "syncmode",
		Usage:    "同步模式 (full, fast, light)",
		Value:    "full",
		Category: "QUICK START",
	},

	// 调试快捷开关
	&cli.BoolFlag{
		Name:     "debug",
		Usage:    "启用调试模式 (详细日志 + pprof)",
		Category: "QUICK START",
		Action: func(ctx *cli.Context, b bool) error {
			if b {
				DefaultConfig.LoggerCfg.Level = "debug"
				DefaultConfig.PprofCfg.Pprof = true
			}
			return nil
		},
	},
}

// AllFlags 返回所有命令行参数，按分类排序
// 优先级：QuickStartFlags > cmd.go 中的详细参数
func AllFlags() []cli.Flag {
	var flags []cli.Flag

	// 1. 快速启动参数（最常用）
	flags = append(flags, QuickStartFlags...)

	// 2. cmd.go 中的所有详细参数
	flags = append(flags, networkFlags...)
	flags = append(flags, settingFlag...)
	flags = append(flags, rpcFlags...)
	flags = append(flags, authRPCFlag...)
	flags = append(flags, consensusFlag...)
	flags = append(flags, loggerFlag...)
	flags = append(flags, pprofCfg...)
	flags = append(flags, nodeFlg...)
	flags = append(flags, configFlag...)
	flags = append(flags, accountFlag...)
	flags = append(flags, metricsFlags...)
	flags = append(flags, p2pFlags...)
	flags = append(flags, p2pLimitFlags...)
	flags = append(flags, devFlags...)

	return flags
}
