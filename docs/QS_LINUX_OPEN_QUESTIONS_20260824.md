# QS Linux 审阅问题清单（待另一位开发答复）

日期：2026-08-24 UTC

仓库：`/home/n42/src/n42/N42-gov5`

用途：在继续舰队压测和 witness 并行重放之前，把本次操作中发现的问题、尚未确认的设计意图、已做修改和操作失误一次写清楚。请熟悉 Windows 周更新工具链、N42 replay-v2、七节点 HotStuff 舰队和 ETH witness 数据格式的开发逐项答复。

本文不包含 BLS、secp256k1 或 faucet 私钥。

## 0. 当前暂停点

- 七节点舰队已全部通过现有 `stop-fleet.sh --no-inspect` 优雅停止，没有使用 `kill -9`。
- 节点 0 最后确认提交高度：`13,539,286`；日志随后在 `2026-08-24 05:04:21 UTC` 记录 HTTP、txpool、HotStuff 和 P2P 正常停止。
- 当前没有运行 `n42` 舰队、`txflood`、`n42-stress`、`bench-run`、`measure-tps` 或 `witness-replay`。
- 没有在本轮得到新的有效 TPS 成绩，也没有开始 witness 硬件 sweep。
- 本文以及 `docs/QS_LINUX_SESSION_HANDOFF_20260824.md` 暂不提交、不推送，等答复后再处理。

## 1. 两种工作负载必须严格分开

### 1.1 N42 七节点舰队

- 自研 N42 链，chainspec 为 `mainnet_qmdb_staggered`，QMDB 状态，7 个本机 HotStuff 验证者。
- 涉及 faucet、sender、nonce、txpool、P2P gossip、baseFee、交易灌注和 TPS。
- 正常周更新数据流应是：停止的舰队 node0 → replay-v2 增量 fold → era seed → 7 个新节点 → 验收 → 压测。

### 1.2 ETH witness replay

- 独立的 Ethereum 历史块 witness 工作负载。
- 并发单位是 block worker，输入是历史 block/header/body/witness/sender/code 数据。
- N42 的 faucet、奖励、sender offset、nonce、txpool 和七节点 gossip 都不能用于解释 witness 结果。

### 1.3 本次曾发生的混淆

我最初曾用 N42 交易压测中的 sender/gas/奖励语境解释 witness 数据，这是错误的。之后已经停止混用，并把两个流程拆开记录。请开发审阅下文时也分别回答 `F/T/H`（N42 舰队）与 `W`（ETH witness）问题。

## 2. 已核准的运行状态与数据

### 2.1 代码和二进制

- 当前本地与 `origin/main`：`c3d8059e fix(txflood): abort partial sender loads`。
- `/data/blockchain/bin/n42`：`5.7.959-6537ed32`。
- n42 SHA-256：`1c0c0dac07b7773f602ddecbe61c43f23b005f0821bd6aee902217d4eaf6ab62`。
- `/data/blockchain/bin/txflood` SHA-256：`e6deb6849073b362d313afc37b7ee351f4b94be0d6543a07d3c80602eb4cd731`。
- 运行目录 `/data/blockchain/scripts-qs/bench-run.sh` 与仓库已推送版本一致。

### 2.2 一次性旧 N42 主网追高和 replay

- `/data/blockchain/mainnet-source` 最终观察到的 source head：`13,497,579`。
- 用户最初提供的旧主网参考高度约 `13,495,953`；实际追高结果以数据库观测值 `13,497,579` 为准。
- `/data/blockchain/qs-replay-linux` target head：`13,536,950`。
- replay 的 source 范围：`13,204,050..13,497,579`。
- replay 统计：293,530 blocks、667,334 tx、`txFailed=0`、24 个 receipt mismatch。
- 用户已说明 24 个 receipt mismatch 是 EIP 规则兼容设计所致，不应直接认定为数据损坏。
- target checkpoint 已指向 target canonical head/hash：
  `13,536,950 / 0x9923b24baf104277f88f4dfdfa842c9c94197099d1ad1f02dcac4f60b1bb3414`。
- `/data/blockchain/qs-era-linux`：12 个 ancient era，sealed end `12,582,912`，deep verify 共抽验 768 块通过；txindex 覆盖至 `13,536,950`。

### 2.3 舰队停机前健康状态

- 运行版本：5.7.959；7 节点；1 秒出块；每节点 `GOMAXPROCS=37`；`--dev.txgen.max 0`。
- RPC `20012..20018`，TCP `32000..32006`，UDP `31000..31006`。
- 停机前最新块为空块，节点各有 6 peers；抽查未见新的 `ValidateState`、root mismatch、panic 或 equivocation。
- 节点 0 最后提交 `13,539,286`，然后优雅停止。

### 2.4 witness 数据

- `/data/blockchain/witness` 约 858 GiB。
- `witness` 与 `senders` freezer：start 0，items `25,765,566`。
- `headerc`、`bodyc` 的 `.cidx` 各 25,168 bytes，即 3,146 个 segment 索引项；现有 reader 报告 `max_block=25,772,032`，但最后一个 segment 可能只传了一部分，不能仅凭该数字确认完整终点。
- `codes`：2,399,937 个地址索引项。
- 未找到来源 manifest，也未找到 `witness.generator.commit`。
- 现有 `/data/blockchain/bin/witness-replay` 的 `go version -m` 只显示 `(devel)`，没有可核对的 VCS revision 或 sidecar。
- 格式 smoke `0..200,000` 已通过：200,000 blocks、121,793 tx、`failed=0`；154,540 block/s 只代表这段稀疏数据的格式读取，不是硬件成绩。
- 之前 `24,000,000..24,200,000` dense 运行有 623 个失败；首个失败块 `24,000,022`，gas mismatch：got `16,980,501`、want `17,009,241`。这轮不能作为性能成绩。

## 3. 需要开发逐项回答的问题

请尽量在每个编号后回答：`设计如此 / 是 bug / 工具使用错误 / 数据错误`，以及应保留、修改或回退什么。

### A. 构建、版本和链身份

#### A1. 自动版本递增的正确发布顺序是什么？

当前 `make n42` / `make build` 会在编译前修改版本文件。因此第一次产出的 binary 可能显示新版本号，但 VCS provenance 仍指向版本提交之前的 commit，且构建时工作树是 dirty。本次做法是：先让 Makefile 改版本并提交，再从该版本提交 clean rebuild，得到 `5.7.959-6537ed32`。

请确认标准流程应是：

1. bump 版本；
2. 单独提交版本；
3. 从 clean commit 编译和安装；

还是应改变 Makefile/链接参数，让一次 `make` 就得到可追溯的最终版本？是否要回退或重做 `468cbe22`、`6537ed32`？

#### A2. 辅助工具如何记录 provenance？

`witness-replay` 等辅助二进制显示 `(devel)`，难以证明它与数据生成器、当前源码或 n42 主程序匹配。是否应统一注入 commit/version，并在安装时生成 `<binary>.commit` / SHA sidecar？

#### A3. 两个 genesis 身份是否确认无误？

- 部署中的旧 N42 mainnet genesis：`0x594aad…`。
- QS 自定义舰队 genesis：`0xa2d2ff…`。

本次曾发现一个 dirty binary 带有不同的 `0x138734…` mainnet genesis，已拒绝并恢复源码中部署身份。请确认以上两个 hash 才是唯一正确身份，并确认 `00cc2bcc` 的 literal identity tests 是否应保留。

### R. replay-v2、旧主网和周更新

#### R1. target 高于 source 的 gap-fill 是否是预期语义？

source head 是 `13,497,579`，target 最终是 `13,536,950`。目前解释为 replay-v2 根据目标链规则补齐 gap，而不是 source 腐坏。请确认：

- 这是 `mainnet_qmdb_staggered` 的预期行为吗？
- gap 的来源和确定规则是什么？
- 验收时应该比较 source head，还是只验证 target canonical head/state root？

#### R2. 24 个 receipt mismatch 的正式豁免规则是什么？

用户已说明这是 EIP 规则设计。请提供可写入工具文档的精确定义：涉及哪些 fork/EIP、哪些字段允许不一致、预期高度或判定签名是什么。工具是否应把这些打印成 `acceptedCompatibilityMismatch`，而不是笼统的 mismatch？

#### R3. checkpoint 必须指向 source 终点还是 target canonical head？

本次修复 `65cda179` 让导出 checkpoint 指向 replay 后 target 的真实 canonical head `13,536,950`。请确认这是长期正确语义；若外部脚本依赖 source 终点，需要怎样兼容？

#### R4. Linux 后续周更新的 canonical source 是什么？

当前理解：

- `mainnet-source -> qs-replay-linux` 只是一次性 Linux bootstrap；
- 以后每周必须先优雅停止七节点舰队，再用停止后的 `/data/blockchain/qs-node0` 作为 source fold 到 `/data/blockchain/qs-replay-linux`；
- 不能再用旧 mainnet，也不能用 ETH witness。

请确认这与 Windows 连续跑几个月的流程完全一致，并给出 Windows 当前实际脚本名、关键参数和 replay source/target 路径。

#### R5. 旧主网追不到链尖时的正式降级策略是什么？

用户要求：若无法继续追高，记录问题，然后从当前可用高度完成 replay 和后续流程。本次最终追到 `13,497,579`。请确认将来 catchup 失败时的最小验收条件：允许停滞多久、至少几个 peer、是否必须核对 finalized/canonical hash、如何在 replay 结果里标记 source 不在链尖。

#### R6. 请审阅本次 replay/同步相关修复

- `65cda179 fix(replay): export target canonical head`
- `4b9d18fd fix(hotstuff): retry state after failed canonical commit`
- `510180fb fix(sync): accept legacy protobuf block chunks`
- `576f5dd7 fix(txindex): synchronize segment reader lifecycle`

这些已经推送，但尚未得到另一位开发的设计确认。请分别判断保留、补测试或回退。

### F. 七节点舰队启动、停止、奖励和 txpool

#### F1. txpool MDBX journal 是否应保存所有 gossip 进来的 pending tx？

`internal/txspool/journal.go` 的 graceful-stop flush 会遍历 pending pool，并写入 MDBX `TxPoolJournal`。观察到多个节点分别恢复大量重叠的历史 txgen pending 交易。问题是：journal 应保存每个节点收到的全部 pending，还是只保存本地提交的交易？

#### F2. benchmark 前清空 journal 是正确做法还是掩盖产品问题？

旧 `bench-run.sh` 只在停机前删除历史文件 `$node/txpool.journal`，但实际 journal 已在 MDBX；优雅停机又会重新写入。实测停止后：

- node0：0
- node1..6：137、136、134、132、142、140
- 合计：821 个未提交 pending

这些条目已用仓库已有的 `txpool-journal-reset` 在全部节点停止时清除。没有改链状态或 committed nonce。`b8dd1f06` 现在让 `bench-run.sh` 先停机、确认全部停止，再清 MDBX journal，然后启动 benchmark。

请确认应：

- 保留这一 benchmark 清理；或
- 增加官方 `--txpool.no-restore` / ephemeral benchmark profile；或
- 修改 journal 只保存 local tx；或
- 完全不清理，让工具处理恢复交易。

#### F3. fresh fleet 的 faucet 资金准备方式是什么？

Linux seed 来自一次性旧主网 replay，不像已运行数月的 Windows fleet 那样积累了大量 faucet 余额。清完 journal、禁用 txgen 后，faucet 实测精确增加 1 ETH/block。标准 3,000 senders × 3,000 tx 预检约需 `1,896.93 ETH`。

请确认 fresh seed 的官方做法：等待空块奖励、replay 时注资、使用既有 funding 工具，还是调小首轮规模？不要引入临时私钥或手工改状态。

#### F4. 每块两个 reward entry 是否符合设计？

空块日志显示 `rewardCount=2`，但 faucet 余额净增 1 ETH/block；另一个应是 validator reward。请确认两个奖励对象、金额和预期账户，避免把设计行为误判成异常。

#### F5. 日常自动交易的实际预期是什么？

原 profile 是 node0-only `--dev.txgen.max 31`，每两秒随机 1..31，理论均值 16。历史抽样最近 100 块平均约 14.47 tx/block、最小 1、最大 43、无空块。请确认这就是 Windows 日常启停时的“自动发交易”，还是 Ubuntu 应复用另一套常驻工具/参数。

#### F6. UDP 端口应固定 31000 还是保留 33000？

Linux 默认 ephemeral range 为 `32768..60999`，33000..33006 会冲突；当前实际运行改用 31000..31006。请确认长期方案：统一改为 31000，还是在部署前永久设置 `ip_local_reserved_ports=33000-33006`。文档与脚本必须只保留一个规范方案。

#### F7. 双签隔离要求是否还缺少防护？

当前 mesh 只绑定/广播 `127.0.0.1`，discovery 关闭，目的是防止 Linux 和 Windows 使用同一批 BLS key 的舰队互相发现并双签。是否还要增加 chainspec/network ID、启动锁、host marker 或 slashing guard，让脚本在非 loopback 地址时硬失败？

#### F8. `N42_TXINDEX_TAIL=1` 是否应由节点默认 profile 强制？

已知不设置时每笔 tx 会对 MDBX 写随机 key，历史对比约为 22.8k tx/block 时 1.9 s/block，而 tail 模式约 0.5 ms。当前脚本设置环境变量。请确认 benchmark / QS chainspec 是否应在程序内强制，避免手工启动漏设。

### T. txflood 的 nonce、批量 RPC、P2P 和供给机制

#### T1. 当前 nonce/sharding 策略是否就是 Windows canonical 策略？

当前工具逻辑：

- faucet funding 使用连续 nonce，只向 node0 RPC 提交；依赖六 peer mesh gossip；
- 每个派生 sender 的完整 nonce chain 固定路由到一个 RPC（`-shard-senders`）；
- 不把同一 sender nonce chain 同时广播到 7 个 RPC；
- 每轮使用从未用过的新 sender offset。

请确认 Windows 最好成绩用的就是这一策略，并提供准确脚本/命令。若不同，请说明差异。

#### T2. nonce probe 失败是否必须 fail-fast？

旧代码在一个 sender 的 nonce 查询重试 5 次仍失败后只记 warning，继续生成任务，导致 raw transaction 槽可能为空，后续“供给不足”统计被污染。`c3d8059e` 已改成任一 sender nonce probe 失败就退出。

请确认应保留 fail-fast，还是应只跳过该 sender、降低总发送数并明确标记 degraded result。

#### T3. `BatchRawTransaction` 的部分成功如何统计？

节点批量接口可能已接受 batch 中一部分交易，但只返回一个 `firstErr`。txflood 当前遇到该错误可能把整批计为失败，造成 submitted/failed 统计不准确，也无法只重试失败项。

请确认 RPC contract 是否应返回逐交易结果；在未改 RPC 前，txflood 应如何准确确认 accepted 数量，避免重复发送已接受 nonce？

#### T4. 同一 sender 的多个并发 batch 是否允许乱序到达？

即使完整 nonce chain 固定到同一 RPC，多个 goroutine 仍可能让较高 nonce batch 先到。正常 txpool 可 queue/promote，但在 pool-full 时会出现 nonce hole 和 demote spiral。请确认 canonical 工具是否保证每 sender 串行、只跨 sender 并发；如是，Linux 工具应按 sender queue 重构。

#### T5. 标准压测是 open-loop 还是 `target-depth` closed-loop？

现有默认一次预签/提交 9,000,000 笔，而节点 pool 约 300k/100k，旧运行出现大量 `pool full`。txflood 代码已有 `-target-depth`，但当前 benchmark HTTP API 没有开放 `txpool` namespace，无法使用该闭环机制。

请确认 Windows 最好成绩使用：

- open-loop 9M；
- `txpool_status` target-depth；
- 或另一套供给工具/反馈机制。

若用 target-depth，QS benchmark 是否应只在 loopback 开启 `txpool` RPC？

#### T6. funding 完成的唯一有效判断是什么？

旧输出曾用 faucet nonce 前进作为 funding 成功依据，但 batch 部分失败时 nonce 前进不能证明所有 3,000 sender 均到账。当前代码已有余额预检、receipt verification 和 funding timeout。请确认必须验收：每笔 funding receipt 成功、所有 sender balance 到账、连续 nonce 无缺口，然后才允许进入 flood。

#### T7. 历史 Linux r1-r4 是否全部应标记 invalid？

- r1：funding nonce 从 21 起，约 545,100 submitted / 8,454,900 failed，并有 pool-full、insufficient funds。
- r2：0 submitted / 9,000,000 failed。
- r3、r4/v957：funding 阶段中止。

当前判断是这些不是“最好成绩”，也不能拿来调硬件。请确认是否全部作废；如果其中有一轮仍可解释，请给出判定依据。

#### T8. Windows 最好成绩和完整参数请提供原始依据

交接信息记录的 Windows 可比 win1 为 22,487 TPS、98.4% occupancy、55/60 full blocks。请提供对应原始日志和当时的：commit/version、block gas limit、block interval、节点 CPU 数、pool 配置、sender/tx 数、gas price、batch、concurrency、nonce/sharding、供给模式、decay 时间和 sender offset。

#### T9. `measure-tps.sh` 的失败语义是否需要修？

审阅发现以下疑点，但还没有修改：

- 没有对每个 block RPC response 做严格的 `error/result` 验证；
- awk 在输入行异常时可能沿用上一行变量；
- RPC readiness 失败后可能留下 benchmark fleet；
- 某些测量失败路径可能仍返回 shell success；
- 结果没有自动保存为带版本、配置、offset 和窗口数据的 artifact。

请确认这些是否属实，并确定由现有完整工具链中的哪个脚本负责。之前准备过一版临时修改，但已全部回退，避免在得到答复前自行创造替代工具。

### H. Linux 硬件压测和优化

#### H1. 37 CPU/节点是正确 baseline 吗？

硬件：AMD EPYC 9B45，128 physical cores / 256 threads，1 socket、1 NUMA node、16 个 L3 domain、512 MiB L3、约 136 GiB RAM。当前 `(nproc+6)/7 = 37`，七节点共 259 个 Go logical CPUs，略超 256 threads；这是按 Windows 32 threads 跑 7×5=35 类比得出。

请确认 baseline 应用 37、36、18/19 个 physical core，还是别的分配。应以 logical thread 还是 physical core 作为 `GOMAXPROCS` 基准？

#### H2. 是否需要绑核/L3 domain？

单 NUMA 不代表无拓扑成本；本机有 16 个 L3 domain。请给出推荐的七节点 CPU affinity/CCD 分配，或者明确先不绑核。不要在拿到正确供给 baseline 前把拓扑优化和工具错误混在一起。

#### H3. 磁盘参数是否需要改变？

`/data` 是约 7 TB Intel NVMe、XFS、scheduler `none`、`nr_requests=1023`、read-ahead 128 KiB。请确认 QMDB/MDBX 七节点压测是否需要调整 XFS mount、queue depth、read-ahead、I/O priority 或 MDBX 参数；若需要，请给出一次只改一个变量的矩阵。

#### H4. 内存与 Go GC 的约束是什么？

总内存约 136 GiB，七节点再加 txflood 预签 9M 交易可能使 memory pressure 成为隐含瓶颈。请给出每节点预期 RSS、txflood 预签文件/内存模式、GOMEMLIMIT/GOGC 是否应设置，以及 OOM 前的硬停止阈值。

#### H5. 有效舰队成绩的最低门槛是什么？

当前约定：只比较同序号窗口，尤其 win1；occupancy 低于约 95% 说明供给管道不足，不算链 TPS；baseFee 在轮内上涨使后续窗口受 gas price ceiling 影响。请确认正式验收字段和阈值，包括 full blocks、occupancy、TPS、p50/p95 commit、错误数、pool depth、CPU、RSS、disk latency 和七节点一致性。

### W. ETH witness 数据与 block-worker sweep

#### W1. 858 GiB 数据是否来自同一 canonical source？

缺少 manifest，当前无法证明 `headers/bodies/witness/senders/codes` 来自同一生成批次、同一 Ethereum canonical chain 和同一终点。请提供生成 manifest、各目录/segment 数、源链标识、终点 block/hash、生成器 commit 和校验和。

#### W2. 数据的精确可重放 block 范围是多少？

`witness/senders` items 是 `25,765,566`，而 compact header/body reader 报 `max_block=25,772,032`。请说明两个终点不同是否正常，以及如何验证最后一个 partial segment，而不是按 index 文件大小推断。

#### W3. 应使用哪个 witness-replay commit/binary？

现有 binary 无 VCS provenance。请提供与这批数据匹配的源码 commit、构建标签、版本和 SHA；确认是否需要本机重编译，再跑 correctness gate。

#### W4. 24,000,022 的 gas mismatch 原因是什么？

首个失败为 got `16,980,501`、want `17,009,241`，总计 623 failures。请判断：

- witness/header/body/sender/code 数据混批；
- replay EVM fork/EIP 配置不匹配；
- generator/replayer 版本不匹配；
- reader 边界或并发 bug；
- 或该范围本身不受支持。

在解释并修复前，不应对该结果做硬件性能分析。

#### W5. 为什么计划 gate 选择 `24,980,000..24,990,000`？

修订后的 `witness-sweep.sh` 计划先用 workers=1 跑此 dense 段，通过 `failed=0` 后才 sweep 8/16/32/64/128。请确认这段已知应通过、覆盖哪些 fork/交易密度，以及它是否足以证明 24M 失败已解决。若无已知依据，请给出 canonical correctness range。

#### W6. `codes` index 完整性如何验收？

目前只有 2,399,937 address items 数量，没有 coverage 证明。请提供缺 code、hash mismatch、地址索引错误的预检工具或抽验办法，并说明 code DB 是否允许 lazy miss/外部回源。

#### W7. witness sweep 的正式参数是什么？

当前草案是 workers `1 gate -> 8/16/32/64/128`、`GOGC=300`、memory 96 GiB、每轮独立输出目录、任一 failed 立即终止、绝不 `--continue-on-error`。请确认：

- worker 列表和 range；
- 内存/GOGC；
- 是否还有 I/O worker、decode worker 或 cache 参数；
- 每轮预热、重复次数和有效指标；
- 正确性通过后才能计性能是否为硬要求。

#### W8. witness 与舰队能否并行？

用户说 witness 本身是并发 replay ETH blocks；这里的“并行”可能有两层含义：witness 内部多 block workers，以及 witness 与 N42 七节点同时运行。当前选择是不同时压满同一机器，以免 CPU/I/O/memory 相互污染。请确认是否有必须并跑的业务场景；若有，分别给资源配额和隔离办法。

## 4. 本次操作中需要透明说明的失误与风险

### O1. 曾混淆 N42 舰队与 ETH witness

这是最重要的语义错误。现在文档和脚本说明已经明确分开，但请开发检查现有 `docs/` 是否还有把 sender/nonce/faucet 用于 witness 的残留描述。

### O2. sandbox 看不到宿主进程，曾导致 PID 判断错误

普通受限 shell 的 `pgrep` 看不到宿主舰队，但能读写共享日志和数据。我曾在该视图下误以为旧舰队已停止，并尝试启动 `txgen=0` profile，覆盖了 node0/node1 的 pidfile；宿主原进程实际仍在。之后通过宿主级检查找回原 PID，全部发送 SIGINT 并确认优雅停止，没有硬杀。

需要决定：运维脚本是否应校验 RPC 端口占用、数据目录 MDBX lock、进程 executable/start-time，而不能只相信 pidfile。建议开发给出 canonical 防重入方案。

### O3. 旧 benchmark 脚本清错了 journal 位置和顺序

这直接造成“舰队一直很顺滑但 Linux 表现异常”的主要假象：恢复的旧 pending tx 在 txgen=0 时仍持续扣 faucet。当前已修复，但 F1/F2 的设计问题仍需确认。

### O4. 在有效供给出现前没有硬件成绩

资金不足、journal 残留、batch 部分失败和 nonce probe 降级使旧 r1-r4 无法代表链性能。不能在这些数字上调 CPU、网络或存储参数。

### O5. 避免自造工具链

用户明确要求复用已经跑过数月的完整 Windows 工具链并适配 Ubuntu。后续不应另写一套逻辑相似但行为不同的压测器。应先取得 Windows 当前脚本和原始最好成绩配置，再只做必要的路径、shell、端口和硬件参数适配。

## 5. 已推送修改，请决定保留还是回退

| Commit | 修改 | 当前判断 | 请开发决定 |
|---|---|---|---|
| `283071f6` | 强制 5.7.956 版本一致 | 修复当时版本漂移 | 保留/回退 |
| `468cbe22` | 编译前自动递增版本 | provenance 流程有 A1 疑问 | 保留/重做 |
| `58b312d9` | 加固 QS 周更新/压测流程 | 需与 Windows canonical 脚本逐项比对 | 保留/修改 |
| `65cda179` | replay 导出 target canonical head | 看起来符合本次 target 语义 | 保留/兼容 |
| `4b9d18fd` | canonical commit 失败后重试 state | 涉及共识关键路径 | 必须深审 |
| `510180fb` | 接受 legacy protobuf block chunk | 为旧主网同步兼容 | 保留/限制范围 |
| `576f5dd7` | txindex segment reader lifecycle 加锁 | race 测试通过 | 保留/补压测 |
| `00cc2bcc` | 固定部署 genesis identity 测试 | 防止再次换错 genesis | 保留/校正 hash |
| `91012699` | witness fail-fast benchmark 文档 | 分离 correctness/performance | 保留/按答案修订 |
| `6537ed32` | 版本 5.7.959 | 当前安装版本 | 保留/重发版 |
| `58d49032` | witness sweep 先单 worker gate | 尚未跑 dense gate | 保留/改 range |
| `b8dd1f06` | benchmark 前停止并清 MDBX journal | 已消除 821 条恢复 pending | 按 F1/F2 决定 |
| `c3d8059e` | nonce probe 失败即中止 | 防止 partial sender load | 按 T2 决定 |

没有把尚未确认的 `measure-tps` 结果保存/cleanup 修改留在工作树；那一版临时修改已回退。

## 6. 证据位置

- 本次完整会话交接：`docs/QS_LINUX_SESSION_HANDOFF_20260824.md`
- replay 统计：`/data/blockchain/qs-replay-linux/replay_stats_20260823.json`
- replay target checkpoint：`/data/blockchain/qs-replay-linux/checkpoint.json`
- seed checkpoint：`/data/blockchain/qs-era-linux/checkpoint.json`
- 当前节点日志：`/data/blockchain/qs-node0..6/log/n42.log`
- 无效 flood 记录：`/data/blockchain/bench-flood-r1..r4.out`、`.err`
- 保留的无效节点代次：`/data/blockchain/qs-node*.r2-invalid-20260823`、`qs-node*.r3-invalid-20260823`
- witness smoke：`/data/blockchain/wr-logs/smoke-20260824T044433Z-2264438.log`
- 运行脚本：`/data/blockchain/scripts-qs/`
- witness 脚本：`/data/blockchain/bin/witness-smoke.sh`、`witness-sweep.sh`
- 代码提交：见第 5 节以及 `git log --oneline`。

## 7. 希望开发优先回答的阻塞项

若时间有限，请先答这 10 项，答完才能安全继续：

1. `R4`：Windows 周更新的真实 source/target 和脚本参数。
2. `T1/T5/T8`：Windows 最好成绩使用的 nonce、sharding、供给闭环和完整原始配置。
3. `F1/F2`：txpool journal 的设计范围以及 benchmark 是否应清空。
4. `T3/T4`：批量 RPC 部分成功、同 sender batch 顺序的正确 contract。
5. `F3/F4`：fresh fleet faucet 准备和双 reward 是否符合设计。
6. `R1/R2/R3`：gap-fill、EIP receipt mismatch 和 checkpoint 的正式语义。
7. `A1`：自动 build 版本的标准发布流程。
8. `W1/W2/W3`：witness 数据 manifest、精确范围和匹配 binary。
9. `W4/W5`：24M gas mismatch 原因和正确 dense gate 区间。
10. `H1/H2/H5`：Linux baseline CPU 分配与有效成绩标准。

收到答复前，建议保持当前状态：七节点停止、witness sweep 不启动、旧数据代次和日志不删除、无新版本提交。
