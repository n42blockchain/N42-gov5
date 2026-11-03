# 最近三个月代码审计与修复（2026-10-06）

## 范围与证据

本轮以已经合并的 `origin/main` / `qs/block-time-budget` 为基准，审计起点为
`d15fccd998d792c5df27b2a8b9bb124d227b6dd3`。时间范围固定为
2026-07-06 00:00:00 EDT 至该提交；可达历史中共有 **2,571 个提交**（包含合并）。
首次探索使用 Git 的相对日期解析得到 2,562，固定自然日边界后采用上述数字。

第一父链在时间窗口前的基线为
`9dfd95b3d7a10955321f3b9c63c1ec695180c3c2`（2026-07-04）。与审计起点相比，
净差异涉及 **2,310 个文件**，新增 341,941 行、删除 19,454 行；Go 非测试文件
684 个，Go 测试文件 1,396 个。合入窗口内的旧分支改动也包含在净差异中。
完整路径、重命名前路径和增删行数见
[机器可读变更清单](THREE_MONTH_AUDIT_20261006_changes.csv)。

修复使用独立 worktree `/data/blockchain/gov5-work/wt-ddn`，避免改动原工作目录的
未提交文件。未提交工作区内容不在本轮已提交代码审计范围内。没有启动、停机或
修改生产节点，也没有部署合约、修改链数据或密钥。

| 子系统 | 净差异文件数 | 本轮检查方式 |
| --- | ---: | --- |
| common | 100 | protobuf 转换链深入检查及回归 |
| internal/consensus | 129 | HotStuff 恢复解析深入检查；投票持久化、QC 绑定、写入 latch 源码抽查及竞态测试 |
| internal/miner | 35 | 应用父块门控、并行 fill/手续费路径调用链抽查 |
| internal/sync | 101 | 范围请求、批量 RLP、接收分配边界深入检查，真实流回归和模糊测试 |
| internal/ddn | 48 | 费用解析、策略/签名结果一致性深入检查；HTTP、quorum、签名、重放边界抽查及整包竞态测试 |
| internal/node | 47 | DDN/HTTP 接入、私有 gRPC 接入与默认配置抽查 |
| internal/vm | 106 | 状态读取与原生执行相关调用链抽查及核心竞态测试 |
| internal/api | 126 | 近期变更筛查，既有历史查询门控对照及全仓基线检查 |
| internal/txspool | 29 | 发送者提示接入调用链筛查及全仓基线检查 |
| internal/ethel | 150 | downloader 顺序、来源/请求绑定、共享头部缓存深入检查、构建标签及整包测试 |
| internal/mobileverify | 41 | signer mask、证书与协调器检查及全仓基线检查 |
| internal/distributed | 72 | 近期变更筛查及全仓基线检查 |
| modules/state | 152 | QMDB reader、余额观察钩子、MVS fold 调用链抽查及核心竞态测试 |
| modules/rawdb | 60 | 近期变更筛查及全仓基线检查 |
| lib/recsplit | 3 | heap/mmap 缓冲区转换深入检查及 race/checkptr 回归 |
| lib/qmdb | 50 | proof/undo/revert 边界源码检查、整包基线及 undo 模糊测试 |

MPT proof/trie 的检查只归入 eth-el 兼容路径，不作为七节点 QMDB 二叉树
性能或正确性验证的替代。七节点需按实际执行、状态根及对应签名层单独验证。

这里的深入检查不等于对全部 2,310 个文件逐行审计。源码抽查、已有测试和全仓
静态检查也不等于密码学证明、性能验收或全仓安全认证。

## 已修复问题

共修复九项生产代码问题、一项测试夹具问题及一项 CI 配置问题，其中部分是沿近期调用链发现的
窗口前存量缺口。生产问题按严重性和可达性分别描述，不把测试夹具缺陷视为
生产服务漏洞。

### A1 — P1：并行执行隐藏了合约可见的前序手续费

`internal/parallel_processor.go` 无条件把手续费接收者的入账推迟至块末。直接
from/to 扫描无法发现合约里的 `COINBASE; BALANCE`，也不能在发送者恢复之前
识别 wire 交易是否由手续费接收账户发出。这样可能使串行和并行执行读到不同
余额，并产生不同存储、收据或状态根。

复现：第一笔普通转账产生 21,000 单位 priority fee，第二笔调用合约将 coinbase
余额写入 slot 0。旧实现存入 `0`；修复后存入 `21000`。
`TestParallelContractObservesPreviousFees` 以真实签名交易、wire round-trip、MDBX
状态和 EVM 字节码执行覆盖该行为，旧代码失败证据保存于本轮验证日志。

修复在发送者恢复和 block-start 之后判断整个块是否可安全延迟手续费。只有
不触碰手续费接收者、无 calldata/授权/创建、目标无代码且非预编译的转账块
保留该优化。其他块继续使用 Block-STM，但通过正常状态写入信用手续费，让
读取依赖参与 MVS 验证。同时补上 executor 提前返回时的 arena 归还。

关联窗口内实现：`a68e5dbaf`，2026-09-06。涉及合约的块可能增加余额冲突，
吞吐需要另行实测；本轮没有把修复描述成性能提升。

### A2 — P2：HotStuff pending-vote 解码可崩溃或分配过多内存

`internal/consensus/hotstuff/persistence.go` 的 `LoadPendingVotes` 将短记录当成
缺失、在检查剩余长度前读取下一轮计数，并按不可信 count 直接创建 map。
44 字节、prepareCount=0 的记录会在读取 commitCount 时发生 slice 越界；
大 count 会在实际数据检查前造成过量分配。重复 validator、尾随数据和截断
signature 也不能被严格拒绝。

修复区分空/缺失记录与损坏记录，并检查完整 48 字节前缀、每轮计数与最小
条目字节数、signature 长度、重复 validator 和尾随数据。任何损坏返回错误，
不输出部分恢复状态。回归覆盖完整合法记录的每个截断位置和伪造大计数/长度。

这是存量问题（`3ff339ab0`，2026-04-01），在近期改动的共识持久化文件中发现。
当前检索只发现测试调用该 pending-vote API，未发现接入生产恢复流程；不将它
夸大成当前网络可达的共识攻击。生产 vote journal 的安全约束保持不变。

### A3 — P2：缺省 protobuf 嵌套 limb 触发空指针

`common/utils/util.go` 的 H256/H160/H384/H512/H768 转换直接访问多层 `Hi/Lo`
指针。合法 protobuf 可以省略任意嵌套消息，例如 `H256{}`；旧转换会崩溃。
这条链用于同步区块号、哈希、地址、公钥和签名转换，影响取决于调用方是否恢复
panic，不能笼统断言一定终止整个节点。

修复使用生成的 nil-safe getter，按 protobuf 语义将缺省 limb 视为零。
`TestSparseProtoNumbers` 覆盖顶层 nil、空嵌套、部分 populated limb 和各位宽，
并核对 H256 数值转换与大端 hash 转换一致。既有 round-trip 测试仍通过。

主要缺陷来自窗口前旧实现；本轮沿近期修改的转换调用链补齐。

### A4 — P2：同步范围未绑定首块步长，服务端改写请求语义

客户端 `SendBodiesByRangeRequest` 只检查首块处于区间内，后续块之间差值为
step 的倍数。因此请求 `1,3,5` 可接受 `2,4,6`。服务端还把 step>1 静默改成 1。

修复在发送和分配响应 slice 前限制 count/step/span，检查 uint256 范围加法
溢出，并验证每个响应块相对起点的步长。服务端保留已验证的步长，完整流回归
验证请求 `1,3,5` 确实返回这些高度。同时补齐 handler 对缺省 P2PLimit 的处理，
与 rate limiter 已有默认值一致。

`TestSendBodiesByRangeRequestRejectsMisalignedFirstBlock` 在旧实现中得到 nil
错误；现在拒绝。另有拨号前界限测试及默认配置下真实 net.Pipe 编解码回归。
这是近期修改调用链中的存量校验缺口，不将它等同于绕过区块共识验证。

### A5 — P2：交易批量消息在分配后才检查数量，缺少接收字节上限

`internal/sync/tx_batch_wire.go` 将整个 RLP list 先解码为 `[][]byte`，才限制
256 个交易，并未执行声明的 256 KiB byte cap。恶意大 list 可放大临时分配。
发送端按原始字节累加，加入最后一笔交易和 RLP framing 后也可能超过 cap。

修复先检查 wire 字节数，再无分配解析外层 list/count，最后解码交易。发送端
遇到超限 batch 时改发兼容的 single 消息，保留全部交易。回归包含两个独立可
解码交易组成的超限有效 RLP，以及 framing 使 batch 超限时的发送保留检查。

关联窗口内提交：`300ebada`，2026-08-04。`FuzzDecodeTxBatch` 运行 10 秒，
完成 100,785 次输入，无 panic 或越界接受。

### A6 — P2：DDN 大整数费用解析早于输入长度检查

`DecisionRequest.Validate` 先对 MaxCost 执行 `big.Int.SetString`，才验证 78 位
十进制上限；scheduler/quorum 还使用独立且较宽松的解析入口。大 RPC 字符串
因此可以在拒绝之前消耗不必要的 CPU 和内存。

修复提供统一 `types.ParseMaxCost`，先限制长度、字符和前导零，再解析并验证
uint256 上限；请求、scheduler、quorum 全部复用。回归覆盖 1 MiB 输入、符号/
前导零、uint256 的最大合法值及最大值+1。deadline 检查同时改用先判顺序、再
计算差值，避免 `now + 24h` 的 uint64 回绕。签名 canonical 字节格式没有改变。

该问题来自本窗口新增的 DDN 实现。

### A7 — P2：System1 人工复核标记与签名答案矛盾

原 gateway/HTTP 在策略要求人工复核或最低置信度不满足时，只把顶层
`NeedEscalation` 置 true，System1 第三个 Noul 答案仍可能为 0。使用不同字段的
消费者将得到矛盾的复核结论。

修复统一为 `DecisionResult.EnforcePolicy`，同步该特定 schema 的 Noul 升级
答案，同时保留 provider 已经要求的升级。修改时复制 slice，避免污染共享
provider 输出；其他治理 Noul 的业务值不被改写。gateway、HTTP、aggregate、
native provider 使用同一入口；回执验证拒绝签名完整但升级字段矛盾的结果。

回归覆盖 RequireHuman、低置信度、保留 typed escalation、共享数据隔离，以及
真实签名矛盾回执的拒绝和修复后接受。System1 provider/model hash 升为版本 2；
旧 model hash pins 需要更新。benchmark 的分类规则和八分类顺序保持一致。

### A8 — P2：压缩头部读取器的共享缓存存在竞态

DATC 并行 benchmark 共享同一 HeaderCompactReader，framed LRU 的提升操作与
其他线程查找同时访问 slice；legacy segment cache、文件句柄 map 和 Close 也
没有共同的同步约束。全仓 race 检查在 TestRunBenchHappyPath 中真实报错。
修复序列化 ReadHeader 与 Close，关闭幂等且关闭后的读取返回 os.ErrClosed。
新增真实 framed/legacy store 上八个 goroutine 的散读回归。返回 header 仍按既有
约定只读；未改变独立 reader 的并行能力，也未宣称同一 reader 的吞吐改善。
关联窗口内 framed 读取实现：`8452e25b`，2026-09-01。

### A9 — P2：RecSplit 原生缓冲区转换超出实际分配

OpenIndexFromBytes 的有效堆内索引在 checkptr 下发生致命错误：将小缓冲区指针
转成极大数组指针，再切片。Golomb Rice、EliasFano16 写入和恢复也使用同类转换。
修复为 unsafe.Slice，长度限定为实际可用字节/word 数，Golomb Rice 记录长度先
检查剩余字节。保持原生字节序与文件格式；已有堆内索引 lookup 和 EF round-trip
测试在 race/checkptr 下通过。没有声称完整审计所有损坏索引的解析和查询行为。

### A10 — 测试缺陷：coldseed 假对象与异步服务竞争

两个服务启动/停止测试在 Seed goroutine 追加 calls 时，直接轮询 slice 长度。
修复 fakeSink 的锁和快照读取。这是测试对象问题，没有把它计入生产服务竞态。

DDN 的 A7 另外补齐 System1 固定 schema 的验证：分类标签、八类分布、三项 typed
答案及升级标志必须一致，CRITICAL/UNKNOWN 必须升级；允许明确的 UNKNOWN
弃权结果。HTTP、gateway、aggregate、sidecar 和签名验证均使用同一验证入口。

### A11 — CI 配置缺陷：v2 linter 与 v1 配置不兼容

CI 固定 golangci-lint v2.9.0，配置却仍使用 v1 的 output.formats list、
linters-settings 和 disable-all 等字段。仓库外安装同版本工具后，旧配置验证
实际失败：`output.formats expected a map, got slice`。使用官方 migrate 转成
version 2 后通过 config verify；保留原有启用规则、integration 标签、排除规则
及 new-only gate，gofmt/goimports 移至 v2 formatter 配置。

Action 更新为与 v2 配套的 v7，参见[官方 v7 文档](https://github.com/golangci/golangci-lint-action/blob/v7/README.md)。
CI Go patch 版本同步到 go.mod 已要求的 1.26.8；没有再升级项目 Go 版本。
make lint 及从审计起点 `d15fccd9` 对全部修复增量执行的 lint 均为 0 issues。
另对所有本轮修改 Go 文件独立执行 formatter diff，结果为空，避免把 v2 的
formatter 配置误当成 run 自动执行的格式检查。保留既有历史 lint 债务范围；
没有把本轮 lint 通过表述成全部历史代码零告警。

## 依赖扫描核对

使用 `govulncheck v1.8.0` 扫描默认构建标签下的全仓依赖，Go 漏洞数据库最后
修改时间为 2026-10-01。JSON 中有两个 advisory、四条不同层级 finding：

- `GO-2026-6443` 报告 grpc v1.84.0，包含 transport 到 node.Start 的符号链。
  本地实际依赖源码在 `internal/transport/http2_server.go:525` 已有缺少 authority/
  Host 时提前拒绝的修复；[上游安全公告](https://github.com/grpc/grpc-go/security/advisories/GHSA-2v4p-qf9q-27wj)
  明确把 **1.84.0** 列为 patched version。项目也使用 grpc.NewServer，而非 xDS
  server。此项按数据库版本范围误报记录，没有盲目降级或引入开发版依赖。
- `GO-2026-5932` 是 x/crypto/openpgp 的弃用 advisory，仅 module 层级命中。
  当前默认构建依赖中未导入 openpgp 包，不属于本次可达问题。该结论不覆盖
  外部插件和未经扫描的构建标签。

没有以 scanner 的进程返回码作为“全部依赖安全”的依据；没有修改 go.mod/go.sum。

## 验证与剩余限制

完整日志保存于本轮 `/tmp/n42-audit-*.log`；源码、回归测试和上述机器可读
清单构成可随仓库复核的证据。

首次高并发 `go test -race -short ./...` 检出了 A8/A9/A10，同时出现多个历史
数据包内存终止、DATC 的默认十分钟超时和 discovery 的初始化超时。discovery、
coldstore、cscompact 单独竞态复跑通过。后续无内存限制的重跑曾被主动终止以
降低内存压力；最终完整重跑采用 `GOMEMLIMIT=4GiB`、`-p 1`，竞态超时设为
30 分钟。该重跑中 freezer 仍被内存终止；把工作线程限制为 `GOMAXPROCS=2`
并使用 `GOMEMLIMIT=1GiB` 后，强制非缓存 freezer 整包竞态复跑 2.649 秒通过。
最终全仓竞态再次采用这组资源参数重跑。不跳过失败用例，不关闭 race/checkptr，
也不修改生产配置。

最终全仓短测通过：`GOMEMLIMIT=4GiB GOFLAGS=-p=1 make test-short`。
最终全仓竞态命令返回 0，282 个有测试的包通过（复用之前已通过的测试缓存）：
`GOMAXPROCS=2 GOMEMLIMIT=1GiB go test -race -short -p 1 -timeout=30m ./...`。
失败的 freezer 另以 `-count=1` 强制重跑通过。DATC 完整竞态重跑耗时
1117.739 秒，证实本机默认十分钟超时不足以覆盖该包的完整 race 场景。

当前已完成：`make build`、全仓 `go vet ./...`、`make race-core`、全仓短测及
竞态重跑、n42el 标签下 ethel/eth69 整包测试、DDN 整包竞态检查和两项模糊测试。
末尾 import/冗余类型转换清理补做了 HotStuff pending-vote、native System1 与
手续费可见性定向竞态回归；全部修改 Go 文件 formatter diff 为空。

Lint：`make lint GOLANGCI_LINT=/tmp/n42-audit-tools/golangci-lint` 与
`golangci-lint run --new-from-rev=d15fccd9 --timeout=10m ./...` 均通过。
工具为源码构建 v2.9.0/Go1.26.8；存在旧 stylecheck nolint 名称告警，未新增
全仓 legacy lint 零告警的结论。

没有对所有构建标签、真实 SP1/密码学后端、完整历史迁移和生产规模重组给出验收结论。
2026-10-07 补充核对：用户指定的性能实现位于相邻 `n42-rs` 仓库；它使用
QMDB 二叉树状态、`0x50` Ed25519 高性能交易、同公钥合并批量验签，以及
`tx_flood --pregen-out/--replay` 预签名文件。普通交易的 ECDSA 与共识层 BLS
是不同层的机制，不能据旧 Go qs 脚本推断这条性能路径不存在。
移出测量阶段的是签名生成，合法性验签仍保留；缓存用于复用已验证的发送者。
Rust native 七节点已完成保留逐笔验签的实测，正式 20 秒窗口为 406,035 TPS；
配置、失败尝试及完整证据见 [七节点补充报告](NATIVE_FLEET7_VERIFICATION_20261007.md)。
本分支的 Go 审计结果与 Rust 七节点实测分别记录，不能把后者写成 Go 的
`0x50` 支持或吞吐验收。原目录未提交代码不在范围内。
首次 `make lint` 因缺少工具退出 2；随后在仓库外安装 v2.9.0、
修复配置并实际复跑通过。CI 未在远端验收，默认 race 并发/超时的表现也不等同
于本地使用明确资源参数的结果。全部修复分逻辑提交推送，最终同步 main/qs。
