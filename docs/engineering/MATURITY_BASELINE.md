# N42 成熟度基线与 Smoke Gate

这份文档把 [`PRODUCTION_MATURITY_PLAN.md`](./PRODUCTION_MATURITY_PLAN.md) 里 Phase 0 的“固定命令清单”和“专项 smoke”落成可执行 gate。

## 1. 固定命令清单

核心基线：

1. `go build ./...`
2. `go vet ./...`
3. `go test -count=1 ./...`
4. `make lint`
5. `make race-core`

专项 smoke：

1. `make maturity-smoke`
2. `make maturity-baseline`
3. `make ops-smoke`
4. `make interop-smoke`
5. `make soak-smoke`
6. `make release-check`

其中：

1. `make maturity-smoke` 只跑聚焦的外部 surface / 恢复性 smoke。
2. `make maturity-baseline` 跑同一套 smoke，并额外执行核心基线命令，结果写入 `build/maturity-baseline/<timestamp>/summary.md`。
3. `make ops-smoke` 固定验证 RPC、metrics、pprof 和短压测。
4. `make interop-smoke` 固定验证 RPC / Blockscout / Hive `engine-auth` / EEST collect-only。
5. `make soak-smoke` 固定验证重启循环和短时负载。
6. `make release-check` 串行执行 `maturity-baseline + ops-smoke + interop-smoke + soak-smoke`。

## 2. Smoke 覆盖面

| 类别 | 包 / 入口 | 目标 |
|---|---|---|
| Engine API | `./internal/api` | 校验 `NewPayload*` / `ForkchoiceUpdated*` / `GetPayload*` 的最小真实闭环 |
| GraphQL | `./internal/api/graphql` | 保证查询路由、参数校验和错误路径仍可用 |
| Clef | `./cmd/clef` | 保证 signer/rule engine 的成功与失败路径 |
| External signer | `./accounts/external` | 保证外部签名账户发现、签名、拒绝路径 |
| Node auth / genesis | `./internal/node` | 保证 auth namespace 过滤和 Hive genesis 启动前置条件 |
| Keystore recovery | `./accounts/keystore` | 保证 watcher 漏事件时缓存仍会补扫刷新 |
| Genesis config | `./conf` | 保证 Hive / engine genesis 解析和 consensus 推断 |
| Checkpoint recovery | `./internal/sync/checkpoint` | 保证重启时不会把只有 canonical hash 的半写入 checkpoint 误判成已完整恢复 |
| Snapshot recovery | `./modules/state/snapshot` | 保证 journal 落盘/重载、损坏输入和取消路径 |
| Freezer recovery | `./modules/rawdb/freezer` | 保证 ancient 元数据滞后或表项数不一致时，重启按最小真实表项恢复而不是截断有效冷数据 |
| History expiry recovery | `./internal/api`、`./internal/node`、`./turbo/rpchelper` | 保证 `earliest` / `feeHistory` / canonical lookup 对齐最早可用历史，并验证重启后会从持久化 earliest 继续推进 |
| TxPool journal | `./internal/txspool` | 保证 graceful shutdown 后的 journal 落盘、重载、去重和旧格式迁移 |

## 3. 输出位置

脚本路径：

1. [scripts/run_maturity_baseline.sh](/Users/jieliu/Documents/n42/N42-gov5/scripts/run_maturity_baseline.sh)

结果目录：

1. `build/maturity-baseline/<timestamp>/summary.md`
2. 同目录下保存每一步的独立 `*.log`
3. `build/ops-smoke/<timestamp>/summary.md`
4. `build/interop-smoke/<timestamp>/summary.md`
5. `build/soak-smoke/<timestamp>/summary.md`
6. `build/release-check/<timestamp>/summary.md`

## 4. 红线

以下任一项失败，都不能把当前状态称为“生产候选”：

1. `make maturity-smoke` 失败
2. `go build ./...` 失败
3. `go vet ./...` 失败
4. `go test -count=1 ./...` 失败
5. `make lint` 失败
6. `make race-core` 失败

## 5. 运行建议

日常收口：

1. 先跑 `make maturity-smoke`
2. 需要形成可追溯记录时跑 `make maturity-baseline`
3. 对运行时或互操作面有改动时，再跑 `make ops-smoke`、`make interop-smoke`

版本发布前：

1. 跑 `make release-check`
2. 记录 `maturity-baseline`、`ops-smoke`、`interop-smoke`、`soak-smoke` 和 `release-check` 的 summary 路径
3. 对照 [`OPERATIONS_RUNBOOK.md`](./OPERATIONS_RUNBOOK.md) 和 [`PUBLISH_CHECKLIST.md`](./PUBLISH_CHECKLIST.md)

## 6. 当前仍未覆盖的成熟度项

`make maturity-baseline` 通过只说明最小 surface 和恢复 smoke 当前是绿的，不代表已经进入“生产候选”。

当前仍未纳入固定 gate 的关键项：

1. archive / historical proof 的查询与恢复路径
2. 24h / nightly 级长时间资源边界和更重的并发压测
3. 更完整的 archive-depth / deep-history / broad RPC compatibility 互操作矩阵
4. 更细的 dashboard 阈值回标和线上告警调优
