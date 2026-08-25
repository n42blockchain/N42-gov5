# witness-replay 0–1000 万块优化交接（2026-08-25）

本文记录本次 Linux `bodyc` witness replay 性能会话，目标是让后续会话无需
重新猜测参数、重复采样或混淆不同运行路径。它是
[`witness-replay-benchmark-runbook.md`](./witness-replay-benchmark-runbook.md)
和 [`../WITNESS_LINUX_PERF_20260824.md`](../WITNESS_LINUX_PERF_20260824.md)
的增量记录，不替代其中的正确性规则。

## 1. 最重要的结论

1. 用户所说的 `3m43s` 是四进程 striped 版本的历史成绩；之后又加入了
   byte-accounted input reservoir（持续预读、避免 worker 饿死）。不能把单进程
   `7m+` 或未启 reservoir 的 `3m30s` 当成这条深度优化路径的结果。
2. 当前深度路径的固定参数是：4 process shards、254 total workers、4 total readers、
   reservoir high/low `20/10 GiB`、GOGC 400、soft memory limit 80 GiB。
3. 当前代码完整验证 `0..9,999,999` 的最终结果是 `3m37.46s`，四个子进程均
   `failed=0`，共执行 10,000,000 blocks、697,373,070 transactions。
4. `/usr/bin/time` 报告 aggregate CPU `21061%`，即平均使用 210.61 个逻辑核；
   主机有 256 个逻辑核，因此整机平均 CPU 占用约 `82.27%`。该平均值包含启动、
   输入读取和结束清理阶段。
5. 合并四分片 CPU pprof 后，worker 执行主路径占 95.6% cumulative，EVM
   interpreter 占 64.8% cumulative。输入解码已不是 CPU 主热点；持续喂任务后
   四个分片进度接近，没有明显的单分片掉队。

## 2. 环境和版本状态

- 仓库：`/home/n42/src/n42/N42-gov5`
- HEAD：`179691e51cd63a00e959b839eb140cef031f05e3`
- Go：`go1.26.0 linux/amd64`
- 主机：AMD EPYC 9B45，128 physical cores / 256 SMT threads，约 136 GiB RAM
- headers/bodies：`/data/blockchain/witness`（N42 columnar `headerc/bodyc`）
- witness：`/data/blockchain/witness`
- senders：`/data/blockchain/witness`
- Code MDBX：`/data/blockchain/code-mdbx`
- 最终本地 binary：`build/bin/witness-replay`
- 最终 binary SHA256：
  `50fc8dd287bfb0c8a77022dc0acc88d4dfcbb918d2785fc905694b8b66e2b453`

重要：HEAD 后存在大量尚未提交的 witness/EVM 性能修改，`go version -m` 明确显示
`+dirty`。上面的 commit 只能标识基线，不能唯一复现最终 binary。下次会话开始时
必须先保存 `git status --short` 和相关 diff；不要 reset 或覆盖现有工作树。

## 3. 深度持续喂任务路径

### 3.1 Process shards

`--process-shards 4` 由父进程启动四个子进程。子进程不是按四段连续高度切分，
而是通过隐藏参数 `--segment-shard-count/--segment-shard-index` 交错领取 bodyc
segments，降低因历史不同区间交易密度差异造成的尾部拖延。

总资源预算由父进程拆分：

| 总预算 | 每个子进程实际预算 |
|---|---|
| 254 workers | shard 0/1 为 64，shard 2/3 为 63 |
| 4 readers | 1 reader |
| 80 GiB memory limit | 20 GiB |
| reservoir high 20 GiB | 5 GiB |
| reservoir low 10 GiB | 2.5 GiB |

接线位置：

- `cmd/witness-replay/main.go`：process shard 启动和总预算拆分
- `internal/ethel/witness_replay_pipeline.go`：parallel feed、body decoder 和 reservoir 接线
- `internal/ethel/witness_replay_reservoir.go`：byte-accounted hysteresis reservoir

### 3.2 Reservoir

reservoir 是 verification-only、`--no-output` 路径上的有界 FIFO。producer 先解码
并累计输入 job 的估算字节数；达到 high watermark 后暂停，只有已完成工作释放容量
并下降到 low watermark 后才继续 refill。该 hysteresis 避免 producer/consumer 在
单一阈值附近频繁唤醒，同时让 worker 前方保持一段已经解码的任务。

reservoir **默认关闭**。`--input-high-gb` 默认是 0；只有同时满足
`--no-output` 且 high watermark 大于 0 时，日志才应出现：

```text
Input reservoir enabled high_gb=5 low_gb=2.5
```

四分片运行必须出现四条该日志。没有这些日志，就没有测试到“线程不饿”的深度路径。

### 3.3 当前正式复现命令

```bash
GOCACHE=/tmp/n42-go-build-cache \
  go build -o build/bin/witness-replay ./cmd/witness-replay

/usr/bin/time -v build/bin/witness-replay \
  --input-headers-bodies /data/blockchain/witness \
  --input-witness /data/blockchain/witness \
  --senders /data/blockchain/witness \
  --datadir /data/blockchain/code-mdbx \
  --output /tmp/wr-current-out \
  --start 0 --end 10000000 \
  --no-output \
  --process-shards 4 \
  --workers 254 \
  --readers 4 \
  --input-high-gb 20 \
  --input-low-gb 10 \
  --gogc 400 \
  --mem-limit-gb 80
```

正确性验收必须同时满足：进程退出 0、父进程报告
`Process-sharded verification complete blocks=10000000`、四条子进程
`Replay complete ... failed=0`。不得增加 `--skip-verify` 或
`--continue-on-error`。

## 4. 历史运行和不可混用的数字

| 运行 | 关键参数 | Wall | User CPU | Aggregate CPU | Max RSS | 说明 |
|---|---|---:|---:|---:|---:|---|
| 历史 striped | shards=4, workers=254, readers=8, GOGC=300, reservoir off | 3m43.16s | 48,342.75s | 21812% | 20,709,808 KiB | 深度 reservoir 之前 |
| 当前深度路径，Bloom 改动前 | shards=4, workers=254, readers=4, GOGC=400, reservoir=20/10 | 3m38.13s | 45,937.48s | 21194% | 20,783,924 KiB | pprof 对应代码 |
| 当前深度路径，Bloom 改动后 | 同上一行 | **3m37.46s** | **45,534.57s** | **21061%** | **20,766,260 KiB** | 当前最终结果 |

最终行相对历史 `3m43.16s` 快 5.70 秒（约 2.55%），但两行的 readers、GOGC
和 reservoir 参数不同，不能把全部差值归因于单个源码修改。

另有一次 `3m30.23s` 运行使用 4 shards、254 workers、8 readers、GOGC 300，
且未传 `--input-high-gb/--input-low-gb`。它证明 striped 路径仍然很快，但没有启用
reservoir，不是深度路径的最终 A/B。早先 `7m10s -> 6m55s` 的两次运行是单进程、
112 workers、GOGC 100、48 GiB，也不能与四进程结果比较。

最终运行详细计数：

```text
blocks                  10,000,000
transactions            697,373,070
wall                    3m37.46s
user CPU                45,534.57s
system CPU              268.13s
aggregate CPU           21061% = 210.61 logical cores = 82.27% of 256
maximum RSS             20,766,260 KiB
major page faults       774,573
voluntary ctx switches  21,361,610
involuntary switches    2,564,795
swaps                    0
failed                   0 on all four shards
```

## 5. pprof 采集方法和结果

`--process-shards > 1` 不允许父进程使用单一 `--cpu-profile`，因为多个进程不能
安全写同一个 profile。因此本次手动并发启动四个隐藏 segment shards，每个子进程
写独立 CPU/heap profile。每个子进程使用与正式父进程拆分后相同的参数：

```text
--process-shards 1
--segment-shard-count 4
--segment-shard-index 0..3
--workers 64,64,63,63
--readers 1
--input-high-gb 5 --input-low-gb 2.5
--gogc 400 --mem-limit-gb 20 --no-output
--cpu-profile cpu-N.pprof --heap-profile heap-N.pprof
```

四进程 profiling wall 是 `3m38.85s`，aggregate CPU 21006%，MaxRSS
20,737,084 KiB，四个 shard 均 `failed=0`。profile 在：

```text
/tmp/wr-pprof-0-10m-reservoir/cpu-{0,1,2,3}.pprof
/tmp/wr-pprof-0-10m-reservoir/heap-{0,1,2,3}.pprof
/tmp/wr-pprof-0-10m-reservoir/shard-{0,1,2,3}.log
/tmp/wr-pprof-0-10m-reservoir/time.log
```

`/tmp` 是临时目录，机器清理后这些文件会消失；本文保留了足以重新采集的参数。
CPU profile 已包含符号，下次即使最终 binary 已变化，也可直接合并查看：

```bash
go tool pprof -top -nodecount=40 \
  /tmp/wr-pprof-0-10m-reservoir/cpu-{0,1,2,3}.pprof

go tool pprof -top -cum -nodecount=40 \
  /tmp/wr-pprof-0-10m-reservoir/cpu-{0,1,2,3}.pprof

go tool pprof -top -sample_index=alloc_space -nodecount=40 \
  /tmp/wr-pprof-0-10m-reservoir/heap-{0,1,2,3}.pprof
```

合并四份 CPU profile 的主要结果：

| CPU node | Flat | Cumulative | 解释 |
|---|---:|---:|---|
| `runWitnessWorker` | 约 0 | 95.63% | 绝大部分 CPU 样本在有效 worker 主路径 |
| `EVMInterpreter.Run` | 12.50% | 64.82% | 当前最大可优化域，但共识风险高 |
| `sha3.keccakF1600` | 12.37% | 12.38% | Keccak 算法本体 |
| `runtime.cgocall` / ecrecover | 4.95% | 约 4.98% | secp256k1 sender/预编译调用 |
| `EthReceiptHash` / `DeriveShaErigon` | 很低 | 4.09% | receipt trie 构建 |
| `FinalizeTx` | 0.13% | 6.74% | 状态 finalize 及下游工作 |

CPU profile 总样本为 45,876.52 秒。bodyc 输入 decode 没进入 CPU top，说明当前
0–1000 万范围已经主要受 EVM/Keccak/receipt 计算限制，不应仅通过继续增大
reservoir 期待显著提速。

四份 end-of-run heap profile 合并后：

- total allocated space：约 3.47 TiB；这是累计 churn，不是峰值 RSS。
- `applyTransaction` cumulative alloc：约 2.54 TiB。
- `decodeBodySegment` cumulative alloc：约 432 GiB，其中 zstd DecodeAll、交易对象和
  calldata 是主要来源；它对 GC/RSS 有影响，但不是当前 CPU top。
- forced-GC 后 in-use heap：约 1.65 GiB；主要是 account code cache、EVM memory 和
  jumpdest analysis cache。

## 6. 本次新增的两项局部优化

### 6.1 Legacy transaction hash：去除 reflection RLP

文件：

- `common/transaction/legacy_tx.go`
- `common/transaction/legacy_tx_test.go`

旧的 `LegacyTx.hash` 通过 `RlpHash([]interface{}{...})` 进入 reflection RLP。单进程
初始 profile 中 `LegacyTx.hash` cumulative 约 4.47%，累计分配约 148.3 GiB；
`Transaction.Hash` cumulative 约 4.72%。

当前实现直接按 Ethereum canonical legacy transaction 的九字段列表编码 RLP，
使用 `sync.Pool` 复用编码 buffer，再计算 Keccak。它必须保持以下语义：

- nil/zero uint256 均编码为空 RLP string；
- `To == nil` 编码为空 string，非 nil 地址编码 20 bytes；
- integer 使用 canonical minimal big-endian；
- list payload 长度和 string 长度使用 canonical short/long prefix。

随机测试用固定 seed 生成 1000 组 nonce、nil/create To、不同长度 calldata、
nil/zero/small/full-width uint256，并逐一与旧 reflection encoder 的 hash 比较。

微基准结果：约 `713 ns, 233 B, 5 allocs` 降至
`334 ns, 32 B, 1 alloc`。在深度路径 profile 中，该函数已跌出主要 CPU top；
end-of-run in-use 只剩约 11.5 MiB cumulative retention path。

### 6.2 Receipt Bloom：复用 hasher 并消除 Address copy

文件：`common/block/bloom9.go`

pprof 显示每笔 receipt 的 `CreateBloom` cumulative 约 1511 CPU 秒。旧实现对每个
log address/topic 都借还一次 Keccak state，而且 `log.Address.Bytes()` 为每条 log
复制一个 20-byte slice。

当前实现：

- 一次 `CreateBloom`/`LogsBloom` 只借还一次 Keccak state；每个元素之间调用 Reset；
- 直接使用 `log.Address[:]`，不再调用分配型 `Address.Bytes()`；
- 公共 `Bloom.Add`/`Bloom.Test` 行为和 bloom bit 计算不变。

已有 bloom/receipt 编码测试验证结果一致。微基准：

| Benchmark | Before | After |
|---|---:|---:|
| CreateBloom small | 1170 ns, 104 B, 5 allocs | 1085 ns, 8 B, 1 alloc |
| CreateBloom large | 114,176 ns, 9,631 B, 401 allocs | 104,500 ns, 11 B, 1 alloc |

完整 0–1000 万同参数 A/B：`3m38.13s -> 3m37.46s`，wall 减少 0.67 秒，
user CPU 减少 402.91 秒。wall 差值较小，不能过度解读单次测量；微基准和
user CPU 方向一致，且正确性 gate 全部通过。

## 7. 已执行验证

```bash
GOCACHE=/tmp/n42-go-build-cache go test \
  ./common/block ./common/hash ./common/transaction \
  ./internal/ethel ./internal/vm ./internal

GOCACHE=/tmp/n42-go-build-cache go test -race \
  ./common/block ./common/transaction

git diff --check -- \
  common/block/bloom9.go \
  common/transaction/legacy_tx.go \
  common/transaction/legacy_tx_test.go
```

上述命令均通过。全仓 `make test-short` 在本会话较早阶段还遇到沙箱禁止本地
listen、只读 module cache，以及已有 `lib/kv/layered` test import cycle；这些不是
本次三个文件的测试失败。不要把 targeted tests 误写成完整仓库 gate 已通过。

## 8. 下次会话的开始顺序

1. 先读本文和 2026-08-24 性能记录。
2. 执行 `git status --short`，确认 dirty 工作树仍包含 process shards、parallel feed、
   reservoir、legacy hash 和 Bloom 修改；不要凭 HEAD 判断修改存在与否。
3. 用第 3.3 节完全相同的参数重建并复跑。任何参数变化都新建一行结果，禁止覆盖
   `3m37.46s` 基线。
4. 必须在日志中确认四条 `Input reservoir enabled` 和四条 `failed=0`。
5. 若继续 pprof，仍采用四个显式 segment shards 写独立文件，最后合并四份 profile；
   只看一个 shard 会遗漏跨分片不均衡。
6. 优先考虑 EVM interpreter、Keccak、receipt trie 这些 profile 证明的热点。修改
   opcode、gas、receipt 编码或 hash 语义前，必须先添加等价性/共识测试，再跑固定
   dense gate 和完整 0–1000 万 gate。

不建议下一步盲目增加 workers、readers 或 reservoir。最终运行已经平均使用
210.61/256 个逻辑核，而 profile 主要是有效 EVM 计算；更高并发可能增加 map、GC、
page fault 和 scheduler traffic。参数优化必须使用相同输入、热/冷缓存说明和至少
两次 A/B，源码优化则同时比较 wall、user CPU、RSS、faults、context switches 和
`failed=0`。

## 9. 本机会话证据路径

```text
/tmp/wr-striped-0-10m-shards4.log       # 历史 3m43.16s
/tmp/wr-current-final-0-10m.log         # 深度路径，Bloom 前 3m38.13s
/tmp/wr-current-bloom-0-10m.log         # 当前最终 3m37.46s
/tmp/wr-pprof-0-10m-reservoir/          # 四分片 CPU/heap profiles 和日志
/tmp/wr-pprof-0-10m-baseline/           # 较早单进程 baseline profiles
/tmp/wr-pprof-0-10m-legacyhash/         # 较早单进程 legacy-hash 后 profiles
```

这些路径用于当前机器上的审计，不应作为长期制品仓库。需要长期保留时，应复制到
带日期、binary SHA256 和 dirty diff sidecar 的持久存储目录。
