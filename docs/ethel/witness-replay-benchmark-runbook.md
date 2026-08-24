# Ethereum witness-replay：正确性与并发性能运行手册

本文只描述 `cmd/witness-replay` 对 **Ethereum mainnet 历史块**的
witness 驱动重放。它不属于 QS/HotStuff 舰队，也不经过 txpool、faucet、
`devBlockReward`、sender funding、baseFee 衰减或 BLS 共识。

> 核心纪律：并发单位是 block；验收单位是 `failed=0`；性能主单位是
> `block/s`。不要用 genesis 空块窗口选 worker，不要把
> `--continue-on-error` 或 `--skip-verify` 的结果写成“验证通过”。

## 1. 实际执行模型与口径

`witness-replay` 固定使用 `params.EthereumMainnetChainConfig` 和
`ethel.NewEthReplayEngine`。reader 按块读取 header、body、witness、senders；
worker pool 把一个完整 block 交给一个 worker，块内交易保持原序；aggregator
再按块号重排结果。每个块携带自己的状态读取 witness，因此不同块可独立执行。

以下指标含义不能混用：

| 指标 | 含义 | 能否单独作为结论 |
|---|---|---|
| `block/s` | 每秒完成的历史块数，主性能单位 | 可以，但必须同时给出区间和负载密度 |
| `tx/s` | 每秒执行的真实 Ethereum 交易数 | 辅助指标 |
| `Mgas/s` | header gas 口径的执行工作量 | 辅助指标；不是 funding gas |
| `failed` | 未复现 header gas/receipt root 或执行失败的块数 | 验收必须为 0 |

`--no-output` 测的是近似纯 EVM+witness 读取，不含 state root、changeset
持久化和最终状态重建，不能与 live client 的端到端吞吐直接比较。

## 2. 五份输入必须同源且覆盖目标区间

典型的 N42 columnar 输入目录应同时包含：

```text
headerc.cidx + headerc.NNNN.cdat
bodyc.cidx   + bodyc.NNNN.cdat
witness.cidx + witness.NNNN.cdat
codes.cidx   + codes.NNNN.cdat    # 或使用完整 MDBX Code 表
senders.cidx + senders.NNNN.cdat  # 可选，但性能测试强烈建议
```

`witness` 必须由将要重放的同一份 canonical bodies 生成。仅看到目录大小、
最后一个 segment 或工具能打开表，不等于传输完整；交接时必须保存源端 manifest：

```bash
(cd "$SRC" && find . -maxdepth 1 -type f -printf '%P\0' \
  | sort -z | xargs -0 sha256sum) > witness.sha256
(cd "$DST" && sha256sum -c /path/to/witness.sha256)
```

大目录跨机复制优先使用 `rsync --archive --partial --info=progress2`，完成后仍要
执行 manifest 校验。没有源端 manifest 时，只能表述为“大小与表头可读”，不能
表述为“完整性已验证”。

用 `freezer-heads` 检查各按块表的 `start/items`。`headerc/bodyc/witness/senders`
必须覆盖目标 `[start,end)`；codes 必须覆盖该区间用到的全部 bytecode：

```bash
go build -tags nosqlite,noboltdb -o /tmp/freezer-heads ./cmd/freezer-heads
/tmp/freezer-heads /data/blockchain/witness
```

缺 code 会让合约被误当作 EOA，随后 witness 流错位，常表现为 nonce、gas 或
receipt root 异常。遇到这类错误先核对 `codes.coverage`；必要时改用录制 witness
时的完整 MDBX Code 表，不能直接跳过验证。

## 3. 二进制必须可追溯到 witness 录制版本

witness 是按录制时 `ProcessBlock` 的读取顺序生成的。EVM fork、gas accounting、
BLOCKHASH、receipt 或状态读取顺序变化，都可能使旧 witness 与新执行器不兼容。

每次生成数据时至少记录：

```bash
git rev-parse HEAD > witness.generator.commit
git diff --quiet || echo DIRTY >> witness.generator.commit
go version >> witness.generator.commit
```

每个分发二进制旁也保存 `<binary>.commit`。验收前检查：

```bash
go version -m /data/blockchain/bin/witness-replay
sha256sum /data/blockchain/bin/witness-replay
```

如果输出只有 `mod ... (devel)`、没有 VCS revision，又没有 sidecar commit，版本
兼容性就是未知，不能据此判断 mismatch 是数据损坏还是执行语义漂移。优先用录制
commit 构建旧 binary 复现同一失败块：旧版通过、新版失败才证明是语义漂移；两版
都失败再查 bodies/witness/codes 对齐及传输完整性。

## 4. 三层运行顺序

下面用 `/data/blockchain/witness` 为例。每层都必须先通过，才能进入下一层。

### 4.1 格式 smoke：只证明表能打开

genesis 前 20 万块可以快速发现缺表、坏索引或错误格式，但早期 Ethereum 几乎是
空块，**这个结果不能用于 worker 选择或性能报告**：

```bash
witness-replay \
  --input-headers-bodies /data/blockchain/witness \
  --input-witness /data/blockchain/witness \
  --codes-freezer /data/blockchain/witness \
  --senders /data/blockchain/witness \
  --output /tmp/wr-smoke --no-output \
  --start 0 --end 200000 --workers 32 \
  --gogc 300 --mem-limit-gb 32
```

格式 smoke 也默认 fail-fast，不加 `--continue-on-error`。

### 4.2 正确性 gate：tx-dense 区间，fail-fast

使用已知交易密集且所有输入都覆盖的固定窗口。仓库已有可复现实例
`24,980,000–24,990,000`；更换数据集时可选新的 dense 窗口，但所有 worker 档位
必须使用完全相同的 `[start,end)`：

```bash
witness-replay \
  --input-headers-bodies /data/blockchain/witness \
  --input-witness /data/blockchain/witness \
  --codes-freezer /data/blockchain/witness \
  --senders /data/blockchain/witness \
  --output /tmp/wr-gate --no-output \
  --start 24980000 --end 24990000 --workers 1 \
  --gogc 300 --mem-limit-gb 32
```

验收条件只有一个：进程退出 0 且 `failed=0`。先用 `workers=1` 排除并发噪声，
再用计划中的 worker 数复跑同一区间。任何 mismatch 都停止性能结论。

### 4.3 worker sweep：只在 gate 通过后运行

建议至少跑 `1 8 16 32 64 128`，每档使用同一 binary、输入、dense 区间、GOGC、
memory limit 和空闲主机。报告每档的 `block/s`、`tx/s`、`Mgas/s`、wall time、
`failed`，不能只挑峰值。

不要与 QS 舰队 TPS、ethexec 生成、bpp、全盘校验或其他 CPU/NVMe 重任务并发。
否则结果是整机资源竞争，不是 witness worker scaling。

Linux 已提供可复用入口：

```bash
scripts/witness-smoke.sh   # 0–200,000 格式 smoke，严格 failed=0
scripts/witness-sweep.sh   # dense 单 worker gate，再跑 8/16/32/64/128
```

部署机可用 `install -m 0755` 把它们放进工具目录。两个脚本均默认开启验证、
不使用 `--continue-on-error`，并以唯一 run ID 保存日志和输出目录，不会先
`rm -rf` 旧结果。`START`、`COUNT`、`WORKERS`、`MEM`、`GOGC` 可通过环境变量
覆盖；即使自定义 `WORKERS`，dense gate 仍固定先用 `workers=1` 执行。

## 5. 两个危险参数

### `--continue-on-error`

只用于一次性收集失败分布。失败块会提前返回，`failed>0` 时聚合吞吐包含“少做了
工作”的块，因此性能数字无效。正确性 gate 和正式输出禁止使用。

### `--skip-verify`

跳过 per-block gas 与 receipt-root gate。只允许用于已经另行证明输入/版本兼容后的
纯 CPU profiling，并必须标注“verification disabled”。不得用于生产输出或“重放
成功”声明，也不得用它掩盖已知 mismatch。

## 6. mismatch 分诊

先用相同区间去掉 `--continue-on-error`，抓第一个错误：

```text
witnessreplay: block N: gas mismatch: got X want Y
```

按以下顺序排查：

1. 记录 block、got/want、binary SHA256、binary commit、generator commit。
2. 用 witness 录制 commit 构建 binary，单 worker 重放同一块/小窗口。
3. 检查 header/body/witness/senders 的 items 和 canonical source 是否同源。
4. 检查 code 命中与 `codes.coverage`；用完整 MDBX Code 表做 A/B。
5. 检查该高度生效的 Ethereum fork，以及近期 gas/receipt/BLOCKHASH/state-read 改动。
6. 只有确认是预期的陈旧 witness 语义漂移后，才能为“纯 profiling”使用
   `--skip-verify`；生产数据必须重生 witness 或使用兼容执行器。

## 7. 正式全量重放

正式生成 acctcs/storcs（可选 receipts/witness）必须从可对齐的起点运行，不使用
两个危险参数，并保留完整日志：

```bash
set -o pipefail
witness-replay \
  --input-headers-bodies /data/blockchain/witness \
  --input-witness /data/blockchain/witness \
  --codes-freezer /data/blockchain/witness \
  --senders /data/blockchain/witness \
  --output /data/blockchain/wr-out \
  --start 0 --end 0 --workers 32 \
  --gogc 300 --mem-limit-gb 32 \
  2>&1 | tee /data/blockchain/wr-full.log
```

`workers=32` 只是历史起点，不是所有硬件的默认最优值；必须由 dense-window sweep
决定。全量结束至少检查：退出码 0、`failed=0`、输出各表 head 对齐、末块编号与输入
覆盖一致，然后才能进入 rebuild-state 或发布流程。

## 8. 2026-08-23 误用复盘

- `0–200,000`：200,000 blocks、121,793 tx，约 0.61 tx/block；得到约
  155k block/s。它只证明格式可读，不能作为 Ethereum 并发 EVM 性能。
- 在该轻载窗口跑 32/64/128/192/256 workers 后宣称“32 最优”是无效结论，已撤回。
- 改用 `24,000,000–24,200,000`：200,000 blocks、50,159,640 tx、约
  634 block/s，但 `failed=623`；fail-fast 首错为 block 24,000,022：
  `gas mismatch: got 16980501 want 17009241`。所以这轮是兼容性调查数据，不是
  通过的 benchmark。
- 同期 QS 舰队出现的 faucet/reward/funding 问题与 witness-replay 完全无关；将两条
  线放在同一因果解释中是错误的。

后续只有 dense 区间 `failed=0` 后的 sweep 才能替换这里的临时调查结果。
