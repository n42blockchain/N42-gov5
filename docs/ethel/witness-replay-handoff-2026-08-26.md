# witness-replay 全量极致执行交接（2026-08-26）

本文是 2026-08-25/26 两天工作的交接入口。细节与每一次运行的证据在
[`witness-bodyc-vs-freezer-and-full-run-2026-08-25.md`](./witness-bodyc-vs-freezer-and-full-run-2026-08-25.md)
（§5 逐运行记录）和
[`evm-block-execution-optimization-map-2026-08-26.md`](./evm-block-execution-optimization-map-2026-08-26.md)
（逐行 profile 的优化清单）。正确性规则不变，见
[`witness-replay-benchmark-runbook.md`](./witness-replay-benchmark-runbook.md)。

## 1. 结果

全量 = `0..25,765,566`，25,765,566 blocks / 3,678,100,106 txs，默认验证开启
（GasUsed + ReceiptHash），无 `--skip-verify`、无 `--continue-on-error`，全部 `failed=0`。

| | wall | CPU-sec | 说明 |
|---|---:|---:|---|
| 起点（08-24，bodyc，104w，1 reader） | 2h27m10s | 456,881 | |
| **生产配置（main 合并后，128w/6r/gc300）** | **48m39s** | **371,444** | tier-1 3/3、tier-2、T1、t3、rebased2 各 1/1 全部通过 |
| 墙钟最快（T1，256w/6r/gc300） | **45m56s** | 482,741 | 256w 累计 9 次 1 败（间歇，未解释） |

相对起点：**墙钟 −67%，CPU-sec −19%**，同时达成。硬件计数器（§5.30）显示解释器 IPC 2.7、
访存停顿可忽略——剩余空间在指令数，不在并发。

## 2. 生产配置与入口

```bash
# 默认即生产配置：freezer 源、128 workers、6 readers、GOGC 300、56 GiB
scripts/witness-full.sh
# 或显式
BIN=build/bin/witness-replay RUN_ID=my-run scripts/witness-full.sh
```

- body/header 源：`/data/blockchain/witness-geth`（geth ancient，完整 0..25,765,566）
- witness / senders：`/data/blockchain/witness`；code：`/data/blockchain/code-mdbx`
- 脚本记录 binary SHA256、commit、dirty 状态，按 5 秒采样进程 RSS，结束打印验收项
- 验收：exit 0、`Replay complete ... failed=0`、blocks=25765566、无
  `Replay failure diagnostic` 行

**不要**用 bodyc（`/data/blockchain/witness`）做全量的 body 源：见 §4.1。

## 3. 代码：分支与 binary

**2026-08-27 已合并进 main**（`72150b2e`，经 `perf/witness-replay-rebased-2026-08-27` rebase；
原分支 `perf/witness-replay-bodyc-full-2026-08-25` 保留作历史）。同日先合入了
`codex/gov5-sync-audit-fixes-20260727`（`50923a19`）。

| commit | 内容 | 效果 |
|---|---|---|
| `23c7d9ec` | 前序会话的工作树（process shards、reservoir、并行 feed、lock-free code cache 等） | 让交接的 binary 可复现 |
| `5deddba0` | 解释器派发表扁平化（一 opcode 一 cache line） | −2.8% CPU |
| `ae2d655e` | bloom Keccak 记忆化 + 去每 topic 分配 | 合计 −4.8% wall（密集窗） |
| `10437af9` | `--body-decoders` flag | 解耦 worker 数与 bodyc 解码并发 |
| `414cfdb7` | DynamicFeeTx / AccessListTx 非反射 hash | 1284→710 ns，7→1 alloc |
| `f95557ef` | balanceInc 空表短路；池化大 map 置 nil | −1.6% CPU |
| `3a8cde6a` | 预编译静态 warm（65536-bit 位图） | 每 tx 少 10–17 次 map 写 + journal |
| `654bf762` | SSTORE gas scratch 放 frame | 每 SSTORE 少 3 次分配（原对象数第一） |
| `b99913ba` | Memory/Stack/Scope/pc 按调用深度复用 | 每 `Run` 0 alloc |
| `d703cb60` | PUSH2..8 折进 uint64 | |
| `84402bae` | 失败时重放诊断 `diagnoseReplayFailure` | 定向 256w 间歇失败 |
| `d41b0a59` | journal 按值存储（去装箱） | 256w 忙碌线程 173→192，wall −7.8% |
| `2003b43b` | 单表 storage + epoch（T1） | 输出逐字节一致；全量 −1.0% wall / −1.5% CPU |
| `00966c5d` | LOG arena（opt-in）+ 余额栈临时值（t3） | 全量 −0.5% wall / −0.7% CPU |

以上 tier-1（`414cfdb7`..`d703cb60`）**要求 GOGC 300**：它把活堆压到 1/3，GOGC 100
下 GC 频率翻倍、mark assist 打在 reader 上，墙钟反而 +15%（§4.3）。

保留的 binary（`/data/blockchain/bin/keep/`）：

| 名称 | sha256 前 8 | 内容 |
|---|---|---|
| `witness-replay.decoderflag` | `3449c2f4` | 前序 + 派发表 + bloom + flag（"base"） |
| `witness-replay.tier1` | `44d60635` | + tier-1 七项 |
| `witness-replay.tier1-diag` | `253fa67b` | + 诊断 |
| `witness-replay.tier2` | `2df12dce` | + journal 去装箱（`d41b0a59`） |
| `witness-replay.t1` | `e92f3b33` | + 单表 storage（`2003b43b`） |
| `witness-replay.t3` | `f1df386b` | + LOG arena + 余额临时值（`00966c5d`，**生产**） |

## 4. 学到的、被证伪的

### 4.1 bodyc 整段物化会让长跑塌陷（三次跨会话、跨 binary 复现）
bodyc reader 钉住整个已解码的 8192 块 segment；从创世跑到约 18M 后密集 band 塌到
165–630 blk/s（正常 3–5k）。10k/100k/1M 窗口**测不出来**。同 binary 同参数只换成
freezer 源，塌陷消失。freezer 是生产源；bodyc 回到归档格式，除非做惰性解码。

### 4.2 度量
- **CPU-seconds 在 SMT 下有偏**：两个兄弟线程各记满 1 秒。独占机器上看墙钟或
  core-seconds（wall × 128）；CPU-sec 只对按 vCPU 计费有意义。
- 每个运行都要用 `sadf -d -- -u /var/log/sysstat/saNN` 核对整机忙碌线程 ≈ 本进程 CPU%。
  另一个会话的并发运行污染过一次结果（§5.11），靠这个查出来的。
- 短窗口三次误导长跑结论；全量是唯一 gate。

### 4.3 GOGC 必须跟着活堆走
任何缩小活堆的优化都要重扫 GOGC 再看墙钟。消融表见 §5.15。

### 4.4 256 线程的空转是 Go 分配器的 `mcentral` 锁
不是 reader（12 readers 墙钟秒级一致）、不是 aggregator（NoOutput 不重排）。
mutex profile：30 小时锁等待，2/3 在 `mallocgc → mcentral.cacheSpan`，主要是
EVM 调用里的 map 分配/扩容。减少每块分配次数是剩下的主杠杆——tier-2 验证了这一点。

### 4.5 被证伪的假设（别再走）
GOMAXPROCS 超订；max-head 包络"塌陷"；reader 数导致塌陷；内存上限；
"128 是拐点"（度量问题）；Keccak 换汇编（更慢 20–25%）。

## 5. 未解决

1. **256w 间歇 nonce 失败**：block 12,854,611 tx 28，state nonce 比 tx 少 1；
   同配置 6 过 1 败，10k/860k 窗口 ×18、0→13M ×4、`-race` 60k 均未复现；
   `-race` 只报 freezer open 里一个良性 metrics gauge。诊断已挂在所有后续 binary 上，
   触发一次即可定向（retry 通过 ⇒ 进程内共享状态；retry 同败 ⇒ 输入错）。
   在此之前生产配置用 128w（3/3 通过）。
2. **`perf stat` 未做**：`kernel.perf_event_paranoid=4`，需要
   `sudo sysctl -w kernel.perf_event_paranoid=1`。
3. T1 已完成并通过输出逐字节护栏与全量 gate；RSS +3–4 GiB 可通过按需存 `origin` 收回。
4. `origin/main` 分叉（§3）。

## 6. 运行队列与脚本

所有链式脚本在本会话的 scratchpad（临时目录，机器重启即失），模式是：
`while pgrep -f 'full[-]run[.]sh' ...; do sleep 30; done` 再 `exec full-run.sh`。
两个坑，都踩过：`pgrep -f` 会匹配到启动它的 shell 自己的 argv（用 `[-]` 括号写法，
且启动命令的 argv 里不要出现脚本名——把逻辑放进文件再 `bash file`）；
内核把 `comm` 截到 15 字符，`pgrep -x` 要用截断后的名字。

证据目录：`/data/blockchain/wr-logs/`（每次运行 `.log/.time/.meta/.rss`），
profile 在 `/data/blockchain/wr-pprof/`（`dense-4shard-20260825/`、
`wait-256w-tier1-gc300-20260826/`）。
