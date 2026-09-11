# DATC 深度审计与 v2 格式（2026-09-01）

对象：`cmd/n42-datc`（Depth-Adaptive Temporal Checkpointing，任意历史高度 EIP-1186 proof）。
方法：先写一套**合成端到端正确性框架**（`cmd/n42-datc/datc_e2e_test.go`），用它把怀疑点逐条实证，再改代码、再用同一框架回归。文中 **[实证]** = 合成框架跑出来的结果；**[推算]** = 用既有 mainnet 实测数字外推。

## 0. 结论速览

| # | 问题 | 后果 | 状态 |
|---|---|---|---|
| F1 | 存储 domain 仍带 8B incarnation（历史清理残留） | SELFDESTRUCT 槽墓碑一条都写不出（合约重建后旧槽复活，48/360 高度根错误 [实证]）；`TrieOfStorage` 永远读不到 → 存储节点记录层整层为空、存储树全部退化成叶折叠 | 已修：domain=32B，v2 格式 |
| F2 | 存储树根（d0）没有 change rows，注释假设 `E_0=1` | `E_0>1`（25M 跑用 cbar=0.25 → E_0=64）或 window 模式下，中间高度的 storageRoot 取到上一 epoch 的陈旧记录 | 已修：`e[d]>1` 的存储层一律写 change rows；window 模式 `e[0]=W` 并持久化 |
| F3 | window 模式 d0 记录只在窗口末写，但 epoch 键按 `n/E_0` 编 | 读侧按 `E_0` 算窗口，与写侧不一致 | 已修（同 F2） |
| F4 | build 写到 depth 5，查询侧存储固定 depth 2、账户 `--fold-depth` 折叠 | 存储 d≥2、账户 d≥fold 的节点记录和 change rows **从未被读**，纯占空间 | 已修：`--acc-depth/--sto-depth` 写入 meta，查询从 meta 取 |
| F5 | 混合节点（`hasState≠hasHash`，含叶/extension 子节点）记录 reader 永远拒绝，却整条写 masks+hashes+rootHash | 小合约的存储节点记录全是废字节 | 已修：1 字节 `MIXED` 标记 + 连续 MIXED 省略 |
| F6 | 账户折叠对子树里每个合约都走"记录 floor + change window + 子树折叠"拿 storageRoot | mainnet 慢 proof 的主成本（"fold 一个 depth-4 子树要枚举 ~6100 键"） | 已修：`DatcStoRoot` 逐块 storage-root 历史，账户 proof 不再触碰存储叶历史 |
| — | 叶历史键块号 8B | 13.3B 行 × 4B 浪费 | 已修：4B |

审计确认**没有问题**的部分：FULL/DIFF 链与 `fullEvery` 回溯；change row 的 varint(Δblock)+nibble 编码与 `changedChildren` 解码；resume 后丢失内存脏位图的 `degraded→FULL` 兜底；leafseg 的 spill→finalize→cursor（含 resume 时与已有段合并）；concurrent-root 的 gold-check 回退；graceful stop。

## 1. 合成端到端框架（`datc_e2e_test.go`）

- 生成器：3000 EOA + 2 个 2200 槽大合约 + 30 个小合约，360 块；每块随机：账户创建/修改/删除、槽写入/清零、SELFDESTRUCT（**带或不带**显式 wipe 行）、同块销毁+重建、绝不存在账户的 ghost 存储写。
- **独立参考 MPT**：测试内自带一个朴素递归 RLP 哈希器（不依赖 `HashBuilder`/`GenStructStep`/`mptNodeRLP`），逐块给出参考 stateRoot 与每个合约的 storageRoot。
- build：fwd 模式（`FwdAcctCS/FwdStorCS` 表），`rootOracle` 用参考根逐块 gold-check（顺带验证了 `TrieRootComputer` 在这些边角序列上的正确性）。
- 查询：**每个高度**重建根与参考比对；再对 6 个键（EOA、大合约、小合约、不存在地址）× 40 个随机高度生成账户 proof + 槽 proof（含不存在槽），用 `walkProof` 对参考根独立验链，值与回放的参考状态逐字节比对。
- 场景：逐块 `E_0=1` / `E_0=4`、window（W=4）、resume（epoch 中途、window 边界）、leafseg、leafseg + 中途 finalize 后 resume（段合并）、记录深度 acc3/sto1、fold-depth 覆盖、concurrent-root。**全部通过。**

跑法：
```
go test -tags "nosqlite,noboltdb" ./cmd/n42-datc/ -run TestE2E -v
DATC_DBG_WINDOW=1 go test -tags "nosqlite,noboltdb" ./cmd/n42-datc/ -run TestDiagFirstMismatch -v   # 首个失配高度的逐键诊断
```

## 2. v2 on-disk 格式（`DatcMeta/format = 2`，旧数据被拒绝，需重建）

| 层 | v1 | v2 |
|---|---|---|
| 存储叶键 | `addrHash(32)‖inc(8)‖slotHash(32)‖block(8)` | `addrHash(32)‖slotHash(32)‖block(4)` |
| 账户叶键 | `addrHash(32)‖block(8)` | `addrHash(32)‖block(4)` |
| 存储节点/chg 键 | domain 40B | domain 32B |
| 节点记录种类 | FULL / DIFF / 墓碑 | + `MIXED`（1 字节；连续 MIXED 省略） |
| 记录深度 | 固定 d≤5 | `--acc-depth`（默认 4：账户 d1..3）/ `--sto-depth`（默认 2：存储 d0..1），写入 meta |
| 存储 d0 change rows | 无 | `e[0]>1` 时写 |
| window 模式 | `e[0]` 照旧 | `e[0]=W` 写入 meta |
| 账户节点记录 | MDBX `DatcAccNode` | `--leaf-seg` 下写入 `na.*` 静态段（桶 = pathLen+前两个 nibble，256 桶）；账户侧 FULL/DIFF 簿记全在内存，不需要回读，因此不必留在 MDBX（B′ 里这是最大的 MDBX 表，3.85× 行开销全是浪费）。存储节点记录仍在 MDBX（lastFull 回读需要） |
| 调度 | 只有 `--alpha/--cbar` 单调公式 | 新增 `--sched e0,…,e5` 显式每层 epoch。25M 推荐形态：`1024,16384,1024,1,4194304,4194304`（存储根层 1024、账户 d1/d2 稀疏、**d3 逐块 dense**）+ `--window=false`，即 B′ 实测验证过的形状：根重建只需 16+256+4096 次记录 seek，不折叠；账户 proof 只折叠目标 depth-4 子树 |
| **新增** `DatcStoRoot` | — | `addrHash(32)‖block(4) → root(32)`（空值 = 无存储）；逐块模式每个存储有变更的合约每块一行，window 模式每窗口一行；节奏写入 meta `srcad`；`--leaf-seg` 下走 `sr.*` 段 |

`DatcStoRoot` 的根来自 `lib/trie` 新增的被动回调 `FlatDBTrieLoader.SetStorageRootHook`（`RootHashAggregator` 在每个账户的存储子树定型时上报 `(addrHash, root)`），经 `TrieRootComputer.SetStorageRootHook` 透传，concurrent-root 的 16 个分片也接入（回调加锁；分片分歧回退串行前发 `(nil,nil)` 重置信号）。回调没上报的变更合约会用 `HashedStorage` 复核必须为空，否则 build 直接报错，不会写错根。

查询侧：`nodeHashAt(domain, [], N)` 先查 `DatcStoRoot` floor；逐块节奏直接精确；窗口节奏再看该合约 d0 change window 里 N 之前有没有变更，没有则精确，有则回落到记录/折叠路径。

## 3. 数据量：合成数据实测 + mainnet 推算

合成集（逐块 E_0=1，360 块）[实证]：

| 表 | 行 | B/行 |
|---|---|---|
| DatcAccNode | 1351（full 180 / diff 1170 / mixed 1） | 301 |
| DatcStorNode | 3427（full 429 / diff 2780 / mixed 218；另有 2708 个 MIXED epoch 被省略） | 292 |
| DatcAccChg / DatcStorChg | 1504 / 5905 | 36 / 51 |
| DatcLeafA / DatcLeafS | 17507 / 25716 | 44 / 83 |
| DatcStoRoot | 1712 | 68 |

同一批行按 v1 布局会多 614,644 B（约 13%），MIXED 标记再省 19,522 B 记录字节 + 2708 行。

mainnet 推算 [推算]（基数取 `docs/datc历史proof秒级性能.txt`：13.33B 次叶变更，其中存储 ~8.6B）：

1. 叶历史键：每行 −4B，存储行再 −8B → 原始 **−122 GB**（zstd 前；零字节压得好，压后收益小于此数，但段内 seek 比较的是原始键，读侧也受益）。
2. 存储节点记录：`--sto-depth 2` 只留 d0/d1。存储树第 d 层节点数按 16^d 增长，d≥2 的行在 v1 中占绝大多数（估 >85%），且小合约的 d0/d1 多为 MIXED（1 字节）。DatcStorNode（v1 全链外推 ~690 GB MDBX）预计降一个数量级。
3. 账户节点记录：`--acc-depth 4` 不再写 d4/d5（v1 默认 sched 下这两层 epoch 很长、行数不多，收益有限；B′ 那种 `e[3]=1` 逐块 dense 的形态不受影响）。
4. `DatcStoRoot`：B′ 实测 435M 行@27% → 全链 ~1.6B 行；段形态每行 68B 原始 ≈ **110 GB**（B′ 放 MDBX 是 69GB@27% ≈ 255GB 全链）。行数由热合约主导（USDT 每块一行），**window 模式（W=1024）下热合约每窗口只有一行**，预计缩到 10–20%；代价是热合约中间高度的槽 proof 仍要折叠变更子树（与之前相同）。
5. 仍在 MDBX 的节点表受 3.85× 行开销影响不变，packed-segment 转换仍是后续项。

### 为什么必须 d3 逐块 dense（数据量与延迟的根本权衡）

每一次叶变更都改动路径上每一层的一个子哈希。任一层若要"任意高度精确"，就要存下每次变更后的新子哈希：32 B × 变更数，与层深无关（账户侧 4.7B 次变更 ≈ 150 GB 原始）。稀疏 epoch 只在浅层有去重收益（同一 (节点, 子) 在 epoch 内反复变化）；深层每 epoch 的变更数远小于 (节点×16) 对数，没有去重。而查询侧，若某层不精确，重建它就要递归到下一层"自记录以来变过的孩子"，扇出逐层相乘，最后落到叶折叠——d2 以上任一层不精确都会让一个 proof 折叠几十到上万个 depth-4 子树（这就是 v5 的分钟级）。所以最优形状只有一种：恰好一层逐块 dense（选 d3：上面三层用 seek 重建只要 16+256+4096 次，下面折叠 1/65536 的状态 ≈ 6000 账户 ≈ 20–50 ms），其余层要么稀疏要么不存。这层 ~150–190 GB（段形态）是"任意高度、亚百毫秒账户 proof"的信息下界，除非折叠本身能降到毫秒级。

## 4. 性能含义

- **账户 proof（绝大多数查询）**：depth-4 子树折叠里每个合约的 storageRoot 变成一次 `sr` 段 floor seek（同一子树的合约共享 2 字节前缀，落在同一桶的相邻帧），不再读存储叶历史；这正是之前"fold 读 ~6100 键、96% 是休眠键"的成本来源。
- **槽 proof**：大合约走 d0/d1 记录 + 变更子树折叠（depth-2 子树 = 1/256），小合约整树折叠；与之前相同，热合约老高度仍是"秒级边缘"，若要压到百 ms 需要热合约的 per-domain 更深记录（可用 `--sto-depth 3` 全局换，或后续做按合约自适应）。
- resume 后存储路径首个 flush 强制 FULL 的逻辑保留；MIXED 不依赖脏位图，resume 安全。

## 5. 后续建议

1. 用新二进制先重建 2M（`--leaf-seg`，默认深度）→ `verify --samples 50`（必须含非边界高度）→ `proof` 抽查大合约老 offset；再上 25M。旧 v1 产物无法被新二进制读取（format 检查）。
2. mainnet 跑 window 模式时 `DatcStoRoot` 是每窗口一行；若账户 proof 的 p99 仍被热合约中间高度拖累，可考虑仅对热合约（change rows 密度高的 domain）逐块补根。
3. 段格式的键前缀增量编码：zstd 已把重复键压到约 3–4 B，显式增量的额外收益估计 10–15%，暂不动格式。
4. 节点表 packed-segment 化（M3）仍是 MDBX 3.85× 行开销的唯一解，独立于本次改动。

## 6. 两进程并行构建（2026-09-01 晚补）

把机器用起来的唯一结构性办法：链的中间某处有一份 Hashed*/TrieOf* 状态，就能让第二个进程从那里正向建到链尾，和从创世开始的进程并行；两段产物用 `merge` 合并成一份。

- `n42-datc prep-state --out <copy>`：把一份含状态表 + `DatcMeta/progress` 的 MDBX 拷贝清成干净的 v2 输出（清空全部 Datc* 表、删多余 meta），`build --out <copy>` 即从 `progress` 自动续建（首块 gold-check 立刻验证状态是否对上）。
- `n42-datc merge --into <lower> --from <upper> [--from-start N]`：上段的段文件重新 spill 到下段目录后 finalize（稳定合并，下段行在前、上段行在后），MDBX 存储节点表按键覆盖拷贝，meta head/progress 更新。边界 epoch 的重复 (path, epoch) 记录由读侧 floor 语义自然选到上段那条；上段每条路径的首条记录是 FULL，DIFF 链不跨边界。`TestE2E_SplitMerge` 覆盖整个流程。
- 主网现状：`n42-datc-cont-25864981`（Windows Pipeline-B 续建库）里的状态表是 32B 格式、`progress=17,900,000`，直接作为上段基底；按叶变更算 0→17.9M 占 7.13B、17.9M→25.86M 占 6.2B，两段接近均分。

### 6.1 2M 真实数据演练（2026-09-02）

下段 0→1M（5 分钟）→ 拷 mdbx + `prep-state` → 上段 1M→2M（7 分钟）→ `merge --into 上段 --from 下段`：**21 s、峰值 RSS 8.6 GB**（重新 spill 8.8M 行 + finalize 合并，边界 epoch 丢弃 268 条已被上段覆盖的部分记录）→ verify 30/30、根重建 p50 1 ms → bench 账户 proof p50 10 ms / p99 32 ms，含槽 100% ≤1 s。与单进程 D 构建完全一致。25M 规模的 finalize 是按桶在内存排序（每桶约 5–8 GB），合并预计小时级。

## 7. 试过但无效的优化（勿重试）

- **存储树变更子树并行折叠**（2026-09-02）：在 `branchSlotsAt` 把 ≥4 个变更孩子交给各自带独立 RoTx 的 goroutine 折叠。2M 实测：账户 proof p50 13→26 ms（goroutine + 事务开销大于收益），TheDAO 级槽 proof 1.27 s 纹丝不动——它的成本是单个热合约在 depth-2 折叠时要读的 30 万行叶历史本身，不是串行。要治只能加深存储记录（`--sto-depth 3`，代价约 +100–250 GB）。已回退。
