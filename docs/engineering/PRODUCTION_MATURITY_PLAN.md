# N42 生产成熟度提升计划

> 计划日期：2026-03-17
> 输入基线：[`docs/GAP.md`](../GAP.md)、[`docs/GAP_ANALYSIS.md`](../GAP_ANALYSIS.md)、[`TEST_REVIEW_FINDINGS.md`](./TEST_REVIEW_FINDINGS.md)
> 目标：优先把已有能力做成熟、可恢复、可运维、可互操作；暂不以补齐新功能项为第一优先级

---

## 一、计划边界

这份计划只做“成熟度/生产化”工作，不把功能清单扩张当成本轮主目标。

本轮优先处理：

1. 已存在接口面的正确性和互操作闭环。
2. 已存在存储/同步/节点能力的恢复性、重启稳定性和可观测性。
3. 已存在高阶功能的资源边界、错误路径和运行时稳态。
4. 生产发布前必须具备的 soak、回归、告警和 runbook。

本轮明确不抢做：

1. 全新 `staged sync` 架构
2. `rpcdaemon/sentry` 级完整进程拆分
3. `io_uring` / async storage 重构
4. Portal / light client
5. 新 archive 数据模型
6. 下一代 DAG 调度器或更激进的并行执行框架

这些不是“不做”，而是先放到成熟度硬门槛之后。

---

## 二、为什么先做成熟度而不是补功能

按当前 [`docs/GAP.md`](../GAP.md) 的结论，N42 的问题已经不是“没有功能”：

- 状态承诺、快照、Witness、GraphQL、Clef、HotStuff、Bundler、MEV、Encrypted TxPool 都已存在。
- 真正拖住生产判断的是：`Engine API` 语义、恢复性、运维拓扑、长期稳定性和跨客户端互操作。

所以接下来的代码计划应改成：

1. 先把已暴露给外部的 surface 做对。
2. 再把节点重启、恢复、压测和可观测做稳。
3. 最后才去拉新功能曲线。

---

## 三、生产候选的退出标准

只有下面 6 个门槛都满足，N42 才适合从“功能很宽”进入“生产候选”口径。

| 门槛 | 要求 | 当前状态 |
|---|---|---|
| 外部语义正确 | `Engine API`、核心 RPC、签名器接口在真实对接环境下行为正确 | 已具备最小闭环 |
| 恢复性 | 重启、崩溃恢复、历史过期、快照恢复、genesis/bootstrap 路径可重复验证 | 已具备最小闭环 |
| 资源边界 | 长时间运行下 goroutine、内存、磁盘、队列、连接数有边界且可观测 | 未完成 |
| 互操作 | Hive / execution-spec / 核心 RPC 兼容矩阵稳定 | 已具备最小闭环 |
| 发布门禁 | 有固定回归套件和 nightly soak，而不是只靠一次性本地绿测 | 已具备最小闭环 |
| 运维交付 | 指标、告警、runbook、备份恢复步骤和故障演练闭环 | 已具备最小闭环 |

### 当前进展快照（截至 2026-03-17）

已落地：

1. `Phase 0` 的最小 gate 已固定为 `make maturity-smoke` 和 `make maturity-baseline`，基线覆盖 `Engine API`、GraphQL、Clef、external signer、node auth/genesis、keystore、genesis config、checkpoint、snapshot、freezer、history expiry recovery、txpool journal。
2. `Phase 1` 已把 `Engine API` 推进到真实 Hive `engine-auth` 绿测，GraphQL / Clef / external signer / node auth+genesis 已进入固定 smoke，并补上了 `EIP-3668 (CCIP-Read)` 支持。
3. `Phase 2` 已把 keystore watcher 漏事件补扫、snapshot journal、txpool journal、checkpoint 半写入恢复、freezer 元数据滞后恢复和 `history expiry` 重启续跑纳入自动化回归。
4. `Phase 4` 已固定 `make interop-smoke`、`make soak-smoke` 和 `make release-check`，覆盖 RPC / Blockscout / Hive `engine-auth` / EEST collect-only / 重启循环 / 短压测；其中 `interop-smoke` 使用临时 `n42 --ethdev` 节点，`maturity-baseline` 仍只跑包级 gate。
5. `Phase 5` 已把最小 runbook、发布 checklist 和 metrics 基线文档落到仓库。

仍未完成：

1. archive / historical proof 的查询与恢复路径仍未进入固定 gate。
2. 24h / nightly 级 goroutine / heap / queue 资源边界仍未形成连续回归红线。
3. 更重的深历史 / archive-depth / broad RPC compatibility 互操作矩阵仍属于下一波工作。

结论：

1. 这份计划里“仓库内可执行、可回归、可追溯”的交付项已经落地完成。
2. 当前状态可称为“受控发布候选基线已具备”，但还不应把它扩写成“无条件生产就绪”。

---

## 四、分阶段执行计划

### Phase 0：建立成熟度基线与门禁

目标：先把“什么叫成熟”落成可执行 gate，而不是继续凭感觉判断。

执行项：

1. 固化一份最小生产基线命令集：
   - `go test ./...`
   - `go vet ./...`
   - `go build ./...`
   - `make lint`
   - `make test`
   - `make race-core`
2. 增加一份“成熟度专项基线”清单，单独覆盖：
   - Hive `engine-auth`
   - 核心 RPC smoke
   - 重启恢复 smoke
   - 关键接口负面路径
3. 记录当前内存、goroutine、磁盘、同步耗时和接口延迟基线，形成可回归对比。
4. 给每一类基线指定红线：
   - goroutine 不持续泄漏
   - 内存使用有上界
   - 重启后数据不丢
   - 同步/恢复不能静默卡住

交付物：

1. 一份固定命令清单
2. 一份成熟度基线表
3. 一份当前 baseline 结果记录

验收标准：

1. 任何后续提交都能对照这份基线判断“是在变稳还是在漂移”
2. 生产判断不再依赖口头结论

当前进展：

1. `make maturity-smoke` / `make maturity-baseline` 已落地，具体覆盖项见 [`MATURITY_BASELINE.md`](./MATURITY_BASELINE.md)。
2. 当前 full baseline 固定执行的是 `go build ./...`、`go vet ./...`、`go test -count=1 ./...`、`make lint`、`make race-core`。
3. `make test` 仍可作为人工复核入口补跑，但由于和 `go test ./...` 覆盖高度重叠，当前没有单独列成 baseline 行。

### Phase 1：收口现有外部 surface 的真实语义

目标：优先把已经对外暴露的接口做成“真实可对接”，尤其是 `Engine API`。

范围：

- `internal/api/engine_api_*.go`
- `internal/node/*`
- `tests/eth-hive/*`
- `modules/rpc/jsonrpc/*`
- `internal/api/graphql/*`
- `cmd/clef/*`
- `accounts/external/*`

执行项：

1. 完整收口 `NewPayload*` / `ForkchoiceUpdated*` / `GetPayload*` 的当前语义差距。
2. 对齐 header hash 和外部客户端的计算兼容性。
3. 把 JWT/authrpc/bootstrap/genesis 路径扩成稳定集成测试，而不是只靠一次性手工验证。
4. 对 GraphQL、JSON-RPC、Clef、external signer 增加跨进程级 smoke 和错误路径回归。
5. 建立“接口行为变化必须有回归测试”的规则。

验收标准：

1. `engine-auth` 相关 Hive 套件从“启动成功但语义失败”推进到“核心路径绿”。
2. `Engine API` 不再以 `namespace 存在` 充当完成标准。
3. GraphQL / Clef / external signer 在成功和失败路径上都有稳定回归。

当前进展：

1. `Engine API` 已具备 `forkchoiceUpdated -> getPayload -> newPayload -> RPC block lookup` 的最小 round-trip 回归。
2. GraphQL、Clef、external signer、node auth/genesis 已纳入固定 smoke。
3. `EIP-3668 (CCIP-Read)` 已补进 ABI / bind 调用层。
4. 真实 Hive `engine-auth`、Blockscout RPC smoke 和 EEST collect-only 已进入固定 interop gate。

### Phase 2：把恢复性做成一等公民

目标：把“节点能启动”提升成“节点能恢复”。

范围：

- `modules/state/snapshot/*`
- `modules/rawdb/*`
- `internal/node/*`
- `internal/sync/*`
- `accounts/keystore/*`
- `conf/*`

执行项：

1. 为快照层补完整的崩溃恢复矩阵：
   - journal 存在 / 缺失 / 损坏
   - 部分写入
   - 启动中断后再次启动
2. 为 freezer / history expiry / checkpoint 恢复路径补重启验证。
3. 为 genesis 初始化、hive genesis、参数迁移路径补 fail-fast 和回归。
4. 为 keystore watcher、txpool journal、历史边界查询补“重启后行为一致”测试。
5. 梳理并收紧所有“静默降级”行为，改成显式错误或明确日志。

验收标准：

1. 出现异常重启时，节点不会留下“看起来起来了，但内部状态漂移”的隐患。
2. 恢复路径有自动化覆盖，不再依赖人工重放。

当前进展：

1. keystore watcher 漏事件补扫、genesis config fail-fast、snapshot journal、txpool journal 已有恢复 smoke。
2. checkpoint 现在会拒绝把“只有 canonical hash 的半写入块”误判成已恢复；freezer 现在会按最小真实表项数恢复，而不是盲信滞后的元数据。
3. `history expiry` 的 `earliest` / `feeHistory` / canonical lookup 边界查询和重启续跑已进入固定 gate；当前恢复性里更明显的剩余缺口转为 archive / historical proof 查询与深历史恢复路径。

### Phase 3：运行时稳态与资源边界

目标：把代码从“功能正确”推进到“长时间运行可控”。

范围：

- `internal/txspool/*`
- `internal/network/*`
- `internal/sync/*`
- `internal/node/*`
- `internal/metrics/prometheus/*`
- `internal/tracing/*`

执行项：

1. 统一清理 goroutine 生命周期、context 取消和 channel 背压。
2. 给关键队列和服务加显式容量、限流、超时和 metrics。
3. 对 TxPool、sync、network、RPC 入口增加资源上限回归。
4. 用 `pprof` / Prometheus 建立以下长期指标：
   - goroutines
   - heap / GC pause
   - pending requests
   - txpool size
   - peer/session counts
   - sync lag
5. 把“日志里能看到问题”升级成“指标和告警能先发现问题”。

验收标准：

1. 长时间运行过程中，goroutine、内存和队列长度不出现持续单向增长。
2. 关键路径出现阻塞、退化或错误率上升时，可以从指标中定位。

### Phase 4：互操作与 soak

目标：把单机/单包级正确性推进成“外部环境里也稳定”。

范围：

- `tests/eth-hive/*`
- `tests/*`
- `scripts/*`

执行项：

1. 把 Hive / EEST / RPC compatibility 拆成分层矩阵：
   - 每次提交必跑 smoke
   - nightly 跑更重的 interop
   - 周期性跑 soak
2. 建立至少 3 类 soak：
   - 单节点 24h 稳定性
   - RPC/txpool 并发压力 6h
   - 重启/恢复/继续同步循环测试
3. 把“失败是因为测试环境”与“失败是因为实现语义”彻底分开记录。
4. 把互操作 blocker 直接追踪到代码模块，而不是只停留在日志。

验收标准：

1. N42 至少具备固定的、可重复的 interop 和 soak 跑法。
2. 失败结果可以稳定归因，不再混在 collect-only / stub / smoke / real-exec 口径里。

当前进展：

1. `make interop-smoke` 已固定执行 RPC / Blockscout / Hive `engine-auth` / EEST collect-only，并固定使用临时 `--ethdev` 节点。
2. `make soak-smoke` 已固定执行 3 轮重启 + 短压测循环。
3. `scripts/run_interop_smoke.sh` 已把 Hive 基础设施 flake 与语义失败分开，通过 cleanup + retry 把 `API 500` 归类为环境抖动而不是实现回归。
4. `make release-check` 已把 `maturity-baseline + ops-smoke + interop-smoke + soak-smoke` 收成单一发布 gate。

### Phase 5：运维交付与发布门禁

目标：把“代码层成熟度”转换成“发布层成熟度”。

执行项：

1. 给已有指标输出一份最小 dashboard 和告警建议。
2. 写清以下 runbook：
   - 初始化
   - 升级
   - 回滚
   - 数据目录备份/恢复
   - authrpc/JWT 故障排查
   - snapshot/history 异常处理
3. 建立发布前 checklist：
   - 基线命令
   - interop smoke
   - 恢复 smoke
   - soak 结果
   - 版本记录
4. 对“只在文档里写成已完成”的模块，增加发布前强约束：必须有可执行 gate。

验收标准：

1. 发布决策可追溯。
2. 线上问题有最小操作手册，不再靠开发者记忆兜底。

当前进展：

1. 最小 dashboard / 告警阈值继续使用 [`METRICS_BASELINE.md`](./METRICS_BASELINE.md)。
2. 运维手册已落在 [`OPERATIONS_RUNBOOK.md`](./OPERATIONS_RUNBOOK.md)。
3. 发布 checklist 已落在 [`PUBLISH_CHECKLIST.md`](./PUBLISH_CHECKLIST.md)。
4. `make release-check` 现在就是发布前的强约束 gate。

---

## 五、按代码域拆分的实施顺序

### 5.1 第一优先级：先收已有外部接口

模块：

- `internal/api/engine_api_*.go`
- `internal/node/*`
- `modules/rpc/jsonrpc/*`
- `internal/api/graphql/*`
- `cmd/clef/*`
- `accounts/external/*`

原因：

- 这些能力已经对外，成熟度不足会直接表现为互操作失败或运维误判。

### 5.2 第二优先级：再收恢复性

模块：

- `modules/state/snapshot/*`
- `modules/rawdb/*`
- `internal/sync/*`
- `accounts/keystore/*`
- `conf/*`

原因：

- 生产事故里，真正致命的往往不是“功能没有”，而是“异常后回不来”。

### 5.3 第三优先级：最后收稳态与运维

模块：

- `internal/txspool/*`
- `internal/network/*`
- `internal/metrics/prometheus/*`
- `internal/tracing/*`
- `scripts/*`
- `tests/*`

原因：

- 这部分决定 N42 能不能从“功能可演示”跨到“能长期跑”。

---

## 六、暂缓项清单

这些项保留在 backlog，但本轮不作为主线：

1. `staged sync` 全量实现
2. `rpcdaemon/sentry` 完整服务拆分
3. `io_uring` / async disk I/O
4. Portal / light client
5. 新 archive 结构或 segment 化历史模型
6. 更激进的 DAG scheduler / execution pipeline 重写

延期理由：

- 它们会显著扩大战线。
- 当前 N42 的真实瓶颈更偏成熟度和互操作，而不是“缺下一项大功能”。

---

## 七、建议的提交节奏

为了避免再回到“大而全但不可验证”的状态，后续建议按下面的 commit 粒度推进：

1. `fix(engine): ...` 只收 `Engine API` 语义和对应 Hive 回归
2. `test(recovery): ...` 只收 snapshot/freezer/restart 恢复测试
3. `fix(runtime): ...` 只收 goroutine/backpressure/resource bound
4. `ops(metrics): ...` 只收指标、告警和 dashboard
5. `ci(interop): ...` 只收 smoke/nightly/soak gate

每个提交都必须附带：

1. 修改的 gate
2. 新增或变更的验证命令
3. 风险边界

---

## 八、这一轮完成后的预期变化

如果严格按这份计划执行，N42 的文档口径应出现下面的变化：

从：

- “功能很多，但成熟度不均衡”

推进到：

- “已有主功能面经过互操作、恢复和运维验证，进入生产候选区间”

再往后，才值得投入更多精力去做：

- staged sync
- rpcdaemon/sentry
- async I/O
- 更激进的执行架构升级
