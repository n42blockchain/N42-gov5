# QS Linux 审阅问题答复

日期：2026-08-24
对应清单：`docs/QS_LINUX_OPEN_QUESTIONS_20260824.md`

标注约定：
- **[确定]** 有代码、文档或本机实测为依据
- **[推断]** 依据充分但未直接验证
- **[未知]** 我答不了，需要另找依据

先说一句：这份清单质量很高。1.3 节主动交代自己混淆了 N42 舰队与 ETH witness 两个语境、
O2 主动交代 pidfile 被覆盖、O4 主动作废自己跑出的全部成绩——这几条比多数问题本身更有价值。

---

## 阻塞项 1 — R4：Windows 周更新的真实 source/target 和脚本参数

**[确定] 你们的理解完全正确。** 权威文档 `docs/QS_WEEKLY_REPLAY_SYNC.md`，实际执行：

```powershell
# Step 1  停舰队(CTRL_BREAK,绝不 kill),确认 7 节点 committedQC 一致,记录 H = node0 最后 commit 高度
foreach ($i in 0..6) { $p = (Get-Content E:\qs-node$i\n42.pid).Split()[0]; sendbreak.exe $p }

# Step 2  增量 fold:source = 停机后的 qs-node0,target = canonical base
replay-v2 --source E:/qs-node0 --target E:/qs-replay-v4 `
          --chain mainnet_qmdb_staggered --tree qmdb `
          --output E:/qs-replay-v4/replay_stats_<date>.json

# Step 2b seal + hot,产出舰队启动用的 seed
n42-ancient-seal --source E:/qs-replay-v4 --out E:/qs-era-out --seal
n42-ancient-seal --source E:/qs-replay-v4 --out E:/qs-era-out --emit-hot
n42-ancient verify --dir E:/qs-era-out/ancient-era --deep --source E:/qs-replay-v4/chaindata --sample 64
Copy-Item E:\qs-replay-v4\checkpoint.json E:\qs-era-out\checkpoint.json

# Step 2c txindex 建进 seed
E:\build-seed-txindex.ps1 -Seed E:\qs-era-out

# Step 3  移开旧代次,重新 seed 七节点
foreach ($i in 0..6) { Move-Item E:\qs-node$i E:\qs-node$i-pre<date> }
pwsh -File E:\deploy-7node.ps1 -Data E:\qs-era-out -Bin <最新 exe>
```

对应到 Linux：`/data/blockchain/qs-node0` → `qs-replay-linux` → `qs-era-linux`。
`mainnet-source` 那趟确实只是一次性 bootstrap，以后不再用。

三个必须记住的坑（都在 runbook 里，都实际踩过）：
1. `--source` / `--target` 传 **datadir 根**，工具自己加 `/chaindata`；传 chaindata 路径会以 Accede-mode 报错。
2. `deploy --data` 传 **era 布局目录**，不是 raw replay base。
3. `--output` 会**覆盖**同名 stats json，必须带日期。

---

## 阻塞项 2 — T1 / T5 / T8：Windows 最好成绩的完整配置

**[确定] T5：open-loop，不是 closed-loop。** 从未用过 `-target-depth`，benchmark 也从未开过 `txpool` namespace。
9,000,000 笔 = 3,000 senders × 3,000 tx，一次性预签后开灌。满负荷下出现 `pool full` 是预期现象，不是故障。

**[确定] T1：`-shard-senders`，你们描述的就是 canonical 策略。** 每个 sender 的完整 nonce chain 固定到一个 RPC，
不把同一条 chain 跨 RPC 广播。`-broadcast` 是对照变体，不是主路径。

**[确定] T8：最好成绩 22,487 TPS / 98.4% occupancy / 55-of-60 full blocks**，win1，v5.7.955，
2026-08-21 的 A' 轮。完整参数：

| 项 | 值 |
|---|---|
| 二进制 | `n42-v5.7.955.exe` |
| gasceil | 480,000,000 |
| block interval | 1000 ms |
| GOMAXPROCS/节点 | 5（32 线程宿主，7×5=35 故意略超） |
| pool | globalslots 300000 / globalqueue 100000 |
| senders × pertx | 3000 × 3000 |
| gasprice | 10 gwei 固定 |
| rpcbatch | 100 |
| conc | 32 |
| sharding | `-shard-senders` |
| sender-offset | 每轮全新，绝不复用 |
| decay | `-DecaySec 90` |
| 环境 | `N42_MAX_GOSSIP_MB=8`、`N42_STRESS_GASLIMIT=1`、`N42_TXINDEX_TAIL=1`、`N42_MDBX_MAPSIZE_GB=128` |
| 竞争 profiling | **关**（mutex/block 采样就在被测临界路径上） |

同轮 win2/win3 是 18,665 @89.1% 和 12,568 @53.2%——**不是退化，是 baseFee 轮内爬升**
（每满块 +12.5%）越过了 10 gwei 的固定灌注上限。所以只有 win1 可比。

`N42_MAX_GOSSIP_MB=8` 尤其关键：默认 1 MiB 把一个块封死在约 8.5k transfers ≈ 480M 档的 37%，
**无论池多深、CPU 多少**。不设它，测的是 wire cap 不是链。

---

## 阻塞项 3 — F1 / F2：txpool journal

**[确定] 你们发现的是 Windows 脚本的真 bug。** `bench-run.ps1` 删的是 `E:\qs-node$i\txpool.journal`
这个**文件**，而 journal 实际写在 MDBX 的 `TxPoolJournal` 表里。那行删除从来没起过作用。

Windows 上没暴露，是因为舰队连续跑了几个月、faucet 余额充裕，几百条恢复的 pending 淹没在噪声里。
你们的 fresh seed 余额紧张，才把它放大成"Linux 表现异常"。**`b8dd1f06` 的修复正确，保留。**

F1 的设计问题（journal 该存全部 pending 还是只存 local）我 **[未知]**，没读过 `journal.go` 的设计意图。
但从 benchmark 角度：**benchmark 必须从空池开始**，否则上一轮残留污染供给统计——这与 journal 本身怎么设计无关。
建议按你们列的第二项做：加显式的 ephemeral benchmark profile，而不是让 benchmark 脚本去清生产表。

---

## 阻塞项 4 — T3 / T4：批量 RPC 与同 sender 顺序

**[未知]**，这两条我没查代码，不凭印象答。

但给一个事实：Windows 最好成绩就是在**当前这个 contract**（`firstErr` + goroutine 并发 batch）下跑出来的，
说明它至少不是达到 22k TPS 的阻碍。你们观察到的统计失真是真的，只是 Windows 侧没被它卡住过。

T4 的隐患我认同：pool-full 时高 nonce 先到会造成 hole。Windows 上 `-conc 32` 配 3000 senders，
单 sender 并发碰撞概率低，可能只是没触发。**按 sender 串行、跨 sender 并发**这个方向对，
但请**先拿到有效 baseline 再改**，否则改动和修复混在一起，分不清哪个起作用。

---

## 阻塞项 5 — F3 / F4：faucet 与双 reward

**[确定] F4：两个 reward 是设计如此。** chainspec 的 `hotstuff.devBlockReward` 打给 `devFaucetAddress`，
另一个是 validator reward。faucet 净增 1 ETH/block 与 `rewardCount=2` 不矛盾。

**[确定] F3：Windows 的做法是"按余额选 pertx"，不是固定 3000。** 资金模型：
devBlockReward 约 86k ETH/天回血、10 gwei 协议价，**根据当前 faucet 余额决定 pertx**，
且 `skip-funding` 禁止跨轮复用。

你们算的 1,896.93 ETH，在 1 秒出块下约 32 分钟空块即可攒够。所以建议：
**先用小规模（如 3000 senders × 300 tx）拿到有效 baseline，再逐步放大**，
不要为了凑 3000×3000 去注资或手工改状态。

---

## 阻塞项 6 — R1 / R2 / R3

**[确定] R1：gap-fill 是设计如此。** runbook 的 "Known deviations" 段明确写着：
replay base 与 live chain **不是 byte-exact**，gap-fill 插入合成空块，
所以 target 块号、时间戳、baseFee 序列、BLOCKHASH 窗口全部平移。
验收比 **target canonical head + 每批 qmdbRoot 无错**，不比 source head。
2026-08-10 那次：708,139 source 块 → target head 13,951,553，多出 39,365 个 gap-fill 空块。

**[确定] R2：你们的 24 个 mismatch 远低于 Windows 基线。** 同一份 runbook 记录 2026-08-10：
`txFailed` 105,080 / 128.8M（0.082%，全是 nonce-too-low 级联）、`receiptMismatch` **36,853**，
而 v3 全量 replay 的基线本身就有 2,952 个非零。你们 293,530 块只有 24 个、且 `txFailed=0`，
**比 Windows 干净得多**。

但要纠正一点：runbook 把这归因于"合成负载下的 gap-fill 漂移"，**没有**给出
"哪些 EIP、哪些字段允许不一致"的精确定义。你们要的那条正式豁免规则**目前不存在**。
`acceptedCompatibilityMismatch` 这个建议方向对，但需要先有判定依据才能实现。

**[确定] R3：这里确实有 bug，但你们的修复会破坏 runbook 的验收门。**

`internal/replay/export.go:89` 当前是：

```go
h, _ := rawdb.ReadCanonicalHash(tx, e.stats.CurrentBlock)   // 从 TARGET 读
cp := CheckpointEntry{Number: e.stats.CurrentBlock, Hash: hash}
```

而 `e.stats.CurrentBlock = batchEnd`（`engine_v2.go:316`）是 **source 块号**。
所以 number 来自 source 编号体系、hash 来自 target 的同号块——**gap-fill 之后这两个不是同一个块**。

Windows 实测印证：`qs-replay-v4/checkpoint.json` number = **14288280**（= `replay_src_height`，source 终点），
而 target 真实 head 是 **14327645**（era-out `SEAL_DONE.json`）。hash 那半确实是错的。

**但是**：runbook Step 2 的 GATE 写的是 "checkpoint.json number == H"，H 是 Step 1 记录的 **source head**。
`65cda179` 把 number 也改成 target canonical head 之后，这条 GATE 永远不成立。

**建议改法**：checkpoint 同时记两个高度，而不是二选一：

```json
{ "sourceHead": 13497579, "number": 13536950, "hash": "0x9923b2..." }
```

`number`/`hash` 一致地指向 target（修掉错配），`sourceHead` 保留 fold 进度供 GATE 和 resume 用。
两边语义都对，且旧消费者读 `number` 不会再拿到自相矛盾的 number+hash 组合。

---

## 阻塞项 7 — A1：版本发布流程

**[确定]** 你们已在 `468cbe22` 改成"编译前恰好递增一次 + `version-check` 强制一致"，并写进 CLAUDE.md。
方向对，**保留**。`6537ed32` 保留。

我在本会话里绕开 make、直接 `go build`（避免 bump），那只适合临时构建，不适合发布。
标准流程按你们写的走：bump → 提交版本 → 从 clean commit 构建。

---

## 阻塞项 8 — W1 / W2 / W3：witness 数据来源、范围、二进制

**[确定] 这批 858 GiB 是我传给你们的，来源完全清楚：**

| 表 | 源路径 | 生成方式 |
|---|---|---|
| `bodyc` `headerc` `senders` | `D:\n42-eth1\chain\freezer` | `wk-ethexec.exe {body-compact,header-compact,sender-recovery} --ancient d:/geth/geth/chaindata/ancient/chain --datadir d:/n42-eth1` |
| `witness` `codes` | `D:\N42-eth1177\chain\freezer` | EVM replay 产物 |

源链 = geth ancient store `D:\geth\geth\chaindata\ancient\chain`，**frozen = 25,765,566**。
所有派生表 items 都是 25,765,566，与源头完全对齐（传输前逐表核对过）。
**没有 manifest 是事实**——Windows 侧也从未生成过，W1 要的那个 manifest 目前不存在。

**[确定] W2：两个数字不同是正常的，不需要验证 partial segment。**
`headerc`/`bodyc` 的 `.cidx` 是 **3146 段 × 8192 = 25,772,032 的容量上限**，不是覆盖高度。
`docs/ethel/weekly-update-runbook.md` 第 32 行原话：

> **headerc/bodyc cidx shows CAPACITY (segments × 8192), not actual coverage.**

真实可重放范围以 `witness`/`senders` 的 items 为准：**0 .. 25,765,565**。

**[确定] W3：`/data/blockchain/bin/witness-replay` 是我在你们机器上构建的，commit = `5ccc9bb9`。**
显示 `(devel)` 是因为我用 `git archive HEAD` 传的源码（无 `.git`）且 `-buildvcs=false`。
现在你们有完整 checkout，重新构建即可拿到 provenance。A2 的 sidecar 建议我赞成。

---

## 阻塞项 9 — W4 / W5：24M gas mismatch（我这边最有把握的一条）

**[确定] 原因几乎可以肯定是 codes 不全——我传数据时漏了一样东西。**

`docs/ethel/witness-input-pipeline.md` 第 53–65 行记录了**完全相同的现象**：

> 初测 N42-eth1177 witness 配**不全的 codes-freezer**(62MB)：block 1M 读到 nonce=172321（≠mainnet 17387）
> + `bytecode not in codes-freezer` → 误判"witness 不对齐"。
> **改用完整 MDBX Code 表（`--datadir D:/N42-eth1177`，codeHash 键、内容寻址、历史正确）**：
> block **1M/8M/16M/24M/25M 全部 `failed=0`、真实 gas/txs**。
> `--datadir D:/N42-eth1177 \   # 完整 MDBX Code(关键,勿只用 codes-freezer)`

机制：witness 是**按 tx 执行顺序的状态读取流**，地址/slot 靠重跑 EVM 隐式重建。
缺某个合约的 code → EVM 把它当 EOA → **读取流错位** → 后续 gas/nonce 全成垃圾。
这正好解释 "got 16,980,501 / want 17,009,241" 这种**接近但不等**的 gas 偏差。

我给你们的是 codes-freezer（5.6 GB / 2,399,937 地址项），**没有传 MDBX 的完整 Code 表**。
Windows 那份权威 Code 表在 `D:\N42-eth1177` 的 MDBX 里。

**建议的验证顺序**（先证伪再谈性能）：

1. 拿已知失败块 24,000,022 单块重放，看日志有没有 `bytecode not in codes-freezer`。
   有 → 坐实 codes 缺失，不必再查别的。
2. 若坐实，我从 Windows 侧导出完整 Code 表传过去（需先量它单独多大）。
3. 补齐后重跑 dense gate，期望 `failed=0`。

**W5：`24,980,000..24,990,000` 这个区间没有已知依据。** 有依据的是 runbook 记录的
**1M / 8M / 16M / 24M / 25M** 这几个采样点（完整 Code 表下 failed=0）。
建议 gate 直接用 24,000,000 起那段——你们已经知道它在 codes 不全时失败，
补齐后由失败转通过，才是有说服力的证据。

**你们"在解释并修复前不做硬件性能分析"的判断完全正确。**

---

## 阻塞项 10 — H1 / H2 / H5：Linux baseline

**[推断] H1：37 可以当起点，但它的来历很弱。**
`(nproc+6)/7` 是我写脚本时按 Windows「7×5=35 略超 32 线程」直接外推的，**没有在任何机器上验证过**。
32 线程宿主上 5/节点也是历史沿用值，不是调优结论。

建议把它当**待测变量**而非 baseline。拿到有效供给后，至少测 37（logical 外推）与 18（physical/7）两档。
我倾向 physical-based 更合理——MDBX 提交和 EVM 执行都吃内存带宽，SMT 兄弟线程互抢 L1/L2 的收益很可能为负。
但这是**推断，不是结论**。

**[未知] H2：绑核我没有依据。** Windows 那台从没做过 affinity 实验，你们 16 个 L3 domain 是全新变量。
**同意先不绑核**，拿到有效 baseline 后再单独做这个实验。

**[确定] H5：有效成绩门槛，这几条是硬的**（`docs/QS_TPS_BENCHMARK.md`）：

1. **只比同序号窗口**，win1 为准。
2. **occupancy < ~95% 说明供给管道是限制项**，那个 TPS 不算链的结果。
3. **baseFee 轮内爬升**（每满块 +12.5%）会让后续窗口撞上灌注的固定 gas price 上限。
4. **同一二进制两轮之间的离散度是显著性标尺**——单轮差异小于该离散度不算结果。
   本会话有实例：某改动第一次测出 20.4%，次日同条件复现只有 4.95%，最终判定为噪声。
5. **单次 profile / 单窗口 TPS 都不能作依据**，profile 落点必须绑定链状态。

你们要的 p50/p95 commit、pool depth、RSS、disk latency 这些字段，**Windows 侧没有形成正式验收表**，
目前只有上面 5 条。你们把它补成结构化 artifact 是改进，我支持。

---

## 其余问题简答

- **A3 [确定]**：`0x594aad…` 是当前源码的 `params.MainnetGenesisHash`，`0x138734…` 是 v5.6.823 那代的常量。
  我追旧主网时实测过：**远端 peer 的 genesis 就是 `0x138734…`**，当前二进制握手会被拒（`wrong fork digest version`）。
  所以 `0x138734…` **不是污染，是旧链的真实身份**，只是当前部署不该用它。
  `00cc2bcc` 的 identity test **保留**，但请把 `0x138734…` 作为"legacy mainnet_compat 身份"一并注释进去，
  否则将来又会有人以为它是错的。
- **F6 [确定]**：**统一改 31000**。`sysctl ip_local_reserved_ports` 是每次开机要设的外部状态，脚本无法保证；
  换端口是脚本内可控的。我原来 `scripts/qs/README.md` 写的是 sysctl 方案，按你们的实践改掉，文档只留一个方案。
- **F7 [推断]**：建议加**硬失败**：`--p2p.local-ip`/`host-ip` 非 loopback 时脚本直接 exit，而不是只写注释。
  两台机器同 BLS key 的双签风险值得一个硬门。
- **F8 [确定]**：`N42_TXINDEX_TAIL=1` **应进 chainspec/profile 默认**。不设它，22.8k tx 的块
  `mdbx_txn_commit` 要 1.9 s，开了 0.5 ms——这不是调优项，是可用性门槛，不该靠人记得设环境变量。
- **T2 [确定]**：保留 fail-fast（`c3d8059e`）。partial sender load 会让"供给不足"统计失真，
  而供给统计正是判断成绩有效性的核心依据。宁可中止重跑。
- **T6 [确定]**：三条都必须验：每笔 funding receipt 成功、所有 sender 余额到账、nonce 连续无洞。
  faucet nonce 前进**不能**作为依据，你们判断正确。
- **T7 [确定]**：r1–r4 全部作废。理由你们自己列全了（余额不足、journal 残留、batch 部分失败、nonce probe 降级），
  任何一条都足以让数字失去意义。
- **T9 [推断]**：你们列的 5 条疑点我认为**属实**。`measure-tps.sh` 是我这周新移植的，没经过实战检验；
  Windows 原版 `measure-tps.ps1` 靠 PowerShell 的强类型侥幸避开了其中几条。该由它自己负责修，不要另造工具。
  **你们回退临时修改、先问再改的做法是对的。**
- **W6 [部分未知]**：codes 覆盖率验收工具**目前不存在**。最直接的抽验是拿已知合约地址查 codes-freezer
  有没有、hash 对不对。若 W4 坐实是 codes 问题，这个工具就有必要建了。
- **W7 [确定]**：正确性通过才计性能，**是硬要求**。worker 列表和 `--gogc 300 --mem-limit-gb 96`
  同样是外推值，没验证过——与 H1 同样保留。
  历史上这条路径在 ≥16 worker 出过 GC 死亡螺旋（按序 emit 的聚合器等队头，其余 worker 把乱序结果堆进 pending，
  堆涨到 15 GB+，形成 GC 频率 × 分配率 × 堆大小的正反馈），已用环形 reorder + GC tuning + async writer 修过，
  **但只在 32 worker 验证过，128 是未知区**。
- **W8 [确定]**：**不要并跑**，你们的判断对。两个负载都是 CPU + I/O 满载，同机跑出来的任何数字都不可解释。
  没有必须并跑的业务场景。
- **O1**：`docs/` 里我没发现把 sender/nonce/faucet 用于 witness 的残留描述，但 `scripts/qs/README.md`
  的压测段和 witness 段挨得很近，建议加一句显式分隔。
- **O2 [确定]**：应该校验，**不能只信 pidfile**。我在 `qs-env.sh` 的 `qs_node_pid` 里做的是
  "pid 存活 + `/proc/<pid>/comm` 前缀匹配 n42"，在你们的 sandbox 视图下仍会误判。
  建议再加 **MDBX lock 检查 + RPC 端口占用检查**，三者任一命中就拒绝启动。

---

## 第 5 节保留 / 回退结论

我的判断：**全部保留**，只有一条要改：

| Commit | 结论 |
|---|---|
| `283071f6` `468cbe22` `6537ed32` | 保留（A1 已定案） |
| `65cda179` | **改**：按 R3 改成同时记 `sourceHead` + target `number`/`hash`，否则 runbook GATE 失效 |
| `4b9d18fd` | 保留，但共识路径我要单独看过再确认（见下） |
| `510180fb` | 保留。这正是我追旧主网时撞到的那堵墙（legacy peer 发 protobuf，新版只解 RLP） |
| `576f5dd7` `00cc2bcc` `91012699` `58d49032` `b8dd1f06` `c3d8059e` `58b312d9` | 保留 |

`4b9d18fd fix(hotstuff): retry state after failed canonical commit` 我要单独深审：
我 8-22 刚在同一区域改过（`ensureParentApplied`，处理"启动 revert 回滚了 lockedQC 的块"导致的全网停摆），
两处都落在 commit 失败 / 重试路径上，要确认不互相干扰。**这条先不动，我看完再答复。**

---

## 我这边的进展（供参考，不影响上面结论）

1. **已同步到你们的 `8f09d764`**，13 个 commit 全部拉下。
2. DATC 的 `stroot-export` / `stroot-merge` / `drop-table` 之前"找不到源码"是我搜索方法错了
   （`strings` 用了 `^...$` 锚定，而 Go 二进制的字符串常量无换行分隔；且只搜了 main 工作区）。
   源码一直在 `concurrent-datc-root` 分支上，已推送。
3. 该分支领先 main 62 个 commit、**落后 544 个**。我已在临时 worktree 完成整支合并：
   20 个冲突文件、75 个 hunk 全部解完，**全树 `go build ./...` 通过**，
   两边功能都保住（分支的 per-shard arena + main 的 `SetReadCache`）。尚未提交。

---

## 补充（2026-08-24 晚，两项已交付）

### W1 已解决：传输完整性有证明了

`/data/blockchain/witness/MANIFEST.txt`

488 个文件的 **大小 + 头尾 4 MiB md5** 在源侧和目标侧逐一比对：
**488/488 全部匹配，0 不符、0 缺失**。源侧多出的 4 个 `.bak-gap` / `.bak-holefix`
是历史备份，本来就不属于这个数据集。

manifest 里同时记了 W1 问的其余几项：源路径、生成命令、上游 geth ancient frozen 高度、
真实可重放范围，以及"`.cidx` 是容量不是覆盖"这条坑。

HANDOFF 第 5 条那句 "full 858 GiB transfer integrity is not cryptographically verified"
现在可以撤掉。（用头尾摘要而非全量校验：858 GiB 全量 checksum 不现实，
而截断、短写、位翻转恰恰最容易出现在文件首尾，头尾摘要能覆盖。）

### W4 已交付修复：完整 Code 表

`/data/blockchain/code-mdbx/mdbx.dat`（18 GB）

三方条目数一致，证明这份是完整的：

| 库 | 表 | 条目 |
|---|---|---|
| `D:\reth2k\db` | `Bytecodes` | 2,673,190 |
| `D:\N42-eth1177` | `Code` | 2,673,190 |
| `/data/blockchain/code-mdbx` | `Code` | **2,673,190** ← 已在 Linux 侧核对 |
| 对比：你们现有的 codes freezer | — | 2,399,937（**缺 273,253**） |

差异不只是数量。**codes freezer 是地址索引（一址一码），Code 表是 codeHash 索引（内容寻址）。**
同一地址在不同时期可以持有不同 code（CREATE2 重部署等），地址索引结构上就表达不了这段历史，
所以重放旧块必然拿到错误的 code —— 这正是 gas「接近但不等」的成因。

**用法（请注意第二条）：**

```bash
/data/blockchain/bin/witness-replay \
  --input-headers-bodies /data/blockchain/witness \
  --input-witness       /data/blockchain/witness \
  --senders             /data/blockchain/witness \
  --datadir             /data/blockchain/code-mdbx \
  --output <dir> --no-output \
  --start 24000000 --end 24000100 --workers 1
```

1. `--datadir` 直接指向含 `mdbx.dat` 的目录本身，不是 `<datadir>/chaindata`。
2. **不要传 `--codes-freezer`。** `cmd/witness-replay/main.go:146` 的逻辑是：
   显式 flag 优先，否则**自动检测 `<hbPath>/codes.cidx`** —— 而你们的 witness 目录里
   正好有一份，所以不显式规避的话，仍可能走那份不全的地址索引。
   跑之前建议确认启动日志里**没有** `Codes freezer auto-detected`。

**建议的验证顺序**：先单块跑 24,000,022（第一个已知失败块）。
它由失败转为 `failed=0`，就证明诊断正确、可以进 dense gate；
若仍失败，说明还有第二个原因，那时再查也不迟。

### 顺带修正 SESSION_HANDOFF 的四处

1. **"24 receipt mismatches 是 accepted EIP-rule behavior，operator 已确认"** ——
   那条正式豁免规则**目前不存在**。runbook 只把它归因为合成负载下的 gap-fill 漂移，
   没有定义涉及哪些 EIP、哪些字段允许不一致。写成 "confirmed" 会把一个没有依据的说法固化下来。
   你们 24 个 / 293,530 块，对比 Windows 2026-08-10 的 36,853 个，本来就低得多。
2. **"checkpoint repaired to target canonical head/hash"** —— 见 R3，会让 runbook 的
   `checkpoint.number == H` GATE 永远不成立。建议改成同时记 `sourceHead` 与 target `number`/`hash`。
3. **"equivalent initial Linux allocation is 37/node"** —— 这不是等价关系，是我写脚本时的
   未验证外推。应作待测变量。
4. **witness gate 用 `24,980,000–24,990,000`** —— 该区间无已知依据。建议直接用 24,000,000 起那段：
   你们已知它在 codes 不全时失败，补齐 Code 表后由失败转通过，才是有说服力的证据。

---

## `4b9d18fd` 深审结论：正确，保留

之前留的那条唯一未决 commit，现在有结论了。

**它修的问题是真的。** `db.Update` 的顺序是「先调 hook → 再 `tx.Commit()`」，
所以存在一个窗口：hook 跑完（`done=true`），随后的 MDBX commit 失败并回滚全部写入。
旧代码只看 `hook.done` 就推进 `lastPersistedView` —— 系统以为共识状态落盘了，实际全被回滚。
下次重启会从一个并不存在的持久化点恢复。

**修复方向与既有语义一致。** 同文件的 `persistStateCtx`（1204 行）一直就是对的：

```go
if err := s.db.Update(ctx, h.run); err != nil { log.Warn(...); return }
s.lastPersistedView = h.view
```

它检查了 `db.Update` 的返回值（含 commit 错误）。而 `handleOutput` 走的是**外部事务**
（`CommitToCanonicalWith`），拿不到那个返回值，于是漏掉了 commit 那一半。
`stateHookCommitted(h, cErr)` 补的正是这个不对称。

**与 `d081d146`（ensureParentApplied）无冲突。** 两者在同一个 `handleOutput` 里但不同 case
（`OutputBlockCommitted` vs `OutputViewChanged`），且 `ensureParentApplied` 不写 `lastPersistedView`。
唯一的间接联系是 `OutputViewChanged` 末尾的速率限制读它：

```go
if output.View-s.lastPersistedView >= s.persistInterval { s.persistState() }
```

修复后 `lastPersistedView` 更保守，这个条件更容易成立，`persistState` 会更积极地重试 ——
**这正是期望行为**：状态没落盘时就该更积极地补落盘。

**验证**：两个改动各自的测试 10/10 通过（我的 6 个 parent-applied + 你们的 4 个 state-hook）；
hotstuff 包全量 ok（4.96s）；race 检测 ok（2.82s）。
你们加的 `TestStateHookRequiresOuterCommit` 断言方向也对。

至此第 5 节的 13 个 commit 全部有结论：**12 个保留，只有 `65cda179` 要按 R3 修改。**

---

## W4 第二原因已定位：是我给错了 headers/bodies 源（不是 code）

你们的判断对：codes 不是唯一原因。而且 **gas 数字在补齐 Code 表前后完全相同**
（got 16980501 / want 17009241），这本身就说明 code 根本不是那个变量 ——
如果缺 code，补上后 gas 至少会变，哪怕仍不对。这一点我先前没抓住。

真正的原因在我这边。做了一组对照实验：**同一台机器、同一个块、同一份 witness、
同一个 witness-replay 二进制，只更换 headers/bodies 源**：

| | headers/bodies 源 | 结果 |
|---|---|---|
| **A** | `d:/geth/geth/chaindata/ancient/chain`（geth ancient） | `failed=0` **gas=17009241** txs=142 |
| **B** | `d:/n42-eth1/chain/freezer`（bodyc / headerc） | `failed=1`，报 `gas mismatch: got 16980501 want 17009241` |

**A 组的 17,009,241 正是你们看到的 `want` 值；B 组逐字复现了你们的失败。**

所以第二个原因不在 witness、不在 Code 表、不在 replay 引擎，**在 bodyc/headerc 这条转码链**。

### 我错在哪

Windows 上验证过 `failed=0` 的配置（1M/8M/16M/24M/25M 全绿）一直是：

```
bodies  = d:\geth\geth\chaindata\ancient\chain     <- 唯一验证过的
witness = D:/N42-eth1177/chain/freezer
codes   = --datadir D:/N42-eth1177
```

我为了省 220 GB 传输量（bodyc 629 GB vs geth bodies 841 GB），把源换成了 N42 columnar。
`internal/ethel/witness_replay_source.go:95` 检测到 `headerc.cidx` 就自动切列式读取器 ——
**能读，但读出来的 body 与 geth ancient 不等价**。这个替换从来没有人验证过，是我自作主张引入的，
而且我当时还把"自动识别、无需额外参数"当成了它可用的证据。

gas 只差 28,740（0.17%）而不是天差地别，也符合"body 里少数 tx 被解成了略微不同的东西"，
而不是"读不到数据"。

### 已在传的修复

先传覆盖 gate 区间的一段（不是全量），好让你们最快确认结论：

- `/data/blockchain/witness-geth/`：`bodies.0383..0395.cdat`（13 段 26 GB）+
  `headers.0006.cdat`（1.6 GB）+ 两个 `.cidx`
- 覆盖块 **23,900,000 – 24,300,000**，包含 24,000,022

用法：只把 `--input-headers-bodies` 换成新目录，其余不变。

```bash
/data/blockchain/bin/witness-replay \
  --input-headers-bodies /data/blockchain/witness-geth \
  --input-witness /data/blockchain/witness \
  --senders       /data/blockchain/witness \
  --datadir       /data/blockchain/code-mdbx \
  --output <dir> --no-output \
  --start 24000022 --end 24000023 --workers 1
```

启动日志应显示 `format=geth-ancient`（不是 `n42-columnar`）。
期望 `failed=0 gas=17009241 txs=142`。

转绿后我再传全量 geth `bodies`（841 GB）+ `headers`（12.8 GB），
你们 `/data` 剩 5.5 T 装得下。**在那之前 dense gate 只能在 23.9M–24.3M 区间内跑。**

### 顺带印证了 b8391c24

B 组日志里，即使显式传了 `--datadir`，仍然出现：

```
Codes freezer auto-detected dir="d:/n42-eth1/chain/freezer" items=2431720
```

自动挂上的又是不全的那份（2,431,720 条 vs 权威 2,673,190）。
你们那个「有效 MDBX 存在时禁止隐式选择 freezer」的修复方向完全正确。

### 对 W1 manifest 的更正

`MANIFEST.txt` 里我把 `bodyc/headerc/senders` 记为这套数据的一部分。**bodyc/headerc 这两张表
不应被 witness-replay 使用**（senders 没问题，它是独立的 ecrecover 缓存）。
拿到全量 geth 数据后我会重发一份修正的 manifest。

---

## W4 已修复：根因是 headerc 丢了 ParentHash（`c19944e4`）

**先撤回我上一节的结论。** 我说"是 bodyc 转码链有问题、要改用 geth ancient"——**那是错的**。
`bodyc` 完全无损。真正的问题在 `headerc`，而且是**代码 bug，不是数据问题**：
你们不需要换输入源，也不需要我传那 841 GB。

### 定位

逐字段比对，两步就收敛了：

**body**（geth ancient vs bodyc，块 24000022）——**完全相同**：
142 笔 tx、**16 个 withdrawals**、0 uncles，每笔 tx 哈希都相等。
（你们问 withdrawals 的方向是对的，正是这一问把我从猜测推到了逐字段比对。）

**header**——17 个字段里**只有一个**不同：

```
!! ParentHash   geth=3850ea3f...455eb9a   headerc=0000...0000
   GasUsed / Time / BaseFee / ExcessBlobGas / ParentBeaconRoot / Root /
   TxHash / ReceiptHash / WithdrawalsHash / Hash()  ... 全部相同
```

`Hash()` 也是对的 —— reader 从段 trailer 恢复权威哈希。所以"哈希对了"掩盖了字段缺失。

### 为什么零 ParentHash 会改 gas

`internal/blockhelp.go:226`：

```go
vm.StoreParentBlockHash(ibs, headerNumber.Uint64()-1, header.ParentHash)
```

**EIP-2935** 每块把 `header.ParentHash` 写进历史环形缓冲。写 0 进去 →
后续 BLOCKHASH 读到 0 → 合约走了不同分支 → gas 差 28,740（**0.17%**）。

差得这么小，正好落在"看起来像 witness 或 code 有问题"的区间，所以前两轮都猜偏了。
而 `n42CompactSource.header()` 的注释白纸黑字写着
"for witness-replay only Hash() matters (BLOCKHASH)" —— **这个断言本身就是 bug**：
BLOCKHASH 窗口确实只用 `Hash()`，但 EIP-2935 用的是 `ParentHash`。

### 修复

父块的权威哈希就在相邻段 trailer 里，一次 O(1) 读就能填回去。

| | 修复前 | 修复后 |
|---|---|---|
| 块 24000022 | `gas mismatch: got 16980501 want 17009241` | **`failed=0 gas=17009241`** |
| 24000000–24002000（316,227 笔 tx） | 该区间原有数百次失败 | **`failed=0`** |

`internal/ethel` 全量测试通过。

### 你们该做的

```bash
cd /home/n42/src/n42/N42-gov5 && git pull          # 取 c19944e4
# 重编 witness-replay,然后用原来的 bodyc 输入直接跑 —— 不需要换数据源
/data/blockchain/bin/witness-replay \
  --input-headers-bodies /data/blockchain/witness \
  --input-witness /data/blockchain/witness \
  --senders /data/blockchain/witness \
  --datadir /data/blockchain/code-mdbx \
  --output <dir> --no-output \
  --start 24000000 --end 24200000 --workers 1
```

期望 `failed=0`。这应该**同时消除**你们那 623 次失败 —— 它们大概率全是同一个原因。

### 我先前几个错误结论的更正

1. ~~"codes 不全是原因"~~ —— 不是。你们指出"gas 数字在补 Code 表前后完全相同"，
   那本身就否证了 code 假说（缺 code 的话补上后 gas 至少会变）。这个信号在我手上我没抓住。
   **不过完整 Code 表仍应保留使用**：`b8391c24` 禁止隐式选 freezer 是对的，
   而且权威表 2,673,190 条 vs freezer 2,399,937 条的差距是真的，只是不是这次的病因。
2. ~~"bodyc 转码链有问题"~~ —— 不是。body 逐字段相同。
3. ~~"要改用 geth ancient、我传 841 GB 全量"~~ —— 不需要。**该修的是代码不是数据。**
   我已传的 26 GB 切片（`/data/blockchain/witness-geth`）现在只剩对照价值，
   验证通过后可以删。
4. `MANIFEST.txt` 里我把 `bodyc/headerc` 标注为"不应被 witness-replay 使用" ——
   **这条也撤回**，它们是正确且更省的输入（`bodyc` 比 geth bodies 小 25%）。

### 顺带：各客户端 body 存储效率（同口径，0–25,765,565）

| | body / transactions | headers | receipts |
|---|---|---|---|
| reth static_files | 972.3 GB | 14.7 GB | 478.9 GB |
| geth ancient | 841.1 GB | 12.8 GB | 362.1 GB |
| **n42 eth-el 列式** | **629.3 GB** | **4.8 GB** | **179.7 GB** |

n42 比 geth 省 25% / 62% / 50%，比 reth 省 35% / 67% / 62%。
三表合计：geth 1216 GB、reth 1466 GB、**n42 814 GB**。
收益来自列式分离 + 逐列 zstd（同类字段相邻，压缩器能看到列内相关性）。
erigon 无法参与对比 —— 本机没有完整归档节点。

现在这个 74.8% 是**无损**的了。

### Linux 回报：正式修复与原 columnar 输入均已验证（2026-08-24 07:44–07:49 UTC）

Linux 已同步包含 `c19944e4` 的 `origin/main`（merge head `3e66c1f5`），从该源码重新编译并
安装 `/data/blockchain/bin/witness-replay`：

```text
sha256 951365cf799b90cd2eca615eea168fd31e091d5fbbf694f0c5b006769399f488
```

输入已切回原 `/data/blockchain/witness`，启动日志明确显示 `format=n42-columnar`。
正式二进制单块复核结果：

```text
Replay complete blocks=1 failed=0 gas=17009241 head=24000022 txs=142
```

日志：`/data/blockchain/wr-logs/smoke-c19944e4-final.log`。

同步正式提交前，Linux 先用同语义最小补丁跑了完整的 2,000 块密集 gate；正式提交比该
补丁只多了“仅在 ParentHash 为零时恢复”的保护：

```text
range=24000000-24002000 workers=1 format=n42-columnar
Replay complete blocks=2000 failed=0 gas=61388491159 txs=316227 elapsed=1m46s
```

日志：`/data/blockchain/wr-logs/w4-bodyc-parenthash-2k-20260824.log`。这与 Windows 修复后
数据逐项一致，确认该区间原有的数百次 mismatch 已消失。

Ubuntu 脚本保留 `d7f40cc6` 引入的显式 source 分离，但修正默认目标：

- `HB=/data/blockchain/witness`：必须存在 `headerc.cidx` / `bodyc.cidx`；
- `D=/data/blockchain/witness`：继续提供 witness/senders；
- `HB` 只有在人工 A/B 时才显式覆盖为 `/data/blockchain/witness-geth`。

因此生产 gate 不再依赖 geth ancient。已传的 26 GB 切片保留作对照证据，未删除；不再传
841 GB 全量 geth bodies。

---

## 关于 `d7f40cc6 fix(witness): use canonical geth ancient inputs`

这个 commit 是你们按我**上一轮错误结论**做的（"改用 geth ancient 输入"）。
现在根因查明是 `headerc` 少填 ParentHash，已在 `c19944e4` 修掉，
所以**换输入源这件事不必要了**。

已推送：`3e66c1f5`（含 `c19944e4`）。

建议：

1. `git pull`，重编 `witness-replay`。
2. **先用原来的 bodyc 输入验证**（`--input-headers-bodies /data/blockchain/witness`），
   期望 `failed=0`。这一步能独立确认 `c19944e4` 是否真的解决了你们那 623 次失败。
3. 确认后再决定 `d7f40cc6` 怎么处理。我的看法：
   - 脚本里"必须用 geth ancient"这条约束**应该撤掉** ——
     `bodyc` 比 geth bodies 小 25%，是更好的输入，而且现在是无损的。
   - 但它顺带加的**显式性**（不再依赖自动检测、把输入源写死在脚本里）**值得保留**，
     只是目标应该指回 `bodyc`。
   - 换句话说：保留"显式指定输入"的形式，改掉"指定成 geth ancient"的内容。

这条要由你们判断，因为脚本是你们在维护，而且我在这件事上已经错过一次方向了。

## 我在这次排查里的三次错误

写下来是因为它们有共同的模式：

1. **"codes 不全是原因"** —— 你们指出"补 Code 表前后 gas 完全相同"，
   那本身就否证了假说。我手里有这个信号却没用它。
2. **"bodyc 转码链有问题"** —— A/B 实验只证明了"换源就好了",
   我却直接跳到"bodyc 坏了"。实际 body 逐字段完全相同，坏的是 header 的一个字段。
3. **"要传 841 GB geth 全量"** —— 基于第 2 条的错误推论，
   差点让你们白等两小时传输、白占 841 GB 磁盘。

共同点：**每次都在"观察到差异"和"定位到原因"之间跳了一步**。
A/B 只能告诉你变量在哪一侧，不能告诉你是哪个字段 —— 中间必须有逐字段比对。
你问的那句"bodyC 是漏了提款吗"正是把方向拉回来的一问：
它逼出了 `bodydiff`/`hdrdiff`，17 个字段里一眼就看到唯一那个零值。
