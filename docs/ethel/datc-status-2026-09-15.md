# DATC 25M 归档状态（2026-09-15）

取代 `datc-status-2026-09-08.md`。周更新操作与数据保留见 `datc-weekly-update.md`。

## 1. 当前状态

| 项目 | 状态 |
|---|---|
| 下段 `datc-25m-v2-lo` | 已完成，区块 `[0, 17,900,000)`，514 GB，leaf 段已收尾 |
| 上段 `datc-25m-v2-hi` | 构建已完成，区块 `[17,900,000, 25,943,311)`，最后一块 **25,943,311**（含周更新延伸）。`DatcMeta`：start 17,900,000、progress/head 25,943,311、format 2，sched 与深度参数和下段逐字节相同 |
| leaf 段收尾 | 进行中。09-15 21:05 UTC 在 `s.7b` 被 OOM 强杀（内存排序），已隔离恢复（见第 6 节）；已完成 1574/1963 桶，剩余 389 桶待用外排序版本（hi10）单独重跑 |
| 下一步 | 行数核对（a/s 桶恢复行数 vs `leafprog` 增量）→ `merge --into hi --from lo` → `verify --samples 50` → `bench` → 删除 `leafspill/` 与下段库 |

## 2. 构建性能优化

上段在 20M 附近长期只有 11–13 块/秒。按顺序做了四处改动（均在 main）：

| 版本 | 改动 | 实测 |
|---|---|---|
| hi6 | 基线 | 11–13 块/秒；每批末尾写库停顿 150–280 秒 |
| hi7 | `b73f222b` DupSort 表一次定位写入（去掉写前删除）+ `--writemap` | 40–42 块/秒；停顿 10–20 秒 |
| hi8 | `70597414` 根计算钩子按 nibble 分 17 片 | 47–48 块/秒 |
| hi9 | `46a4c152` dense 钩子只保留会被取走的层级（修复内存泄漏） | GC 占比 81% → 2.5%，在用堆 33 GB → 6.6 GB，24 → 41 块/秒 |

要点：
- 写库停顿的主因是 MDBX 大事务反复重排脏页列表（`txn_dpl_sort_slowpath`），WRITEMAP 下没有脏页列表。独立基准：碎片化后每次写入 11.3 µs → 1.0–1.1 µs。升级 mdbx-go 到 v0.43.0（libmdbx 0.14.3）无收益，未升级。
- `Put(k, v, MDBX_ALLDUPS)` 在键只有一个值时会追加而不是替换，不能用来做 DupSort 替换。
- dense 钩子泄漏从 hi6 起就存在：加载器上报所有算过的分支，但只有账户 0..accDepth-1 层、存储 0..stoDepth-1 层会被取走。18.9M 附近的"GC 饥饿"很可能就是它。
- 速度随时间下降时，除了看批末停顿，还要看批次行的 `heap=` 和 CPU profile 里的 GC 占比。

后段（24–25.9M）实测 20–27 块/秒，是区块本身变重，不是故障。

## 3. 停机前 profile（25.46M）

profile 与火焰图在 `/data/blockchain/datc-out/profiles/`，生成脚本 `scripts/datc/pprof2flame.py <profile> <out.svg> [title]`（不依赖 graphviz 或 flamegraph.pl）。

稳态 60 秒平均只用约 3.1 个核：状态根计算 73%（其中游标 seek 40%，经 cgo 读 MDBX 占总 CPU 33%，哈希构建约 20%，keccak 7%），逐块执行 14%，预取加解码约 6%，GC 4.5%，spill/zstd 不到 2%。

结论：`--decode-workers`、`--prefetch`、`--stocache.m`、`--dirty.gb`（WRITEMAP 下无效）都不在热点上；GOGC 收益不超过 5% 且堆离上限不远，维持现有参数。唯一可能有收益的是 `--batch 8192`，需要实测。真正的瓶颈是根计算分片不均（热门合约的存储树整棵落在一个分片）和 cgo 读 MDBX，需要改代码，构建中途不做。

## 4. 周更新延伸（25,864,982 → 25,943,311）

1. 暂存数据 `datc-input-staging-25943310/` 验收：
   - 变更集覆盖到 25,943,310；按 DATC 同款打开方式逐块读取，重叠区与现有数据逐块相同，78,329 个新块全部非空。
   - 区块头与周更新 eth-el 节点的 headerc 逐块比对 81,167 块，stateRoot、txRoot、receiptRoot 全部一致。
   - 注意：变更集尾段被原位改写（最后一批从 22 项重编码为 64 项），必须整文件替换。
2. 同步进 `datc-input`：写临时文件 → 逐字节比对 → rename；旧文件硬链接备份到 `datc-input-backup-25864982/`。运行中的构建不受影响（持有旧 inode）。
3. 在上段跑到旧终点之前优雅停下，改用 `--end 99999999` 续跑（自动截到 25,943,311），这样 leaf 段只收尾一次。续跑 482,095 块用时 6 小时 5 分钟，无 ROOT MISMATCH。
4. 区块头 `headerc.0000/0001` 原为指向 `witness/` 的软链（witness-replay 项目的目录），已拷成实体文件，DATC 输入不再依赖其他项目目录。

## 5. leaf 段收尾中的 kill-tail 损坏帧

收尾时每个 spill 桶都报告跳过 1–14 个 kill-tail 损坏帧。核查结论：**不丢数据、不重复**。

- 构建日志共有 15 次被 OOM 强杀（rc=137，集中在 09-02 与 09-04）。
- 每批的顺序是先提交 MDBX，再切 spill 帧并刷盘，最后打印批次行。只有强杀落在"已提交、未切帧"的窗口里才会丢这一批。
- 逐次比对"强杀前最后打印的批次块号"与"下次续跑起点"：13 次相等（已切帧落盘）；另 2 次在第一批完成前被杀（未提交，从同一起点重跑）。
- 截断帧来自未提交批次，续跑时已重新写出，解码失败会整帧丢弃。

因为出现过损坏帧，收尾结束会打印汇总 WARNING 并保留 `leafspill/`。按规程先 verify，通过后再手动删除 spill。

**收尾过程中禁止打断**：spill 被保留时重跑收尾，会把同一批行再合并进已有段，造成重复。

## 6. leaf 段收尾 OOM 事故（2026-09-15）与外排序

**经过**

1. 旧的 `finalizeBucket` 把整个 spill 读进内存（`io.ReadAll`），解码、排序后再写段。USDT 这类存储极多的合约，所有 leaf 行都落在同一个桶：`s.ab` 压缩后 14.9 GB，`s.7b` 9.6 GB，`s.15` 12 GB，解码后是几十 GB。21:05 在 `s.7b` 被 OOM 强杀。
2. `supervise.sh` 在 rc≠0 后自动重启构建。此时 progress == end，构建跑 0 块就直接再次收尾。之前因 kill-tail 损坏帧保留了 spill 的桶被重新合并进已有段，`a.00.seg` 出现重复行。30 秒后发现并停止。

**恢复**（已执行）

- 先停掉所有进程。
- 已有 `.seg`、但 spill 仍在 `leafspill/` 的桶（684 个）：把 spill 移到 `datc-25m-v2-hi-quarantine-20260915/leafspill-finalized/`，不删除。
- 被重启进程改写过的 `a.00.seg`：移到 `leafseg-dup/`，再由它的 spill 重建。
- 只对"没有段的 spill"重跑收尾。

**修复：外排序**（`cmd/n42-datc/leafseg.go`，hi10）

- spill 不再整体读入。按块流式扫描帧起点，每帧用 `ReadAt` 读取。损坏帧的扩展重试只增量读取新增字节。
- 每解出一帧就解析其中的完整行，残尾留给下一帧。损坏帧、组内残尾丢弃的语义不变。
- 行攒到 `DATC_FINALIZE_RUN_BYTES`（默认 1 GiB）就稳定排序，写成段旁的临时批次文件 `<seg>.runNNNN.tmp`。
- 最后做堆多路归并，相同键的顺序是：已有段优先，然后按批次先后。
- 只有一个批次时仍走内存路径。
- 验证：与旧实现在同一 spill 上（含重复键、kill-tail 截断帧、续跑合并）的输出逐字节相同。见 `TestFinalizeExternalSortMatchesInMemory`。
- 内存上限约为 2×批次大小；临时磁盘约等于最大桶解码后的大小。

**规则（避免再犯）**

- 收尾绝不在 `supervise.sh` 或任何自动重启下运行，用 `datc.bin finalize-leaves` 单独跑并盯着。
- 启动收尾前，先列出最大的 spill（`ls -l leafspill | sort -k5 -n | tail`），并检查空闲内存和磁盘。
- 收尾被打断后，按上面的"恢复"步骤处理，不要直接重跑。
- `supervise.sh` 已修：构建到达终点后若在收尾中退出（rc≠0），不再重启；日志里有未完成的收尾（最后一个 `[leafseg] finalizing` 之后没有 `[leafseg] done`）时拒绝启动。人工恢复并单独收尾后，往日志追加一行 `[leafseg] done (standalone)` 才能再用它。

## 7. 以后可以改进的地方

- leaf 段收尾是单线程逐桶处理（约 22 MB/s，377 GB spill 约 4.5 小时）。各桶互相独立，可以改成多桶并行。
- 根计算分片不均：需要存储树内部并行或更细的分片，涉及节点记录生成方式，需在非构建期间开发并用 e2e 验证。
