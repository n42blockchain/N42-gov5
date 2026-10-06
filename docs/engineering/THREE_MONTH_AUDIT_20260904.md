# 最近三个月代码审计与 n42-rs 对照

审计开始日期：2026-09-04，后续验证延续至9月5日。编号问题、修复和验证持续更新；历史回填的迁移与重组边界、完整独立重放和旗舰性能验收仍有未闭环项。本报告不是全仓安全认证。

## 1. 范围与方法

- 时间窗口：2026-06-04 00:00 UTC 至审计开始时的 HEAD。
- Go HEAD：`4533358fc28db215a12b78577968037ee4fec33d`，分支 `qs/block-time-budget`。
- 窗口前基线：`909f6cec52428c45b1c411375c488ac464bb3006`。
- 窗口内日志计数：1,176 个提交；基线到 HEAD 的净差异涉及 1,282 个文件，新增 177,093 行、删除 16,672 行。其中 Go 非测试文件 730 个、Go 测试文件 407 个。
- Rust 参考：固定检出 `build/perf7/_n42-rs-reference`，commit `48263495dd5783ca946cb740340405b0dddfd911`；已使用固定二进制、独立目录和端口运行七节点对照，详细条件见性能报告。其 `SECURITY_AUDIT.md` 日期为 2025-12-20，不能作为本窗口实现已经安全的证据。
- 按提交和变更规模建立清单，再围绕不可信消息、投票前置条件、状态包装器、历史查询、证明解析检查调用链。先加入回归测试并观察旧实现失败，再修复；已有跨客户端向量用于验证格式和签名域兼容性。
- 后续压测和只读数据库核对均使用本任务创建的目录及集群；测量、停机、重启和审计证据见 `FLEET7_PERFORMANCE_TARGET_20260904.md`。改动尚未提交或推送。

重点变更规模如下。数量为净差异文件数，包含该目录内测试。

| 目录 | 变更文件 | 本轮覆盖方式 |
| --- | ---: | --- |
| `lib/qmdb` | 52 | 证明验证深入审查；增量加载、undo、持久化源码抽查及整包测试 |
| `modules/state` | 82 | QMDB reader、枚举、历史查询调用链深入审查及整包测试 |
| `internal/consensus` | 48 | HotStuff PrepareQC、两阶段投票、签名域深入审查及 hotstuff 整包测试 |
| `internal/api` | 49 | 历史状态 gate、证明 API 对接审查及整包测试 |
| `internal/sync` | 43 | 区块范围、RLP fallback、Snappy 长度边界抽查及整包测试 |
| `internal/vm` | 49 | 状态碰撞与 guest gas 相关调用链抽查及整包测试 |
| `internal/txspool` | 24 | 变更筛查及整包测试 |
| `internal/miner` | 11 | seal、push-before-write、builder 结果复用路径抽查 |
| `internal/mobileverify` | 36 | coordinator 的相位与并发保护抽查；网络测试受沙箱限制 |
| `internal/ethel` | 92 | 变更筛查；部分网络测试受沙箱限制 |
| `internal/cl` | 73 | 变更筛查，未逐行审计 n42el 构建标签下全部代码 |
| `internal/distributed` | 62 | 变更筛查；网络测试受沙箱限制 |

没有将上述所有文件描述为逐行审计。ZK 在本窗口只有少量提交，检查了 compact 编码迁移、累计 gas 以及 verifier 的后端边界，并运行 prover/guest/verifier 测试；未验证真实 SP1 证明系统的密码学实现。

## 2. 已修复发现

### A1 — P1：PrepareQC 外层消息没有绑定内层签名证书

位置：`internal/consensus/hotstuff/proposal.go`，`processPrepareQC`。

旧代码仅检查外层 `View` 等于当前视图，再验证内层 QC 的签名；最终却对外层 `BlockHash` 和视图签 CommitVote。攻击者无需伪造签名，只需将已有有效 QC 放入不同区块或不同视图的外层消息，就能使节点为证书没有授权的对象投票。两阶段模式还可能把这种消息存入 `pendingCommitQC`。

修复：在验签和状态变更前，要求 `pqc.QC.View == pqc.View` 且 `pqc.QC.BlockHash == pqc.BlockHash`。错误消息不改变锁、投票记录或待处理提交。

Rust 依据：`../n42-rs/crates/n42/h2-consensus/src/protocol/proposal.rs` 的 `process_prepare_qc` 已有这两项检查。Go 的 Decide 路径也已有同类检查，PrepareQC 是遗漏点。

回归：`TestPrepareQCRequiresEnvelopeBinding`、`TestPrepareQCMatchingEnvelopeStillVotes`。有效但不匹配的 QC 在旧实现中被接受，在修复后被拒绝。**归因注意**：缺失检查可追溯到 2026-03-10，属于本窗口修改过的调用链中发现的存量问题，不能说成这三个月新引入。

### A2 — P1：两阶段 R2 投票没有补查区块父子关系

位置：同文件的 `processPrepareQC` / `onBlockImported`。

两阶段 R1 在区块尚未导入时投票。后来导入的块可能不延伸 Proposal 的 JustifyQC；旧 R2 路径只检查“已经导入”，没有补查 `extendsJustify`。由于 R1 已投过票，`onBlockImported` 尾部的 R1 检查也不会再执行。

修复：两阶段模式在导入完成、签 CommitVote 前检查已有的父块约束，覆盖直接收到 PrepareQC 和恢复 `pendingCommitQC` 两条路径。保持原有未知父块/未知 justify 的处理语义，本轮没有把它升级为新的协议规则。

回归：`TestTwoPhaseCommitChecksImportedParent` 使用真实签名 Proposal/QC，覆盖 QC 先于/晚于 import，以及正确/错误父块四种组合。两个错误父块组合在旧实现中均签出了 CommitVote。

相关窗口提交：`1842c367`，2026-07-10 引入可选两阶段投票。Rust 的 proposal extends 约束提供对照，但这项修复是对 Go 特有 R1/R2 时序的补全，不是逐行移植 Rust。

### A3 — P1：QMDB reader 包装器丢失存储枚举能力

位置：`modules/state/commitment/qmdb_state_reader.go`。

包装器的注释承诺委托 `ForEachStorage`，实际却没有实现它。`IntraBlockState` 因此退回有限槽位探测：只有 slot 42 有值的账户会被误判为空存储；SELFDESTRUCT 也可能遗漏未触碰槽位的 commitment 删除。`verify` 模式同样受影响，不能因为点读仍来自 PlainState 就认为它不改变执行行为。

修复：实现 `ForEachStorage`，完整转发枚举、回调返回值和错误；底层不支持时返回 `state.ErrNoStorageEnumeration`，保留原有 fallback 语义。

回归：`TestQMDBReaderPreservesStorageCollision` 覆盖 off/verify/on 三种包装器模式，验证真实 PlainState 中 slot 42 的碰撞检测及 SELFDESTRUCT 后 QMDB 槽位删除；`TestQMDBReaderEnumerationErrors` 验证错误和不支持能力的传播。

相关提交：`bcb9b641`，2026-09-04。Rust 参考 `qmdb-reth/src/changes.rs` 明确讨论了存储 wipe 与枚举的要求，但两端执行器对 SELFDESTRUCT 的前提不同，不能直接照搬 bundle 处理。

### A4 — P1：落后的历史索引会污染 marker 之前的查询

位置：`internal/api/api.go`，`deferredRefusesQuery`。

旧 gate 在查询高度不大于 marker 时放行。实际 `state.GetAsOf` 要找的是**查询高度之后的第一次修改**，未找到就回退当前值。例如 head=1000、marker=500，账户在 900 从 nonce 1 改为 2，而 900 尚未建索引：查询区块 100 仍错误返回 nonce 2。

修复：历史查询必须等索引覆盖当前数据库快照的 head；head 与 marker 都从同一 `kv.Tx` 获取。无法获取 head 时拒绝；快照的当前状态继续可读。相应修正 backfiller、rawdb、开关及启动日志中的错误说明。

回归：`TestDeferredHistoryRejectsMissingTail` 使用真实 changeset 和历史 reader，复现错误 nonce；补齐索引后验证可恢复 nonce 1。`TestDeferredRefusesQuery` 覆盖 marker 之前、正好 marker、之后、缺失 marker 和 latest。

Rust 对照：`crates/storage/provider/src/providers/state/historical.rs` 的 `HistoryInfo::InChangeset/InPlainState` 同样明确使用之后的修改和当前值回退。相关 Go 提交：`116fa879`、`3b2aef41`，2026-09-04。

**限制**：修复依赖 marker 本身诚实地描述索引覆盖；下文 R1/R2 尚未解决。索引持续追不上 head 时，历史 RPC 的可用性会下降，这是避免返回错误状态所需的行为变化。

### A5 — P2：QMDB 证明槽位可重标记，Go/Rust 解析规则不同

位置：`lib/qmdb/proof.go`。

旧验证器只消费 UpperPath 对应的低位，忽略剩余 twig ID 高位。一棵只有一个 twig 的树，其 slot 0 的有效证明改成 slot 2048 后仍通过。另有尾随字节被忽略、超长 UpperPath 被接受、32 位目标上 valueLen 转 int 可能溢出的边界。

修复：检查上层路径耗尽槽位高位；twig ID 保持 uint64；将路径上限与 Rust 对齐到 64；拒绝尾随字节；长度比较在 uint64 中完成后才转换 int；nil proof 返回 false。新增 `VerifyEncodedProofForKey`，供不可信 RPC 消费者同时绑定查询 key；保留原接口的 membership 语义。

Rust 依据：`../n42-rs/crates/n42/twig-core/src/qmdb_compat.rs`，`QmdbProof::decode`、`verify_for_key`。

回归：`TestProofRejectsRelabelledSlot`、`TestProofCodecRejectsTrailingBytes`、`TestProofCodecRejectsOversizedUpperPath`、`TestProofCodecRejectsOverflowingValueLength`、`TestEncodedProofBindsRequestedKey`；有效单 twig、多 twig 和现有跨客户端向量仍通过。

影响边界：发现证明位置的可塑性和解码不一致，**没有证明可由此伪造任意 key/value 或余额**。严格解码会拒绝以前被忽略的尾随数据；本仓正常 Marshal 输出不受影响。旧客户端仍须自行升级验证器。

### A6 — P1：HotStuff 规范链提交没有通知跟随节点的交易池

七节点 broadcast-07 实测中，规范链持续收录交易，但 pending 长时间停在 407600，
导致深度限流停止供给。代码中 `ChainHighestBlock` 唯一发送点是 miner 的本地封块；
跟随节点通过 `CommitToCanonicalWith` 提交别人的区块时没有发布该事件。
交易池、new-head 订阅及 gas-price 缓存依赖该事件，因此只能等本节点再次封块刷新。
同时，本地封块在 HotStuff 中尚属候选，不应提前作为规范链头通知。

修复：在规范提交事务成功、`currentBlock` 更新后发布事件；移除 HotStuff miner
的候选事件，保留计时封块引擎原有行为。重复 QC、失败提交不产生事件。
`TestCommitToCanonicalAdvancesBlockAndHeaderHeads` 验证跟随提交、重复和失败情形；
internal/miner/txspool 完整测试及提交路径 race 检查通过。七节点效果见性能报告。
相关近期提交：`ff1321a3d`（2026-07-17）增加规范提交 callback，却未接通现有 head event。

### A7 — P2：真实提交未训练自适应 pacemaker

满块执行超过基础 6 秒超时后，baseline-02 出现持续超时和第三窗口停滞。
`ObserveCommitLatency` 原本仅有测试调用，运行中的自适应估算器一直缺少样本。
现于已形成 CommitQC 的视图推进时、重置计时状态之前记录正耗时；未提交视图
不训练。保留 6 秒基础值和 30 秒最大值，真实引擎提交测试与 race 检查通过。
adaptive-03 的三个窗口均推进，但吞吐尚未对齐 Rust，不能据此宣称消除所有活性问题。

### A8 — P2：候选块回滚逐项扫描整份待回收列表

`lib/qmdb/revert.go` 的恢复循环对每个旧槽位扫描 `deadFlushed`，使一次回滚
具有 O(恢复条目数 × 待回收条目数) 的开销。`git blame` 指向 2026-07-08
的 `c719417d2`，属于本次三个月范围。该路径在矿工复用独立 QMDB 计算器时也
会执行，不仅发生在链重组中。memory-15 CPU 采样中 ApplyUndo 为 3.89 CPU 秒，
占 10.43%。

改为建立恢复槽位集合，再单次过滤回收列表。保留无关的待删除条目，恢复条目不能
被后续 FlushTo 删除；错误预检、索引恢复、边界 twig 和根哈希算法保持原有语义。
新回归测试覆盖跨 twig 的覆盖写、删除、块内再次删除、无关回收项以及回滚后落盘
读取。QMDB 和 commitment 测试通过。均匀分布键的 163,000 项回滚基准中位数从
4.308 秒降至 25.34 毫秒，但增加约 4.7 MB 临时集合空间；这不是集群 TPS 数值。

### A9 — P2：undo 解码器按未验证的条目数分配

`UnmarshalBlockUndo` 读取 count 后直接 `make([]UndoEntry, 0, count)`。仅几个
字节的损坏记录即可声明 MaxUint64 项，导致 makeslice panic 或大额分配，而不是
返回解码错误。输入来自持久化 undo 记录；本次未声称已发现远程写入这些记录的
攻击路径。新版按每项至少 34 字节检查剩余数据，再分配。v1/v2 最短合法记录
和超大计数回归通过，10 秒 fuzz 执行 324,205 次后通过。QMDB、commitment、
rawdb、API 全套定向测试通过；该修改发生在 undo-16 二进制构建之后，不混计为
那轮压测使用的代码。

### 关联发现：binary ingest 限流统计顺序错误

实际交易池 Stats 返回地址数、交易数交错的四项；ingest 适配器原样转发，
而入口将第一项解释为交易数。少量发送者的高深度负载因此几乎触发不了 hardcap。
修正四项映射并加入 7 地址/700 交易的回归，node 和 ingest race 测试通过。
`git blame` 指向 2026-03-21 的 d066b650d，早于本次三个月范围，因此作为
关联路径发现记录，不计入近期提交引入的问题。入口默认关闭，binary-17 显式
在七个 loopback 端口启用并完成状态根、收据及完整窗口交易构成核对。

## 3. 已测量优化：签名消息一次分配

Rust `h2-primitives/src/consensus/h2_v4.rs` 使用固定 56/88/120 字节数组。Go 原实现先分配 56 字节，再 append hash，Proposal/Commit 连续扩容两次。本轮保留 Go API，在构造域前缀时一次预留完整 payload 容量；没有改变签名字节、域、链身份或共识格式。

`BenchmarkH2V4SigningMessage` 在同一工作区前后各运行三次；Go 1.26.0、linux/amd64、AMD EPYC 9B45。下表时延为三次中位数。

| 消息 | 原 ns/op | 优化后 ns/op | 原 B/op → 新 B/op | 原 allocs/op → 新 allocs/op |
| --- | ---: | ---: | --- | --- |
| Proposal | 126.4 | 45.81 | 400 → 128 | 3 → 1 |
| Vote | 63.48 | 35.79 | 176 → 96 | 2 → 1 |
| Commit | 124.2 | 44.00 | 400 → 128 | 3 → 1 |
| Timeout | 29.41 | 28.75 | 64 → 64 | 1 → 1 |
| NewView | 29.37 | 28.86 | 64 → 64 | 1 → 1 |

分配减少是稳定结果；共享机器上 ns/op 有噪声，未进行集群 A-B-A 吞吐试验。这不是整链 TPS 提升的证据。验签前检查消息绑定和证明路径也减少无效输入的后续计算，但没有把它们量化为吞吐收益。

## 4. 未闭环事项与后续优化顺序

### R1 — 高优先级：历史索引 coverage 缺少持久化来源证明

`HistoryBackfiller.seedMarker` 将“没有 marker”解释为“以前一直 inline 建索引”，直接写当前 head。然而 `N42_NO_HISTORY_INDEX` 期间不会留下这样的来源证明；从该模式切入 deferred、某些外部快照导入或 marker 损坏，不能据此推出历史完整。现有 `TestFirstStartSeedsTheMarkerAtHead` 甚至在没有任何索引行的测试数据库上写入 5,000,000，它验证了 seeding 行为而非完整性。

此外，退出 deferred/disabled 模式后 API gate 只看当前进程开关，不能证明旧数据库没有遗留缺口。这是代码可确认的前提缺失，不是本次修复后已满足的条件。需要一套写入路径维护的持久 coverage/模式迁移协议、旧库验证/修复工具及相应回归；本轮未自动迁移或修改任何链数据。

### R2 — 高优先级候选：backfill 的读写快照与重组一致性

`step` 在 View 事务扫描 changeset，随后在另一个 Update 事务写索引和 marker；中间没有校验 canonical hash/generation。marker 只有高度，且搜索不到 unwind 对它的更新。读写间重组、已有 marker 之后的同高度替换、源 changeset 已裁剪等场景需要验证。

“索引行和 marker 在一个事务”只证明这两者同时提交，不能证明它们描述的是写入时的 canonical 分支。尚未完成并发重组/裁剪集成复现，故列为待验证风险。下一步应让覆盖声明绑定分支，并同 unwind/pruner 的数据保留边界一起测试。

### R3 — 中优先级：QMDB verify 模式的零 mismatch 不是完整健康证明

`ReadAccountData` 只在 QMDB 与 plain 两边都无错误时计 mismatch；QMDB 解码失败但 plain 成功会被跳过。因此单看 mismatch=0 不足以准许停写 PlainState。需要独立统计解码/读取错误，并把完整性、枚举和 fork 规则纳入准入指标。早期仅修复枚举丢失；第27轮后进一步把任一侧读取/解码错误计作失败比较，包含在 mismatch 中，并增加关机最终汇总。这样报错不会被误读为零差异；仍需完整性和fork范围验证，不能据此停止写入PlainState。

### 性能路线的取舍

1. 优先闭环状态和历史覆盖的一致性，再考虑减少 PlainState 随机写。不能因为短时点读对比没有 mismatch 就停写 Account/Storage。
2. Go 已有 `LoadIncremental` 和 undo；Rust `qmdb-reth/src/node_state.rs` 采用 checkpoint + delta log。两者存储事务模型不同，本轮保留 Go 的“增量恢复后核对持久 root，失败退回完整加载”保护，没有机械替换成 Rust 的文件日志。
3. Rust 的 direct-import、builder queue、已执行结果复用有价值，但必须分别证明 parent、交易根、执行配置、交易回队和落盘失败恢复。Go 现有 push-before-write 保持 Proposal 在成功写入之后，本轮没有改默认开关或搬迁整段执行路径。
4. 接下来的吞吐测量应固定写集合、同机负载和 block size，记录 commit、MDBX dirty bytes、执行、传播、R1/R2 延迟；不能将 Rust fleet 的绝对 TPS 当成此 Go 节点的预期收益。

## 5. 验证记录

2026-09-04 21:20 UTC 补充：开放网络权限后的最新全仓 `make -o version-build build`、
`make test-short`（250 个测试包通过）、`make race-core`（vm/state/internal/sync）
和 `make vet` 均通过。`make lint` 因未安装 golangci-lint 退出 2，未记为通过。
七节点诊断已完成多轮状态根及收据抽查，但尚未达到 n42-rs 旗舰性能，详细参数、
结果和后续优化见 [七节点性能报告](FLEET7_PERFORMANCE_TARGET_20260904.md)。
下方为此前受限执行环境阶段的历史记录，不代表最新测试状态。

命令统一使用 `GOCACHE=/tmp/n42-gov5-audit-gocache GOPROXY=off GOSUMDB=off`，复用本机依赖、不安装新工具。

| 检查 | 结果 |
| --- | --- |
| `make -o version-build build` | 通过；仅跳过与审计无关的版本号自增副作用，执行原 Makefile 全仓 build 配方 |
| `make vet` | 通过，全仓 `go vet ./...` |
| 核心整包 `go test -short` | `lib/qmdb`、`internal/consensus/hotstuff`、`modules/state`、`modules/state/commitment`、`internal/vm`、`internal/parallel`、`internal/zkprover/...`、`internal/zkverifier/...` 通过 |
| 扩展整包 `go test -short` | 上述相关包及 `internal/api`、`internal`、`internal/sync`、`internal/txspool` 通过 |
| 定向 `go test -race -short` | hotstuff、qmdb、commitment、api 的新增回归及 H2-v4/Proof 测试通过 |
| `make test-short` | 最终运行 239 个包通过，11 个包因沙箱禁止 socket/listen 失败，不能记为全绿 |
| `make lint` | 未执行 lint 本体：本机缺少 `golangci-lint` |
| `CGO_ENABLED=0 GOARCH=386 go test ./lib/qmdb -run 'TestProof\|TestEncodedProof'` | 未运行：间接依赖 mdbx 无可用非 CGO 实现；32 位实际运行验证仍待补齐 |
| 签名构造 benchmark | 前后各三次，分配结果见第 3 节 |
| `gofmt` / `git diff --check` | 修改的 Go 文件格式化，差异空白检查通过 |

临时证据位于 `/tmp/n42-gov5-audit-*.log`：`repro`、`repro-extra` 保存旧实现失败，`core`、`extended`、`race` 保存通过结果，`bench-before/after` 保存完整基准输出。证据日志不纳入源码提交。

全仓最终失败包为 `accounts/abi/bind`、`internal/distributed/messaging/stream`、`internal/distributed/storage`、`internal/ethel/bootstrap`、`internal/ethel/engineapi`、`internal/ethel/fetch`、`internal/ethel/stateless/serve`、`internal/ingest`、`internal/mev`、`internal/mobileverify`、`internal/tracing`。失败记录均包含 `socket: operation not permitted`。`test-short-final.log` 是修复后的最终结果；较早的 `test-short.log` 还包含修复前新增回归测试的预期失败，不能混用两次结果。

未运行真实链重放、Go/Rust 混合集群、断电恢复、长时模糊测试或完整 `-race ./...`；没有产出新的覆盖率数值，也不声称达到仓库 20% coverage 门槛。


## 性能追踪补充：统一入口与连续领导者（第 19–20 轮）

性能对照发现参考生成器的收款地址调度会改变状态工作集，因此实现双协议同源
`txflood`：Gov5 原生帧与 Rust Ethereum 帧共享交易构造，Rust 8 字节 ACK 完整
消费，部分接收报错。双协议七节点模拟入口逐笔验签对照与 race 已通过。实测
Rust-19 全窗口 RPC 区块扫描确认 619.4 万笔转账及两百万不同收款人；Go-20
只读数据库扫描确认 293.4 万笔及相同地址空间。完整性能数据、偏差和证据路径
见 `FLEET7_PERFORMANCE_TARGET_20260904.md`，尚未达标。

新增 `hotstuff.leaderTenure` 为链共识配置，所有领导者判断采用相同公式；默认
保持单视图轮换。16 视图的七节点模拟覆盖领导者切换、两处超时、113 个视图和
111 次提交，同时验证 H2-v4 与旧签名模式；race 通过。Go-20 的真实日志确认
连续领导者，但尚未证明吞吐提升。测试中的提前构建条件还揭示本地封块通知
缺少已持久化证据，正在补齐通知链并分别验证；不放宽 follower 的执行门槛。


本地持久化通知已补齐并通过 HotStuff/miner race；真实七节点 hint-22 的 24 个
规范测量块有 22 次提前构建命中，TPS 为 48,899 / 43,466 / 38,033。全仓构建及
七节点停机后的独立验签、收款余额/发送者 nonce、changeset 与历史位图双向
一致性检查通过。node2 的 applied 候选领先规范链一块，报告单独标注并不计入
TPS。完整 EVM 和费用/奖励重执行仍未完成，300k TPS 目标仍未达到。


## 关联发现：共识恢复失败时仍继续启动

`Service.Start` 仅警告恢复错误后继续启用投票；`recoverEpochState` 吞掉 active
读取错误并忽略 staged 读取错误；三个恢复解码器把短于 16 字节的记录当作
不存在。短记录还可能被后续保存视为初始状态而覆盖。启动处的行为可追溯到
2026-03-10 的 `93f9c612e`，早于审计窗口，属于本次性能路径发现的关联问题。
没有复现网络攻击或具体双投票，既有单调合并仍保护合法记录的投票承诺。

修复后损坏记录阻止服务启动；active/staged 错误上报，QC 视图异常返回错误，
缺失键与已存在的空/截断记录分开处理。同时限制持久化 validator count 不得
超过剩余字节可容纳的条目数，在分配前拒绝；公钥长度使用剩余字节比较。
测试覆盖空值、1–15 字节、最大数量/长度、两个 epoch 键、超前 QC、正常
恢复投票水位；HotStuff/node race 通过。合法旧版记录和真正的新节点仍兼容。

性能侧新增池内签名缓存复用，保留完整哈希及 fork signer 核验、wire From
比较。伪造字段、错误哈希、池未命中、错误链 ID 和缓存失效回归及整包 race
通过；七节点 pool-23 仍约 38k–49k TPS，未证明集群提升。快照实际捕获投票
日志在 MDBX BeginRw 等待，下一步量化并评估独立持久化路径，不能去掉投票
发布前的同步日志来换取吞吐。


## A10：父块导入后未恢复被暂缓的领导者生产

`ensureParentApplied` 的异步父块恢复路径来自 2026-08-22 `b120d3d22`，在本次
三个月窗口内。该门槛是必要的，但恢复完成后没有同视图生产重试。独立同步
投票日志让 QC 更早抵达，第 26 轮 node2 在 view144 等父块落盘后仍空等约8秒，
直到 view145 超时转换才生产。影响为恢复/轮换延迟，没有声称发生安全性破坏。

修复用串行 output loop 保存被暂缓的生产请求。持久化应用完成后可唤醒重试，
早期执行通知不能；重试核对当前视图、成员与领导者身份、LockedQC父块，重跑
所有同步/应用门槛。过期请求丢弃、成功请求清除。顺序回归、重复通知、授权
变化、取消和门槛回归及 HotStuff/miner/node race 通过。详见性能报告第26/27轮。

独立同步投票库默认关闭，启用后持久化 marker 使它成为恢复必需数据。覆盖
主写事务阻塞、进程直接退出、错误身份、损坏记录、冲突投票、epoch 切换及
去掉启用变量的七节点真实重启。它是性能改造，不是删除投票持久化；旧二进制
忽略 marker 的降级不受支持，不能据此声称跨任意版本恢复已经验证。


## A11：QMDB点读将冷存储/索引失败解释为缺键

冷库适配器 `8f356e3e7`（2026-06-09）、MDBX索引 `a133cb961`（2026-06-09）
和前缀索引 `9f9fa3d6f`（2026-08-04）均在审计窗口内。它们原来的bool接口
把I/O失败、短条目或无法解析前缀holder混同缺失；用作执行状态来源时，可能
把未知状态当作空账户/零槽。这是配置相关的读取缺陷，没有复现远程攻击；
默认plain读源不因此改变。

新增错误感知的GetChecked、ColdEntryChecked与索引查询，QMDBStateReader优先
使用它们，实际QMDB读返回错误，verify计失败且保持plain结果。已知索引必须
找到匹配完整key的条目，内存条目还必须活跃；不存在的索引键仍正常返回缺失。
覆盖故障注入、前缀碰撞、合法slot0和多轮驱逐/删除等价。改造范围是状态点读，
其他使用旧bool接口的树更新/辅助路径仍需继续审计，不将其标记为全面修复。


### A12：已落盘驻留条目的回收边界与内存驱逐耦合

`lib/qmdb/qmdb.go`的deactivate只把已驱逐的旧slot加入deadFlushed。该逻辑来自
`31d8cd00`（2026-06-10）。在默认每次清除全部已落盘entry的路径下，两条边界
重合；第34轮有界保留实验使两者分离后，根和undo仍相同，但第3块开始的
qmdbEntries表出现未回收的旧行，字节对照测试明确失败。

新增独立的已提交slot边界：FlushTo只暂存新边界，CommitFlush在事务成功后
采用，AbortFlush丢弃；完整/增量加载恢复该边界，undo截断时同步收缩。可回收
的已落盘驻留slot死亡也进入回收队列。无leaf blob或归档模式不会因新驻留分支
积累无法处理的回收工作；原冷条目行为保留。未改变磁盘格式或推迟写入。

测试覆盖保留/驱逐两套树10块的持久化四表逐字节一致、root/undo一致、点读
与证明、跨保留窗口的完整回退及分叉重执行、磁盘重载，以及成功flush后撤销、
末尾metadata写失败、Abort后不会推进提交边界、无leaf blob的旧行保留。
针对性race通过（qmdb1.025s、commitment1.393s）；全量检查和七节点性能试验继续。

### A13：规范提交只校验执行高度，未校验执行分支（回归、集群及七节点离线审计通过）

`CommitToCanonicalWith`的QMDB执行守卫来自`59c5e6b47`（2026-07-10）：只有
读到标记且目标高度高于applied高度才拒绝。读取失败或标记缺失时继续；若目标
为已存储的同高度兄弟块，或applied已沿另一分支前进，也不会拒绝。调用方
`Service.handleOutput(OutputBlockCommitted)`虽先记录未执行状态并请求追赶，
仍继续调用规范提交。这使规范头和本地world state可能指向不同分支；CommitQC
本身不证明该节点已执行获胜分支。此结论来自代码路径审计，修复后的拒绝路径
由回归验证，未声称已在集群复现远程攻击。

修复在同一个规范写事务、任何规范行/头指针改写前，要求存在有效的
applied标记，并沿实际applied祖先链核对目标hash，校验沿途头的number/hash；
未知、缺失或不一致均返回可重试错误。标记解码严格要求非空值恰为40字节；
投票使用的`HasAppliedBlock`在QMDB启用时不再把缺标记回退为区块头存在，
沿途头也校验number/hash。非QMDB无标记的旧回退保持。

新创世初始化在alloc和QMDB forest落入同一事务、计算root与创世头一致时，
写入显式的applied(0, genesisHash)；提供但未被计算结果证实的root override
不产生执行证据。没有自动替旧库补写未知执行标记。其他启动/分支恢复调用点
仍有忽略标记读取错误的逻辑，本项不把整套恢复流程宣称为全面修复。

已写回归覆盖同头/真实后代、同高度兄弟及兄弟后代、执行落后、标记缺失/
短长格式/读取错误、祖先头缺失/内容不符；失败时磁盘三种头与规范行、内存头、
事务hook、提交回调和事件均不能前进，执行标记对齐后可重试成功。另有0–48
字节解码边界、QMDB投票探针和已验证创世标记测试，以及未验证创世root
override不得产生applied标记的对照。失败后对齐marker的重试测试是执行证据
夹具，不是全EVM重执行。

按共享占用协议取得机器后，针对性race通过：rawdb1.020秒、internal1.091秒。
完整受影响包race通过：rawdb1.985秒、internal1.712秒、HotStuff15.491秒、
node2.962秒；独立节点二进制`build/perf7/n42-qmdb-commit-guard35`及全量
`make -o version-build build`成功，gofmt/diff检查通过。源码/脚本哈希见
`build/perf7/commit35-source-manifest.json`，日志前缀`/tmp/n42-perf7-commit35-`。
检查入口为`build/perf7/audit34-check35.sh`。

第35轮已于2026-09-05 03:16:10–03:19:29 UTC运行该二进制：原有固定seed
启动成功，七节点持续导入交易并规范提交；三窗口共3,874,200笔，七节点窗口末
规范块hash/root一致，9笔抽样收据及正常停止通过。占用入口记录isolated=true。
该轮按协议属于预热，不能作为性能提升证据；实际七节点进程均未开启WRITEMAP。
七节点独立签名/账户/history审计于03:22:48–03:26:09 UTC全部通过：各自
验签6,021,800笔、核对1004个发送者nonce和两百万收款人余额，双向history
一致。七节点applied和规范块均153，hash均为
`0x574d2b35ebe1b641932956e817fa09713dde85d9e3b6f653b9485dd571872703`。
证据为`commit35-warmup/offline-audit-summary.json`。此审计仍不含费用/奖励
余额的独立重放、全EVM重执行或分叉攻击集群验证。


### A14：存储枚举和状态读取错误未阻止最终写入（修复已写，验证待执行）

PlainStateReader.ForEachStorage于98ddfc80（2026-06-08）加入的循环只检查
k != nil，虽然循环体检查err，但MDBX Seek/Next返回(nil,nil,error)时循环体
根本不会执行。调用方可能把读失败当成无存储或枚举完成，影响CREATE碰撞检查、
SELFDESTRUCT完整槽清理和历史撤销。Plain/Buffered writer的预清理收集及清理、
Buffered reader枚举、BufferSnapshot.ApplyTo也有同形问题；其中多处来自4月，
属于审计近三个月路径时发现的既有缺陷，不能都归因于近期提交。

六处循环条件现改为在key非nil或err非nil时进入，先传播错误，再判断前缀和
处理数据；正常空扫描及回调主动停止行为保留。新增storage_cursor_error_test.go
覆盖6种操作的open/seek/next故障，验证扫描错误撤销同事务的前序账户写入、
保留原存储、关闭游标且不发布部分undo/wipe；另覆盖两种reader的CREATE碰撞
和SELFDESTRUCT错误传播。测试尚未执行，不宣称已通过。

第38轮使用固定的修改前二进制；这些源码与测试编辑未改变该轮harness、guard、
启动脚本或测量二进制。验证将等待该轮测量及离线审计全部完成，再独立申领机器。

进一步追踪同一6月8日提交的captureWipedSlots，发现调用方直接丢弃枚举错误。
现保留ErrNoStorageEnumeration的既有能力回退，但真实读取错误写入savedErr，
且不发布部分wipedStorageSlots。FinalizeTx/CommitBlock/MakeWriteSet原先也
未检查savedErr，因此这条错误传播链还缺提交边界。现于入口拒绝既有错误，并在
FinalizeTx的延迟余额读取之后再次检查；CommitBlock的延迟读取错误由随后的
MakeWriteSet入口拒绝。错误返回前不清空journal、不推进storageEpoch。

新增验证准备包括5种执行期/延迟余额读取失败的最终写入情形，以及真实EVM调用
已部署合约的SLOAD缺失槽/数据库错误对照：后者必须返回执行错误，无收据、不
发布累计gas、不写发送者状态，不能把它变成成功或普通EVM revert。错误后的
IntraBlockState仍须由调用方丢弃，不宣称EVM已消耗的内存状态或GasPool自动恢复。
四个负对照分别仅恢复旧循环、旧capture错误处理、旧提交边界及旧边界下真实EVM
行为，预期18/4/11/1个子用例失败。检查入口check-cursor39.sh和源码manifest
已准备，尚未运行；固定第38轮不含这些修改。


第39轮首轮检查于05:13:19取得claim，05:13:45启动。四个负对照均命中预期
18/4/11/1个失败；修复后状态包针对性race1.064s及真实EVM错误传播race1.035s
通过。随后因发现临时负对照.go目录会被go build ./...枚举，主动停止受影响包
长测，05:14:12结束并释放；该次不算完整验证成功。原脚本、日志和夹具保持不动，
在夹具目录新增独立go.mod隔离根模块枚举，并以check-cursor39b.sh重新验证。
新源码清单为cursor39b-source-manifest.json，等待新的完整测试及构建结果。
