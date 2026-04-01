# N42 发布 Checklist

> 更新日期：2026-03-17
> 目标：把发布判断从“感觉差不多”收敛到可执行 gate 和可追溯记录

---

## 1. 代码与版本

1. 工作树干净，待发布 commit 已固定。
2. 发布说明里包含 commit hash、分支、版本号。
3. 本次行为变化、配置变化、RPC/Engine surface 变化已写清。

## 2. 必跑门禁

发布前必须保留以下命令结果：

1. `make maturity-baseline`
2. `make ops-smoke`
3. `make interop-smoke`
4. `make soak-smoke`
5. `make release-check`

说明：

1. `maturity-baseline` 只覆盖包级 gate，不启动临时节点。
2. `interop-smoke` 会启动临时 `n42 --ethdev` 节点，并执行 RPC / Blockscout / Hive / EEST 互操作检查。

最低要求：

1. 所有 step `PASS`
2. 失败原因不能是“暂时先忽略”
3. 结果目录和 summary 要可追溯

## 3. 配置与安全

1. JWT secret 不随版本漂移。
2. RPC / WS / authrpc 端口暴露符合部署预期。
3. metrics / pprof 仅在允许的网络范围暴露。
4. 不提交 keystore、链数据和临时测试产物。

## 4. 数据与恢复

1. 升级前完成数据目录备份。
2. 备份包含 `chaindata`、ancient/freezer、keystore、JWT secret、配置。
3. 已准备上一个可运行版本的回滚二进制。
4. 如涉及历史裁剪配置，已确认目标环境是否接受非 archive 语义。

## 5. 上线后检查

1. `eth_blockNumber`
2. `eth_chainId`
3. `rpc_modules`
4. `txpool_content`
5. `engine_exchangeCapabilities`
6. metrics / pprof 可访问

如果发布涉及 Engine / RPC 互操作面，再补：

1. `make interop-smoke`

## 6. 发布记录模板

建议至少记录：

1. 发布 commit
2. 版本号
3. 执行时间
4. `maturity-baseline` summary 路径
5. `ops-smoke` summary 路径
6. `interop-smoke` summary 路径
7. `soak-smoke` summary 路径
8. `release-check` summary 路径
9. 回滚版本和备份位置
