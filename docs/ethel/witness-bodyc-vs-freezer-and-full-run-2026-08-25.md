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

入口：`scripts/witness-full-bodyc.sh`。它固定了上面这组参数、
强制保留验证、记录 binary SHA256 与 commit、按 5 秒采样四个子进程 RSS 之和，
结束后自动打印验收项。

```bash
cd /home/n42/src/n42/N42-gov5
RUN_ID=full-bodyc-20260825 scripts/witness-full-bodyc.sh
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

### 5.3 这次运行暴露的问题：CPU 效率倒退

与 2026-08-24 的单进程基线相比：

| | 2026-08-24 单进程 w104/gc100/48G | 本次 4×254/gc400/80G |
|---|---:|---:|
| wall | 2h27m10s | **2h14m10s（−8.8%）** |
| user+sys CPU | 456,881 s | **1,620,625 s（+255%）** |
| 聚合 CPU | 5174% | 20131% |
| 峰值 RSS | 49.3 GiB（单进程） | 89.35 GiB（四进程之和） |
| major faults | 7,638,869 | 22,219,831 |
| involuntary ctx switches | 1,283,944 | **132,442,573（约 103 倍）** |

**同样的工作量花了 3.5 倍 CPU，只换来 8.8% 的 wall。** 这不是可接受的结果，
即使 wall 更好。两条线索：

1. **GOMAXPROCS 超订**。`runProcessShards` 会把 workers、readers、mem-limit
   和 reservoir 按 shard 数拆分，但**不设 GOMAXPROCS**：四个子进程各自继承机器
   默认的 256，于是 256 个硬件线程上有 1024 个 P。暴涨 103 倍的
   involuntary context switches 与此完全吻合。
2. **尾部不均衡**。四个 shard 的 blocks 与 gas 差异都在 0.2% 以内（交错分片是有
   效的），但完成时间从 1h41m 到 2h14m 相差 32 分钟。最快的 shard 结束后，
   它的 64 个 worker 就闲置了，整轮 wall 由最慢的 shard 决定，而那个 shard
   最后只用 63 个 worker 跑在 256 核的机器上。

另外要指出：**100k 块的窗口 sweep 第二次误判了长跑最优值**。
2026-08-24 已经记录过一次（warm 100k 说 128/300/64，冷全量证明是错的），
这次 §4 的 sweep 说 254/400/80，全量再次证明它不是长跑最优。
以后凡是要给全量选参数，必须用**至少 100 万块的密集切片**做 A/B，
100k 窗口只能用来做源码改动的 A/B（那种场景两侧条件相同，偏差会抵消）。

正在做的对照实验（`24,500,000..25,500,000`，100 万密集块）：

<!-- LONG-CFG-RESULT -->


---

## 6. 下一步的候选优化（按 profile 排序，尚未做）

1. `accessList.Contains` / `AddSlot` / `ContainsAddress`：`mapaccess2` 里最大的
   一块（合计约 184 CPU 秒 / 1.2%）。EIP-2929 的 warm/cold 判定用嵌套 map，
   改成扁平结构可省，但触碰 gas 计费，必须先有等价性测试。
2. `IntraBlockState.foldBalanceIncrease` 104.87 s（0.68%）。
3. 每次调用都新建的 `ScopeContext` / `pool.Get().(*Memory)` / `stack.New()`
   合计约 123 CPU 秒（0.8%）：可以按调用深度复用，模式与已有的
   `evm.contractFrames` 相同。
4. bodyc 分块解码：把 8192 块整段物化改成按列游标惰性物化，
   直接压掉 §2.4 那 17 GiB 的驻留差。这是唯一能显著抬高 worker 上限的改动，
   但要重写 `decodeBodySegment`，风险最高，应单独立项并配 segment 级
   round-trip 测试。
5. `ecrecover` 预编译（cgo，2.95%）和 `bn256`（2.76%）是共识必需，
   除非换实现，否则没有空间。

不建议再盲目调 worker/reader/reservoir：254 worker 已经平均占用 218/256 个
逻辑核，且内存峰值 82.3 GiB。任何参数改动都要新开一行结果，
不得覆盖本文记录的基线。
