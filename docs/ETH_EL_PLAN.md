# N42 ETH EL — 完整实施计划

## 目标

单二进制（`n42 --chain eth-mainnet`）作为标准以太坊执行层，能：
1. 从头同步（BT 下载 freezer 文件 → 执行 → 追赶链头）
2. CL 驱动（Engine API v1-v4 接收 payloads → 执行 → 验证 state root）
3. 提供标准 JSON-RPC（eth_*, debug_*, trace_*）
4. 状态快照同步（snap/1 快速追赶高度）

## 设计原则

- **极简**：最少代码，最大复用已有模块
- **正确**：每块 gas + state root 与 geth 100% 一致
- **共用**：ETH EL 和 N42 共用 EVM、MDBX、RPC、crypto、freezer

---

## 架构

```
                        ┌─────────────┐
                        │  CL Client  │  (Lighthouse/Prysm)
                        │  (外部)      │
                        └──────┬──────┘
                               │ Engine API v1-v4
                               ▼
┌──────────────────────────────────────────────────────┐
│                    n42 --chain eth-mainnet            │
├──────────┬───────────┬────────────┬──────────────────┤
│ Engine   │ JSON-RPC  │  devp2p    │   BT Sync        │
│ API      │ eth_*     │  eth/69    │   (OtterSync)    │
│ (已有)    │ (已有)    │  (已有骨架) │   (已有基础)      │
├──────────┴───────────┴────────────┴──────────────────┤
│              Staged Sync Pipeline (已有框架)           │
│  Headers → Bodies → Senders → Execution →            │
│  HashState → Commitment → Indices → Finish           │
├──────────────────────────────────────────────────────┤
│              Block Executor (internal/ethel/)         │
│  ✅ RLP decode  ✅ EVM execute  ✅ State write        │
│  ✅ Receipts    ✅ Senders      ✅ Changesets         │
│  ✅ Witness     ✅ Leaves       ✅ State root verify  │
├──────────────────────────────────────────────────────┤
│              Storage Layer                           │
│  Freezer (cidx/cdat) ← ✅ Geth 兼容 + 跨文件读取     │
│  MDBX ← ✅ PlainState + HashedAccounts + Trie        │
└──────────────────────────────────────────────────────┘
```

---

## 已完成 ✅

| 模块 | 文件 | 状态 |
|------|------|------|
| Freezer 改造 | modules/rawdb/freezer/ | Geth cidx/cdat 6B 格式，跨文件读取，ErrPruned |
| RLP 解码 | internal/ethel/rlp_decode.go | Snappy + RLP，全分叉 + typed tx 解包 |
| Replay 引擎 | internal/ethel/consensus.go | EthReplayEngine（区块+uncle 奖励）|
| 执行循环 | internal/ethel/executor.go | 读 freezer → EVM → 写 state + outputs |
| Output Freezer | internal/ethel/executor.go | receipts, senders, changesets, leaves, witness |
| Genesis 加载 | internal/ethel/genesis.go | 8893 ETH 主网账户 |
| State Root 验证 | internal/ethel/hashstate.go | FullStateRootVerify（2M blocks 验证通过）|
| CLI 工具 | cmd/ethexec/ | 独立批量执行，断点续传 |
| Engine API | internal/api/engine_api_*.go | v1-v4 完整 |
| Engine Adapter | internal/api/engine_state_adapter.go | 骨架 |
| Profile 系统 | params/profile.go | IsEthereumEL() |
| EIP-155 签名 | common/transaction/ | SpuriousDragon signer 分支 |
| DAO gas 修复 | modules/state/ | shouldAllowWriteBack |
| ExistPure | modules/state/ + internal/vm/ | 无副作用 Exist |
| eth/69 协议 | internal/network/eth69/ | 消息类型定义 |
| Staged Sync 框架 | internal/sync/staged/ | Pipeline/Stage 接口 |
| BT 下载基础 | lib/downloader/ | BitTorrent 客户端 |
| OtterSync | internal/sync/torrentsync/ | EraE 段导入导出 |

## 待完成

### Phase 1: 从头同步（BT + 执行）

**目标**：新节点通过 BT 下载 freezer 文件，然后执行追赶链头。

#### 1.1 Freezer 文件 BT 种子生成与分发

```
复用：lib/downloader/ (BitTorrent 客户端)
复用：internal/sync/torrentsync/ (OtterSync 段导出)
新建：internal/ethel/seeder.go
```

- 将 output freezer 的 cdat 文件打包为 BT 种子
- 按月度或固定块数分段（如 500K blocks/段）
- manifest.json 记录段范围和哈希
- 新节点通过 BT 下载 freezer 段文件

**复用基础**：`lib/downloader/` 已有完整 BT 客户端（anacrolix/torrent），`internal/sync/torrentsync/` 已有段导出/导入逻辑。

#### 1.2 状态快照同步（snap/1）

```
新建：internal/sync/snap/
复用：modules/state/plain_state_reader.go
复用：modules/state/commitment/ (CalcTrieRoot)
```

- 实现 snap/1 协议（GetAccountRange, GetStorageRanges, GetByteCodes, GetTrieNodes）
- 从 peer 下载最新 state 快照 → 写入 MDBX PlainState
- Healing 阶段填补缺失 trie 节点
- 验证 state root 与链头一致

**或替代方案**：直接复制已同步节点的 MDBX 文件（最简单），或从 Geth state dump 导入。

#### 1.3 快速追赶（BT freezer + snap state）

```
修改：cmd/ethexec/ 或集成到 cmd/n42/
```

启动流程：
1. 检查本地 state 高度
2. 如果落后链头 >N blocks：
   a. BT 下载缺失的 freezer 段文件
   b. 从已有 state 继续执行 freezer 中的 blocks
3. 如果无 state：snap 同步获取最新 state，然后从该高度继续
4. 追赶到链头后切换到 CL 驱动模式

### Phase 2: CL 驱动（Engine API 集成）

**目标**：连接 Lighthouse/Prysm，实时跟链。

#### 2.1 EngineStateAdapter 完善

```
修改：internal/api/engine_state_adapter.go
复用：internal/ethel/executor.go (processBlock 逻辑)
```

- `ExecutePayload`：接收 CL payload → 构建 block → EVM 执行 → state root 验证
- `ForkchoiceUpdated`：更新 head/safe/finalized → reorg 处理（changeset 回退）
- `GetPayload`：payload building（从 mempool 选 tx 构建 block）

**关键**：提取 `executor.processBlock` 为共享函数，Engine adapter 和 batch executor 共用。

#### 2.2 Reorg 处理

```
复用：modules/rawdb/freezer/ (changeset 读取)
复用：modules/state/ (PlainStateWriter 回滚)
```

- 从 changeset freezer 读取回滚数据
- 用 original values 覆盖 PlainState
- 更新 HashedAccounts/TrieOfAccounts

### Phase 3: devp2p 网络

**目标**：独立 ETH 节点，不依赖 BT 或外部数据。

#### 3.1 devp2p RLPx 传输

```
依赖：github.com/ethereum/go-ethereum/p2p (作为 Go 库)
新建：internal/devp2p/server.go
```

- RLPx 加密传输
- discv4/discv5 节点发现
- 连接管理

#### 3.2 eth/68-69 协议处理

```
复用：internal/network/eth69/ (已有消息类型)
新建：internal/devp2p/eth_handler.go
新建：internal/devp2p/downloader.go
```

- block announcement + propagation
- header/body 下载
- transaction propagation（到 mempool）
- 与 staged sync pipeline 集成

### Phase 4: 索引与 RPC

**目标**：完整的 JSON-RPC 支持。

#### 4.1 索引 Stage

```
复用：modules/rawdb/log_index.go
复用：modules/rawdb/ (WriteTxLookupEntries)
修改：internal/sync/staged/eth_stages.go
```

- TxLookup（tx hash → block number）
- LogTopicIndex / LogAddressIndex（bitmap 索引）
- CallFromIndex / CallToIndex

#### 4.2 历史查询

```
复用：modules/state/ (HistoryStateReader)
复用：freezer changeset 表
```

- `eth_call` 带历史 block number → 从 changeset 回溯 state
- `eth_getBalance`/`eth_getStorageAt` 历史查询

### Phase 5: 增量 State Root（性能优化）

**目标**：每块增量计算 state root，不需要全量 re-hash。

```
复用：lib/commitment/hex_patricia_hashed.go (Erigon HPH)
复用：modules/state/commitment/trie_root_computer.go
修改：internal/ethel/hash_only_computer.go
```

- 每块通过 HashOnlyComputer 更新 HashedAccounts/Storage
- 在 CommitBlock 之前调用 IntermediateRoot 收集 dirty keys
- CalcTrieRoot 只重算 dirty paths（RetainList）
- 验证 root 匹配 header.Root

---

## 缺失的 Freezer 输出

| 表 | 状态 | 用途 |
|----|------|------|
| receipts | ✅ | RPC 查询 |
| senders | ✅ | 避免重复恢复签名 |
| account_changes | ✅ | 历史查询 + reorg 回滚 |
| storage_changes | ✅ | 历史查询 + reorg 回滚 |
| leaves_journal | ✅ | 增量 trie 更新 |
| block_witness | ✅ | 无状态执行验证（不含 code）|
| trie_history | ❌ 待做 | trie 节点增量（state proof）|
| exec_input | ❌ 待做 | 块执行 input stream |

---

## 实施优先级

```
Phase 1.1 (BT sync)     ██████████░░░░ 可复用 OtterSync
Phase 2.1 (Engine API)   ████████░░░░░░ 骨架已有，需完善
Phase 2.2 (Reorg)        ██████░░░░░░░░ changeset 已写入
Phase 4.1 (Indices)      ████░░░░░░░░░░ rawdb 函数已有
Phase 1.2 (Snap sync)    ██░░░░░░░░░░░░ 全新
Phase 3   (devp2p)       ██░░░░░░░░░░░░ 全新
Phase 5   (增量 root)     ████████░░░░░░ HPH 已有，需调通 timing
```

**推荐顺序**：2.1 → 2.2 → 4.1 → 1.1 → 5 → 1.2 → 3

先让 CL 驱动工作（Engine API），再加索引（RPC），再加同步。

---

## 关键复用文件

| 组件 | 路径 |
|------|------|
| Freezer | modules/rawdb/freezer/ |
| EVM | internal/vm/, internal/state_processor.go |
| State R/W | modules/state/plain_state_*.go |
| HPH MPT | lib/commitment/hex_patricia_hashed.go |
| CalcTrieRoot | lib/trie/trie_root.go |
| ETL | lib/etl/ |
| Engine API | internal/api/engine_api_*.go |
| RPC | internal/api/, modules/rpc/ |
| Profile | params/profile.go |
| ETH Config | params/eth_chain_config.go |
| BT Client | lib/downloader/ |
| OtterSync | internal/sync/torrentsync/ |
| Staged Sync | internal/sync/staged/ |
| eth/69 | internal/network/eth69/ |
| Account V2 | common/account/state_account.go |
| TX Signing | common/transaction/transaction_signing.go |
