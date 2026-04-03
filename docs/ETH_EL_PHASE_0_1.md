# ETH EL — Phase 0 + Phase 1 实施计划

## Phase 0: 集成到 n42 二进制（CL 驱动 + RPC）

**目标**：`n42 --chain eth-mainnet --authrpc` 能连接 CL，实时跟链，提供 RPC。

### 0.1 共享 processBlock 提取

```
改：internal/ethel/executor.go → 提取 ProcessBlock 为包级函数
改：internal/api/engine_state_adapter.go → 调用 ethel.ProcessBlock
```

当前 `executor.processBlock` 和 `EngineStateAdapter.processBlock` 重复。提取为：

```go
// internal/ethel/process.go
func ProcessBlock(
    chainCfg *params.ChainConfig,
    engine   consensus.Engine,
    header   *block.Header,
    txs      []*transaction.Transaction,
    uncles   []block.IHeader,
    ibs      *state.IntraBlockState,
    blockHashFunc func(uint64) types.Hash,
) (receipts block.Receipts, senders []types.Address, gasUsed uint64, err error)
```

executor.processBlock 和 engine_state_adapter 都调用此函数。

**预估：~200 行改动**

### 0.2 Engine API Adapter 完善

```
改：internal/api/engine_state_adapter.go
复用：ethel.ProcessBlock
复用：modules/state/plain_state_writer.go
```

实现三个核心方法：

**ExecutePayload (NewPayload)**：
1. 从 payload 构建 block（已有 executionPayloadV4ToBlock）
2. 从 MDBX 读 parent state
3. 调用 `ethel.ProcessBlock` → receipts + state changes
4. `CommitBlock` 写入 MDBX + output freezer
5. 计算 state root → 对比 header.Root
6. 返回 VALID/INVALID

**ForkchoiceUpdated**：
1. 写 head/safe/finalized hash 到 `LastForkchoice` 表
2. 如果 headBlockHash != current head → 需要 reorg
3. 如果 attrs != nil → 启动 payload building

**GetPayload**：
1. 从 mempool 选 tx（复用 txspool）
2. 构建 block → 执行 → 返回 payload

**预估：~500 行**

### 0.3 Reorg 回退

```
新建：internal/ethel/reorg.go
复用：freezer changeset 表
复用：modules/state/plain_state_writer.go
```

当 ForkchoiceUpdated 指定的 head 不在 canonical chain 上：
1. 找到分叉点（LCA）
2. 从当前 head 回退到 LCA：逐块读 changeset，用 original values 覆盖 PlainState
3. 从 LCA 前进到新 head：执行新 blocks

```go
// internal/ethel/reorg.go
func Reorg(db kv.RwDB, freezer *freezer.Freezer, from, to uint64) error
```

**预估：~200 行**

### 0.4 集成到 node.go

```
改：internal/node/node.go
复用：params/profile.go (IsEthereumEL)
```

在 `node.go` 的 ETH EL 启动路径中：
1. 创建 EthReplayEngine（或真正的 ethash engine）
2. 创建 EngineStateAdapter
3. 注册到 Engine API handler
4. 启动 background freezer（从 MDBX 迁移历史数据到 freezer）

```go
if profile.IsEthereumEL() {
    adapter := ethel.NewEngineStateAdapter(chainDB, outFreezer, chainCfg, engine)
    engineAPI.SetAdapter(adapter)
}
```

**预估：~100 行改动**

### 0.5 索引 Stage（RPC 支持）

```
新建：internal/ethel/indices.go
复用：modules/rawdb/ (WriteTxLookupEntries, log_index)
```

在每块执行后写入索引：
- `TxLookup`：tx hash → block number（MDBX 表）
- `LogTopicIndex` / `LogAddressIndex`：bitmap 索引（MDBX 表）

索引可以在执行时实时写入，或作为单独的 Staged Sync stage。

**预估：~150 行**

---

## Phase 1: 从头同步

**目标**：新节点能自动下载数据并追赶链头。

### 1.1 Freezer 段文件生成

```
新建：internal/ethel/segment.go
复用：internal/sync/torrentsync/exporter.go
```

将 output freezer 的 cdat 文件按固定块数（500K）分段：
```
segments/
├── manifest.json       # [{range: "0-499999", hash: "...", tables: [...]}]
├── seg-000000-499999/
│   ├── headers.0000.cdat
│   ├── bodies.0000.cdat
│   ├── receipts.0000.cdat
│   ├── senders.0000.cdat
│   ├── ...
│   └── segment.torrent
├── seg-500000-999999/
│   └── ...
```

每段生成 `.torrent` 文件用于 BT 分发。

**复用**：`internal/sync/torrentsync/exporter.go` 已有段导出逻辑，`lib/downloader/torrent.go` 已有种子生成。

**预估：~300 行**

### 1.2 BT 下载 + 导入

```
新建：internal/ethel/bt_sync.go
复用：lib/downloader/ (BitTorrent 客户端)
复用：internal/sync/torrentsync/importer.go
```

新节点启动时：
1. 下载 `manifest.json`（从 well-known URL 或 DHT）
2. 对比本地已有段 vs manifest
3. BT 下载缺失段
4. 验证段 hash
5. 将下载的 cdat 文件链接/复制到 freezer 目录

```go
// internal/ethel/bt_sync.go
type BTSync struct {
    downloader *downloader.Downloader
    freezer    *freezer.Freezer
    manifest   *Manifest
}

func (s *BTSync) SyncToLatest(ctx context.Context) error
```

**复用**：`lib/downloader/` 已有完整 BT 客户端（连接管理、piece 验证、rate limiting），`torrentsync/importer.go` 已有段导入验证。

**预估：~400 行**

### 1.3 状态快照导入

```
新建：internal/ethel/state_import.go
```

三种方式获取初始 state（按优先级）：

**方式 A：直接复制 MDBX（最简单）**
```bash
# 从已同步节点复制
cp -r /other-node/datadir/mdbx.dat /new-node/datadir/
```
不需要代码，文档说明即可。

**方式 B：从 Geth state dump 导入**
```go
// 类似 InitEthGenesisState 但从 state dump 文件
func ImportGethStateDump(tx kv.RwTx, dumpPath string) error
```
Geth 的 `geth dump` 输出 JSON，格式和 genesis alloc 相同。

**方式 C：snap/1 协议（完整但复杂）**
暂不实现，Phase 3 和 devp2p 一起做。

**预估：方式 B ~150 行**

### 1.4 快速追赶编排

```
新建：internal/ethel/catch_up.go
```

整合 BT sync + 执行 + CL 驱动的完整启动流程：

```go
func CatchUp(ctx context.Context, cfg CatchUpConfig) error {
    // 1. 检查本地进度
    localHead := ReadProgress(tx)
    
    // 2. BT 下载缺失的 freezer 段
    btSync.SyncToLatest(ctx)
    
    // 3. 从 localHead 执行到 freezer 顶部
    executor.Run(ctx) // 断点续传
    
    // 4. 如果有 CL 连接，切换到 Engine API 模式
    // （自动由 ForkchoiceUpdated 驱动后续块）
}
```

**预估：~200 行**

---

## 文件清单

### Phase 0 新建/修改

| 文件 | 动作 | 行数 |
|------|------|------|
| internal/ethel/process.go | 新建 | ~200 |
| internal/api/engine_state_adapter.go | 改写 | ~500 |
| internal/ethel/reorg.go | 新建 | ~200 |
| internal/ethel/indices.go | 新建 | ~150 |
| internal/node/node.go | 修改 | ~100 |
| **Phase 0 小计** | | **~1,150** |

### Phase 1 新建/修改

| 文件 | 动作 | 行数 |
|------|------|------|
| internal/ethel/segment.go | 新建 | ~300 |
| internal/ethel/bt_sync.go | 新建 | ~400 |
| internal/ethel/state_import.go | 新建 | ~150 |
| internal/ethel/catch_up.go | 新建 | ~200 |
| **Phase 1 小计** | | **~1,050** |

**总计：~2,200 行新代码**

---

## 实施顺序

```
Week 1: 0.1 共享 processBlock + 0.2 Engine API adapter
Week 2: 0.3 Reorg + 0.4 集成 node.go
Week 3: 0.5 索引 + 测试（Lighthouse 连接）
Week 4: 1.1 段文件生成 + 1.2 BT 下载
Week 5: 1.3 State 导入 + 1.4 追赶编排 + 集成测试
```

## 验证里程碑

| 里程碑 | 验证方法 |
|--------|---------|
| Engine API 工作 | `n42 --chain eth-mainnet` + Lighthouse → 跟链 |
| Reorg 正确 | 手动制造 1-block reorg → state 正确回退 |
| RPC 工作 | `eth_getBlockByNumber` / `eth_getLogs` 返回正确 |
| BT 同步 | 新节点下载 freezer 段 → 执行追赶 → 连 CL 跟链 |
| 端到端 | 从空节点到跟链，全自动 |
