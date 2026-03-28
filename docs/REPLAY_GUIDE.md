# 全量状态重放指南 (Chain Replay with JMT + LtHash)

## 目标

从旧链数据库 (`d:\mainnet\chaindata\mdbx.dat`) 重放全量状态到新数据库 (`d:\mainnetnew\chaindata\mdbx.dat`)，同时：

1. **启用 JMT + LtHash** 从创世块开始，构建完整的 Merkle 状态承诺
2. **保留全量历史** — GC 禁用，所有历史 JMT 节点永久保留（支持任意高度 proof）
3. **新 Header 格式** — 使用最新 Header 结构，包含 LtHashRoot 字段
4. **分叉提前激活** — 所有分叉从创世块启用
5. **硬分叉数据写入创世块** — 7601200 高度的余额注入改为创世分配
6. **系统合约写入创世块** — Prague/Pectra 系统合约预部署到创世状态
7. **时间线连续** — 出块间隔 >15 秒时自动补空块，保证 8 秒出块节奏
8. **JMTVersionRoots 索引** — 每个区块记录 height→root 映射，支持任意高度 proof

## 架构

```
旧链 DB (d:\mainnet)                     新链 DB (d:\mainnetnew)
┌─────────────────────┐                 ┌───────────────────────────┐
│ Headers (旧格式)     │                 │ Headers (新格式+LtHashRoot)│
│ Bodies (旧交易)      │  ──重放──→      │ Bodies (过滤后交易)         │
│ PlainState (旧状态)  │                 │ PlainState (新状态)        │
│                     │                 │ JMTNode (Blake3 节点)      │
│                     │                 │ JMTRoot (最新 root)        │
│                     │                 │ JMTVersionRoots (历史索引)  │
│                     │                 │ LtHashDigest (2048B 摘要)  │
└─────────────────────┘                 └───────────────────────────┘
```

## 新创世配置

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
- 所有 timestamp-based 分叉从 0 激活（Pectra、Osaka、Fusaka）
- `ltHashTime: 0` — LtHash 从创世启用
- 移除 deposit contract 地址（新链不需要旧的质押合约）

### 创世块额外分配

以下数据从 hardfork_alloc.json 移入创世块分配：

| 地址 | 金额 | 原高度 |
|------|------|--------|
| `0x4f88c44eeb74fecf4ad37b95a6d81bcae0f3f091` | `0x9B18AB5DF7180B6B8000000` | 7601200 |

### 系统合约预部署（创世块）

| 合约 | 地址 | 用途 |
|------|------|------|
| EIP-2935 History Storage | `0x0000F90827F1C53a10CB7A02335B175320002935` | 父块哈希存储 |
| EIP-7002 Withdrawals | `0x00000961EF480EB55E80D19AD83579A64C007002` | 验证者提款请求 |
| EIP-7251 Consolidation | `0x0000BBDDC7CE488642FB579F8B00F3A590007251` | 验证者合并 |
| EIP-4788 Beacon Roots | `0x000F3df6D732807Ef1319fB7B8bB8522d0Beac02` | Beacon root 存储 |

## 重放规则

### 交易过滤

| 规则 | 处理 |
|------|------|
| 目标地址为旧 deposit contract | 跳过 |
| 空合约创建 (to=nil, data=empty) | 跳过 |
| 签名恢复失败 | 跳过 |
| 正常转账 | 执行余额变更 |
| 合约部署 | 创建账户 + 存储代码 |
| 合约调用 | 仅执行 value transfer（lossy） |

### 时间线补齐

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

### JMT + LtHash 更新

每个块（包括补空块）：
1. 收集 dirty accounts + dirty storage
2. 更新 JMT：`BatchUpdate(entries)` → 计算新 root
3. 更新 LtHash：增量 `Update(old, new)` → 计算新 digest
4. 写入 Header：`Root = JMT root`, `LtHashRoot = LtHash.Sum()`
5. 持久化：JMTNode、JMTRoot、JMTVersionRoots、LtHashDigest

### GC 策略

**禁用 GC** — `tree.EnableGC()` 不调用，所有历史 JMT 节点保留在 JMTNode 表中。

这意味着：
- 可以用 `ReadJMTVersionRootAt(height)` + `NewFromRootReadOnly(store, root)` 查询任意历史状态
- 存储成本：约 40 bytes/节点 × ~100 节点/块 × 1200 万块 ≈ ~50GB JMT 节点
- 总新数据库预估：原始数据 + JMT 节点 + 版本索引 ≈ 原始大小 × 1.5-2x

## 命令行

```bash
n42 replay-v2 \
    --source d:\mainnet \
    --target d:\mainnetnew \
    --chain mainnet_v2 \
    --jmt \
    --lthash \
    --fill-gaps \
    --gap-period 8 \
    --gap-tolerance 15 \
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
| `--fill-gaps` | `true` | 补空块填充时间间隔 |
| `--gap-period` | `8` | 补空块间隔（秒） |
| `--gap-tolerance` | `15` | 触发补空块的阈值（秒） |
| `--batch` | `10000` | 每批提交的块数 |
| `--from` | `0` | 起始块高度 |
| `--to` | `0` (自动) | 结束块高度 |
| `--output` | `replay_stats.json` | 统计输出文件 |

## 进度与恢复

- 每 `--batch` 个块提交一次 MDBX 事务
- 进度保存在 JMTVersion（已处理的最高块高度）
- 中断后重启自动从上次提交点继续
- 进度日志每 10 秒输出一次

## 预估时间

| 阶段 | 块数 | 预估速度 | 预估时间 |
|------|------|---------|---------|
| Genesis + 补空块 | ~1200 万 | ~5000 blk/s (空块) | ~40 分钟 |
| 有交易的块 | ~500 万 | ~1000 blk/s (state write + JMT) | ~80 分钟 |
| JMT 节点写入 | ~12 亿节点 | ~100K writes/s | 含在上面 |
| **总计** | | | **~2-3 小时** |

## 验证

重放完成后验证：

```bash
# 1. 检查最终高度
n42 replay-v2 --source d:\mainnetnew --verify

# 2. 检查 JMT proof 在任意高度
n42 jmt-verify --data-dir d:\mainnetnew --height 3304451 --address 0x...

# 3. 比较账户余额
n42 replay-v2 --source d:\mainnet --target d:\mainnetnew --compare-balances
```

## 调整后重新重放

如需调整参数重新重放：

```bash
# 1. 删除目标目录
rmdir /s /q d:\mainnetnew

# 2. 修改参数后重新运行
n42 replay-v2 --source d:\mainnet --target d:\mainnetnew ...
```

## 关键文件

| 文件 | 用途 |
|------|------|
| `cmd/n42/replaycmd.go` | 原始 replay 命令（lossy，无 JMT） |
| `internal/replay/replay.go` | 原始 replay 引擎 |
| `cmd/n42/migratecmd.go` | JMT 迁移工具（从 flat state 构建 JMT） |
| `params/chainspecs/mainnet.json` | 当前主网配置 |
| `hardfork_alloc.json` | 当前硬分叉余额注入 |
| `internal/genesis_block.go` | 创世块构造 |
| `modules/state/commitment/` | JMT + LtHash 承诺层 |
| `lib/jmt/` | Jellyfish Merkle Tree 核心 |
| `lib/lthash/` | 格密码状态摘要 |
