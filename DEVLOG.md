# N42 Development Log

> 记录每次重大开发操作，从新到旧排列。

---

## 2026-02-23 — Refactor: 命名规范化（snake_case）

**提交**: `c67c274`, `b009161`

**目标**: 统一全项目文件名、目录名、包名为 snake_case / Go 标准规范。

**操作步骤**:
1. 重命名文件: `gasLimit.go` → `gas_limit.go`, `blake2bAVX2_amd64.go` → `blake2b_avx2_amd64.go`
2. 修复包名: `astdb` → `amcdb`, `hash` → `pedersen_hash`
3. `lib/rlp2` 包声明从 `rlp` 改为 `rlp2`（10 个文件），更新 2 个 importer
4. 合约目录小写化: `contracts/deposit/{AMT,FUJI,NFT}` → `{amt,fuji,nft}`，更新 `node.go` 导入
5. 目录重命名: `leaky-bucket` → `leakybucket`, `initial-sync` → `initialsync`，更新 4 个 importer
6. `p2p/types` → `p2p/p2ptypes`，移除 9 个 importer 的冗余别名

**影响范围**: 整个代码库（VERSION 自动递增）

---

## 2026-02-23 — Refactor: 大文件拆分 + KV/State Bug 修复

**提交**: `783e8a0`, `4c0294d`, `c0d8b74`

**目标**: 职责分离，修复历史遗留 bug。

### 大文件拆分 (`783e8a0`)
将 4 个超大文件拆分为单职责模块：
- 拆分 `blockchain.go` → `blockchain_reader.go`, `blockchain_insert.go`, `blockchain_reorg_audit.go`
- 拆分 `miner/worker.go` 相关逻辑

### KV/State Bug 修复 (`4c0294d`)
修复 5 个预存在的 cursor 和 key-ordering bug：
1. MDBX cursor 使用后未关闭
2. key 排序在某些 state 遍历中不一致
3. 相关测试对齐

### 全包清理 (`c0d8b74`)
- 移除无用导入、死代码
- 统一错误处理模式
- 简化复杂函数

---

## 2026-02-15 — Feat: 同步进度条增强

**提交**: `7e4dc3b`, `3e5fe25`, `479d506`, `14d18fb`, `5446ff5`, `13f4cd4`, `828620f`, `fb1a33e`, `cd38182`, `b024e9d`, `974b395`

**目标**: 提升节点启动和同步时的控制台输出体验。

**操作步骤**:
1. `974b395` — 添加进度条、分节和错误框（基础框架）
2. `b024e9d` — 修复分隔符宽度上限，解决进度条碰撞和填充显示问题
3. `cd38182` — 将系统信息移至 banner 右侧
4. `fb1a33e` / `828620f` — 统一启动/关闭进度风格
5. `13f4cd4` — 截断日志中过长的 ENR 值
6. `5446ff5` — 修复进度条对齐、清除行残留、同步期间屏蔽 P2P 输出
7. `14d18fb` — 进度条添加时间戳
8. `479d506` — 简化时间戳格式，复用 formatter 实例
9. `3e5fe25` — 同步期间将 deposit/reward 日志聚合进进度条
10. `7e4dc3b` — 进度条百分比后显示区块高度

---

## 2026-02-14 — Fix: Audit Round 4（全库审计，11 个阶段）

**提交**: `e438c33` → `4b71cb1`（共 6 次提交覆盖 11 个 phase）

**目标**: 系统性审计全部生产代码，修复安全、正确性和鲁棒性问题。

| Phase | 提交 | 覆盖范围 |
|-------|------|---------|
| 1 | `e438c33` | cmd/, conf/, params/ |
| 2 | `b174d53` | internal/blockchain*, internal/miner |
| 3 | `c7e16d3` | internal/api/ — RPC 错误处理 |
| 4-6 | `52c8605` | P2P, sync, consensus, contracts |
| 7-9 | `a3965eb` | txpool, state, storage |
| 10-11 | `4b71cb1` | accounts/, turbo/rpchelper |

**同期热修复**（同日，不同时区）:
- `15ccd1a` — genesis: 修复 mdbx.NewMDBX 空指针 panic（缺少 logger 参数）
- `fe01950` — log: 防止重要 P2P 字段（enr, multiAddr）被截断
- `ea4c80a` — sync: block 订阅日志补充 stateRoot 和 txs 字段
- `091bb17` — sync: 添加循环继续同步机制（同步期间链前进时）
- `8d30a33` — p2p: 移除 discovery 日志中的 ENR 截断
- `d0ccc2d` — rpc: 从 HTTP API 移除 admin 模块（安全）
- `c0171b6` — p2p: 更新主网 bootnode ENR 为当前生产节点

---

## 2026-02-12 — Build/Infra: 依赖升级与基础设施

**提交**: `bab3ac4`, `0d7222a`, `709b3a1`, `93de8ba`, `158649b`, `a29d55c`

**操作步骤**:
1. `158649b` — 升级到 Go 1.24，移除 GOPROXY
2. `93de8ba` — chainspec 添加 Shanghai, Cancun, Pectra, Osaka, Fusaka fork 配置
3. `709b3a1` — 升级后量子密码学（PQC）模块依赖
4. `0d7222a` — 添加 `.dockerignore`
5. `a29d55c` — sync: 添加 near-synced 阈值，平滑 gossip 过渡
6. `bab3ac4` — 升级所有直接依赖至最新版本

---

## 2026-02-11 — Fix/Feat: Blockscout 兼容 + Audit Round 2

**提交**: `ddde1e2`, `759b944`, `26c5ecd`, `9a99c2e`, `3887971`, `4d4e81a`, `7143353`, `8fd2349`

**操作步骤**:
1. `8fd2349` — 版本发布 v5.4.600，升级依赖
2. `7143353` — 修复关闭顺序：sync 先于 blockchain 停止
3. `4d4e81a` — Audit Round 1: 安全、正确性、鲁棒性修复
4. `3887971` — 代码可读性、一致性重构清理
5. `9a99c2e` — 完善 Blockscout API 现代 Ethereum RPC 响应字段
6. `26c5ecd` — Audit Round 2: 类型安全、性能、测试对齐
7. `ddde1e2` — 更新 Blockscout 兼容版本至 v9.3.3
8. `759b944` — 使用动态 `params.Version` 替换硬编码 NodeVersion

---

## 2026-02-26 — Fix: 实现 API/P2P/Miner 集成，清理 TODO，实现 ForkID

**提交**: (待提交)

**目标**: 完成记录在 DEVLOG 中的所有关键 TODO 项。

### 操作步骤

1. **`log/root.go`** — 添加 `SetLevel(int)` 和 `GetLevel() int` 公共函数，供 `debug_verbosity` RPC 动态调整日志级别。

2. **`internal/api/api.go`** — 扩展 `API` 结构体：
   - 定义 `P2PAdmin` 接口（`PeerInfos`, `SelfNodeID`, `SelfENR`, `SelfListenAddrs`, `AddPeer`, `RemovePeer`）
   - 定义 `MinerAdmin` 接口（`Mining`, `SetCoinbase`）
   - 添加 `api.p2p P2PAdmin` 和 `api.miner MinerAdmin` 字段
   - 添加 `SetP2P()` 和 `SetMiner()` setter 方法

3. **`internal/api/rpc_extra.go`** — 实现所有 TODO 方法：
   - `AdminAPI.NodeInfo()`: 从 P2P 层读取真实 NodeID/ENR/ListenAddrs
   - `AdminAPI.Peers()`: 返回真实连接的 peer 列表
   - `AdminAPI.AddPeer()`: 接受 multiaddr 格式，调用 P2P 连接
   - `AdminAPI.RemovePeer()`: 接受 peer ID，调用 P2P 断开
   - `MinerAPI.Mining()`: 返回 miner.Mining() 真实状态
   - `MinerAPI.SetEtherbase()`: 调用 miner.SetCoinbase()
   - `DebugAPI.Verbosity()`: 通过 log.SetLevel() 设置全局日志级别（0~5 映射到 Crit~Trace）
   - `DebugAPI.Vmodule()`: 接受调用但说明 logrus 不支持模块级过滤

4. **`internal/node/node.go`** — 添加适配器：
   - `p2pAdminAdapter`：实现 `P2PAdmin`，桥接 `p2p.P2P`（包括 Peers/ENR/Host/AddPeer/RemovePeer）
   - `minerAdminAdapter`：实现 `MinerAdmin`，桥接 `*miner.Miner`
   - 在 `NewNode()` 中连线：`api.SetP2P()` 和 `api.SetMiner()`

5. **`internal/network/eth69/handler.go`** — 实现 `makeForkID()`：
   - 返回 `genesisHash[:4]`，与 `utils.CreateForkDigest` 保持一致
   - 空 genesis hash 时返回空字节（安全处理）

6. **`internal/block_validator.go`** — 移除过时 `TODO 替换 emptyroot` 注释（代码逻辑已正确）

7. **`internal/blockchain.go`** —
   - `InsertHeader`: 改为正式文档说明"light client 未支持"
   - `AddFutureBlock`: 移除过时 TODO，改为正确描述 PoS 处理逻辑

**验证**: `go build ./...` 全部通过，版本升至 v5.4.640

---

## 待处理事项（Outstanding TODOs）

以下为核心代码中尚未实现的功能（共 122 处，以下为关键项）：

### 已解决（2026-02-26）
| 文件 | 原 TODO | 状态 |
|------|---------|------|
| `internal/block_validator.go` | 替换 emptyroot | ✅ 移除（逻辑已正确） |
| `internal/blockchain.go:205` | InsertHeader not implemented | ✅ 改为正式文档说明 |
| `internal/blockchain.go:1073` | future block 过渡后清理 | ✅ 移除，改为描述现有行为 |
| `internal/api/rpc_extra.go` | 12 处 P2P/miner/logger 集成 | ✅ 全部实现 |
| `internal/network/eth69/handler.go:227` | Implement ForkID | ✅ 实现（genesis hash[:4]） |
| `log/root.go` | 无 SetLevel 公共 API | ✅ 添加 SetLevel/GetLevel |

### 仍待处理
| 文件 | TODO | 说明 |
|------|------|------|
| `interfaces.go:33` | move Subscription to package event | 低优先级重构，影响 `accounts/abi/bind` |
| `internal/blockchain.go:205` | Light client InsertHeader | 需要完整轻客户端架构设计 |
| `internal/api/filters/filter.go` | bloombits matcher | 性能优化，当前走全量扫描 |
| `internal/api/filters/filter.go` | use header.Bloom | 同上 |
| `internal/p2p/discover/v5wire/encoding.go` | WHOAREYOU tie-breaker; rehandshake | 协议层优化，上游 libp2p 相关 |
| `lib/state/inverted_index.go` | pass error properly around | 错误传播重构 |

---

## 工作区当前状态

```
分支:  main（与 origin/main 同步）
未跟踪: .claude/, CLAUDE.md, verify（n42 arm64 可执行文件，38MB）
待提交变更: 无
```

**结论**: 无未完成计划。所有审计轮次和重构均已提交。待处理的是长期 TODO（主要为 API 与 P2P/miner 集成），需独立规划。
