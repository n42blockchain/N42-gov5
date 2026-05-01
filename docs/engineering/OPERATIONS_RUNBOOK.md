# N42 运维 Runbook

> 更新日期：2026-03-17
> 适用范围：当前主仓可执行 gate，对应 `make maturity-baseline`、`make ops-smoke`、`make interop-smoke`、`make soak-smoke`、`make release-check`

说明：

1. `make maturity-baseline` 只跑包级 gate，不启动临时节点。
2. `make interop-smoke` 会启动临时 `n42 --ethdev` 节点，再执行 RPC / Blockscout / Hive / EEST 互操作检查。
3. `make release-check` 现在会额外执行一次 `make eest-audit` 等价的结果目录审计，确保历史 EEST run 产物没有残缺目录。

---

## 1. 发布前基线

发布前至少保留以下结果：

1. `make maturity-baseline`
2. `make ops-smoke`
3. `make interop-smoke`
4. `make soak-smoke`
5. `make release-check`

推荐把本轮通过的结果目录一起记录到发布记录里：

1. [`../../build/maturity-baseline/20260317-074829Z/summary.md`](../../build/maturity-baseline/20260317-074829Z/summary.md)
2. [`../../build/ops-smoke/20260317-075716Z/summary.md`](../../build/ops-smoke/20260317-075716Z/summary.md)
3. [`../../build/interop-smoke/20260317-075747Z/summary.md`](../../build/interop-smoke/20260317-075747Z/summary.md)
4. [`../../build/soak-smoke/20260317-075856Z/summary.md`](../../build/soak-smoke/20260317-075856Z/summary.md)
5. [`../../build/release-check/20260317-075540Z/summary.md`](../../build/release-check/20260317-075540Z/summary.md)

## 2. 启动后健康检查

节点启动后，至少确认：

1. `eth_blockNumber`
2. `eth_chainId`
3. `rpc_modules`
4. `txpool_content`
5. `authrpc` 可完成 `engine_exchangeCapabilities`
6. metrics 端点可访问，且 `go_goroutines` 只出现一次
7. pprof 端点可访问

如果是发布验证，优先直接跑：

1. `make ops-smoke`
2. `make interop-smoke`

其中：

1. `ops-smoke` 面向当前节点运行时指标和短压测。
2. `interop-smoke` 面向 Ethereum EL 私链互操作，固定使用临时 `--ethdev` 节点。

## 3. 升级步骤

1. 在待发布代码上执行 `make release-check`。
2. 备份数据目录、JWT secret、keystore 和当前二进制。
3. 停节点，替换二进制与配置。
4. 启动节点后执行健康检查。
5. 确认 `release-check` summary 中 `eest-audit` 为 `PASS`，再观察 metrics / pprof / RPC 至少一个短周期。
6. 如是 Engine 对接环境，再执行一次 `make interop-smoke` 或至少跑 Hive `engine-auth`；这里的 interop gate 会临时起一条 `--ethdev` 节点，不复用线上 datadir。

## 4. 回滚步骤

1. 停节点。
2. 恢复上一个已验证版本的二进制。
3. 恢复升级前备份的数据目录和 JWT secret。
4. 启动后检查 `eth_blockNumber`、`eth_chainId`、`rpc_modules`。
5. 若回滚原因与恢复性有关，再跑一次 `make maturity-smoke`。

## 5. 数据目录备份与恢复

备份时至少包含：

1. `chaindata`
2. ancient / freezer 数据
3. keystore
4. JWT secret
5. 节点配置文件

恢复后重点检查：

1. `eth_getBlockByNumber("latest", false)` 是否返回正常链头
2. `eth_getBlockByNumber("earliest", false)` 是否符合当前历史边界
3. `txpool_content`、keystore 账户和 authrpc 是否可用

## 6. authrpc / JWT 故障排查

出现 `engine_*` 调用失败时，先检查：

1. JWT 文件路径是否和启动参数一致
2. CL/测试环境使用的 secret 是否与节点一致
3. `rpc_modules` 是否暴露了认证命名空间
4. `engine_exchangeCapabilities` 是否成功
5. `eth_getBlockByNumber("latest", false)` 返回的链头哈希是否与 `forkchoiceUpdated` 输入一致

如果是 Hive / Docker 场景，先执行：

1. `tests/eth-hive/build/bin/hive -cleanup`
2. 再重跑 `make interop-smoke`

## 7. snapshot / history / 恢复异常处理

当前固定回归已覆盖：

1. keystore reload
2. genesis config
3. checkpoint recovery
4. snapshot journal
5. freezer recovery
6. history expiry recovery
7. txpool journal

线上排查顺序：

1. 先看最近一次 `make maturity-baseline` 是否通过
2. 对照 `eth_getBlockByNumber("earliest", false)`、`eth_feeHistory`、canonical block 查询
3. 如果只需要最早可用历史，确认 history expiry 配置是否符合预期
4. 如果需要深历史 / archive / historical proof，当前应直接切回保留完整历史的备份节点，不要在裁剪节点上继续兜底

## 8. 指标与告警入口

最小指标/阈值基线见：

1. [`./METRICS_BASELINE.md`](./METRICS_BASELINE.md)

发布前至少确认：

1. metrics 端点可用
2. `go_goroutines` 指标存在且未重复注册
3. `rpc_duration_seconds` 指标存在
4. pprof goroutine 端点可访问
