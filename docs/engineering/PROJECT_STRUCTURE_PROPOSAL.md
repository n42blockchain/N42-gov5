# N42 项目文件结构评估与调整方案

> 日期：2026-03-26
> 原则：不依赖 geth/erigon 历史惯例，从 N42 自身产品逻辑出发

---

## 1. 现状诊断

### 1.1 顶层目录统计

| 目录 | .go 文件数 | 职责 | 问题 |
|------|:---------:|------|------|
| `internal/` | 877 | 核心业务逻辑 | 正确，Go 标准 |
| `lib/` | 520 | 共享库（KV 存储、JMT、RLP 等） | 命名模糊——"lib" 不自描述 |
| `common/` | 383 | 类型、加密、编码、区块定义 | 太杂——混合了核心类型和密码学库 |
| `modules/` | 149 | 数据层（state、rawdb、RPC） | 与 `lib/` 职责边界不清 |
| `conf/` | 53 | 配置 | 正确 |
| `cmd/` | 51 | 入口程序 | 正确 |
| `accounts/` | 50 | 账户管理、ABI | 正确 |
| `api/` | 31 | Protobuf 定义 | 应叫 `proto/` 更清晰 |
| `params/` | 15 | 链参数 | 正确 |
| `tests/` | 19 | EEST/Hive 测试 | 正确 |
| `utils/` | 12 | 小工具函数 | 应合入 `common/` 或 `lib/` |
| `log/` | 7 | 日志 | 与 `lib/log/` 重复 |
| `tools/` | 6 | 性能基准 | 可合入 `cmd/` |
| `turbo/` | 4 | backup + rpchelper | Erigon 残留命名 |
| `pkg/` | 2 | 仅 errors 包 | 无意义 |
| `sdk/` | 1 | 单文件 | 无意义 |
| `console/` | 1 | 仅 prompt 包 | 可合入 |

### 1.2 核心问题

1. **`common/` vs `lib/` vs `modules/` 三层混淆**
   - `common/crypto/` (383 个文件含 PQ 密码学) vs `lib/crypto/` — 哪个是正统？
   - `common/types/` 和 `lib/types/` 都存在
   - `common/rlp/` 和 `lib/rlp/` + `lib/rlp2/` — 三套 RLP？
   - `modules/state/` 是状态管理，`lib/state/` 是历史聚合器 — 命名完全一样

2. **Erigon 残留**
   - `turbo/` — Erigon 的 "turbo-geth" 命名
   - `lib/gointerfaces/` — Erigon 的 gRPC 接口生成
   - `lib/direct/` — Erigon 的直连 KV 客户端
   - `modules/changeset/` — Erigon 的变更集

3. **空目录 / 单文件目录**
   - `pkg/errors/`（2 个文件）、`sdk/`（1 个文件）、`console/`（1 个文件）

4. **`internal/` 子目录过多**
   - 35 个子目录，部分极小：`cache/`(1)、`txgen/`(1)、`pubsub/`(2)、`replay/`(2)、`debug/`(2)

---

## 2. 调整原则

1. **按产品域分组，不按技术层分组**
   - "这段代码服务于什么功能？" 而非 "这是哪种技术抽象？"

2. **最多 3 层深度**
   - 顶层 → 功能域 → 具体模块，不再嵌套

3. **消除歧义命名**
   - 如果两个目录名需要解释区别，说明命名有问题

4. **小目录合并**
   - < 3 个文件的目录合入父目录

5. **保持 Go 惯例**
   - `internal/` 放私有逻辑
   - `cmd/` 放入口
   - 公开库顶层放置

---

## 3. 推荐结构

### 3.1 顶层目录（12 个，当前 24 个）

```
N42-gov5/
├── cmd/                    # 入口程序（不变）
│   ├── n42/               # 主节点
│   ├── rpcdaemon/         # 独立 RPC
│   ├── clef/              # 外部签名
│   ├── evmsdk/            # 移动 SDK
│   ├── zkguest/           # ZK guest 程序
│   └── tools/             # sszdiag, stresstest, verify, hotstuff-testnet (合并 cmd/* 小工具)
│
├── internal/               # 私有业务逻辑（不变，内部调整见 3.2）
│
├── crypto/                 # ← 从 common/crypto/ 提升（383 文件的独立域）
│   ├── bls/               # BLS12-381
│   ├── dilithium/         # PQ 签名
│   ├── falcon/            # PQ 签名
│   ├── kyber/             # PQ KEM
│   ├── kzg/               # KZG 承诺
│   ├── stark/             # STARK
│   ├── sha3/              # SHA3/Keccak
│   └── ...                # ecies, bn256 等
│
├── types/                  # ← 合并 common/types/ + common/block/ + common/transaction/ + common/account/
│   ├── address.go         # Address, Hash
│   ├── block.go           # Header, Body, Block 接口
│   ├── transaction.go     # Tx 类型（Legacy, EIP-1559, Blob, SetCode, PQ）
│   ├── receipt.go         # Receipt
│   └── account.go         # StateAccount
│
├── encoding/               # ← 合并 common/rlp/ + common/encoding/ + common/hexutil/
│   ├── rlp/               # RLP 编解码（合并 lib/rlp/ lib/rlp2/ common/rlp/）
│   └── hexutil/           # Hex 工具
│
├── storage/                # ← 合并 lib/kv/ + modules/ethdb/ + modules/rawdb/ + lib/etl/ + lib/seg/
│   ├── kv/                # KV 抽象 (mdbx, memdb, remotedb)
│   ├── rawdb/             # 底层数据库操作
│   ├── etl/               # 批量加载
│   └── segment/           # 分段文件（freezer/era）
│
├── state/                  # ← 合并 modules/state/ + lib/state/ + lib/commitment/
│   ├── intra/             # IntraBlockState（块内状态管理）
│   ├── commitment/        # JMT + LtHash 状态承诺
│   ├── history/           # HistoryV3 聚合器（原 lib/state/）
│   ├── changeset/         # 变更集（原 modules/changeset/）
│   └── jmt/               # Jellyfish Merkle Tree（原 lib/jmt/）
│
├── p2p/                    # ← 从 internal/p2p/ 提升为公开包（84 文件，独立域）
│
├── accounts/               # 账户管理 + ABI（不变）
│
├── contracts/              # Solidity 合约（不变）
│
├── conf/                   # 配置（不变）
│
├── params/                 # 链参数 + chainspec（不变）
│
├── docs/                   # 文档（不变）
│
├── proto/                  # ← 重命名 api/protocol/（Protobuf 定义）
│
├── scripts/                # 构建/部署脚本（不变）
│
├── tests/                  # EEST/Hive 测试（不变）
│
└── deployments/            # Docker/Prometheus/Explorer 部署配置（不变）
```

### 3.2 internal/ 调整（4 个功能域）

```
internal/
├── node/                   # 节点装配 + 生命周期（不变）
│
├── execution/              # ← 合并执行相关
│   ├── vm/                # EVM（原 internal/vm/）
│   ├── avm/               # AVM（原 internal/avm/）
│   ├── processor.go       # 状态处理器（原 internal/state_processor.go）
│   ├── parallel.go        # 并行执行（原 internal/parallel/）
│   ├── deferred/          # 延迟执行管道
│   ├── tracers/           # 调试/跟踪
│   └── tile/              # Tile 并行
│
├── consensus/              # 共识引擎（不变）
│   ├── apoa/              # PoA
│   ├── apos/              # PoS
│   └── hotstuff/          # HotStuff-2 BFT
│
├── network/                # ← 合并网络相关
│   ├── sync/              # 链同步（原 internal/sync/）
│   ├── txpool/            # 交易池（原 internal/txspool/）
│   ├── download/          # 下载管理
│   └── peerdas/           # 数据可用性采样
│
├── api/                    # JSON-RPC API（不变）
│
├── bridge/                 # 跨链桥（不变，已整理）
│
├── ai/                     # AI 基础设施（不变）
│   ├── wallet/            # Agent 钱包
│   ├── coord/             # Agent 协调
│   ├── governance/        # 数据治理
│   ├── training/          # 训练验证
│   └── attestation/       # 推理证明
│
├── distributed/            # 分布式基础设施（不变）
│   ├── compute/           # 计算引擎
│   ├── coprocessor/       # 协处理器
│   ├── messaging/         # 消息传递
│   ├── storage/           # 分布式存储
│   └── notify/            # 推送通知
│
├── zk/                     # ← 合并 ZK 相关
│   ├── prover/            # ZK 证明（原 internal/zkprover/）
│   └── verifier/          # ZK 验证（原 internal/zkverifier/）
│
├── mev/                    # MEV（不变）
├── miner/                  # 出块（不变）
├── bundler/                # ERC-4337（不变）
├── mcp/                    # MCP Server（不变）
├── exex/                   # 执行扩展（不变）
└── metrics/                # Prometheus 指标（不变）
```

### 3.3 要删除/合并的目录

| 当前目录 | 行动 | 去向 |
|---------|------|------|
| `turbo/` | **删除** | `turbo/backup/` → `internal/node/backup/`；`turbo/rpchelper/` → `internal/api/rpchelper/` |
| `pkg/` | **删除** | `pkg/errors/` → 直接用 `fmt.Errorf` 或合入 `common/` |
| `sdk/` | **删除** | 1 个文件，合入 `cmd/evmsdk/` |
| `console/` | **删除** | 1 个文件，合入 `cmd/n42/` |
| `utils/` | **删除** | 12 个文件合入 `common/` |
| `log/` | **删除** | 合入 `lib/log/`（避免两个 log 包） |
| `tools/` | **合并** | 合入 `cmd/tools/` |
| `benchmarks/` | **合并** | 合入 `tests/benchmarks/` |
| `hotstuff_testnet/` | **移动** | 移入 `devtest/hotstuff_testnet/` |

---

## 4. 不建议动的部分

| 目录 | 原因 |
|------|------|
| `cmd/` | Go 标准，已清晰 |
| `internal/consensus/` | 边界清晰，3 个引擎互不干扰 |
| `internal/bridge/` | 刚完成整理，14 个文件 + doc.go |
| `internal/ai/` | 5 个子包，结构合理 |
| `internal/distributed/` | 5 个子包，结构合理 |
| `contracts/` | Solidity 合约，不适合 Go 结构调整 |
| `params/` | 小且独立 |
| `conf/` | 配置集中 |
| `tests/` | 测试数据独立 |

---

## 5. 实施策略

### 优先级

```
P0（立即价值，低风险）:
  1. 删除空/单文件目录（turbo, pkg, sdk, console）      — 1 小时
  2. utils/ 合入 common/                                  — 0.5 天
  3. log/ 合入 lib/log/                                   — 0.5 天
  4. hotstuff_testnet/ 移入 devtest/                       — 0.5 天

P1（中等价值，需仔细测试）:
  5. common/crypto/ 提升为顶层 crypto/                     — 2-3 天
  6. 三套 RLP 合一                                         — 1-2 天
  7. modules/state/ + lib/state/ + lib/commitment/ 合并    — 3-5 天
  8. api/protocol/ 重命名为 proto/                          — 0.5 天

P2（大重构，建议下一版本周期）:
  9. common/types/ + common/block/ 合并为顶层 types/        — 5+ 天
  10. lib/kv/ + modules/ethdb/ + modules/rawdb/ 合并       — 5+ 天
  11. internal/ 子目录精简（execution/ 合并等）             — 5+ 天
```

### 关键约束

- **所有重命名必须同时更新 import 路径**（`goimports` + `sed`）
- **`go.mod` module path 不变**（`github.com/n42blockchain/N42`）
- **每个 P0/P1 变更独立提交**，确保可回滚
- **不影响 Hive/EEST 测试通过**
- **不改变任何公开 API**

---

## 6. 结论

N42 的文件结构主要问题不是"太乱"，而是**层次不明**：`common/` vs `lib/` vs `modules/` 三个顶层目录职责重叠。核心改进是：

1. **密码学独立**：383 文件的 `common/crypto/` 值得成为顶层 `crypto/`
2. **状态合一**：3 个状态相关目录（`modules/state/`, `lib/state/`, `lib/commitment/`）应合并
3. **清理残留**：`turbo/`, `pkg/`, `sdk/`, `console/` 是无意义的空壳
4. **命名修正**：`api/protocol/` → `proto/`，`router.go` → `types.go`（已完成）

P0 项可以立即执行（几小时），P1 项需要 1-2 周，P2 项建议在下一个版本周期内渐进完成。

---

*本分析基于对 24 个顶层目录、35 个 internal/ 子目录、2100+ Go 文件的完整扫描。*
