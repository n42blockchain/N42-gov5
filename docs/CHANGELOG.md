# N42 修改日志 (Changelog)

本文件记录 N42 项目的所有重要修改和调整。

---

## [未发布] - 开发中

### 2024-12-16

#### 📋 测试补充计划

创建 `docs/TEST_PLAN.md`，包含：
- 当前测试覆盖率分析
- 8 阶段分步执行计划
- 参考 geth/erigon 测试模式
- 目标：整体覆盖率从 ~15% 提升至 50%+

#### ✅ 补充 Phase 3 & 5: 缺失测试补齐

**Phase 3 (block/tx 核心数据结构)**:
| 文件 | 测试数 | 覆盖率 |
|------|--------|--------|
| `common/block/block_test.go` | 20+/6 | 6.4%→14.0% |
| `common/transaction/transaction_test.go` | 12+/4 | 新增 |

**Phase 5 (TxPool/Miner)**:
| 文件 | 测试数 | 覆盖率 |
|------|--------|--------|
| `internal/txspool/txspool_test.go` | 10+/4 | 0%→2.0% |
| `internal/miner/miner_test.go` | 8+/4 | 0%→3.8% |

---

#### ✅ Phase 8: 集成测试和最终覆盖率完成

**新增测试文件**:
| 文件 | 测试数 | 说明 |
|------|--------|------|
| `tests/integration_test.go` | 18+/4 | 跨模块集成测试 |

**最终覆盖率排名 (Top 20)**:
```
pkg/errors                     100.0%
common/crypto/blake2b           94.7%
internal/p2p/types              94.1%
common/crypto/bn256/google      91.6%
common/rlp                      88.9%
common/crypto/rand              88.9%
internal/avm/rlp                88.8%
common/prque                    88.2%
common/hexutil                  83.5%
common/crypto/ecies             82.6%
internal/vm/stack               78.4%
accounts/keystore               78.1%
internal/vm/precompiles         75.9%
common                          70.6%
log                             69.1%
internal/consensus              65.8%
```

**Benchmark 结果摘要**:
```
BenchmarkCrossModuleHashOperation    395 ns/op    1 allocs
BenchmarkGasPoolCycle               0.32 ns/op    0 allocs
BenchmarkUint256Operations          2.58 ns/op    0 allocs
BenchmarkTypeConversions            0.32 ns/op    0 allocs
```

---

#### ✅ Phase 7: 工具/通用测试完成

**覆盖率提升**:
- `common` 0% → **70.6%**
- `utils` 6.1% → **31.3%**

**新增测试文件**:
| 文件 | 测试数 | 说明 |
|------|--------|------|
| `common/common_test.go` | 25+/7 | Big/GasPool/PrettyDuration 测试 |
| `utils/utils_extra_test.go` | 30+/9 | ToBytes/Keccak256/Lock 测试 |

**Benchmark 结果摘要**:
```
BenchmarkGasPoolAddGas              0.32 ns/op     0 allocs
BenchmarkGasPoolString              66.3 ns/op     2 allocs
BenchmarkPrettyDurationString        119 ns/op     3 allocs
BenchmarkToBytes4                   0.32 ns/op     0 allocs
BenchmarkKeccak256                   388 ns/op     1 allocs
BenchmarkHexPrefix                  3.16 ns/op     0 allocs
```

---

#### ✅ Phase 6: 核心层测试完成

**覆盖率提升**:
- `internal` 6.0% → 8.0%

**新增测试文件**:
| 文件 | 测试数 | 说明 |
|------|--------|------|
| `internal/blockchain_test.go` | 20+ | Error/DeriveSha/Pool 测试 |
| `internal/forkchoice_test.go` | 10+ | ForkChoice/ChainReader 测试 |
| `internal/evm_test.go` | 15+ | CanTransfer/Transfer 测试 |

**Benchmark 结果摘要**:
```
BenchmarkDeriveSha                   10740 ns/op    101 allocs
BenchmarkHasherPoolGetPut             8.24 ns/op      0 allocs
BenchmarkCanTransfer                  27.3 ns/op      1 allocs
BenchmarkTransfer                      220 ns/op      6 allocs
BenchmarkNewForkChoice               11866 ns/op      6 allocs
BenchmarkTDComparison                 1.25 ns/op      0 allocs
```

---

#### ✅ Phase 5: P2P/同步层测试完成

**覆盖率提升**:
- `internal/p2p/types` 0% → 94.1%
- `internal/sync` 13.7% → 13.8%
- `internal/p2p` 8.0% (保持)

**新增测试文件**:
| 文件 | 测试数 | 说明 |
|------|--------|------|
| `internal/p2p/types/types_test.go` | 25+ | SSZ/Goodbye/Error 测试 |
| `internal/sync/sync_test.go` | 5+ | Response Code 测试 |

**Benchmark 结果摘要**:
```
BenchmarkSSZBytesHashTreeRoot        2694 ns/op    0 allocs
BenchmarkBlockByRootsReqMarshalSSZ   489.8 ns/op   1 allocs
BenchmarkErrorMessageMarshalSSZ      14.98 ns/op   1 allocs
```

---

#### ✅ Phase 4: 共识层测试完成

**覆盖率提升**:
- `internal/consensus/misc` 25.5% → 30.7%
- `internal/consensus/apoa` 0% → 测试结构
- `internal/consensus/apos` 0% → 0.1%
- `internal/consensus` 65.8% (保持)

**新增测试文件**:
| 文件 | 测试数 | 说明 |
|------|--------|------|
| `misc/consensus_misc_test.go` | 15+ | 常量/GasLimit/Error 测试 |
| `apoa/apoa_test.go` | 15+ | Vote/Tally/Snapshot 测试 |
| `apos/apos_test.go` | 15+ | Vote/Faker/API 测试 |

**Benchmark 结果摘要**:
```
BenchmarkVoteCreation           0.32 ns/op   0 allocs
BenchmarkSnapshotSignerLookup   8.59 ns/op   0 allocs
BenchmarkVerifyGaslimitCheck    2.08 ns/op   0 allocs
BenchmarkNewFaker               0.32 ns/op   0 allocs
```

---

#### ✅ Phase 3: VM 层测试完成

**覆盖率提升**:
- `internal/vm` 7.6% → 8.8%
- `internal/vm/stack` 0% → 78.4%
- `internal/vm/precompiles` 75.9% (保持)

**新增测试文件**:
| 文件 | 测试数 | 说明 |
|------|--------|------|
| `internal/vm/vm_test.go` | 30+ | Gas/Memory/Data 测试 |
| `internal/vm/stack/stack_test.go` | 20+ | Stack/ReturnStack 测试 |

**Benchmark 结果摘要**:
```
BenchmarkStackPush             2.19 ns/op   0 allocs
BenchmarkStackPop              4.71 ns/op   0 allocs
BenchmarkStackPeek             0.37 ns/op   0 allocs
BenchmarkCalcMemSize64         2.12 ns/op   0 allocs
BenchmarkCallGasEIP150         2.14 ns/op   0 allocs
```

---

#### ✅ Phase 2: 数据层测试完成

**覆盖率提升**:
- `modules/state` 6.7% → 10.3%
- `modules/rawdb` 3.1% (schema/key 函数)

**新增测试文件**:
| 文件 | 测试数 | 说明 |
|------|--------|------|
| `modules/rawdb/accessors_test.go` | 12+ | Key 生成/一致性测试 |
| `modules/rawdb/bench_test.go` | 11 | 性能基准测试 |
| `modules/state/state_test.go` | 20+ | AccessList/Journal/Account 测试 |

**Benchmark 结果摘要**:
```
BenchmarkHeaderKeyGen              0.39 ns/op   0 allocs
BenchmarkAccessListAddAddress      8.57 ns/op   0 allocs
BenchmarkTransientStorageSet      30.54 ns/op   0 allocs
BenchmarkTransientStorageGet      23.80 ns/op   0 allocs
```

---

#### ✅ Phase 1: API 层测试完成

**覆盖率提升**: `internal/api` 2.5% → 5.5%

**新增测试文件**:
| 文件 | 测试数 | 说明 |
|------|--------|------|
| `eth_methods_test.go` | 20+ | eth 方法测试 |
| `debug_trace_test.go` | 15+ | 追踪方法测试 |
| `rpc_extra_test.go` | 25+ | 额外命名空间测试 |
| `api_bench_test.go` | 26 | 性能基准测试 |

**Benchmark 结果摘要**:
```
BenchmarkRPCTransactionMarshal     2351 ns/op
BenchmarkAddrLockerLockUnlock      45.30 ns/op
BenchmarkMemStats                  19967 ns/op
BenchmarkNodeInfo                  45.37 ns/op
```

---

### 2024-12-15

#### 🔌 RPC API 补齐 - 完整命名空间支持

**RPC 计划全部完成 ✅**

| Step | 内容 | 状态 |
|------|------|------|
| Step 1 | eth 基础方法 | ✅ 完成 |
| Step 2 | eth 交易签名/原始数据 | ✅ 完成 |
| Step 3 | eth 高级查询 | ✅ 完成 |
| Step 4 | debug 追踪 | ✅ 完成 |
| Step 5 | debug 辅助 | ✅ 完成 |
| Step 6 | admin (PoA 适用部分) | ✅ 完成 |

**新增命名空间 (rpc_extra.go)：**
| 命名空间 | 方法 | 说明 |
|----------|------|------|
| `admin_*` | nodeInfo, peers, datadir, addPeer, removePeer | 节点管理 |
| `personal_*` | listAccounts, listWallets | 账户管理 (默认禁用) |
| `miner_*` | start, stop, mining, setEtherbase | 挖矿控制 (PoA 兼容) |
| `rpc_*` | modules | RPC 模块信息 |
| `txpool_*` | contentFrom | 按地址查询交易池 |
| `eth_*` | protocolVersion | 协议版本 |
| `web3_*` | version | 客户端版本 |

**debug 方法 (debug_trace.go + rpc_extra.go)：**
| 方法 | 说明 |
|------|------|
| `debug_traceTransaction` | 追踪交易执行 |
| `debug_traceBlockByNumber/Hash` | 追踪区块 |
| `debug_traceCall` | 追踪 call 执行 |
| `debug_getBadBlocks` | 获取坏块列表 |
| `debug_storageRangeAt` | 存储范围查询 |
| `debug_accountRange` | 账户范围查询 |
| `debug_getBlockRlp/getHeaderRlp` | 获取 RLP 数据 |
| `debug_printBlock` | 打印区块信息 |
| `debug_memStats/gcStats/stacks` | 运行时调试 |

**新增文件：**
- `internal/api/rpc_extra.go` (~430 行)
- `internal/api/debug_trace.go` (~720 行)
- `internal/api/eth_raw.go` (~330 行)

**更新文件：**
- `internal/api/router.go` - 注册新命名空间

**跳过 (不适用于 N42 PoA)：**
- `engine_*` - 仅 PoS 需要

---

#### 🔌 RPC API 补齐 - Step 1-2

**目标：** 对照 geth/erigon 补齐标准 eth_* RPC 方法。

**Step 1 (已存在于 blockscout.go)：**
- ✅ `eth_syncing` - 同步状态
- ✅ `eth_coinbase` - 挖矿地址
- ✅ `eth_mining` - 是否挖矿
- ✅ `eth_hashrate` - 算力 (PoA 返回 0)
- ✅ `eth_accounts` - 账户列表
- ✅ `eth_getBlockTransactionCountByNumber` - 区块交易数
- ✅ `eth_getTransactionByBlockNumberAndIndex` - 按区块号获取交易
- ✅ `eth_getUncleCountByBlockNumber` - 叔块数 (PoA 返回 0)
- ✅ `eth_getBlockReceipts` - 批量收据

**Step 2 (新增 eth_raw.go)：**
| 方法 | 说明 |
|------|------|
| `eth_sign` | 消息签名 |
| `eth_signTransaction` | 签名交易不发送 |
| `eth_getRawTransactionByHash` | 原始交易数据 |
| `eth_getRawTransactionByBlockHashAndIndex` | 按区块哈希获取原始交易 |
| `eth_getRawTransactionByBlockNumberAndIndex` | 按区块号获取原始交易 |
| `eth_pendingTransactions` | 待处理交易列表 |
| `eth_resend` | 重发交易 (提高 gas) |

**新增文件：**
- `internal/api/eth_raw.go` (~280 行)

**验收：** `make build && make test && make vet` 通过

---

#### 🏗️ Phase 10: init() 清理 + 指标基线 (模块化解耦)

**目标：** 建立性能指标基线，完善 init() 管理策略，提供预编译合约辅助函数。

**新增文件：**
| 文件 | 行数 | 说明 |
|------|------|------|
| `docs/METRICS_BASELINE.md` | 280 | 性能指标基线文档 |
| `internal/vm/precompiles_init.go` | 95 | 预编译合约辅助函数 |

**METRICS_BASELINE.md 内容：**
```
1. RPC 延迟指标
   ├── 核心读取方法 (eth_blockNumber, eth_getBlock*, etc.)
   ├── 计算密集方法 (eth_call, eth_estimateGas, eth_getLogs)
   └── 写入方法 (eth_sendRawTransaction)

2. 同步性能指标
   ├── Initial Sync: > 100 blocks/s (空块)
   └── Catch-up Sync: < 500ms (单区块)

3. Reorg 性能指标
   ├── Depth 1: < 100ms
   ├── Depth 5: < 500ms
   └── Depth 10: < 2s

4. 资源使用基线
   ├── Memory: 空闲 < 500MB, 正常 < 2GB
   ├── Disk: < 50GB/month 增长
   └── CPU: 正常 < 30%

5. 告警阈值定义
```

**precompiles_init.go 函数：**
```go
// 初始化状态检查
func PrecompilesInitialized() bool

// 各分叉预编译数量
func PrecompileCount() map[string]int

// 获取预编译地址列表
func GetPrecompiledAddresses(rules *params.Rules) []types.Address

// 检查是否为预编译
func IsPrecompiled(addr types.Address, rules *params.Rules) bool

// 获取预编译合约
func GetPrecompiledContract(addr types.Address, rules *params.Rules) PrecompiledContract
```

**init() 管理策略：**
```
保留的 init() (标准 Go 模式):
├── vm/contracts.go: 预编译地址填充
├── tracers/native/*.go: Tracer 注册
└── crypto/*.go: 硬件特性检测

已改为显式调用:
└── p2p/gossip_topic_mappings.go → InitGossipTopics()
```

**验收命令：**
```bash
make build && make test && make vet
```

**回滚方式：**
```bash
rm docs/METRICS_BASELINE.md internal/vm/precompiles_init.go
git checkout HEAD -- docs/CHANGELOG.md
```

---

#### 🏗️ Phase 9: rawdb 访问边界 (模块化解耦)

**目标：** 定义清晰的 DB 访问接口，建立访问边界，支持依赖注入和测试 mock。

**新增文件：**
| 文件 | 行数 | 说明 |
|------|------|------|
| `modules/rawdb/interfaces.go` | 200 | DB 访问接口定义 |
| `modules/rawdb/interfaces_test.go` | 165 | 接口测试 |

**接口体系：**
```
Database (完整接口)
├── DatabaseReader (只读)
│   ├── ChainReader: 链数据读取
│   │   ├── ReadCanonicalHash, IsCanonicalHash
│   │   ├── ReadHeader, ReadHeaderNumber, ReadHeaderByNumber
│   │   ├── ReadBlock, ReadBlockByNumber, HasBlock
│   │   └── ReadTd
│   ├── ReceiptReader: 收据读取
│   │   └── ReadReceipts, ReadReceiptsByHash
│   ├── TxLookupReader: 交易查找
│   │   └── ReadTxLookupEntry
│   └── HeadReader: 链头读取
│       └── ReadCurrentBlock, ReadCurrentHeader
└── DatabaseWriter (写入)
    ├── ChainWriter: 链数据写入
    │   ├── WriteCanonicalHash, WriteHeader, WriteBlock
    │   └── WriteTd, DeleteHeader, DeleteBlock
    ├── ReceiptWriter: 收据写入
    │   └── WriteReceipts, DeleteReceipts
    ├── TxLookupWriter: 交易查找写入
    │   └── WriteTxLookupEntries, DeleteTxLookupEntry
    └── HeadWriter: 链头写入
        └── WriteHeadBlockHash, WriteHeadHeaderHash
```

**设计原则：**
- ✅ 接口隔离原则 (ISP): 细粒度接口，按需依赖
- ✅ 依赖倒置原则 (DIP): 依赖抽象而非具体
- ✅ 单一职责原则 (SRP): Reader/Writer 分离

**验收命令：**
```bash
make build && make test && make vet
go test ./modules/rawdb/... -v
```

**回滚方式：**
```bash
rm modules/rawdb/interfaces.go modules/rawdb/interfaces_test.go
git checkout HEAD -- docs/CHANGELOG.md
```

---

#### 🏗️ Phase 8: blockchain.go 职责分离 (模块化解耦)

**目标：** 将 1511 行的 `blockchain.go` God Object 拆分，提取只读查询方法到独立文件。

**修改文件：**
| 文件 | 操作 | 行数 | 说明 |
|------|------|------|------|
| `internal/blockchain_reader.go` | 新增 | 392 | 只读查询方法 (25 个) |
| `internal/blockchain.go` | 修改 | 1206 | 移除已提取方法 (-305 行) |

**提取的方法 (→ blockchain_reader.go)**:
```
链配置访问:
  - Config() *params.ChainConfig
  - Engine() interface{}
  - DB() kv.RwDB

区块访问:
  - CurrentBlock() block.IBlock
  - GenesisBlock() block.IBlock
  - Blocks() []block.IBlock
  - GetBlock(hash, number) block.IBlock
  - GetBlockByHash(hash) (block.IBlock, error)
  - GetBlockByNumber(number) (block.IBlock, error)
  - GetBlocksFromHash(hash, n) []block.IBlock
  - HasBlock(hash, number) bool

Header 访问:
  - GetHeader(hash, number) block.IHeader
  - GetHeaderByNumber(number) block.IHeader
  - GetHeaderByHash(hash) (block.IHeader, error)
  - GetCanonicalHash(number) types.Hash
  - GetBlockNumber(hash) *uint64
  - GetTd(hash, number) *uint256.Int

收据/日志访问:
  - GetReceipts(blockHash) (block.Receipts, error)
  - GetLogs(blockHash) ([][]*block.Log, error)

状态访问:
  - StateAt(tx, blockNr) interface{}
  - HasState(hash) bool
  - HasBlockAndState(hash, number) bool

Deposit/Reward:
  - GetDepositInfo(address) (*uint256.Int, *uint256.Int)
  - GetAccountRewardUnpaid(account) (*uint256.Int, error)

生命周期:
  - Quit() <-chan struct{}
```

**架构变化：**
```
修改前:                          修改后:
┌────────────────────┐          ┌────────────────────┐
│ blockchain.go      │          │ blockchain.go      │
│ (1511 行)          │          │ (1206 行)          │
│ ├── 结构体定义      │          │ ├── 结构体定义      │
│ ├── 只读方法 (25个) │    →     │ ├── 写入方法        │
│ ├── 写入方法        │          │ ├── 事件循环        │
│ ├── 事件循环        │          │ └── Reorg          │
│ └── Reorg          │          └────────────────────┘
└────────────────────┘                    │
                                         ▼
                              ┌────────────────────┐
                              │blockchain_reader.go│
                              │ (392 行)           │
                              │ └── 只读方法 (25个) │
                              └────────────────────┘
```

**验收命令：**
```bash
make build && make test && make vet
```

**回滚方式：**
```bash
# 合并回单文件
cat internal/blockchain_reader.go >> internal/blockchain.go
rm internal/blockchain_reader.go
# 然后整理导入
```

---

#### 🏗️ Phase 7: RPC API Gateway 重构 (模块化解耦)

**目标：** 完善 RPC API 层的接口抽象和路由系统，支持 namespace 动态启用/禁用和指标收集。

**核心文件：**
| 文件 | 行数 | 说明 |
|------|------|------|
| `internal/api/backend.go` | 196 | Backend 接口定义（5 个子接口组合） |
| `internal/api/interface.go` | 202 | RPCMetrics 指标收集 |
| `internal/api/router.go` | 206 | API Router 路由管理 |
| `internal/api/backend_test.go` | 184 | Backend 接口测试 |
| `internal/api/interface_test.go` | 288 | RPCMetrics 测试 |

**架构概览：**
```
┌─────────────────────────────────────────────────────────────┐
│                      Router (路由器)                         │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐       │
│  │   eth    │ │   web3   │ │   net    │ │  debug   │ ...   │
│  └────┬─────┘ └────┬─────┘ └────┬─────┘ └────┬─────┘       │
│       └────────────┴────────────┴────────────┘              │
│                         │                                   │
│                    ┌────┴────┐                              │
│                    │ Backend │                              │
│                    └─────────┘                              │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│                    Backend (接口)                            │
│  ├── BlockchainBackend: 链数据访问                           │
│  ├── StateBackend: 状态访问                                  │
│  ├── TxPoolBackend: 交易池访问                               │
│  ├── AccountBackend: 账户管理                                │
│  └── ConfigBackend: 配置访问                                 │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│               RPCMetrics (指标收集)                          │
│  ├── methodCalls/methodErrors: 调用/错误计数                 │
│  ├── methodLatency: 延迟分布 (P50/P95)                       │
│  └── TopMethods: 热点方法统计                                │
└─────────────────────────────────────────────────────────────┘
```

**编译时类型检查：**
- `var _ Backend = (*API)(nil)`
- `var _ BlockchainBackend = (*API)(nil)`
- `var _ StateBackend = (*API)(nil)`
- `var _ TxPoolBackend = (*API)(nil)`
- `var _ AccountBackend = (*API)(nil)`
- `var _ ConfigBackend = (*API)(nil)`
- `var _ BlockReader = (*API)(nil)`
- `var _ HeaderReader = (*API)(nil)`
- `var _ StateReader = (*API)(nil)`

**特性：**
- ✅ Backend 接口抽象（5 个子接口组合）
- ✅ Router namespace 动态启用/禁用
- ✅ RPCMetrics P50/P95 延迟统计
- ✅ 完整测试覆盖（含并发测试）
- ✅ 编译时接口检查

**验收命令：**
```bash
make build && make test && make vet
go test ./internal/api/... -v
```

**回滚方式：**
```bash
git revert <commit-hash>
```

---

#### 🏗️ Phase 6: P2P 接口抽象完善 (模块化解耦)

**目标：** 完善 P2P 接口抽象体系，添加编译时类型检查和接口文档，确保类型安全。

**修改文件：**
| 文件 | 改动 |
|------|------|
| `internal/p2p/interfaces.go` | 添加 `P2P` 接口文档和编译时检查 `var _ P2P = (*Service)(nil)` |
| `internal/p2p/sync_interface.go` | 添加 `SyncP2P` 接口组合的编译时检查 |

**架构概览：**
```
P2P (主接口) ← Service 实现
├── Broadcaster: 消息广播 (gossipsub)
├── SetStreamHandler: 流协议处理
├── PubSubProvider: PubSub 实例访问
├── PubSubTopicUser: Topic 管理 (join/leave/publish/subscribe)
├── SenderEncoder: 消息编码/发送
├── PeerManager: 节点生命周期 (disconnect, ENR, discovery)
├── ConnectionHandler: 连接/断开事件处理
├── PeersProvider: 节点状态访问
└── PingProvider: Ping/Pong 协议
        ↓
SyncP2P (同步专用接口)
├── PeerProvider: ConnectedPeers, BestPeers, PeerCount
├── BlockRequester: RequestBlocksByRange, RequestBlocksByHash
├── TopicSubscriber: SubscribeToBlocks, SubscribeToTxs
└── PeerScorer: IncrementPeerScore, DecrementPeerScore, BanPeer
        ↓
P2PMetrics (指标收集)
├── 节点: peersConnected, peersDisconnected, peersBanned
├── 请求: requestsTotal, requestsFailed, requestLatency
└── 区块: blocksReceived, bytesReceived
        ↓
TopicRegistry (Topic 注册)
├── Register/SetHandler/GetConfig/GetHandler
└── RegisterDefaultTopics (显式初始化替代 init())
```

**编译时类型检查：**
- `var _ P2P = (*Service)(nil)`
- `var _ PeerProvider = (SyncP2P)(nil)`
- `var _ BlockRequester = (SyncP2P)(nil)`
- `var _ TopicSubscriber = (SyncP2P)(nil)`
- `var _ PeerScorer = (SyncP2P)(nil)`

**特性：**
- ✅ 完整的 P2P 接口层次结构
- ✅ 同步专用 SyncP2P 接口解耦
- ✅ P2PMetrics 指标收集
- ✅ TopicRegistry 显式注册替代 init()
- ✅ 编译时接口检查

**验收命令：**
```bash
make build && make test && make vet
go test ./internal/p2p/... -v
```

**回滚方式：**
```bash
git revert <commit-hash>
```

---

#### 🏗️ Phase 5: Sync State Machine 完善 (模块化解耦)

**目标：** 完善同步状态机系统，添加接口文档和类型别名，确保 API 清晰易用。

**修改文件：**
| 文件 | 改动 |
|------|------|
| `internal/sync/fetcher.go` | 添加 `BlockFetcher` 接口文档和 `SyncFetcher` 类型别名 |

**架构概览：**
```
SyncStateMachine (状态机)
    ├── SyncState (Idle → InitialSync → CatchUp → Synced)
    ├── SyncMetrics (指标收集)
    └── Checker 接口实现 (Syncing, Synced, Status, Resync)
        ↓
BlockFetcher / SyncFetcher (获取接口)
    ├── BasicFetcher (基础实现)
    │   └── FetchBlocks, FetchBlocksByHash
    └── InstrumentedFetcher (带指标包装)
        └── FetcherMetrics 自动收集
            ↓
FetchResult (结果结构)
    ├── Blocks [][]byte
    ├── PeerID peer.ID
    ├── Start, Count
    └── Duration
```

**编译时类型检查：**
- `var _ BlockFetcher = (*BasicFetcher)(nil)`
- `var _ BlockFetcher = (*InstrumentedFetcher)(nil)`
- `var _ Checker = (*SyncStateMachine)(nil)`

**特性：**
- ✅ 状态机模式管理同步状态
- ✅ 可配置的状态转换阈值
- ✅ 指标收集和日志记录
- ✅ 可注入的同步处理器
- ✅ 编译时接口检查

**验收命令：**
```bash
make build && make test && make vet
go test ./internal/sync/... -v
```

**回滚方式：**
```bash
git revert <commit-hash>
```

---

#### 🏗️ Phase 4: Consensus Engine 接口统一 (模块化解耦)

**目标：** 完善共识引擎接口体系，添加编译时类型检查，统一 BasePoA 公共逻辑。

**修改文件：**
| 文件 | 改动 |
|------|------|
| `internal/consensus/base.go` | 添加 `BasePoAInterface` 接口和编译时检查 |

**架构概览：**
```
consensus.Engine (主接口)
    ↑
consensus.CoreEngine (简化接口)
    ↑
consensus.EngineAdapter (适配器)
    ↑
consensus.InstrumentedEngine (带指标包装)
    ↑
consensus.BasePoA (公共逻辑)
    ├── Database, Recents, Signatures
    ├── Proposals, Signer
    └── Author, SealHash, Close
        ↑
consensus.misc (工具包)
    ├── constants.go, difficulty.go
    ├── errors.go, header.go, seal.go
```

**编译时类型检查：**
- `var _ consensus.Engine = (*apoa.Apoa)(nil)`
- `var _ consensus.Engine = (*apos.APos)(nil)`
- `var _ consensus.Engine = (*InstrumentedEngine)(nil)`
- `var _ consensus.CoreEngine = (*EngineAdapter)(nil)`
- `var _ BasePoAInterface = (*BasePoA)(nil)`

**验收命令：**
```bash
make build && make test && make vet
go test ./internal/consensus/... -v
```

**回滚方式：**
```bash
git revert <commit-hash>
```

---

#### 🏗️ Phase 3: Precompiled Contracts Registry 注入 (模块化解耦)

**目标：** 完善预编译合约注册表系统，添加编译时类型检查，确保类型安全。

**修改文件：**
| 文件 | 改动 |
|------|------|
| `internal/vm/precompiles/registry.go` | 添加编译时检查 `var _ vm.PrecompileRegistry = (*Registry)(nil)` |
| `internal/vm/evm.go` | 扩展 `PrecompileRegistry` 接口，添加 `ActivePrecompiles()` 方法 |

**核心架构：**
```
vm.PrecompileRegistry (接口)
    ↑
precompiles.Registry (实现)
    ↑
precompiles.NewXxx() (工厂函数)
    ↑
vm.contracts.go (底层实现)
```

**特性：**
- ✅ 依赖注入替代全局 map
- ✅ 基于链规则动态注册 (Homestead → Berlin → Prague)
- ✅ 可选的指标收集 (WithMetrics)
- ✅ 向后兼容 (FromLegacyMap)
- ✅ P-256 预编译支持 (EIP-7212/EIP-7951)

**验收命令：**
```bash
make build && make test && make vet
go test ./internal/vm/precompiles/... -v
```

**回滚方式：**
```bash
git revert <commit-hash>
```

---

#### 🏗️ Phase 2: StateDB 接口抽象 (模块化解耦)

**目标：** 将 `evmtypes.IntraBlockState` 接口定义统一到 `common` 层，确保类型安全和编译时检查。

**修改文件：**
| 文件 | 改动 |
|------|------|
| `common/state_types.go` | 扩展 `StateDB` 接口，添加完整的 EVM 状态操作方法（156 行） |
| `internal/vm/evmtypes/evmtypes.go` | `IntraBlockState` 改为 `common.StateDB` 的类型别名 |
| `modules/state/intra_block_state.go` | 添加编译时检查 `var _ common.StateDB = (*IntraBlockState)(nil)` |

**接口方法分类：**
- 账户管理: `CreateAccount`, `Exist`, `Empty`
- 余额操作: `SubBalance`, `AddBalance`, `GetBalance`
- Nonce 操作: `GetNonce`, `SetNonce`
- 代码操作: `GetCodeHash`, `GetCode`, `SetCode`, `GetCodeSize`
- 退款操作: `AddRefund`, `SubRefund`, `GetRefund`
- 存储操作: `GetCommittedState`, `GetState`, `SetState`
- 自毁操作: `Selfdestruct`, `HasSelfdestructed`
- 访问列表 (EIP-2930): `PrepareAccessList`, `AddressInAccessList`, `SlotInAccessList`, `AddAddressToAccessList`, `AddSlotToAccessList`
- 快照/回滚: `Snapshot`, `RevertToSnapshot`
- 日志: `AddLog`
- 临时存储 (EIP-1153): `GetTransientState`, `SetTransientState`

**验收命令：**
```bash
make build && make test && make vet
```

**回滚方式：**
```bash
git revert <commit-hash>
```

---

#### 🏗️ Phase 1: 修复 common 层违反 (模块化解耦)

**目标：** 消除 `common` 包对 `internal/consensus` 和 `modules/state` 的不当依赖，恢复正确的分层架构。

**新增文件：**
- `common/engine.go` - 定义 `ChainHeaderReader` 和 `ConsensusEngine` 接口 (common 层本地版本)
- `common/state_types.go` - 定义 `StateDB` 接口 (common 层本地版本)

**修改文件：**
| 文件 | 改动 |
|------|------|
| `common/blockchain.go` | 移除 `internal/consensus` 和 `modules/state` 导入，使用 `interface{}` 代替具体类型 |
| `common/events.go` | `MinedEntireEvent.Entire` 改为 `interface{}` |
| `internal/blockchain.go` | `Engine()/SetEngine()/StateAt()/WriteBlockWithState()` 签名改为 `interface{}` |
| `internal/api/api.go` | 添加类型断言处理 `MinedEntireEvent.Entire` |
| `internal/api/agg_sign.go` | 添加类型断言处理 `MinedEntireEvent.Entire` |
| `internal/api/api_backend.go` | 添加类型断言处理 `Engine()` 和 `StateAt()` 返回值 |

**依赖变化：**
```
修改前:
common ──▶ internal/consensus  ❌
common ──▶ modules/state       ❌

修改后:
common ──▶ (无 internal/modules 依赖)  ✅
```

**验收命令：**
```bash
make build && make test && make vet
go list -f '{{join .Imports "\n"}}' ./common | grep -E "(internal|modules)"  # 应无输出
```

**回滚方式：**
```bash
git revert <commit-hash>
# 或删除新文件并恢复修改的文件
```

---

#### 🔧 Makefile 增强

**修改文件：**
- `Makefile` - 新增多个实用目标

**新增目标：**
| 目标 | 说明 |
|------|------|
| `race` | 全仓 race 检测 |
| `bench` | 完整基准测试 |
| `cover` | 覆盖率摘要 |
| `test-cover` | 生成覆盖率 HTML 报告 |
| `test-verbose` | 详细测试输出 |
| `check` | 组合检查 (fmt + vet + lint) |
| `install` | 安装到 $GOPATH/bin |
| `tidy` | 整理依赖 |
| `ci-full` | 完整 CI (+ lint + race) |
| `help` | 显示帮助信息 |

**使用方法：**
```bash
make help          # 查看所有可用目标
make build         # 编译
make test          # 测试
make check         # 代码质量检查
make cover         # 覆盖率
make test-cover    # 生成 HTML 覆盖率报告
make ci            # CI 流程
```

---

#### 🧪 测试覆盖率提升

**新增测试文件：**
- `log/root_test.go` - 日志系统测试
- `conf/logger_config_test.go` - 日志配置测试
- `pkg/errors/errors_test.go` - 错误包测试
- `internal/api/filters/filter_test.go` - 过滤器测试

**修复测试文件：**
- `log/logrus-prefixed-formatter/formatter_test.go` - 修复包导入

**覆盖率提升：**
| 包 | 修改前 | 修改后 |
|-----|--------|--------|
| `pkg/errors` | 0% | **100%** |
| `log` | 0% | **69.1%** |
| `log/logrus-prefixed-formatter` | 0% | **72.3%** |
| `conf` | 0% | **18.2%** |
| `internal/api/filters` | 0% | **0.9%** |

**测试内容：**
- 日志级别、初始化、输出、上下文
- 日志管理器启停、清理逻辑
- 配置验证、序列化 (JSON/YAML)
- 错误定义、辅助函数 (Wrap, Is, As)
- 过滤器类型、订阅 ID、边界条件

---

#### 🔌 Blockscout 兼容接口

**新增文件：**
- `internal/api/blockscout.go` - Blockscout 兼容 RPC 接口
- `internal/api/blockscout_test.go` - Blockscout 接口测试
- `scripts/test_blockscout.sh` - Blockscout RPC 兼容性测试脚本

**新增接口：**
| 方法 | 说明 |
|------|------|
| `eth_syncing` | 获取同步状态 |
| `eth_coinbase` | 获取挖矿收益地址 |
| `eth_mining` | 获取挖矿状态 |
| `eth_hashrate` | 获取算力 (POA 返回 0) |
| `eth_getBlockTransactionCountByNumber` | 按区块号获取交易数量 |
| `eth_getTransactionByBlockNumberAndIndex` | 按区块号和索引获取交易 |
| `eth_getUncleCountByBlockNumber` | 按区块号获取叔块数量 |
| `eth_getUncleByBlockNumberAndIndex` | 按区块号和索引获取叔块 |
| `eth_getBlockReceipts` | 获取区块所有交易收据 |
| `eth_accounts` | 获取节点管理的账户列表 |
| `eth_getProof` | 获取账户 Merkle 证明 |

**测试方法：**
```bash
# 运行单元测试
go test -v ./internal/api/... -run Blockscout

# 运行兼容性测试脚本
./scripts/test_blockscout.sh http://localhost:8545
```

---

#### 📝 新增修改日志

**新增文件：**
- `docs/CHANGELOG.md` - 项目修改日志（本文件）

---

#### ⚡ 日志系统增强

**修改文件：**
- `conf/logger_config.go` - 扩展日志配置选项
- `log/root.go` - 增强日志初始化和自动清理
- `cmd/n42/cmd.go` - 添加新的日志命令行参数
- `cmd/n42/config.go` - 更新默认日志配置

**新增功能：**
1. **日志文件分段**: 单文件超过 MaxSize 自动切分
2. **自动清理策略**:
   - 按数量清理: MaxBackups 控制保留文件数
   - 按时间清理: MaxAge 控制保留天数
   - 按总大小清理: TotalSizeCap 控制总大小上限
3. **压缩支持**: 旧文件自动压缩为 .gz，节省约 90% 空间
4. **多输出目标**: 可同时输出到文件和控制台
5. **格式选择**: 支持 JSON 和文本格式

**新增命令行参数：**
- `--log.totalsize` - 日志总大小上限 (MB)
- `--log.console` - 同时输出到控制台
- `--log.json` - 使用 JSON 格式

**推荐配置：**
```bash
# 生产环境 (自动清理，防止磁盘占满)
n42 --log.file n42.log --log.maxsize 100 --log.maxbackups 10 --log.maxage 30 --log.compress --log.totalsize 1000

# 磁盘紧张环境
n42 --log.file n42.log --log.maxsize 50 --log.maxbackups 5 --log.maxage 7 --log.compress --log.totalsize 500
```

---

#### 🔧 命令行参数整理

**新增文件：**
- `cmd/n42/flags.go` - 快捷启动参数定义

**修改文件：**
- `cmd/n42/main.go` - 更新启动流程和帮助信息
- `cmd/n42/config.go` - 规范化默认配置值
- `cmd/n42/cmd.go` - 添加参数分类和中文说明

**改进内容：**
- 新增快捷参数：`--testnet`, `--dev`, `--port`, `--mine`, `--etherbase`, `--syncmode`, `--debug`
- 规范化端口默认值：HTTP 8545, WS 8546, P2P 30303
- 增加 P2P 默认连接数：5 → 50
- 日志默认级别：debug → info
- 所有参数添加分类标签和中文说明
- 添加常用别名支持

---

#### 🐛 Bug 修复

**修复文件：**
- `internal/p2p/gossip_topic_mappings.go` - 修复锁竞态问题
- `internal/api/router.go` - 修复空指针解引用风险
- `internal/blockchain_reorg_audit.go` - 修复 Number64() 空指针风险

**问题详情：**
1. `gossip_topic_mappings.go`: RLock 释放后重新获取导致 defer RUnlock 异常，使用 `sync.Once` 替代
2. `router.go`: `GetChainConfig().ChainID.Uint64()` 未检查 nil
3. `blockchain_reorg_audit.go`: `Number64()` 返回值可能为 nil

---

#### 📚 文档

**新增文件：**
- `docs/QUICKSTART.md` - 节点快速启动指南
- `docs/CHANGELOG.md` - 修改日志（本文件）

---

### 2024-12-14

#### 🏗️ PR 7.1: Hardening 收口

**新增文件：**
- `internal/p2p/gossip_topic_mappings.go` - 显式 Topic 注册（替代 init()）
- `internal/p2p/gossip_topic_mappings_test.go` - Topic 注册测试
- `internal/blockchain_reorg_audit.go` - Reorg 审计系统
- `internal/blockchain_reorg_audit_test.go` - Reorg 审计测试
- `tools/bench/README.md` - 基准测试工具文档
- `tools/bench/run_smoke.sh` - RPC 冒烟测试脚本
- `tools/bench/cmd/rpc/main.go` - RPC 压力测试工具
- `tools/bench/cmd/metrics/main.go` - 指标收集工具

**修改文件：**
- `internal/blockchain.go` - 集成 ReorgAudit

---

#### 🏗️ PR 6.1: RPC 层职责分离

**新增文件：**
- `internal/api/backend.go` - Backend 接口定义
- `internal/api/backend_test.go` - Backend 接口测试
- `internal/api/router.go` - RPC 路由器
- `internal/api/interface.go` - RPCMetrics 定义
- `internal/api/interface_test.go` - RPCMetrics 测试
- `scripts/test_rpc.sh` - RPC 兼容性测试脚本

---

#### 🏗️ PR 5.1-5.2: Sync 状态机 & P2P 解耦

**新增文件：**
- `internal/sync/state_machine.go` - 同步状态机
- `internal/sync/state_machine_test.go` - 状态机测试
- `internal/p2p/sync_interface.go` - P2P 同步接口
- `internal/p2p/sync_interface_test.go` - P2P 接口测试
- `internal/sync/fetcher.go` - 区块获取器
- `internal/sync/fetcher_test.go` - 获取器测试

---

#### 🏗️ PR 4.1-4.2: 共识引擎统一

**新增文件：**
- `internal/consensus/engine.go` - 统一 Engine 接口
- `internal/consensus/engine_test.go` - Engine 接口测试
- `internal/consensus/base.go` - 基础 PoA 引擎
- `internal/consensus/base_test.go` - 基础引擎测试
- `internal/consensus/misc/errors.go` - 共识错误定义
- `internal/consensus/misc/constants.go` - 共识常量
- `internal/consensus/misc/difficulty.go` - 难度计算
- `internal/consensus/misc/seal.go` - 签名逻辑
- `internal/consensus/misc/header.go` - 头验证
- `internal/consensus/misc/misc_test.go` - misc 包测试

---

#### 🏗️ PR 3.1-3.2: EVM 接口化

**新增文件：**
- `internal/vm/interface.go` - VM 接口定义
- `internal/vm/interface_test.go` - VM 接口测试
- `internal/vm/instrumented.go` - 带监控的 VM 包装器
- `internal/vm/precompiles/registry.go` - 预编译合约注册表
- `internal/vm/precompiles/contracts.go` - 预编译合约工厂
- `internal/vm/precompiles/registry_test.go` - 注册表测试

**EVM 升级 (Cancun/Prague)：**
- `internal/vm/eips_cancun.go` - Cancun EIPs
- `internal/vm/eips_prague.go` - Prague EIPs
- `internal/vm/contracts_p256.go` - secp256r1 预编译
- `modules/state/transient_storage.go` - 临时存储 (EIP-1153)

---

#### 🏗️ PR 2.1-2.2: State 接口抽象

**新增文件：**
- `modules/state/interfaces.go` - StateReader/Writer 接口
- `modules/state/interfaces_test.go` - 接口测试
- `modules/state/instrumented.go` - 带监控的 State 包装器
- `modules/state/instrumented_test.go` - 监控测试

---

#### 🏗️ PR 1.x: 代码清理与规范化

**主要改动：**
- 移除已废弃和注释掉的代码块
- 统一命名：ast → n42
- 解决包别名混乱：block2 → block, mvm_types → avmtypes
- 创建统一错误包：`pkg/errors/errors.go`
- 移动 metrics 包：`internal/metrics/prometheus` → `common/metrics`
- 更新文件头版权信息

---

## 版本历史

### v0.01.1 (当前)

- 初始重构版本
- 接口统一化
- EVM Cancun/Prague 升级支持
- 命令行参数整理

---

## 贡献指南

提交代码时请同步更新本文件，格式如下：

```markdown
### YYYY-MM-DD

#### 类别 (使用 emoji)

**新增/修改/删除文件：**
- `path/to/file.go` - 简要说明

**改进内容：**
- 具体改动点
```

常用类别：
- 🆕 新功能
- 🔧 改进
- 🐛 Bug 修复
- 📚 文档
- 🏗️ 重构
- ⚡ 性能优化
- 🔒 安全修复
- 🧪 测试

