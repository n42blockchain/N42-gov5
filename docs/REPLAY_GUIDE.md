# 全量状态重放指南 (Chain Replay with JMT + LtHash)

## 目标

从旧链数据库 (`d:\mainnet\chaindata\mdbx.dat`) 重放全量状态到新数据库 (`d:\mainnetnew\chaindata\mdbx.dat`)，同时：

1. **启用 JMT + LtHash** 从创世块开始，构建完整的 Merkle 状态承诺
2. **保留全量 JMT 历史** — GC 禁用，所有节点永久保留（支持任意高度 proof）
3. **新 Header 格式** — 使用最新 Header 结构，包含 LtHashRoot 字段
4. **所有分叉从创世启用** — 包括 Glamsterdam (EIP-7904) 预分叉
5. **硬分叉数据写入创世块** — 7601200 高度的余额注入改为创世分配
6. **系统合约写入创世块** — Prague/Pectra 系统合约预部署
7. **时间线连续** — 出块间隔 >15 秒时自动补空块
8. **JMTVersionRoots 索引** — 每个区块记录 height→root，支持任意高度 proof
9. **生成快照 + EraE 归档** — 重放完成后导出 snapshot 和 BitTorrent 可分发的 EraE 段文件
10. **支持多种新节点同步方式** — OtterSync(BT)、Checkpoint、SnapSync、P2P InitialSync

---

## 一、架构

```
旧链 DB (d:\mainnet)                        新链 DB (d:\mainnetnew)
┌─────────────────────┐                    ┌──────────────────────────────┐
│ Headers (旧格式)     │                    │ Headers (新格式+LtHashRoot)   │
│ Bodies (旧交易)      │  ──重放──→         │ Bodies (过滤后交易)            │
│ PlainState (旧状态)  │                    │ PlainState (新状态)           │
│                     │                    │ JMTNode (Blake3 节点, 全量)    │
│                     │                    │ JMTRoot (最新 root)           │
│                     │                    │ JMTVersionRoots (历史索引)     │
│                     │                    │ LtHashDigest (2048B 摘要)     │
│                     │                    │ SnapshotIndex (快照元数据)     │
└─────────────────────┘                    └──────────────┬───────────────┘
                                                         │
                                              ┌──────────▼──────────┐
                                              │  Post-Replay 导出    │
                                              ├─────────────────────┤
                                              │ era/ (EraE 段文件)   │
                                              │ manifest.json       │
                                              │ checkpoint.json     │
                                              └─────────────────────┘
```

---

## 二、新创世配置

### 链参数 (`chainspec/mainnet_v2.json`)

```json
{
  "chainId": 94,
  "homesteadBlock": 0,
  "eip150Block": 0,
  "eip155Block": 0,
  "eip158Block": 0,
  "byzantiumBlock": 0,
  "constantinopleBlock": 0,
  "petersburgBlock": 0,
  "istanbulBlock": 0,
  "berlinBlock": 0,
  "londonBlock": 0,
  "arrowGlacierBlock": 0,
  "beijingBlock": 0,
  "shanghaiBlock": 0,
  "cancunBlock": 0,
  "pectraTime": 0,
  "osakaTime": 0,
  "fusakaTime": 0,
  "glamsterdamTime": 0,
  "ltHashTime": 0,
  "consensus": "apos",
  "apos": {
    "period": 8,
    "epoch": 3000,
    "rewardEpoch": 10800,
    "rewardLimit": 500000000000000000
  }
}
```

**关键变更**:
- 所有 block-based 分叉从 0 激活（Beijing、Shanghai、Cancun）
- 所有 timestamp-based 分叉从 0 激活（Pectra、Osaka、Fusaka、**Glamsterdam**）
- `ltHashTime: 0` — LtHash 从创世启用
- `glamsterdamTime: 0` — EIP-7904 gas 降价从创世启用（转账 21000→4500 gas）
- 移除 deposit contract 地址（新链不需要旧质押合约）

### Glamsterdam (EIP-7904) 生效参数

| 参数 | 旧值 | Glamsterdam |
|------|------|-------------|
| TxGas (基础转账) | 21000 | **4500** |
| TxGasContractCreation | 53000 | **12500** |
| TxDataZeroGas | 4 | **1** |
| TxDataNonZeroGas | 16 | **4** |
| AccessListAddressGas | 1200 | **600** |
| AccessListStorageKeyGas | 1900 | **475** |
| CallNewAccountGas | 25000 | **6250** |

### 创世块额外分配

以下数据从 hardfork_alloc.json 移入创世块分配：

| 地址 | 金额 | 原高度 |
|------|------|--------|
| `0x4f88c44eeb74fecf4ad37b95a6d81bcae0f3f091` | `0x9B18AB5DF7180B6B8000000` | 7601200 |

### 系统合约预部署（创世块）

| 合约 | 地址 | 用途 |
|------|------|------|
| EIP-2935 History Storage | `0x0000F90827F1C53a10CB7A02335B175320002935` | 父块哈希环形存储 |
| EIP-7002 Withdrawals | `0x00000961EF480EB55E80D19AD83579A64C007002` | 验证者提款请求 |
| EIP-7251 Consolidation | `0x0000BBDDC7CE488642FB579F8B00F3A590007251` | 验证者合并 |
| EIP-4788 Beacon Roots | `0x000F3df6D732807Ef1319fB7B8bB8522d0Beac02` | Beacon root 存储 |

---

## 三、重放规则

### 3.1 交易过滤

| 规则 | 处理 |
|------|------|
| 目标地址为旧 deposit contract | 跳过 |
| 空合约创建 (to=nil, data=empty) | 跳过 |
| 签名恢复失败 | 跳过 |
| 正常转账 | 执行余额变更 |
| 合约部署 | 创建账户 + 存储代码 |
| 合约调用 | 仅执行 value transfer（lossy） |

### 3.2 时间线补齐

```
原始链:  block 100 (t=1000) → block 101 (t=1032)  // 间隔 32 秒
新链:    block 100 (t=1000)
         block 101 (t=1008)  ← 补空块
         block 102 (t=1016)  ← 补空块
         block 103 (t=1024)  ← 补空块
         block 104 (t=1032)  ← 原 block 101 的交易

规则: 如果两个相邻原始块的时间差 > period(8s) + tolerance(7s) = 15s
      则在中间补充空块，每 period(8s) 一个
```

### 3.3 JMT + LtHash 状态承诺

每个块（包括补空块）执行：

```
1. 收集 dirty accounts + dirty storage (from IntraBlockState)
2. JMT BatchUpdate(entries) → 计算新 root (Blake3)
3. LtHash Update(old, new) → 增量更新 2048B digest
4. 写入 Header:
   - Root = JMT root
   - LtHashRoot = Blake3(digest[:])
5. 持久化 (同一 MDBX 事务):
   - JMTNode 表 ← 新/修改的树节点
   - JMTRoot 表 ← 最新 root + version
   - JMTVersionRoots 表 ← height → root 映射
   - LtHashDigest 表 ← 2048B digest
```

### 3.4 GC 全量历史策略

**禁用 GC** — `tree.EnableGC()` 不调用，所有历史 JMT 节点永久保留。

| 特性 | 说明 |
|------|------|
| 历史 proof | 任意高度可用 (`ReadJMTVersionRootAt` + `NewFromRootReadOnly`) |
| 存储成本 | ~40 bytes/节点 × ~100 节点/块 × 1200 万块 ≈ ~50GB JMT 节点 |
| 裁剪选项 | 后续可通过 `migrate-jmt --gc-before HEIGHT` 清理旧节点 |

---

## 四、Post-Replay 导出

### 4.1 Snapshot 快照

重放完成后自动创建快照，记录到 `SnapshotIndex` 表：

```
最终高度快照:
- BlockNumber = 重放最终高度
- AccountCount = 总账户数
- StorageCount = 总存储槽数
- CodeCount = 总合约数
```

快照用于新节点 SnapSync：新节点连接到已完成重放的种子节点，通过 `GetSnapshotInfo` / `GetSnapshotAccountRange` / `GetSnapshotStorageRange` RPC 下载状态。

### 4.2 EraE 归档（OtterSync / BitTorrent）

重放完成后导出 EraE 段文件：

```bash
n42 replay-v2 ... --export-era --era-segment-size 8192
```

产出：
```
d:\mainnetnew\era\
├── era-00000000-00008192.era    (block 0-8191)
├── era-00008192-00016384.era    (block 8192-16383)
├── ...
├── era-12000000-12008192.era    (最后一段)
└── manifest.json                (段目录 + SHA256 校验)
```

**分发方式**：
1. **BitTorrent** — 每个 .era 文件作为独立种子分发
2. **HTTP WebSeed** — 上传到 CDN，新节点通过 `ManifestURLs` 获取
3. **P2P OtterSync** — 节点间直接传输 EraE 段文件

### 4.3 Checkpoint 导出

记录重放完成的可信检查点：

```json
{
  "checkpoints": [
    {"number": 12000000, "hash": "0xabc...def"}
  ]
}
```

新节点可配置 `--checkpoint.block` + `--checkpoint.hash` 跳过 genesis 直接从该高度开始 snap sync。

---

## 五、新节点加入方式

重放完成后，新节点有 4 种方式加入：

### 方式 1: OtterSync (BitTorrent) — 最快冷启动

```bash
n42 --data.dir ./n42data --chain mainnet_v2 \
    --torrent-sync.enabled \
    --torrent-sync.manifest-urls "https://cdn.n42.network/manifest.json"
```

**流程**: 下载 EraE 段 → 导入块数据 → P2P 同步剩余 → 就绪
**速度**: ~2-3 小时（取决于带宽）
**CPU**: 极低（无 EVM 执行，仅解码+写入）

### 方式 2: Checkpoint + SnapSync — 最快状态可用

```bash
n42 --data.dir ./n42data --chain mainnet_v2 \
    --checkpoint.sync \
    --checkpoint.block 12000000 \
    --checkpoint.hash "0xabc...def"
```

**流程**: 下载 checkpoint 块 → 下载状态快照 → 下载剩余块 → 就绪
**速度**: ~1-2 小时
**CPU**: 中等（状态下载 + 少量块执行）

### 方式 3: P2P InitialSync — 完全去信任

```bash
n42 --data.dir ./n42data --chain mainnet_v2
```

**流程**: P2P 发现 → 批量下载块 → EVM 执行每个块 → 就绪
**速度**: ~1-2 天
**CPU**: 高（全量 EVM 执行）

### 方式 4: 离线导入 + P2P 追赶

```bash
# 1. 复制 EraE 文件到 era/ 目录
xcopy \\share\era\* d:\n42data\era\ /s

# 2. 启动节点，自动导入 + P2P 追赶
n42 --data.dir d:\n42data --chain mainnet_v2 \
    --torrent-sync.enabled
```

---

## 六、命令行

```bash
n42 replay-v2 \
    --source d:\mainnet \
    --target d:\mainnetnew \
    --chain mainnet_v2 \
    --jmt \
    --lthash \
    --no-gc \
    --fill-gaps \
    --gap-period 8 \
    --gap-tolerance 15 \
    --snapshot-at-end \
    --export-era \
    --era-segment-size 8192 \
    --batch 10000 \
    --output replay_stats.json
```

### 参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--source` | 必填 | 旧链数据目录 |
| `--target` | 必填 | 新链数据目录 |
| `--chain` | `mainnet_v2` | 新链配置名称 |
| `--jmt` | `true` | 启用 JMT 状态承诺 |
| `--lthash` | `true` | 启用 LtHash 状态摘要 |
| `--no-gc` | `true` | 禁用 JMT GC，保留全量历史 |
| `--fill-gaps` | `true` | 补空块填充时间间隔 |
| `--gap-period` | `8` | 补空块间隔（秒） |
| `--gap-tolerance` | `15` | 触发补空块的阈值（秒） |
| `--snapshot-at-end` | `true` | 重放完成后创建状态快照 |
| `--export-era` | `false` | 重放完成后导出 EraE 归档 |
| `--era-segment-size` | `8192` | 每个 EraE 段的块数 |
| `--batch` | `10000` | 每批提交的块数 |
| `--from` | `0` | 起始块高度 |
| `--to` | `0` (自动) | 结束块高度 |
| `--output` | `replay_stats.json` | 统计输出文件 |

---

## 七、实施计划

### Phase 0: 前置准备 (Day 1)

| # | 任务 | 说明 |
|---|------|------|
| 0.1 | 完成首遍数据同步 | 当前正在进行，同步旧链到最新高度 |
| 0.2 | 创建 `mainnet_v2.json` chainspec | 所有分叉从 0，Glamsterdam 启用 |
| 0.3 | 创建 `mainnet_v2_genesis.json` | 合并原始创世 + 硬分叉分配 + 系统合约 |
| 0.4 | 验证 `isWrongStateRootBlockNumber` | 确认 16 个旧链 bug 块在新链中不需要特殊处理 |

### Phase 1: replay-v2 引擎开发 (Day 2-3)

| # | 任务 | 说明 |
|---|------|------|
| 1.1 | `cmd/n42/replay_v2_cmd.go` | 新命令入口，解析 CLI 参数 |
| 1.2 | `internal/replay/engine_v2.go` | 增强引擎：JMT+LtHash实时更新 |
| 1.3 | 时间线补空块逻辑 | `fillGaps(prevTime, currTime, period, tolerance)` |
| 1.4 | 新 Header 构建 | 包含 LtHashRoot、JMT Root、正确 timestamp |
| 1.5 | 创世块初始化 | 系统合约预部署 + 硬分叉分配 + JMT 初始化 |
| 1.6 | 断点续传 | 读取 JMTVersion 确定上次进度，从下一块继续 |

### Phase 2: 执行重放 (Day 4)

| # | 任务 | 说明 |
|---|------|------|
| 2.1 | 执行 `n42 replay-v2` | 预估 2-3 小时 |
| 2.2 | 监控进度日志 | 每 10 秒输出 blocks/s + 当前高度 |
| 2.3 | 验证最终状态 | 比较关键账户余额 + JMT proof 验证 |

### Phase 3: Post-Replay 导出 (Day 4-5)

| # | 任务 | 说明 |
|---|------|------|
| 3.1 | 创建 Snapshot | 记录最终高度快照到 SnapshotIndex |
| 3.2 | 导出 EraE 段文件 | 使用 `torrentsync.Exporter` 导出全链 |
| 3.3 | 生成 manifest.json | 记录所有段文件的块范围 + SHA256 |
| 3.4 | 记录 Checkpoint | 导出最终高度 + hash 作为可信检查点 |
| 3.5 | 上传到 CDN/BT | 分发 EraE 文件供新节点下载 |

### Phase 4: 新节点验证 (Day 5-6)

| # | 任务 | 说明 |
|---|------|------|
| 4.1 | OtterSync 冷启动测试 | 新空节点通过 BT 下载 EraE + P2P 追赶 |
| 4.2 | SnapSync 测试 | 新节点通过快照同步状态 |
| 4.3 | Checkpoint 测试 | 新节点从检查点快进 + snap sync |
| 4.4 | 历史 Proof 验证 | `eth_getProof` 任意历史高度 |
| 4.5 | 出块测试 | 启动矿工，验证新块使用 Glamsterdam gas |

### Phase 5: 生产切换 (Day 7)

| # | 任务 | 说明 |
|---|------|------|
| 5.1 | 种子节点部署 | 使用重放后的数据库启动种子节点 |
| 5.2 | 更新 bootnodes.go | 添加新种子节点 ENR |
| 5.3 | 更新文档 | README + DEVLOG + GAP 反映新链状态 |
| 5.4 | 废弃旧链同步 | 移除 `mainnet_compat` 兼容配置 |

---

## 八、关键代码文件

| 文件 | 用途 |
|------|------|
| `cmd/n42/replaycmd.go` | 原始 replay 命令（参考） |
| `internal/replay/replay.go` | 原始 replay 引擎（增强基础） |
| `cmd/n42/migratecmd.go` | JMT 迁移工具（参考） |
| `internal/sync/torrentsync/exporter.go` | EraE 导出（Post-Replay 使用） |
| `internal/sync/torrentsync/manifest.go` | Manifest 生成 |
| `internal/snapshot/manager.go` | 快照管理器 |
| `modules/state/commitment/` | JMT + LtHash 承诺层 |
| `lib/jmt/` | Jellyfish Merkle Tree 核心 |
| `lib/lthash/` | 格密码状态摘要 |
| `params/chainspecs/mainnet.json` | 当前主网配置（参考） |
| `hardfork_alloc.json` | 硬分叉余额注入（移入创世） |
| `internal/genesis_block.go` | 创世块构造 |
| `params/protocol_params.go` | Glamsterdam gas 常量 |

---

## 九、预估资源

| 资源 | 预估值 |
|------|--------|
| 源数据库大小 | ~80-120 GB |
| 新数据库大小 | ~150-200 GB（含 JMT 全量历史） |
| JMT 节点存储 | ~50 GB |
| JMTVersionRoots 索引 | ~400 MB（1200 万 × 40 bytes） |
| EraE 归档大小 | ~60-80 GB |
| 重放时间 | ~2-3 小时 |
| 内存需求 | ~8-16 GB |
| 磁盘 IOPS | ~50K writes/s（建议 NVMe SSD） |
