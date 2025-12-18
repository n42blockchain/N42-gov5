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
	"github.com/n42blockchain/N42/params/networkname"
	"github.com/urfave/cli/v2"
)

var (
	privateKey string
	//engine        string
	miner bool
	// todo
	listenAddress = cli.NewStringSlice()
	bootstraps    = cli.NewStringSlice()
	cfgFile       string

	p2pStaticPeers   = cli.NewStringSlice()
	p2pBootstrapNode = cli.NewStringSlice()
	p2pDenyList      = cli.NewStringSlice()
)

var rootCmd []*cli.Command

var networkFlags = []cli.Flag{
	&cli.StringSliceFlag{
		Name:        "p2p.listen",
		Usage:       "P2P 监听地址",
		Category:    "P2P NETWORK",
		Value:       cli.NewStringSlice(),
		Destination: listenAddress,
	},

	&cli.StringSliceFlag{
		Name:        "p2p.bootstrap",
		Usage:       "引导节点信息",
		Category:    "P2P NETWORK",
		Value:       cli.NewStringSlice(),
		Destination: bootstraps,
	},

	&cli.StringFlag{
		Name:        "p2p.key",
		Usage:       "P2P 节点私钥",
		Category:    "P2P NETWORK",
		Value:       "",
		Destination: &DefaultConfig.NetworkCfg.LocalPeerKey,
	},
}

var nodeFlg = []cli.Flag{
	&cli.StringFlag{
		Name:        "node.key",
		Usage:       "节点私钥",
		Category:    "NODE",
		Value:       "",
		Destination: &DefaultConfig.NodeCfg.NodePrivate,
	},
}

var rpcFlags = []cli.Flag{
	// IPC 配置
	&cli.StringFlag{
		Name:        "ipcpath",
		Usage:       "IPC socket 文件名",
		Category:    "IPC",
		Value:       DefaultConfig.NodeCfg.IPCPath,
		Destination: &DefaultConfig.NodeCfg.IPCPath,
	},

	// HTTP RPC 配置
	&cli.BoolFlag{
		Name:        "http",
		Usage:       "启用 HTTP JSON-RPC 服务",
		Category:    "HTTP-RPC",
		Value:       false,
		Destination: &DefaultConfig.NodeCfg.HTTP,
	},
	&cli.StringFlag{
		Name:        "http.addr",
		Usage:       "HTTP-RPC 监听地址 (默认 127.0.0.1 仅本地访问)",
		Category:    "HTTP-RPC",
		Value:       "127.0.0.1",
		Destination: &DefaultConfig.NodeCfg.HTTPHost,
	},
	&cli.StringFlag{
		Name:        "http.port",
		Usage:       "HTTP-RPC 监听端口",
		Category:    "HTTP-RPC",
		Value:       "8545",
		Destination: &DefaultConfig.NodeCfg.HTTPPort,
	},
	&cli.StringFlag{
		Name:        "http.api",
		Usage:       "HTTP-RPC 开放的 API 模块 (eth,web3,net,debug,txpool)",
		Category:    "HTTP-RPC",
		Value:       "eth,web3,net",
		Destination: &DefaultConfig.NodeCfg.HTTPApi,
	},
	&cli.StringFlag{
		Name:        "http.corsdomain",
		Usage:       "允许跨域请求的域名 (逗号分隔，* 表示所有)",
		Category:    "HTTP-RPC",
		Value:       "",
		Destination: &DefaultConfig.NodeCfg.HTTPCors,
	},

	// WebSocket RPC 配置
	&cli.BoolFlag{
		Name:        "ws",
		Usage:       "启用 WebSocket JSON-RPC 服务",
		Category:    "WS-RPC",
		Value:       false,
		Destination: &DefaultConfig.NodeCfg.WS,
	},
	&cli.StringFlag{
		Name:        "ws.addr",
		Usage:       "WebSocket-RPC 监听地址",
		Category:    "WS-RPC",
		Value:       "127.0.0.1",
		Destination: &DefaultConfig.NodeCfg.WSHost,
	},
	&cli.StringFlag{
		Name:        "ws.port",
		Usage:       "WebSocket-RPC 监听端口",
		Category:    "WS-RPC",
		Value:       "8546",
		Destination: &DefaultConfig.NodeCfg.WSPort,
	},
	&cli.StringFlag{
		Name:        "ws.api",
		Usage:       "WebSocket-RPC 开放的 API 模块",
		Category:    "WS-RPC",
		Value:       "eth,web3,net",
		Destination: &DefaultConfig.NodeCfg.WSApi,
	},
	&cli.StringFlag{
		Name:        "ws.origins",
		Usage:       "允许 WebSocket 连接的来源域名",
		Category:    "WS-RPC",
		Value:       "",
		Destination: &DefaultConfig.NodeCfg.WSOrigins,
	},
}

var consensusFlag = []cli.Flag{
	&cli.BoolFlag{
		Name:        "engine.miner",
		Usage:       "启用挖矿/验证",
		Category:    "MINER",
		Value:       false,
		Destination: &DefaultConfig.NodeCfg.Miner,
	},
	&cli.StringFlag{
		Name:        "engine.etherbase",
		Usage:       "挖矿奖励接收地址 (0x开头的以太坊地址)",
		Category:    "MINER",
		Value:       "",
		Destination: &DefaultConfig.Miner.Etherbase,
	},
}

var configFlag = []cli.Flag{
	&cli.StringFlag{
		Name:        "config",
		Aliases:     []string{"c", "blockchain"},
		Usage:       "配置文件路径 (TOML 或 YAML 格式)",
		Category:    "CONFIG",
		Destination: &cfgFile,
	},
}

var pprofCfg = []cli.Flag{
	&cli.BoolFlag{
		Name:        "pprof",
		Usage:       "启用 pprof HTTP 性能分析服务",
		Category:    "DEBUG",
		Value:       false,
		Destination: &DefaultConfig.PprofCfg.Pprof,
	},
	&cli.IntFlag{
		Name:        "pprof.port",
		Usage:       "pprof HTTP 服务端口",
		Category:    "DEBUG",
		Value:       6060,
		Destination: &DefaultConfig.PprofCfg.Port,
	},
	&cli.BoolFlag{
		Name:        "pprof.block",
		Usage:       "启用阻塞分析",
		Category:    "DEBUG",
		Value:       false,
		Destination: &DefaultConfig.PprofCfg.TraceBlock,
	},
	&cli.BoolFlag{
		Name:        "pprof.mutex",
		Usage:       "启用互斥锁分析",
		Category:    "DEBUG",
		Value:       false,
		Destination: &DefaultConfig.PprofCfg.TraceMutex,
	},
	&cli.IntFlag{
		Name:        "pprof.maxcpu",
		Usage:       "使用的 CPU 核心数 (0=全部)",
		Category:    "DEBUG",
		Value:       0,
		Destination: &DefaultConfig.PprofCfg.MaxCpu,
	},
}

var loggerFlag = []cli.Flag{
	&cli.StringFlag{
		Name:        "log.level",
		Aliases:     []string{"verbosity"},
		Usage:       "日志级别 (trace, debug, info, warn, error, fatal)",
		Category:    "LOGGING",
		Value:       "info",
		Destination: &DefaultConfig.LoggerCfg.Level,
	},
	&cli.StringFlag{
		Name:        "log.file",
		Aliases:     []string{"log.name"},
		Usage:       "日志文件名 (留空仅输出到控制台)",
		Category:    "LOGGING",
		Value:       "",
		Destination: &DefaultConfig.LoggerCfg.LogFile,
	},
	&cli.IntFlag{
		Name:        "log.maxsize",
		Aliases:     []string{"log.maxSize"},
		Usage:       "单个日志文件最大大小 (MB)，超过自动切分",
		Category:    "LOGGING",
		Value:       100,
		Destination: &DefaultConfig.LoggerCfg.MaxSize,
	},
	&cli.IntFlag{
		Name:        "log.maxbackups",
		Aliases:     []string{"log.maxBackups"},
		Usage:       "保留的旧日志文件数量 (0=不限)",
		Category:    "LOGGING",
		Value:       10,
		Destination: &DefaultConfig.LoggerCfg.MaxBackups,
	},
	&cli.IntFlag{
		Name:        "log.maxage",
		Aliases:     []string{"log.maxAge"},
		Usage:       "日志文件保留天数 (0=不限)",
		Category:    "LOGGING",
		Value:       30,
		Destination: &DefaultConfig.LoggerCfg.MaxAge,
	},
	&cli.BoolFlag{
		Name:        "log.compress",
		Usage:       "压缩旧日志文件 (节省约 90% 空间)",
		Category:    "LOGGING",
		Value:       true,
		Destination: &DefaultConfig.LoggerCfg.Compress,
	},
	&cli.IntFlag{
		Name:        "log.totalsize",
		Usage:       "日志文件总大小上限 (MB)，超过自动删除最旧文件 (0=不限)",
		Category:    "LOGGING",
		Value:       0,
		Destination: &DefaultConfig.LoggerCfg.TotalSizeCap,
	},
	&cli.BoolFlag{
		Name:        "log.console",
		Usage:       "同时输出到控制台 (即使指定了日志文件)",
		Category:    "LOGGING",
		Value:       true,
		Destination: &DefaultConfig.LoggerCfg.Console,
	},
	&cli.BoolFlag{
		Name:        "log.json",
		Usage:       "使用 JSON 格式输出到文件 (便于日志分析)",
		Category:    "LOGGING",
		Value:       true,
		Destination: &DefaultConfig.LoggerCfg.JSONFormat,
	},
}
var (
	// P2PNoDiscovery specifies whether we are running a local network and have no need for connecting
	// to the bootstrap nodes in the cloud
	P2PNoDiscovery = &cli.BoolFlag{
		Name:        "p2p.no-discovery",
		Usage:       "Enable only local network p2p and do not connect to cloud bootstrap nodes.",
		Destination: &DefaultConfig.P2PCfg.NoDiscovery,
	}
	// P2PStaticPeers specifies a set of peers to connect to explicitly.
	P2PStaticPeers = &cli.StringSliceFlag{
		Name:        "p2p.peer",
		Usage:       "Connect with this peer. This flag may be used multiple times.",
		Destination: p2pStaticPeers,
	}
	// P2PBootstrapNode tells the beacon node which bootstrap node to connect to
	P2PBootstrapNode = &cli.StringSliceFlag{
		Name:        "p2p.bootstrap-node",
		Usage:       "The address of bootstrap node. Beacon node will connect for peer discovery via DHT.  Multiple nodes can be passed by using the flag multiple times but not comma-separated. You can also pass YAML files containing multiple nodes.",
		Destination: p2pBootstrapNode,
	}
	// P2PRelayNode tells the beacon node which relay node to connect to.
	P2PRelayNode = &cli.StringFlag{
		Name: "p2p.relay-node",
		Usage: "The address of relay node. The beacon node will connect to the " +
			"relay node and advertise their address via the relay node to other peers",
		Value:       "",
		Destination: &DefaultConfig.P2PCfg.RelayNodeAddr,
	}
	// P2PUDPPort defines the port to be used by discv5.
	P2PUDPPort = &cli.IntFlag{
		Name:        "p2p.udp-port",
		Usage:       "The port used by discv5.",
		Value:       61015,
		Destination: &DefaultConfig.P2PCfg.UDPPort,
	}
	// P2PTCPPort defines the port to be used by libp2p.
	P2PTCPPort = &cli.IntFlag{
		Name:        "p2p.tcp-port",
		Usage:       "The port used by libp2p.",
		Value:       61016,
		Destination: &DefaultConfig.P2PCfg.TCPPort,
	}
	// P2PIP defines the local IP to be used by libp2p.
	P2PIP = &cli.StringFlag{
		Name:        "p2p.local-ip",
		Usage:       "The local ip address to listen for incoming data.",
		Value:       "",
		Destination: &DefaultConfig.P2PCfg.LocalIP,
	}
	// P2PHost defines the host IP to be used by libp2p.
	P2PHost = &cli.StringFlag{
		Name:        "p2p.host-ip",
		Usage:       "The IP address advertised by libp2p. This may be used to advertise an external IP.",
		Value:       "",
		Destination: &DefaultConfig.P2PCfg.HostAddress,
	}
	// P2PHostDNS defines the host DNS to be used by libp2p.
	P2PHostDNS = &cli.StringFlag{
		Name:        "p2p.host-dns",
		Usage:       "The DNS address advertised by libp2p. This may be used to advertise an external DNS.",
		Value:       "",
		Destination: &DefaultConfig.P2PCfg.HostDNS,
	}
	// P2PPrivKey defines a flag to specify the location of the private key file for libp2p.
	P2PPrivKey = &cli.StringFlag{
		Name:        "p2p.priv-key",
		Usage:       "The file containing the private key to use in communications with other peers.",
		Value:       "",
		Destination: &DefaultConfig.P2PCfg.PrivateKey,
	}
	P2PStaticID = &cli.BoolFlag{
		Name:        "p2p.static-id",
		Usage:       "Enables the peer id of the node to be fixed by saving the generated network key to the default key path.",
		Value:       true,
		Destination: &DefaultConfig.P2PCfg.StaticPeerID,
	}
	// P2PMetadata defines a flag to specify the location of the peer metadata file.
	P2PMetadata = &cli.StringFlag{
		Name:        "p2p.metadata",
		Usage:       "The file containing the metadata to communicate with other peers.",
		Value:       "",
		Destination: &DefaultConfig.P2PCfg.MetaDataDir,
	}
	// P2PMaxPeers defines a flag to specify the max number of peers in libp2p.
	P2PMaxPeers = &cli.IntFlag{
		Name:        "p2p.max-peers",
		Usage:       "The max number of p2p peers to maintain.",
		Value:       5,
		Destination: &DefaultConfig.P2PCfg.MaxPeers,
	}
	// P2PAllowList defines a CIDR subnet to exclusively allow connections.
	P2PAllowList = &cli.StringFlag{
		Name: "p2p.allowlist",
		Usage: "The CIDR subnet for allowing only certain peer connections. " +
			"Using \"public\" would allow only public subnets. Example: " +
			"192.168.0.0/16 would permit connections to peers on your local network only. The " +
			"default is to accept all connections.",
		Destination: &DefaultConfig.P2PCfg.AllowListCIDR,
	}
	// P2PDenyList defines a list of CIDR subnets to disallow connections from them.
	P2PDenyList = &cli.StringSliceFlag{
		Name: "p2p.denylist",
		Usage: "The CIDR subnets for denying certainty peer connections. " +
			"Using \"private\" would deny all private subnets. Example: " +
			"192.168.0.0/16 would deny connections from peers on your local network only. The " +
			"default is to accept all connections.",
		Destination: p2pDenyList,
	}

	// P2PMinSyncPeers specifies the required number of successful peer handshakes in order
	// to start syncing with external peers.
	P2PMinSyncPeers = &cli.IntFlag{
		Name:        "p2p.min-sync-peers",
		Usage:       "The required number of valid peers to connect with before syncing.",
		Value:       1,
		Destination: &DefaultConfig.P2PCfg.MinSyncPeers,
	}

	// P2PBlockBatchLimit specifies the requested block batch size.
	P2PBlockBatchLimit = &cli.IntFlag{
		Name:        "p2p.limit.block-batch",
		Usage:       "The amount of blocks the local peer is bounded to request and respond to in a batch.",
		Value:       64,
		Destination: &DefaultConfig.P2PCfg.P2PLimit.BlockBatchLimit,
	}
	// P2PBlockBatchLimitBurstFactor specifies the factor by which block batch size may increase.
	P2PBlockBatchLimitBurstFactor = &cli.IntFlag{
		Name:        "p2p.limit.block-burst-factor",
		Usage:       "The factor by which block batch limit may increase on burst.",
		Value:       2,
		Destination: &DefaultConfig.P2PCfg.P2PLimit.BlockBatchLimitBurstFactor,
	}
	// P2PBlockBatchLimiterPeriod Period to calculate expected limit for a single peer.
	P2PBlockBatchLimiterPeriod = &cli.IntFlag{
		Name:        "p2p.limit.block-limiter-period",
		Usage:       "Period to calculate expected limit for a single peer.",
		Value:       5,
		Destination: &DefaultConfig.P2PCfg.P2PLimit.BlockBatchLimiterPeriod,
	}
)

var (
	DataDirFlag = &cli.StringFlag{
		Name:        "data.dir",
		Aliases:     []string{"datadir"},
		Usage:       "数据存储目录",
		Category:    "DATA",
		Value:       "./n42data",
		Destination: &DefaultConfig.NodeCfg.DataDir,
	}

	MinFreeDiskSpaceFlag = &cli.IntFlag{
		Name:        "data.minfreedisk",
		Aliases:     []string{"data.dir.minfreedisk"},
		Usage:       "最小剩余磁盘空间 (GB)，低于此值自动关闭节点",
		Category:    "DATA",
		Value:       10,
		Destination: &DefaultConfig.NodeCfg.MinFreeDiskSpace,
	}

	FromDataDirFlag = &cli.StringFlag{
		Name:     "chaindata.from",
		Usage:    "源数据目录 (用于数据迁移)",
		Category: "DATA",
	}
	ToDataDirFlag = &cli.StringFlag{
		Name:     "chaindata.to",
		Usage:    "目标数据目录 (用于数据迁移)",
		Category: "DATA",
	}

	ChainFlag = &cli.StringFlag{
		Name:        "chain",
		Usage:       "区块链网络 (mainnet, testnet, private)",
		Category:    "NETWORK",
		Value:       networkname.MainnetChainName,
		Destination: &DefaultConfig.NodeCfg.Chain,
	}
)

var (
	AuthRPCFlag = &cli.BoolFlag{
		Name:        "authrpc",
		Usage:       "启用认证 RPC (Engine API，用于共识层通信)",
		Category:    "AUTH-RPC",
		Value:       false,
		Destination: &DefaultConfig.NodeCfg.AuthRPC,
	}
	AuthRPCListenFlag = &cli.StringFlag{
		Name:        "authrpc.addr",
		Usage:       "认证 RPC 监听地址",
		Category:    "AUTH-RPC",
		Value:       "127.0.0.1",
		Destination: &DefaultConfig.NodeCfg.AuthAddr,
	}
	AuthRPCPortFlag = &cli.IntFlag{
		Name:        "authrpc.port",
		Usage:       "认证 RPC 监听端口",
		Category:    "AUTH-RPC",
		Value:       8551,
		Destination: &DefaultConfig.NodeCfg.AuthPort,
	}
	JWTSecretFlag = &cli.StringFlag{
		Name:        "authrpc.jwtsecret",
		Usage:       "JWT 密钥文件路径 (用于认证 RPC)",
		Category:    "AUTH-RPC",
		Value:       "",
		Destination: &DefaultConfig.NodeCfg.JWTSecret,
	}
)

var (
	// 账户设置
	UnlockedAccountFlag = &cli.StringFlag{
		Name:     "unlock",
		Aliases:  []string{"account.unlock"},
		Usage:    "启动时解锁的账户地址 (逗号分隔)",
		Category: "ACCOUNT",
		Value:    "",
	}
	PasswordFileFlag = &cli.PathFlag{
		Name:        "password",
		Aliases:     []string{"account.password"},
		Usage:       "密码文件路径 (用于解锁账户)",
		Category:    "ACCOUNT",
		Destination: &DefaultConfig.NodeCfg.PasswordFile,
	}
	LightKDFFlag = &cli.BoolFlag{
		Name:     "lightkdf",
		Aliases:  []string{"account.lightkdf"},
		Usage:    "降低密钥派生的资源消耗 (牺牲安全性)",
		Category: "ACCOUNT",
	}
	KeyStoreDirFlag = &cli.PathFlag{
		Name:        "keystore",
		Aliases:     []string{"account.keystore"},
		Usage:       "密钥库目录 (默认在数据目录内)",
		Category:    "ACCOUNT",
		TakesFile:   true,
		Destination: &DefaultConfig.NodeCfg.KeyStoreDir,
	}
	InsecureUnlockAllowedFlag = &cli.BoolFlag{
		Name:        "allow-insecure-unlock",
		Aliases:     []string{"account.allow.insecure.unlock"},
		Usage:       "允许通过 HTTP 解锁账户 (不安全，不推荐)",
		Category:    "ACCOUNT",
		Value:       false,
		Destination: &DefaultConfig.NodeCfg.InsecureUnlockAllowed,
	}

	// 指标收集设置
	MetricsEnabledFlag = &cli.BoolFlag{
		Name:        "metrics",
		Usage:       "启用指标收集 (Prometheus 格式)",
		Category:    "METRICS",
		Value:       false,
		Destination: &DefaultConfig.MetricsCfg.Enable,
	}
	MetricsHTTPFlag = &cli.StringFlag{
		Name:        "metrics.addr",
		Usage:       "指标服务监听地址",
		Category:    "METRICS",
		Value:       "127.0.0.1",
		Destination: &DefaultConfig.MetricsCfg.HTTP,
	}
	MetricsPortFlag = &cli.IntFlag{
		Name:        "metrics.port",
		Usage:       "指标服务监听端口",
		Category:    "METRICS",
		Value:       6061,
		Destination: &DefaultConfig.MetricsCfg.Port,
	}
)

var (
	authRPCFlag = []cli.Flag{
		AuthRPCFlag,
		AuthRPCListenFlag,
		AuthRPCPortFlag,
		JWTSecretFlag,
	}
	settingFlag = []cli.Flag{
		DataDirFlag,
		ChainFlag,
		MinFreeDiskSpaceFlag,
	}
	accountFlag = []cli.Flag{
		PasswordFileFlag,
		KeyStoreDirFlag,
		LightKDFFlag,
		InsecureUnlockAllowedFlag,
		UnlockedAccountFlag,
	}

	metricsFlags = []cli.Flag{
		MetricsEnabledFlag,
		MetricsHTTPFlag,
		MetricsPortFlag,
	}

	p2pFlags = []cli.Flag{
		P2PNoDiscovery,
		P2PAllowList,
		P2PBootstrapNode,
		P2PDenyList,
		P2PIP,
		P2PHost,
		P2PMaxPeers,
		P2PMetadata,
		P2PStaticID,
		P2PPrivKey,
		P2PHostDNS,
		P2PRelayNode,
		P2PStaticPeers,
		P2PUDPPort,
		P2PTCPPort,
		P2PMinSyncPeers,
	}

	p2pLimitFlags = []cli.Flag{
		P2PBlockBatchLimit,
		P2PBlockBatchLimitBurstFactor,
		P2PBlockBatchLimiterPeriod,
	}

	// Development flags
	devFlags = []cli.Flag{
		DevTxGenFlag,
		DevTxGenMaxFlag,
	}
)

var (
	// DevTxGenFlag enables automatic transaction generation for testing.
	DevTxGenFlag = &cli.BoolFlag{
		Name:        "dev.txgen",
		Usage:       "启用自动交易生成器 (开发测试用)",
		Category:    "DEVELOPMENT",
		Value:       false,
		Destination: &DefaultConfig.DevCfg.TxGenEnabled,
	}

	// DevTxGenMaxFlag sets the maximum transactions per block.
	DevTxGenMaxFlag = &cli.IntFlag{
		Name:        "dev.txgen.max",
		Usage:       "每个块的最大交易数 (0-31)",
		Category:    "DEVELOPMENT",
		Value:       10,
		Destination: &DefaultConfig.DevCfg.TxGenMaxPerBlock,
	}
)
