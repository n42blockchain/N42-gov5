# bodyc 与 geth freezer 对比、极限优化与全量执行（2026-08-25）

本文承接
[`witness-replay-optimization-handoff-2026-08-25.md`](./witness-replay-optimization-handoff-2026-08-25.md)
（0–1000 万块深度路径，`3m37.46s`），回答三个问题：

1. witness replay 读 `bodyc`（N42 列式）与读 `bodies`（geth ancient freezer）
   在同一区间上到底差多少；
2. 在此基础上还能做哪些经过 profile 证明的源码优化；
3. 全量 `0..25,765,565`（25,765,566 blocks）应该用什么参数、怎么验收。

正确性规则不在本文重复，见
[`witness-replay-benchmark-runbook.md`](./witness-replay-benchmark-runbook.md)。
本文所有性能行都在默认验证开启（GasUsed + ReceiptHash）、
不带 `--skip-verify`、不带 `--continue-on-error` 的前提下取得，且都 `failed=0`。

---

## 1. 环境

- 仓库 `/home/n42/src/n42/N42-gov5`，HEAD `179691e5`，工作树 **dirty**
  （深度路径、reservoir、本次两项优化都尚未提交；diff 存于
  `/data/blockchain/wr-logs/full-bodyc-20260825.diff`）
- Go `go1.26.0 linux/amd64`
- 主机 AMD EPYC 9B45，128 physical / 256 SMT，约 136 GiB RAM，`/data` 为 XFS on NVMe
- 输入 `/data/blockchain/witness`（`headerc/bodyc/witness/senders/codes`），
  bytecode 用 `/data/blockchain/code-mdbx` 的完整 Code 表
- 对比用 geth ancient 输入 `/data/blockchain/witness-geth`
- binary SHA256
  - 优化前基线 `50fc8dd287bfb0c8a77022dc0acc88d4dfcbb918d2785fc905694b8b66e2b453`
  - 本次优化后 `1a997d3badaed750d3c0ff6c1a3c4f5af0a17a1eb1ff32c9fd87865a58aedf4d`

---

## 2. bodyc 与 freezer 的对比

### 2.1 两种格式实际是什么

`internal/ethel/witness_replay_source.go` 在打开输入目录时探测 `headerc.cidx`：
存在走 `n42CompactSource`，否则走 `gethFreezerSource`。

| | bodyc（N42 列式） | bodies（geth ancient） |
|---|---|---|
| 单位 | 8192 块一个 zstd segment | 64 块一个 zstd batch |
| 布局 | 每个字段一列（txType/R/S/V bitpack/To 字典/nonce varint/…） | 每块一份完整 RLP |
| 读一个块 | 解压整段 → 解码全段 8192 块 → 取一块 | 解压一个 64 块 batch（缓存一份）→ RLP 解一块 |
| ParentHash | 不存，读侧用上一块 trailer 补 | RLP 自带 |
| 索引 | `bodyc.cidx`，每 segment 8 字节 | `bodies.cidx`，每块 6 字节 |

### 2.2 磁盘占用（同一区间实测）

区间 `23,896,284..24,310,354`（geth 侧本机仅有 `bodies.0383..0395`）：

| 源 | 字节 | 每块 |
|---|---:|---:|
| bodyc（segments 2917..2967） | 14.94 GiB | **37.5 KiB** |
| geth bodies（0383..0395） | 24.21 GiB | 61.3 KiB |

bodyc 小 **38.8%**。按整档折算：bodyc 全量 bodies 为 598 GiB，
同样内容的 geth 格式约 977 GiB。本机 `/data` 只有 5.4 TiB 可用且要同时放
witness(178 GiB)/senders(41 GiB)/codes(5.6 GiB)，**geth 格式全量在本机不成立**——
本机 geth 目录只有 13 个 body segment（24.2 GiB），无法跑全量。

### 2.3 执行 A/B（同区间、同参数、热页缓存）

区间 `23,900,000..24,000,000`：100,000 blocks / 21,812,059 txs。
两侧 witness、senders、code 输入完全相同，只换 `--input-headers-bodies`。
参数 `--workers 64 --readers 4 --gogc 300 --mem-limit-gb 32 --no-output`。
每侧跑两遍取第二遍（`File system inputs = 0`，两侧都已热）。

| 指标 | bodyc | geth freezer | 差 |
|---|---:|---:|---|
| blk/s | 1309 | 1346 | geth +2.8% |
| wall | 1m17.90s | 1m15.09s | geth −3.6% |
| user CPU | 4878.94 s | 4843.71 s | geth −0.7% |
| CPU 占用 | 6291% | 6469% | |
| **Maximum RSS** | **32.77 GiB** | **15.73 GiB** | **geth −52%** |
| voluntary ctx switches | 1,148,490 | 1,167,930 | |
| gas 合计 | 3,054,260,276,886 | 3,054,260,276,886 | 一致 |
| failed | 0 | 0 | |

两侧 gas 与 tx 数逐位一致，说明两条读路径语义等价。

### 2.4 结论：差异的根因是解码粒度，不是压缩率

user CPU 只差 0.7%，说明列式解码并不比 RLP 解码贵；差的是**驻留内存**。
bodyc 一次把整段 8192 块全部物化成对象（`decodeSegment` → `decodeBodySegment`），
4 个 reader 各自持有当前段并预读下一段，于是同时活着的解码对象是
「8192 块 × 4 × 2」量级；geth freezer 每张表只缓存一个 64 块 batch，
解码工作集小两个数量级。

这条结论决定了全量参数怎么选：**bodyc 的瓶颈是内存，不是 CPU 或磁盘带宽**。
4 分片 254 worker 实测四个子进程 RSS 峰值合计 **82.3 GiB**（见 §4），
在 136 GiB 主机上可行但余量不大，所以不能再往上加 worker。

反过来，bodyc 换来的是 38.8% 的磁盘节省和按 segment 天然分片的并行读能力
（`feedBlocksParallel` 按 8192 块边界分配 range，每段只解一次）。
**全量必须用 bodyc**：geth 格式在本机既没有全量数据，也放不下。

---

## 3. 本次两项优化（profile 驱动）

profile 采集：`24,000,000..24,200,000`（200k 块，密集区），4 个显式
`--segment-shard-index` 子进程各写独立 CPU/heap profile，
落在 `/data/blockchain/wr-pprof/dense-4shard-20260825/`。
合并四份后总样本 15,338 CPU 秒。

密集区热点与 0–1000 万区间不同，前几名是：

| 节点 | flat | cum |
|---|---:|---:|
| `EVMInterpreter.Run` | **16.87%** | 82.97% |
| `sha3.keccakF1600` | 8.86% | 8.88% |
| `runtime.cgocall`（几乎全是 ecrecover 预编译） | 2.98% | 2.99% |
| `bn256.gfpMul` | 2.76% | 2.76% |
| `runtime.mapaccess2` | 0.81% | 3.97% |
| `runtime.mallocgc` | 0.31% | 3.78% |
| `block.CreateBloom` | 0.09% | **3.12%** |

`Run` 的 16.87% flat 用 `-list` 拆到行以后，指向两条具体的行：

```text
485.03s  3.16%   operation := in.jt[op]
452.75s  2.95%   cost = operation.constantGas
250.50s  1.63%   if in.cfg.Debug {           (循环内第一处)
195.60s  1.28%   logged, pcCopy, gasCopy = ...
146.47s  0.95%   if in.cfg.Debug {           (循环内第二处)
```

### 3.1 扁平化指令派发表（`internal/vm/jump_meta.go`）

`JumpTable` 是 `[256]*operation`，所以每执行一个 opcode 要走两次依赖加载：
先从 2 KiB 指针表取指针，再从该 `operation` 自己的堆对象取字段。
每个 `operation` 是独立分配的小对象，第二次加载等于一次随机堆访问，
除最热的两三个 opcode 外基本必然 cache miss。

`opMeta` 把循环真正读的六个字段（`execute`/`dynamicGas`/`memorySize`/
`constantGas`/`numPop`/`maxStack`）复制进一条 64 字节 cache line，
并放进连续的 `[256]opMeta`，于是一个 opcode 只付一次索引加载，
之后所有字段都在同一条 line 上。

- 表按 `*JumpTable` 记忆化（`sync.Map`）。各分叉指令集是包级单例，
  `EnableEIP` 只作用在 `copyJumpTable` 的副本上，所以缓存不会读到被改过的表。
- 同时把 `in.cfg.Debug` 和 `in.meta` 提升为循环外局部变量。
  `cfg` 在内嵌的 `*VM` 后面，每次迭代读三次就是三次依赖加载。
- `Run` 顶部保留一次 `meta == nil` 兜底，让手写构造的 interpreter
  （`eof_execution_test.go`）继续可用。

正确性护栏 `internal/vm/jump_meta_test.go`：对 **19 个分叉指令集 × 256 个
opcode** 逐字段比对扁平表与原 `*operation`（函数指针按代码地址比），
另有分叉数量断言、记忆化断言、`unsafe.Sizeof(opMeta) == 64` 断言。

### 3.2 Bloom 的 Keccak 记忆化（`common/block/bloom9.go`）

`CreateBloom` 占 3.12% CPU，几乎全在 keccak 置换上。但一个密集主网块里
log address 和 topic0 事件签名是高度重复的（成千上万笔 ERC-20 Transfer
只对应少数几个代币合约和一个事件签名），每次重复都在重算同一个 Keccak。

bloom 位是输入字节的纯函数，所以记忆化是可证明安全的。难点是所有权：
254 个 worker 共用这段代码，进程级缓存要么在热路径上锁，要么在条目上竞态。
做法是把 memo 挂在**池化的 hasher 对象**上——`CreateBloom` 期间它被单个调用者
独占，`sync.Pool` 的 per-P 复用又让同一个 worker 通常拿回自己那份热 memo，
完全不需要把 cache 参数一路穿过 `ProcessBlock`。

顺带修掉一个既有的分配问题：原来的 `for _, b := range log.Topics { …(b[:])… }`
把 topic 复制到局部变量再切片，Go 的逃逸分析是按变量而非按分支的，
所以**每个 topic 都堆分配一次**。改成对调用方已在堆上的 `Log` 字段取地址
（`&log.Address` / `&log.Topics[i]`）后，命中和未命中路径都不分配。

微基准（200 receipts，热点地址/主题重复，与主网形态接近）：

| | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| 记忆化前（`referenceBloom`） | 356,420 | 26,740 | 834 |
| 记忆化后（`CreateBloom`） | **23,113** | **2** | **0** |

这是 memo 全热的上界，真实收益低得多，以整档 A/B 为准。

正确性护栏 `common/block/bloom9_memo_test.go`：与未记忆化实现在 200 组随机
receipt 上逐位比对；专门覆盖「20 字节地址与前 20 字节相同的 32 字节 topic」
这一种「用同一张按原始字节索引的表就会串味」的陷阱；覆盖 memo 超限清空路径；
`LogsBloom` 与 `CreateBloom` 交叉比对；64 goroutine 并发 `-race` 通过。

### 3.3 A/B 结果

密集窗口 `24,000,000..24,100,000`，深度路径参数
（4 shards / 254 workers / 4 readers / reservoir 20-10 / GOGC 400 / 80 GiB），
两个 binary 交替跑三轮，避免机器状态漂移偏袒某一侧。

| binary | wall（三次） | 均值 | user CPU 均值 | failed |
|---|---|---:|---:|---:|
| 基线 | 36.02 / 36.07 / 36.11 | 36.067 s | 8065.2 s | 0 |
| 仅 3.1 派发表 | 35.25 / 35.21 / 35.11 | 35.190 s | 7827.3 s | 0 |
| 3.1 + 3.2 | 34.29 / 34.42 / 34.33 | **34.347 s** | **7653.5 s** | 0 |

- 派发表：wall −2.8%，user CPU −2.8%
- 加上 bloom：wall 合计 **−4.77%**，user CPU 合计 **−5.10%**
- 三轮里每一次优化后的运行都快于每一次基线运行，没有交叠

（派发表那一轮的基线均值是 36.20s，与本表基线均值 36.07s 的差别是机器温度/
静默度漂移，所以两项改动的归因用「合计 − 单项」而不是跨表相减。）

### 3.4 试过但否掉的方向

- **换 Keccak 实现**：profile 里 `sha3.keccakF1600` 占 8.86% flat。
  `golang.org/x/crypto` v0.54.0 的 legacy Keccak 是纯 Go；v0.43.0 和 Go 1.26
  的 `crypto/internal/fips140/sha3` 里有同一份 5419 行 amd64 汇编。
  实测在本机上**汇编反而更慢**：

  | 输入 | v0.43.0（汇编） | v0.54.0（纯 Go） |
  |---|---:|---:|
  | 32 B | 334.9 ns | **264.3 ns** |
  | 64 B | 333.6 ns | **265.4 ns** |
  | 136 B | 620.9 ns | **504.8 ns** |
  | 1024 B | 2480 ns | **1987 ns** |

  当前依赖已经是更快的一侧，此路不通。
- **加大 worker / reader / reservoir**：见 §4，254 worker 已经用掉
  218 个逻辑核和 82.3 GiB，继续加只会加剧内存与调度压力。

---

## 4. 全量参数是怎么定的

密集窗口 `24,000,000..24,100,000`（100k 块）上做深度路径 sizing sweep，
全部 `failed=0`：

| workers | mem-limit | GOGC | reservoir | wall | blk/s | CPU | 最大单子进程 RSS |
|---:|---:|---:|---|---:|---:|---:|---:|
| 128 | 64 GiB | 300 | 16/8 | 45.15 s | 2215 | 11910% | 16.3 GiB |
| 192 | 80 GiB | 400 | 20/10 | 39.49 s | 2533 | 17546% | 20.2 GiB |
| 192 | 96 GiB | 200 | 16/8 | 39.50 s | 2533 | 17556% | 24.1 GiB |
| 254 | 112 GiB | 300 | 24/12 | 37.94 s | 2637 | 21322% | 26.6 GiB |
| **254** | **80 GiB** | **400** | **20/10** | **36.93–37.27 s** | **2684–2708** | **21822–22008%** | **20.2–20.4 GiB** |

**同一组参数在稀疏早期链和密集晚期链上同时最优**，所以全量不需要分段换参数。

`/usr/bin/time` 的 `Maximum resident set size` 在多子进程下只报「最大的那个子
进程」，会把 4 进程运行低估约 4 倍。用 2 秒采样求四个子进程 RSS 之和，
密集区峰值是 **82.3 GiB**（`PEAK_SUM_RSS_KB=86311672`）。
136 GiB 主机上可行，但这就是不能再加 worker 的原因。

优化后 binary 的 `0..10,000,000` 复跑（同参数，作为全量前的正确性与性能门）：

| | 优化前 | 优化后 |
|---|---:|---:|
| wall | 3m37.46s | **3m32.35s**（−2.35%） |
| user CPU | 45,534.57 s | **43,624.13 s**（−4.20%） |
| 聚合 CPU | 21061% | 20657% |
| 最大单子进程 RSS | 19.8 GiB | 19.6 GiB |
| blocks / txs | 10,000,000 / 697,373,070 | 10,000,000 / **697,373,070** |
| failed | 0 | 0 |

tx 总数与交接文档记录的 697,373,070 逐位一致。

---

## 5. 全量运行

入口：`scripts/witness-full.sh（原 witness-full-bodyc.sh，已改为默认 freezer 源）`。它固定了上面这组参数、
强制保留验证、记录 binary SHA256 与 commit、按 5 秒采样四个子进程 RSS 之和，
结束后自动打印验收项。

```bash
cd /home/n42/src/n42/N42-gov5
RUN_ID=full-bodyc-20260825 scripts/witness-full.sh（原 witness-full-bodyc.sh，已改为默认 freezer 源）
```

等价的直接命令：

```bash
ulimit -n 65536
/usr/bin/time -v build/bin/witness-replay \
  --input-headers-bodies /data/blockchain/witness \
  --input-witness /data/blockchain/witness \
  --senders /data/blockchain/witness \
  --datadir /data/blockchain/code-mdbx \
  --output /data/blockchain/wr-out/full-bodyc-20260825 \
  --no-output \
  --start 0 --end 25765566 \
  --process-shards 4 --workers 254 --readers 4 \
  --input-high-gb 20 --input-low-gb 10 \
  --gogc 400 --mem-limit-gb 80
```

### 5.1 验收条件（全部必须满足）

1. 进程退出码 0；
2. 日志出现 **4 条** `Input reservoir enabled high_gb=5 low_gb=2.5`
   （没有这 4 条就说明没跑在深度路径上）；
3. **4 条** `Replay complete ... failed=0`；
4. 父进程 `Process-sharded verification complete blocks=25765566 shards=4`；
5. 四个 shard 的 blocks 之和 = 25,765,566；
6. 日志中**不出现** `restored legacy EIP-7702 authorization V`
   ——出现说明还在读旧格式 bodyc segment；
7. 没有 `--skip-verify`、没有 `--continue-on-error`。

### 5.2 结果

### 5.2 结果（2026-08-25 全量执行）

```text
run_id            full-bodyc-20260825
binary sha256     1a997d3badaed750d3c0ff6c1a3c4f5af0a17a1eb1ff32c9fd87865a58aedf4d
range             0..25765566
wall              2h14m10.24s
user CPU          1,586,243.54 s
system CPU        34,381.89 s
aggregate CPU     20131%  = 201.31 logical cores
peak summed RSS   93,694,420 KiB = 89.35 GiB (四子进程之和，5 秒采样)
major faults      22,219,831
filesystem input  1,885,607,928 blocks ≈ 899 GiB
swaps             0
exit code         0
legacy authV 修复 0 次
```

四个 shard：

| shard | blocks | txs | gas | blk/s | elapsed | failed |
|---|---:|---:|---:|---:|---:|---:|
| 0 | 6,440,638 | 922,590,073 | 78.03 T | 1054 | 1h41m48s | 0 |
| 1 | 6,438,912 | 918,513,546 | 78.15 T | 910 | 1h57m56s | 0 |
| 2 | 6,447,104 | 922,516,506 | 78.21 T | 900 | 1h59m21s | 0 |
| 3 | 6,438,912 | 914,479,981 | 78.07 T | 800 | 2h14m08s | 0 |
| 合计 | **25,765,566** | **3,678,100,106** | **312.45 T** | | **2h14m10s** | **0** |

§5.1 七条验收条件全部满足：退出码 0、4 条 reservoir、4 条 `failed=0`、
父进程 `blocks=25765566 shards=4`、blocks 之和相符、无 legacy authV 修复行、
未使用任何危险参数。**全量 bodyc 重放通过。**

### 5.3 第二次全量：同样代码、旧的"理智"配置 —— 反而更慢

为了把「代码改动本身值多少」和「参数值多少」拆开，用新 binary `1a997d3b`
跑了一次逐项对齐 2026-08-24 基线的配置（单进程、104 worker、GOGC 100、
mem-limit 48 GiB、`--readers` 走 auto）。

```text
run_id       full-sane-new-20260825
wall         3h59m16s
user CPU     1,128,930.40 s
system CPU      19,267.58 s
aggregate    7995% = 79.95 logical cores
MaxRSS       61,433,540 KiB = 58.6 GiB   （mem-limit 是 48，软限制没守住）
major faults 10,379,836
fs input     1,798,389,648 blocks ≈ 858 GiB
blocks       25,765,566   txs 3,678,100,106   gas 312,450,256,843,510
failed       0            exit 0
```

三次跑完的全量并列：

| 运行 | 配置 | wall | CPU-sec |
|---|---|---:|---:|
| 08-24 基线 | 单进程 104w，**1 reader** | **2h27m10s** | **456,881** |
| 08-25 deep path | 4×254w，1 reader/child，reservoir | 2h14m10s | 1,620,625 |
| 08-25 sane | 单进程 104w，**6 readers（auto）** | 3h59m16s | 1,148,198 |

**第三次比第一次墙钟慢 62%、CPU 多 2.5 倍**，而两者只差 reader 数。

### 5.4 Band 剖面：双峰，80% 的时间在四个 band

| band | blk/s | 分钟 | band | blk/s | 分钟 |
|---|---:|---:|---|---:|---:|
| 0–18M | 3,400–77,000 | 30.4 | 20–24M | 3,000–5,200 | 16.2 |
| **18–19M** | **292** | **57.3** | **24–25M** | **630** | **26.6** |
| **19–20M** | **531** | **30.9** | **25–25.77M** | **165** | **77.7** |

四个塌陷 band 吃掉 192 / 239 分钟。进度行之间最大间隔只有 27 秒，所以不是单次
GC 停顿，是持续爬行。

### 5.5 已被证伪的三个假设

不要重走这三条路。

**① GOMAXPROCS 超订**（deep path 的 3.5× CPU）。1M 密集切片上
GOMAXPROCS=256 与每子进程 =64 的结果是 5:09.83 vs 5:09.86、CPU 差 0.1%。
把每个子进程钉在 1/4 份额上什么都没改变。

**② "run #3 在 24–25M 掉到 260–479 blk/s"**。这是测量假象：那张表用四个
shard 的 max-head 包络，而 shard 会漂开，末段只剩最慢的一个在推进。撤回。

**③ reader 数导致 5.4 的塌陷**。假设是「一个 reader 钉住一整个已解码的 8192 块
segment，密集 band 里那是约 160 万个含指针的对象，六个 reader 让 GC mark 成本
爆炸」。在**确认塌陷的 band** `24,900,000–25,000,000`（10 万块）单独重跑，
104 worker / GOGC 100 钉死，只扫 reader 数，全部 `failed=0` 且 gas/txs 逐位一致：

| readers | mem | gogc | blk/s | CPU-sec | CPU% | MaxRSS | major faults |
|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 48 | 100 | 1521 | 4,383 | 6517% | 26.7 GiB | 105,651 |
| 2 | 48 | 100 | **2197** | 4,385 | 9343% | 42.6 GiB | 1,434 |
| 4 | 48 | 100 | 2197 | 4,458 | 9378% | 48.9 GiB | 45,615 |
| 6 | 48 | 100 | 2185 | 4,489 | 9404% | 49.0 GiB | 25,552 |
| 6 | 96 | 100 | 2077 | 4,514 | 8845% | 65.0 GiB | 20,314 |
| 6 | 96 | 300 | **被 SIGKILL** | 3,221 | 7920% | 78.6 GiB | 255,787 |

同样这些块、同样 6 个 reader，**单独跑是 2185 blk/s，在全量里是 165 blk/s ——
差 13 倍**。所以 reader 数不是原因。内存上限也不是：96 GiB 没有改善。
`GODEBUG=gctrace=1` 那一行显示 GC CPU 占比 **0%**。

这一轮仍有两个可用结论：

- **reader 2 就够了**。1→2 提升 44%（65 核 → 94 核，1 个 reader 会饿死机器），
  2→6 没有任何提升，且 CPU-sec 全程持平（4,383–4,514）。auto 给的 6 是浪费。
- **GOGC 300 在密集 band 会被 OOM killer 杀掉**（RSS 78.6 GiB 仍在涨）。
  密集区不要用 GOGC 300。

### 5.6 判决实验：同一配置只换 body 源 —— 塌陷消失，全面创纪录

2026-08-26 02:07 起，geth ancient freezer 已完整落盘（`bodies.0000..0451`、
`headers.0000..0006`，cidx 尾条目与文件 EOF 逐字节对齐，头/中/尾三段解码 `failed=0`）。
于是用**与 run A 完全相同的配置**（单进程、104 worker、readers 6、GOGC 100、
mem-limit 48 GiB）只把 `--input-headers-bodies` 换成 `/data/blockchain/witness-geth`：

```text
run_id       full-geth-r6-20260826
wall         1h13m23s
user CPU     443,692.13 s
system CPU     2,459.74 s
aggregate    10131% = 101.3 logical cores
MaxRSS       20,549,652 KiB = 19.6 GiB   （采样峰值 62.9 GiB，含 mmap 的 code-mdbx 页）
major faults 10,310,433
fs input     2,327,489,474 blocks ≈ 1.08 TiB
blocks       25,765,566   txs 3,678,100,106   gas 312,450,256,843,510
failed       0            exit 0
```

四次全量并列：

| 运行 | body 源 | 配置 | wall | CPU-sec |
|---|---|---|---:|---:|
| 08-24 基线 | bodyc | 单进程 104w，1 reader | 2h27m10s | 456,881 |
| 08-25 deep path | bodyc | 4×254w，reservoir | 2h14m10s | 1,620,625 |
| 08-25 sane | bodyc | 单进程 104w，6 readers | 3h59m16s | 1,148,198 |
| **08-26 freezer** | **geth freezer** | 单进程 104w，6 readers | **1h13m23s** | **446,152** |

**墙钟比之前最好成绩快 45%，CPU-sec 比之前最省的还少 2.3%**——两个判据同时创纪录。

逐 band 对照（同一 binary、同一参数，只差 body 源）：

| band | freezer blk/s | bodyc(run A) blk/s | band | freezer | bodyc(run A) |
|---|---:|---:|---|---:|---:|
| 10–11M | 8,324 | 9,599 | 18–19M | **3,978** | **292** |
| 14–15M | 4,343 | 5,257 | 19–20M | **4,262** | **531** |
| 17–18M | 4,487 | 3,440 | 24–25M | **2,270** | **630** |
| 20–21M | 4,339 | 5,177 | 25–25.77M | **2,204** | **165** |

稀疏段 bodyc 反而快 10–20%（列式解码在小块上更省），密集段 freezer 快 4–14 倍；
**塌陷完全消失**。

### 5.7 根因结论

5.2 列出的三个嫌疑现在只剩一个成立：

- **A. bodyc 整段物化 —— 成立。** 换成每 reader 只持一个 64 块 batch 的 freezer，
  其他一切不变，长跑塌陷不再出现。
- **B. 流式输入挤空 page cache 拖累 code-mdbx —— 不成立。** freezer 的流式读取量
  更大（1.08 TiB vs 858 GiB），major faults 相同量级（10.3M vs 10.4M），却没有塌陷。
- **C. 定容缓存颠簸 —— 不成立。** 两次运行缓存配置相同。

机制：一个 bodyc reader 钉住整个已解码的 8192 块 segment（密集段约 160 万个含指针
对象），6 个 reader 加预读 ≈ 数千万活对象；GC 的 mark 成本随活对象数走，
但只有当**历史累积**到某个规模后才压垮（短窗口重跑同一批块看不到，`gctrace` 在
健康态也报 0%）。具体是堆碎片化、mark assist 还是 sweep 尚未拆开，但工程结论
已经足够：

> **全量执行的生产输入是 geth ancient freezer，不是 bodyc。**
> bodyc 退回到它擅长的位置：归档格式（省 38.8% 磁盘）和顺序单读者场景。
> 让 bodyc 重新适合这条流水线的唯一路径是 §6 的惰性解码（P2），单独立项。

同时撤回本文 §2.4 的判断"bodyc 的瓶颈是内存不是 CPU"——它只对了一半：
瓶颈是**活对象数随时间累积**，短窗口 A/B 测不出来，这是 100k 窗口第三次误导长跑结论。

### 5.8 单变量推进：worker 104 → 128

freezer 源上其余参数不变（readers 6、GOGC 100），worker 抬到一物理核一个，
mem-limit 48 → 56 GiB 只为给多出的 24 个 worker 留堆余量：

```text
run_id       full-geth-w128-20260826
wall         1h06m29s          （−9.4%）
user+sys     458,311.83 s      （+2.7%）
aggregate    11488% = 114.9 logical cores
MaxRSS       23.2 GiB          （采样峰值 23.7 GiB）
major faults 8,783,280
failed       0   exit 0   blocks/txs/gas 与前次逐位一致
```

逐 band 增益 1–20%，密集段（10–20M）13–20%，稀疏段和尾段 2–8%。
**+23% 线程换来 −9.4% 墙钟、+2.7% CPU** —— 与 §4 的判断一致：物理核以内接近线性，
SMT 第二线程才是 +82% CPU 的那一段。128 是拐点，不再往上。

五次全量并列（全部 `failed=0`）：

| 运行 | body 源 | 配置 | wall | CPU-sec |
|---|---|---|---:|---:|
| 08-24 基线 | bodyc | 单进程 104w，1 reader | 2h27m10s | 456,881 |
| 08-25 deep path | bodyc | 4×254w，reservoir | 2h14m10s | 1,620,625 |
| 08-25 sane | bodyc | 单进程 104w，6 readers | 3h59m16s | 1,148,198 |
| 08-26 freezer | freezer | 单进程 104w，6 readers | 1h13m23s | **446,152** |
| **08-26 freezer 128w** | freezer | 单进程 128w，6 readers | **1h06m29s** | 458,312 |

CPU-sec 最省仍是 104w（446k）；墙钟最快是 128w。两者差 2.7% CPU / 9.4% 墙钟，
按"CPU-sec 优先"的判据两者都可接受，**默认取 128w**（地板估算 ≈ 450k ÷ 128 ≈ 58 分钟，
已到 66 分钟，剩余差距主要是尾段密集块的负载不均和 GC）。

### 5.9 单变量：readers 6 → 2 —— 预期落空，freezer 上 reader 是真成本

```text
run_id       full-geth-w128-r2-20260826   （128w，readers 2，其余同 5.8）
wall         1h21m36s          （+22.7%）
user+sys     382,331.69 s      （−16.6%）
aggregate    7808% = 78.1 logical cores
MaxRSS       17.6 GiB
failed       0
```

预期"无差别"是错的：2 个 reader 只能喂饱 78 个线程，128 个 worker 有 40% 时间在等
输入。bodyc sweep 里 2 就够，是因为 bodyc 一次解出 8192 块，reader 的每块开销极低；
freezer 是 64 块一个 zstd batch 加逐块 RLP 解码，每块开销高一个数量级，
**reader 数必须跟着 worker 数走**。readers 6 保留为默认；256 worker 时可能需要更多。

CPU-sec 下降 16.6% 同时墙钟上升 22.7%，是 SMT 记账效应的直接展示：忙碌线程从 115 降到
78，几乎全部落在独立物理核上，每个 CPU-秒的有效算力更高。这再次说明单看 CPU-sec
会系统性偏向低并发（见 5.10）。

### 5.10 关于 SMT 与度量：一处更正

本文与前序会话里"128 线程 → 234 线程吞吐 +21%、CPU +82%"的说法，+82% 是 **CPU 占用率**
（23368% vs 12846%），不是 CPU-seconds；CPU-seconds 是 +50%。更重要的是
**CPU-seconds 在 SMT 下有偏**：两个兄弟线程各记满 1 秒，合起来只是一个物理核。
按物理核时（core-seconds = wall × 128）重算 1M 密集切片：

| 忙碌线程 | blk/s | wall | CPU-sec | core-sec |
|---:|---:|---:|---:|---:|
| 102 | 2280 | 438.6 s | 44,946 | 56,145 |
| 128 | 2666 | 375.1 s | 48,191 | 48,015 |
| 234 | 3228 | 309.8 s | 72,402 | **39,658** |

所以 SMT 在这个负载上是**正收益**（+21% 吞吐，core-sec −17%），EVM 解释器的
指针追逐/分支失败/cache miss 正是 SMT 填空的典型场景。"128 是拐点"撤回；
它只在"按 vCPU 计费"的成本函数下成立。

独占机器上的正确指标：墙钟（= 整机吞吐）或 core-seconds。CPU-seconds 只用于
云 vCPU 计费场景。

### 5.11 测量污染审计（2026-08-26 06:40）

另一个会话在 `00:56:53–02:46:47` 用 `witness-replay.main-2aa0d4f0`（main 分支，bodyc 源，
104w / 6 readers / GOGC 100）跑了一次全量，与本文的部分运行重叠。用 sysstat 的 10 分钟
系统级 CPU 记录（`sadf -d -- -u /var/log/sysstat/saNN`）逐一核对本文每个运行窗口内的
系统忙碌线程数是否等于该运行自己的 `Percent of CPU`：

| 运行 | 窗口 | 系统忙碌线程 | 本运行 CPU% | 判定 |
|---|---|---:|---:|---|
| full-bodyc 4×254（5.2） | 08-25 10:46–13:00 | 216–244 | 4 子进程合计约 210 | **干净** |
| run A bodyc 104w（5.3） | 08-25 20:48–00:47 | 70–108 | 80 | **干净** |
| p0a readers sweep（5.5） | 08-26 00:50–00:56 | 51 | — | 最后一行与对方 smoke 重叠数秒，可忽略 |
| **r6 freezer 104w（5.6）** | 08-26 02:07–03:20 | **178–185（02:20–02:40）** | 101 | **前 40 分钟被污染** |
| w128 freezer（5.8） | 08-26 03:22–04:28 | 68–128 | 115 | **干净** |
| w128 r2（5.9） | 08-26 04:29–05:51 | 57–97 | 78 | **干净** |
| 1M 切片 a2/b2/c2/d2（5.10） | 08-25 20:15–20:39 | 113–175 | 与各行 CPU% 一致 | **干净** |

结论：
- **只有 r6（1h13m23s / 446,152 CPU-s）不可用**。它在 0–17M 段与对方的 79 线程共享
  SMT 兄弟核，逐 band 比 run A 慢 10–15%（本应更快），CPU-sec 也被 SMT 记账放大。
  已排队同参数干净重跑（`full-geth-r6-clean-20260826`）。
- **"freezer 消灭塌陷"的结论不受影响**：r6 的 18–26M 段（02:47 之后）是干净的，
  4114 / 4303 / 3346 / 2241 blk/s，无塌陷。
- **塌陷本身得到独立确认**：对方那次 bodyc 运行在同一 band 塌到 **279 blk/s、79 分钟**，
  且从 01:31 开始——比本文任何重叠早 36 分钟，用的还是另一个分支的 binary。
  这是第三次、跨会话、跨 binary 的复现。
- w128 的 `1h06m29s / 458,312 CPU-s` 干净，仍是当前有效基线。

另外注意：`origin/main` 已经前进到 `2aa0d4f0`（含 `1209327d perf(witness): keep replay
workers fed`），与本分支基点 `179691e5` 分叉；合并前需要看两边对 witness 流水线的改动。

### 5.12 SMT 判决：256 worker（干净，sar 核对）

单变量 128 → 256（readers 6、GOGC 100 不变，mem 56 → 80）：

```text
run_id       full-geth-w256-20260826
wall         1h02m43s          （对 128w −5.7%）
user+sys     535,022 s         （+16.7%）
aggregate    14215% = 142 logical threads 平均忙碌（256 个 worker）
MaxRSS       34.5 GiB
failed       0   exit 0   blocks/txs/gas 逐位一致
sar          05:50–06:50 整机 70→187 忙碌线程，只有本进程，干净
```

| band | 128w | 256w | Δ | band | 128w | 256w | Δ |
|---|---:|---:|---:|---|---:|---:|---:|
| 2–4M | 62,055 | 61,456 | −1.0% | 16–18M | 5,526 | 5,853 | +5.9% |
| 8–10M | 14,034 | 14,979 | +6.7% | 20–22M | 4,554 | 4,838 | +6.2% |
| 12–14M | 5,449 | 5,561 | +2.0% | 22–24M | 3,576 | 3,903 | **+9.1%** |
| 14–16M | 5,202 | 5,433 | +4.4% | 24–26M | 2,417 | 2,629 | **+8.8%** |

SMT 是正收益，但比 1M 切片上的 +21% 小得多（全链 −5.7% 墙钟）。原因在 CPU 占用：
256 个 worker 平均只有 **142 个线程忙**，且沿密集段递增（06:10 125 → 06:50 187）。
这是 **worker 在等输入** 的特征：6 个 freezer reader 每块要解一个 64 块 zstd batch
加 RLP，喂 128 个 worker 已到 115 线程，喂 256 个只多到 142。SMT 的真实上限还没测到，
下一个单变量是 readers 12（已排队）。

六次全量并列（全部 `failed=0`、全部经 sar 核对为干净，r6 除外）：

| 运行 | body 源 | 配置 | wall | CPU-sec | 干净 |
|---|---|---|---:|---:|---|
| 08-24 基线 | bodyc | 单进程 104w，1 reader | 2h27m10s | 456,881 | 是（当时无其他运行） |
| 08-25 deep path | bodyc | 4×254w | 2h14m10s | 1,620,625 | 是 |
| 08-25 run A | bodyc | 单进程 104w，6r | 3h59m16s | 1,148,198 | 是 |
| 08-26 r6 | freezer | 单进程 104w，6r | ~~1h13m23s~~ | ~~446,152~~ | **否，重跑中** |
| 08-26 w128 | freezer | 单进程 128w，6r | 1h06m29s | 458,312 | 是 |
| **08-26 w256** | freezer | 单进程 256w，6r | **1h02m43s** | 535,022 | 是 |

### 5.13 readers 6 → 12 @ 256w（干净）

```text
run_id       full-geth-w256-r12-20260826
wall         1h00m56s          （对 6r −2.9%）
user+sys     629,568 s         （+17.7%）
aggregate    17218% = 172 threads 平均忙碌
MaxRSS       38.5 GiB
failed       0
```

| band | 6r | 12r | Δ | band | 6r | 12r | Δ |
|---|---:|---:|---:|---|---:|---:|---:|
| 2–4M | 61,456 | 66,928 | +8.9% | 14–16M | 5,433 | 5,570 | +2.5% |
| 4–6M | 14,920 | 18,261 | **+22.4%** | 18–20M | 5,015 | 5,168 | +3.0% |
| 6–8M | 15,583 | 17,736 | +13.8% | 22–24M | 3,903 | 3,912 | +0.2% |
| 8–10M | 14,979 | 15,933 | +6.4% | 24–26M | 2,629 | 2,744 | +4.4% |

reader 数只在**稀疏段**是瓶颈（+9–22%）；密集段 +0–4%，说明那里 256 个 worker 不是在等
输入。但忙碌线程也只从 142 升到 172，密集段仍有 80 多个 worker 空转，限制因素另有其他
（候选：aggregator 重排窗口、GC、超大块的长尾）。按 CPU-sec 判据 12 readers 不值
（+17.7% 换 −2.9%）；按墙钟是小幅正收益。

### 5.14 tier-1 代码 A/B：CPU-sec −5.5%，墙钟 +15% —— 待归因

同配置（256w / 6r / GOGC 100 / freezer）只换 binary：

| | base `3449c2f4` | tier-1 `44d60635` | Δ |
|---|---:|---:|---:|
| wall | 1h02m43s | **1h12m07s** | **+15.0%** |
| user+sys | 535,022 | **505,416** | **−5.5%** |
| 忙碌线程 | 142 | 117 | |
| MaxRSS | 34.5 GiB | **14.5 GiB** | −58% |
| minor faults | 39.2M | 19.9M | −49% |
| failed / gas / txs | 0 / 一致 | 0 / 一致 | |

每块 CPU 确实少了（代码收益成立），但流水线整体变慢：6–10M 段掉 29–35%，密集段掉 6–10%。
假设：活堆缩小超过一半（R2 把大 map 交给 GC、M1/M2 去掉几十亿对象），GOGC=100 下 GC
触发点 = 2× 活堆，周期频率翻倍以上，mark assist 落在最大的分配者——reader——头上，
而 reader 正是流水线瓶颈。**这是假设，不是结论。** 归因消融（1M 密集切片、256w/6r，
每行只差一个变量：tier-1 gc300、tier-1 −R2、−R1、−S2、base gc300）正在跑，结果出来前
tier-1 不作为默认 binary。

### 5.15 归因消融：倒退来自 GOGC 与活堆的耦合，不是代码

1M 密集块 `24.5–25.5M`，256w / 6r / freezer，机器独占，全部 `failed=0`：

| 行 | wall | blk/s | CPU-sec | 忙碌线程 | MaxRSS |
|---|---:|---:|---:|---:|---:|
| base，GOGC 100 | 6:44.5 | 2472 | 66,899 | 165 | 22.4 GiB |
| tier-1，GOGC 100 | 7:54.1 | 2109 | 60,708 | 128 | 8.4 GiB |
| **tier-1，GOGC 300** | **5:47.7** | **2876** | **60,522** | 174 | 14.2 GiB |
| tier-1 −R2，GOGC 100 | 6:36.3 | 2523 | 59,199 | 149 | 23.5 GiB |
| tier-1 −R1，GOGC 100 | 8:03.7 | 2067 | 63,499 | 131 | 9.0 GiB |
| tier-1 −S2，GOGC 100 | 9:05.0 | 1835 | 59,817 | 110 | 8.2 GiB |
| base，GOGC 300 | 5:49.6 | 2861 | 67,455 | 193 | 64.9 GiB |

读法：
- **tier-1 + GOGC 300 是全表最优**：墙钟 −14.0%、CPU-sec −9.5%（对 base gc100），
  且比 base gc300 少 10.3% CPU、少 78% 内存（14.2 vs 64.9 GiB），墙钟持平略优。
- 机制得到确认：tier-1 把活堆压到 8.4 GiB（base 22.4），GOGC=100 的触发点是 2× 活堆，
  GC 周期频率翻倍以上，mark assist 打在最大的分配者（reader）上，喂料变慢，
  worker 空转（忙碌线程 165 → 128）。**代码没有变慢，是 GC 参数没有跟着活堆缩小而调。**
- 撤掉 R2（大 map 不再交给 GC，RSS 回到 23.5）就恢复到 6:36——它是缩小活堆的主因，
  但它本身是正确的改动；正确的处置是调 GOGC，不是回退。
- −R1 / −S2 两行落在 GC 抖动区（RSS 8–9 GiB、GOGC 100），互相差 1 分钟以上，
  不能用来评价 R1/S2 本身。

**结论：tier-1 七项全部保留；freezer 全量的 GOGC 从 100 改为 300。**
此前"密集段 GOGC 300 会被 OOM 杀"的记录（5.5）是 bodyc 源 + 6 reader 各持整段的场景，
RSS 78 GiB 仍在涨；freezer + tier-1 在 gc300 下只有 14 GiB，且 `--mem-limit-gb` 仍是硬顶。

### 5.16 下一步

1. r6 干净重跑（104w，跑中）；
2. **tier-1 + GOGC 300 全量 @ 256w/6r**（已排队）——最终 gate，预期墙钟 < 1h、CPU-sec < 500k；
3. 密集段 80+ 空转 worker 的原因；
4. `perf stat` 量 IPC。
