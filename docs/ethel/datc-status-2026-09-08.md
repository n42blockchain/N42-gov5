# DATC 现状与交接（2026-09-08）

目标（用户原话）：**用较少的数据完成全量 proof，保持可用的延时/性能，多数 1 秒内或更快。**
本文记录到今天为止的全部结论、正在跑的东西、以及接手要做的事。设计与审计细节见
[`datc-audit-format-v2-2026-09-01.md`](datc-audit-format-v2-2026-09-01.md)，Windows 侧操作见
[`datc-windows-genesis-runbook.md`](datc-windows-genesis-runbook.md)。

## 1. 一句话结论

审计发现并修掉了 6 个会导致历史根错误或纯浪费空间的缺陷（根因是存储键里残留的 8 字节
incarnation），升级为 v2 格式；在 2M 真实主网数据上，账户 proof 从 p50 782 ms 降到
**p50 10–12 ms、p99 32–35 ms，100% ≤100 ms**，含槽 proof 100% ≤1 s；全链最终体积预计
**600–880 GB**。25M 全链构建分两段并行，目前上段 75.9%，下段在 Windows 侧。

## 2. 已落地的改动（都在 origin/main）

| commit | 内容 |
|---|---|
| `2a0cae17` | v2 格式：32B 存储域、4B 块号、MIXED 标记、`DatcStoRoot` 存储根历史、记录深度写入 meta；`lib/trie` 新增被动 `SetStorageRootHook`；合成端到端测试框架 `datc_e2e_test.go` |
| `33e0c66c` | `--sched` 显式每层 epoch；账户节点记录改走静态段（`na.*`） |
| `e21a6b1a` | dense-node 钩子（混合节点也记完整哈希）、`--acc-root-epoch` 逐块根记录、finalize 假魔数修复、spill 流 id 位宽修复、`fullEvery` 按记录数计 |
| `f16be7b7` | `prep-state` + `merge`（分段构建与合并）、`TestE2E_SplitMerge` |
| `fff8a81e` | `merge` 方向无关、`--mem.gb` 软内存上限、`scripts/datc/` 运行与守护脚本、Windows runbook |
| `1877c21f` | 分段合并演练数字、并行折叠的负结果 |

## 3. 关键设计判断（为什么是这个形状）

- 每次叶变更都会改动路径上每层的一个子哈希；某层若要"任意高度精确"，代价就是 32 B × 变更数，
  与层深无关。上层不精确会让重建扇出逐层相乘，最终退化成大规模叶折叠（v5 的分钟级由此而来）。
  因此最优形状是**恰好一层逐块 dense**：`--sched 1024,16384,1024,1,4194304,4194304`
  （d3 逐块，d1/d2 稀疏，存储根层 1024）。
- `--acc-root-epoch 1`（逐块记录账户树根）是本轮最大的单项收益：proof 只读 1 条根记录，
  不再从 16+256+4096 条深层记录重建根。代价约 240–260 GB，占段文件 38%。
  **这是唯一的大旋钮**：放到 64 可省 150–200 GB，账户 proof 退到约 350 ms（仍远在 1 秒内）。
- `DatcStoRoot`（存储根历史）让账户 proof 完全不碰存储叶历史——这是原先"折叠一个 depth-4
  子树要枚举 6100 个键、96% 是休眠键"的成本来源。

## 4. 实测数字（2M 真实主网数据，构建 D）

| 指标 | 值 |
|---|---|
| verify（随机高度重建根） | 20/20 正确，p50 <1 ms |
| 账户 proof（串行） | p50 11.8 ms、p99 35 ms、max 46 ms，**100% ≤100 ms** |
| 账户+槽 proof | p50 12.3 ms、p99 507 ms、max 900 ms，95% ≤100 ms，**100% ≤1 s** |
| 8 路并发 | 83 proof/s，账户 p99 75 ms |
| 数据量 | 段 1.1 GB + MDBX 2.0 GB（其中状态表） |

剩余的尾巴：TheDAO/USDT 级热合约的老高度**槽** proof 0.5–1.3 s，成本是 depth-2 折叠要读
25–30 万行叶历史。要压到百 ms 只能加深存储记录（`--sto-depth 3`，估计 +100–250 GB），
建议等全链数字出来再定。

## 5. 全链最终体积预估（基于三个实测锚点外推）

| 层 | 预计 | 占比 |
|---|---|---|
| 叶历史 a + s | 300–330 GB | 48% |
| 账户节点记录 na | 240–260 GB | 38% |
| 存储根历史 sr | 60–70 GB | 10% |
| 变更索引 ca + cs | 25–30 GB | 4% |
| **静态段小计** | **约 630 GB** | |
| DatcStorNode（MDBX，packed 后约 19 GB） | 约 75 GB | |
| 当前态表 Hashed*/TrieOf*（查询不需要） | 约 180 GB | |
| **合计 / 仅 proof 归档** | **约 880 GB / 650 GB** | |

早期区间用了 DeFi 区单位成本外推，偏保守（0→2M 实测 1.1 GB，模型给 8 GB），真实值大概率
落在区间下半段。

## 6. 25M 全链构建：现在的状态

**分工**：Windows 跑下段 `[0, 17,900,000)`；Linux 跑上段 `[17,900,000, 25,864,982)`
（基底是 Windows 传来的 17.9M 状态表，`prep-state` 清成干净的 v2 输出后续建）。
两段跑完在 Linux 上 `merge`。这个流程已在 2M 真实数据上完整演练：合并 21 s / 8.6 GB，
verify 30/30，bench 与单进程构建一致。

**上段（Linux）**：`/data/blockchain/datc-out/datc-25m-v2-hi`，停在块 **19,620,320**（全链 75.9%），
492 GB，剩余约 624 万块。无干扰时 55–65 blk/s，预计再需 30–45 小时。
恢复命令（自动从 `DatcMeta/progress` 续跑）：

```
setsid nohup /data/blockchain/datc-out/supervise.sh \
  /data/blockchain/datc-out/run-25m-hi.sh \
  /data/blockchain/datc-out/datc-25m-v2-hi.build.log >/dev/null 2>&1 </dev/null &
```

**下段（Windows）**：按 runbook 跑 `--end 17900000`，产物传到
`/data/blockchain/datc-out/datc-25m-v2-lo/`。

**合并（两段齐了之后）**：

```
BIN=/data/blockchain/datc-out/n42-datc-25m-hi5.bin   # hi5 才有方向无关的 merge
$BIN merge --into /data/blockchain/datc-out/datc-25m-v2-hi --from /data/blockchain/datc-out/datc-25m-v2-lo
$BIN verify --out ... --samples 50
$BIN bench  --out ... --samples 500 --mode mixed --parallel 8
```

## 7. 运维教训（都是踩过的坑）

1. **`--mem.gb` 不能贴着实际堆设**：DeFi 区活跃堆 15–25 GB，设 24 GB 时 GC 吃掉 90% CPU，
   吞吐从 58 blk/s 掉到 7 blk/s（pprof 显示全在 gcDrain/sweep）。现用 40 GB，内存紧张时
   改小 `--stocache.m` 而不是压 `--mem.gb`。
2. **MDBX 脏页不受 Go 内存上限管**：`--dirty.gb` 要单独算进进程驻留。
3. **长任务必须 `setsid nohup ... </dev/null &`**：从工具 shell 直接起的构建，会随该 shell
   的进程组被回收而静默死亡（发生过一次，无日志无 OOM）。
4. **别用会匹配到自己命令行的 `pgrep -f`**：杀进程一律用显式 PID 或锚定路径的模式。
   这条也适用于**等待**别人的作业：写 `pgrep -f "fleet7-bench.sh"` 的脚本会匹配到自己，
   于是永远认为对方还在跑——真发生过，机器空转了 10 小时。用方括号写法
   `pgrep -f "[f]leet7-bench.sh"`（正则里 `[f]` 匹配 f，而字面串 `[f]leet` 不等于 `fleet`）。
5. **停机要三件事凑齐才算干净**：TERM 转发给子进程（`scripts/datc/supervise.sh` 的 `trap`）、
   构建收到 TERM 后收尾当前批次并把 spill 切在 zstd 帧边界、守护脚本收到 TERM 后不再重拉。
   少任何一件，`kill` 都会留下孤儿进程或被自动重启。
6. **共享机器上 DATC 是 OOM 的首选目标**（它最大）：至今被别的基准挤掉 17 次，
   每次损失一批 + 约 20 分钟页缓存预热。`supervise.sh` 会自动续跑，但代价真实。
   机器协调用 `/data/blockchain/.box-claim-*` 文件（与 n42-gov5-e7 会话约定）。
7. **磁盘**：`/data` 一度到 90%。已删掉 435 GB 的 `n42-datc-cont-25864981`（状态已拷进上段输出）。
   上段跑完还需 250–300 GB。

## 8. 试过但无效 / 已放弃

- **存储树变更子树并行折叠**：账户 proof p50 从 13 ms 劣化到 26 ms（goroutine + 事务开销），
  热合约槽 proof 1.27 s 纹丝不动（瓶颈是要读的 30 万行叶历史本身）。已回退，勿重试。
- **40B 存储域的 archive-plus 管线**（另一条线的 `fd4d03f0`）：与 v2 的 32B 域冲突，
  建议废弃而不是做格式迁移——1.1 TB 老库是可再生的派生数据，重建比迁移便宜。

## 9. 接手要做的事（按顺序）

1. 恢复上段构建跑完（约 30–45 小时）。
2. 等 Windows 下段产物传回，`merge` 到上段目录。
3. 全链 `verify --samples 50`（必须含非边界高度）+ `bench --samples 500 --parallel 8`，
   拿到真实的体积和延迟数字。
4. 拿数字定两件事：`--acc-root-epoch` 是否放宽（省 150–200 GB）、`--sto-depth 3` 是否值得
   （治热合约槽 proof 的秒级尾巴）。
5. 可选：节点表 packed-segment 化（MDBX 3.85× 行开销的唯一解，与本轮改动正交）。
