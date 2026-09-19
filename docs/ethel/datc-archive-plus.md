# DATC / archive-plus：以太坊主网任意高度的 EIP-1186 证明

> 这是 DATC 的总文档：目标、设计、实现、踩过的坑、交付物、性能、维护、以及它怎样作为 **archive-plus 层**接进 eth-el。
> 状态截至 2026-09-19。细节文档：`datc-ladder-redesign-design-2026-09-18.md`（本轮重新设计的全部实测）、
> `datc-weekly-update.md`（周更新操作）、`datc-audit-format-v2-2026-09-01.md`（v2 盘上格式）、`datc-pipeline.md`（构建管线）。
> 运行现场的流水账在 `/data/blockchain/datc-out/HANDOFF.md`（不进 git）。

## 1. 目标

| | 目标 | 现状 |
|---|---|---|
| 能力 | 对主网 **任意历史高度** 回答 `eth_getProof`（账户 + 存储槽，存在或不存在），证明对该块区块头的 stateRoot 可验 | 0 – 25,943,310 全覆盖 |
| 延迟 | 绝大多数 < 200 ms，最差 < 1 s；**包括**巨型合约、刚创建的合约、早期高度 | 热缓存 p50 30 ms / p99 77 ms / 最慢 213 ms；≤ 200 ms 99.95%，≤ 1 s 100% |
| 体积 | 远小于"全量 archive 节点"（Erigon 3 archive ≈ 4 TB 且不含历史证明；reth/geth archive 12–20 TB） | 查询所需 ≈ 800 GB（另有 433 GB 是续跑用的当前态，不参与查询） |
| 维护 | 每周追加新块，不重建 | 续跑 + 三步离线推导，约 1–1.5 小时 |
| 正确性 | 每一个证明都能被不信任我们的客户端独立验证 | bench/节点侧都对区块头根校验；顺序严格按 EIP-1186 |

不做的事：不重放 EVM（输入是执行节点产出的逐块变更集）；不存每个高度的整棵 trie。

## 2. 设计

### 2.1 一个证明需要什么

路径上每个节点在高度 N 的 16 个孩子哈希。路径外的孩子只有两种来源：**存**（某条记录里有它在 N 的值）或**折**（从叶历史把该子树重算出来）。
DATC = 叶历史（一切的真相）+ 少量节点记录（让"折"的范围小到几毫秒）。

### 2.2 成本定律（重新设计的出发点）

- 一条 epoch 记录（每 E 块记一次节点）只在窗口内没变过的孩子上有用。**树稠密处**窗口内 16 个孩子全变过，读取端只能对每个孩子递归，
  每多一层 epoch 层读取量 ×16（v3 账户阶梯 p50 40 s 的原因）；**树稀疏处**每次写入本来就落在不同 epoch，epoch 记录与逐块记录一样多。
  → epoch 层要么没用、要么不省：**全部去掉**。
- 逐块精确层（记录即 N 处状态）存的是"(块, 折叠单元) 变更对"的哈希。单元小到折得动时，这个数 ≈ 写入次数：
  **任意高度精确 ≈ 每次状态写入存 1 条 32 B 哈希**，与深度无关。深度只决定折叠单元大小和精确层之上要读多少条记录（16^k）。
- 折叠成本 ∝ 前缀下**历史上出现过的全部键**（未来才出生的键也要逐个跳过），不是 N 时刻的键数。→ 早期高度、合约幼年期需要单独处理（§2.4）。

### 2.3 纯精确层阶梯

| | 记录 | 折叠 | 每个证明 |
|---|---|---|---|
| 账户树 | 根（第 0 层）逐块 + **第 3 层逐块**（`na` 段）；第 1、2 层是 v2 遗留的 epoch 记录，仍在用、可去 | 第 4 层（单元 ≈ 7k 历史键） | ≈ 273 条记录 + 1 次折叠 |
| 存储树（合约在精确阶梯上，深度 D ≤ 3） | **只有第 D−1 层逐块**（`ns` 段）；之上的层由 16 个孩子合成 | 第 D 层 | D=1/2/3 → 1 / 17 / 273 条记录 + 1 次折叠 |
| 存储树（其余 2800 万个合约） | 无 | 整棵树（4 个 goroutine 分 16 个首 nibble 区间） | 1 次折叠 |

合约是否上阶梯由**整树折叠估价**决定：`键数 × 1.2 µs + 热键数 × 15 µs > T`（T = 150 ms；热键 = 版本数 > 4，折叠对它走 seek）。
深度取单元估价 ≤ 35 ms 的最浅一层。策略是子命令 `derive-plan`，可复现。主网：2,848 个合约在阶梯上（深度 1/2/3 = 2251/565/29 + P1 多选的 3 个）。

### 2.4 成长阶段：版本化深度 + 出生分区

合约的深度跟着它**历史出生键数**走：越过 `U·16^(g−1)`（U = 终单元键数）的那个 1024 块 bin 起，深度变为 g（`ns.ladders` 里一合约多行 rung）。
每个早期阶段在自己的层（g−1）有一份逐块记录，写到下一个 rung 为止。

最后一个 rung 之前出生的键，连同它们到该 rung 为止的历史，**复制**进出生分区 `s0..s2`（账户树：`a0..a2`，边界 1.2M / 2.4M / 4.4M，记在 `a.stages`）。
高度在阶段 g 的折叠只归并分区 0..g，永远碰不到之后出生的键；过了最后一个 rung 照旧读主历史。主 `a`/`s` 不动；分区共 7.5 GB。
效果：USDT 出生后 4 万块处的槽证明 299 s → 46 ms；块 6 万的账户证明 17.6 s → 40 ms。

### 2.5 存储记录离线推导（不重放链）

单元的键在 `s` 里是连续区间：取出某个 D−1 层节点之下的全部行 → 按块排序 → 16 棵内存增量 MPT（`unittrie.go`）逐块重放 →
每个变更块输出该节点的 DIFF/FULL 记录。节点之间完全独立，全链 54 亿行约 25 分钟。
**每个合约在采样块上由推导出的哈希合成根，与 `sr`（构建时对着区块头验过的存储根历史）比对，不一致就不收尾。**

这意味着换阶梯策略不再需要一周的全链重建——这是 v3 方案被放弃、v2 归档被保留为底座的原因。

## 3. 实现

代码在 `internal/datc`（`cmd/n42-datc` 只是入口）。

| 文件 | 作用 |
|---|---|
| `main.go` `emit.go` `pipeline.go` `lastfull.go` `schedule.go` `record.go` | **构建器**：重放 acctcs/storcs 变更集，维护 erigon 布局的 trie，逐块对 headerc 校验根（金标），写叶历史 / `sr` / 账户节点记录 |
| `leafseg.go` | 段存储：spill → 外排序收尾 → zstd 帧段 + 页脚索引；无指针、按需打开、进程内共享、引用计数的帧索引；向前 gallop 的 Seek |
| `verify.go` `proof.go` | **读取端**：`branchSlotsAt`（记录路径）/ `asOfLeaves`（折叠的键遍历）/ `proofPath`（EIP-1186 节点序列）/ `walkProof`（严格校验） |
| `exactladder.go` | 精确阶梯读取端 + sidecar `leafseg/ns.ladders` |
| `birthparts.go` | 出生分区读取、整树并行折叠、`derive-acc-parts` |
| `unittrie.go` | 增量 MPT（4.6 µs / 3 次分配每次更新；对 `mptNodeRLP` 和 GenStructStep 折叠差分测试） |
| `derivens.go` `derivestages.go` `deriveplan.go` | 离线推导：`derive-ns`（全量 / `--early-only` / `--from E` 周更新）、成长阶段、`derive-plan` |
| `verifyns.go` `bench.go` `benchplan.go` | 验收：`verify-ns`（经读取端合成存储根 vs `sr`）、`bench`（每个证明对区块头根校验；`--queries` 分层）、`bench-plan` |
| `reframe.go` | 按表重切段帧（逐段读回校验 行数/字节/CRC64；`--view` 不动原档） |
| `archive.go` | **库读取端**：`OpenArchive` / `Prove`，给长驻进程（eth-el）用 |
| `merge.go` `prepstate.go` | 两段构建合并、从状态拷贝起一段构建 |

盘上布局（归档目录）：

```
mdbx.dat                当前态表（续跑用）+ DatcMeta（head/sched/format/...）；v2 的 DatcStorNode 读取端已不用
leafseg/a.*  s.*        账户 / 存储叶历史  key = hashedKey | block(4)，64 KiB 帧
leafseg/sr.*            存储根历史        key = addrHash | block(4)
leafseg/na.*            账户节点记录      key = pathLen | path | epoch(4)，16 KiB 帧
leafseg/ns.* ns.ladders 精确存储记录      key = pathLen | addrHash | path | block(4)；rung 清单
leafseg/s0..s2 a0..a2 a.stages  出生分区
leafseg/ca.* cs.*       变更索引：ca 账户第 1/2 层 epoch 记录还在用；cs 已不用（留作保险，19.6 GB）
```

## 4. 踩过的坑

按"后果有多贵"排序。前 5 条都与数据无关，是读取端或验收的问题——教训是**先量读取端在干什么，再决定要不要加数据**。

1. **证明节点顺序错了，而自家校验器查不出来。** 折叠部分的节点由 `mptNodeRLP` 后序产出（最深的在前），`walkProof` 把节点放进哈希表按引用找，
   不看顺序，于是 bench 的"全部验证通过"只证明了节点**集合**对。EIP-1186 是根在前的路径，标准客户端会拒。接 eth-el 时用独立校验器
   `mptproof.VerifyStandardProof` 才发现。现在 `proofPath` 根在前、内嵌（< 32 B）节点不单列，`walkProof` 严格按序、不许多余节点
   （`TestProofOrderIsChecked`）。**教训：校验器必须和客户端一样严格，最好就是另一份独立实现。**
2. **v3 账户阶梯把逐块层换成 epoch 层** → 16^4 = 65,536 次折叠，p50 40 s。在跑了 18 小时的全链重建的 20M 闸口才发现。
   教训：任何阶梯改动先过 2M 原型 + 分层闸口；折叠层正上方必须是精确层。
3. **奇数长度路径的折叠逐键 seek 跳过 15 个兄弟单元**（`asOfLeaves` 的 odd-nibble 分支）：深度 1、3 的单元折叠实际花 16 倍。
   这是"巨型合约慢""早期高度慢"里最大的一块；修掉后分层 bench 5 分钟 → 22 秒。
4. **折叠扫的是最终键集**：块 6 万的账户证明 17.6 s，bench 从没采到过（最低 2.9M）。教训：验收样本必须分层（合约档位 × 生命阶段 × 高度时代），均匀采样采不到最坏情形。
5. **账户证明 80% 的时间在解压**：273 条记录各落在一个 256 KiB 帧里，每帧还新建一个 zstd 解码器。16 KiB 帧 + 共用解码器：165 → 30 ms，零新增数据。
   随后的连带坑：帧索引涨到 4700 万条，`[]struct{...; key []byte}` 每个读取器一份，GC 扫描吃掉一半 CPU → 索引必须无指针且进程内共享。
6. `proofPath` 在折叠层扫两遍叶历史，又用第二个构建器把子树再哈希一遍（比扫描还贵）→ 扫一遍；交叉校验在对区块头根校验的场合关掉。
7. 构建期：`onDenseNode` 的 map 只进不出 → 堆 25 GB、GC 81% CPU；`--mem.gb` 贴着活跃堆 → GC 饥饿；对正在写的 `mdbx.dat` 做 reflink 克隆 →
   写放大 25 倍；USDT 级的桶收尾必须外排序；**不要在自动重启下跑收尾**（OOM 后重跑会把同一批行重复并入）；旧 `merge` 把一个桶写成单个巨帧，
   收尾时超 512 MiB 被当坏帧丢弃 → 静默丢整桶。
8. MDBX DupSort：ALLDUPS put 在单值键上是追加；`Count` 是全表的；WRITEMAP 避免脏页表排序（构建 11 → 48 块/秒）。
9. 格式：存储键不带 incarnation（v2 起 32 B 域）；存储节点记录的路径键曾带一个清零的 nibble（73779b51 修）。
10. 吞吐只按 ≥ 1 小时的块差算；日志里的 blk/s 是启动以来的均值。

## 5. 交付物

- **归档**：`/data/blockchain/datc-out/datc-25m-v2-hi`（Linux 192.168.0.166），head 25,943,311。1.2 TB = 段 795 GB + `mdbx.dat` 433 GB。
- **二进制**：`/data/blockchain/datc-out/datc.bin` → `n42-datc-25m-hi25.bin`。
- **代码**：`internal/datc`、`cmd/n42-datc`、`internal/ethel/publicrpc/datc.go`、`internal/api` 的 `ProofSource`。
- **验收记录**：`/data/blockchain/datc-out/redesign-2026-09-18/`（普查、bench JSON、pprof、推导日志）。

| 部分 | 大小 | 说明 |
|---|---|---|
| 叶历史 + 存储根 `a` `s` `sr` | 320 GB | 真相；64 KiB 帧 |
| 账户节点记录 `na` | 288 GB | 64.3 亿条 ≈ 40 B/条（哈希不可压，已贴近 32 B 下限） |
| 精确存储记录 `ns` | 205 GB | 2,848 个合约；前 4 个合约占 ≈ 98 GB |
| 出生分区 | 7.5 GB | |
| `ca` + `cs` | 30 GB | `cs` 可删 |
| 查询所需合计 | **≈ 800 GB** | v2 是 1011 GB（存储证明 p90 148 s）；v3 外推 1.3 TB |
| `mdbx.dat` | 433 GB | 续跑用的当前态 + 已不用的 DatcStorNode 90 GB；查询只读 DatcMeta |

## 6. 性能

验收 bench：8 路并发，每个证明严格按 EIP-1186 顺序对真实区块头 stateRoot 校验。失败的只有 bench 区块头源不到 25.86M 的那些高度。

| bench | 通过 | p50 | p90 | p99 | 最慢 | ≤ 200 ms | ≤ 1 s |
|---|---|---|---|---|---|---|---|
| 一般 2000（账户 + ≤ 2 槽，按变更集均匀采），热缓存 | 1981 | **29.5** | 44 | 77 | 213 | 99.95% | 100% |
| 同上，冷缓存 | 1981 | 74 | 110 | 142 | 227 | 99.95% | 100% |
| 分层 336（giant/large/mid/midhot/small × 幼年/早/中/晚），热 | 334 | 34 | 49 | 102 | 105 | 100% | 100% |
| 同上，冷 | 334 | 79 | 122 | 152 | 171 | 100% | 100% |

冷/热差别来自账户侧 273 次 16 KiB 读（冷时每次一个 NVMe pread）。吞吐 8 路 ≈ 235 证明/秒（热）。

经 eth-el 完整 JSON-RPC 链路（`TestDATCGetProofMainnet`，独立校验器）：块 6 万 21 ms；块 100 万 6 ms；USDT 出生后几天 2 槽 105 ms；
USDT @15M / @25.8M 2 槽 132 / 149 ms；USDC @20M 121 ms；不存在的账户 76 ms（首次触碰、缓存未热）。

历史对照：v2 账户 p50 164 ms、带槽 p90 148 s / p99 22 min、块 6 万 17.6 s；v3 账户 p50 40 s。

离线工具：`reframe` 270 GB 5.5 分钟；`derive-ns` 全量 30 亿行 24 分钟（≈ 300 万行/秒）；`derive-plan` 1.2 分钟；`derive-acc-parts` 28 秒；`verify-ns` 3,742 个根 < 1 分钟。

## 7. 集成到 eth-el（archive-plus）

eth-el 自己不保留历史状态，也没有 trie 后端的证明提供者：没有 DATC 时，`eth_getProof` 对历史块没有真东西可返回。接法：

```
eth_getProof ──► internal/api.BlockChainAPI.GetProof
                   ├─ ProofSource（若已安装）.ProveAt(块号)         ← 新增的钩子
                   │     └─ publicrpc.datcSource ─► datc.Archive.Prove ─► 归档目录
                   │           覆盖不到（≥ 归档 head）→ ErrProofNotCovered → 落回下面
                   └─ 节点自己的路径（State() + StateProofProvider）
```

```bash
eth-el ... --publicrpc.enabled --publicrpc.mode archive \
  --publicrpc.datc /data/blockchain/datc-out/datc-25m-v2-hi \
  --publicrpc.datc.verify header        # header | strict | off
```

- `datc.OpenArchive` 只读打开，**不碰进程级的表配置**（节点有自己的 MDBX）；读取器放在池里（默认 16 个并发证明），段索引在打开时加载并由 Archive 持有。
- **返回整份答案**（nonce/balance/codeHash/storageHash/槽值 + 证明），因为节点在历史高度也读不到这些值。
- **校验**：`header`（默认）= 节点有该块区块头就先对 stateRoot 走一遍证明、并核对叶子与返回值一致，不一致直接报错（`ErrProofMismatch`，绝不回落到弱路径）；
  `strict` = 没有区块头就拒绝；`off` = 不在节点侧校验（客户端反正要自己验）。快照启动的 eth-el 没有老区块头，显式块号按原样交给归档。
- **周更新不用停节点**：段是 rename 替换的，打开的文件保持旧 inode；Archive 每 30 秒看一眼 `leafseg/`、`ns.ladders`、`a.stages`、head，变了就换一代读取器。
- 部分归档（`start > 0`，只有上段）拒绝服务：它离开 `--base` 在任何高度都会答错。
- 归档 head 到链头之间的块 DATC 不覆盖（周更新节奏）；这一段走节点自己的路径——目前 eth-el 只有 latest 状态，没有 MPT 证明。补这一段见 §9。

## 8. 维护

**每周**（详见 `datc-weekly-update.md`）：变更集/区块头同步并验收 → 构建器续跑（`datc.bin build`，自动从 `DatcMeta/progress` 接，35–45 分钟）→
`derive-ns --from E`（≈ 25 分钟）→ `derive-plan` + 对新上榜合约 `derive-ns --contracts` → `verify-ns --from E` → `verify --samples 50` + 分层 bench + 一般 bench。

**不能丢的**：归档目录（含 `mdbx.dat` 的当前态表）、变更集与区块头输入、Windows 执行节点（下周变更集的唯一来源）。
**不能做的**：对归档跑 `prep-state`/`set-start`；打断收尾或在自动重启下跑收尾；`rsync --append` 变更集尾段；`pgrep -f` 带脚本名杀进程。

**健康检查**（一分钟）：`DatcMeta/progress == head`；`verify-ns --contracts 100`；`bench --queries redesign-2026-09-18/p1-queries.json`（最慢应 < 200 ms）；
eth-el 日志里的 `eth_getProof served from the DATC archive ... blocks [0, N)`。

**容量**：全链平均每百万块 ≈ 段 +31 GB（a/s/sr ≈ 12、na ≈ 11、ns ≈ 8），近期区块更密、按 1.5–2 倍预留；另加 mdbx 增长。前 4 个合约的 `ns` 继续按 ≈ 1 哈希/写入涨。

**何时要动设计**：某合约折叠单元 > ~10 万键（USDT 现在 2.1 万）→ 第二个精确层（深度 4）；账户历史键 > 5.4 亿（现在 4.59 亿，账户单元 > 8k 键）→ 账户精确层下移一层（代价 ≈ 再一份 `na`）。

## 9. 后续工作（已备好入口）

| 项 | 为什么 | 怎么做 |
|---|---|---|
| 归档 head → 链头的证明 | DATC 按周更新，最近几天的高度没有证明 | 让 eth-el 的 CS sink 直接喂一个常驻的 DATC 增量构建（输入就是它自己写的 acctcs/storcs），把 head 滞后从一周降到分钟级；`derive-ns --from` 已支持小步追加 |
| 账户侧也改成纯精确层 | 去掉 `ca`、第 1/2 层 epoch 记录和 `branchSlotsAt` 的窗口递归，读取端只剩一种逻辑 | `na` 第 3 层已是逐块；读取端对账户走 `exactSlotsAt` 同样的"合成上层"即可，数据不用动 |
| `mdbx.dat` 瘦身 | 433 GB 里查询只用 DatcMeta；DatcStorNode 90 GB 已是死数据 | 构建器加 `--no-sto-records`（不再写 v2 存储 epoch 记录）；服务节点只需要段 + 一个只含 DatcMeta 的小库 |
| 周更新一键化 | 现在是 5 条命令 | `n42-datc weekly --out A` 串起来，任何一步校验不过就停 |
| `eth_getStorageAt` / `eth_getBalance` 的历史值 | 归档里已经有任意高度的叶值（`leafFloor`），比证明便宜得多 | 同一个 `ProofSource` 思路给 `stateReaderProvider` 加一个 DATC 后端 |
| 分发 | 800 GB 的只读段适合做成可下载的 archive-plus 数据包 | 段是内容不变的文件，按表 + 桶分片、附 CRC 清单 |
