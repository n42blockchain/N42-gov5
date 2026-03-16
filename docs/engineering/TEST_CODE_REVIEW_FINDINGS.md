# Test Code Review Findings

## 基线

- 审查日期：`2026-03-16`
- 正式范围：`429` 个 `*_test.go`
- 审查方法：
  - 全量 inventory 与模式扫描覆盖全部测试文件
  - 对命中 `sleep / skip / TODO / global state / weak assertion` 模式的文件做人工逐文件复核
  - 对确认问题直接修正并补包级回归验证

## 状态

- `Phase 1` 入口层与基础设施：完成
- `Phase 2` 核心执行、状态与存储：完成本轮高风险模式清扫
- `Phase 3` 共识、网络、节点与 RPC：完成本轮高风险模式清扫
- `Phase 4` 密码学、ZK、合约与工具链：完成本轮高风险模式清扫
- `Phase 5` 基础库与慢速集成：完成本轮高风险模式清扫
- `Phase 6` 统一回归与收口：完成

## 已修问题

### 1. `cmd/evmsdk/common_test.go`

- 问题类型：`weak_assertion` `external_dependency`
- 原问题：
  - `TestGetNetInfos` 没有任何有效 case
  - `TestBlsSign` / `TestBlssign` / `TestBenchmark` 只打印结果，不断言行为
  - 测试依赖真实网络和全局 `EE`
- 修正：
  - 用 stubbed `http.DefaultClient` 覆盖外网依赖
  - 补 `GetNetInfos`、`TouchFile/ReadTouchedFile`、`Emit` 错误路径和 `blssign` dispatch 的确定性断言
  - 对全局 `EE` 做测试级隔离

### 2. `accounts/keystore/account_cache_test.go`

- 问题类型：`flaky` `global_state_leak` `slow_or_redundant`
- 原问题：
  - 使用 `rand.Seed(time.Now().UnixNano())` 污染全局随机源
  - 用随机目录名和多处固定 `Sleep(100ms~1000ms)` 等待 watcher / modtime
  - 文件更新时间依赖真实时钟跨秒，导致测试慢且脆弱
- 修正：
  - 改成 `t.TempDir()` 派生的缺失目录
  - 用 watcher 状态轮询替代固定睡眠
  - 用 `os.Chtimes` 主动推进 `modTime`，移除 3 处 `1s` 等待

### 3. `accounts/keystore/keystore_test.go`

- 问题类型：`flaky` `weak_assertion`
- 原问题：
  - `TimedUnlock` / `OverrideUnlock` 用固定 `250ms` 睡眠等待锁过期
  - `TestWalletNotifierLifecycle` 用固定 `250ms` 间隔探测后台 notifier
  - `TestWalletNotifications` 依赖包级 `math/rand`
- 修正：
  - 引入基于条件轮询的等待 helper，按真实状态收敛
  - wallet notifier 改为等待 `ks.updating` 的明确状态变迁
  - 事件生成改用局部 deterministic RNG

### 4. `utils/safego_test.go`

- 问题类型：`weak_assertion` `flaky`
- 原问题：
  - `TestSafeGo_PanicRecovery` 没有任何断言
  - 多个用例依赖盲等 `Sleep`
- 修正：
  - 用 channel / `WaitGroup` 验证 goroutine 实际完成
  - 为 panic recovery 增加明确断言
  - 并发测试改成等待全部 goroutine 完成，不再靠时间猜测

### 5. `utils/async_test.go`

- 问题类型：`flaky`
- 原问题：
  - `RunEvery` 测试完全依赖固定时间窗口，取消后的停止判断不稳
- 修正：
  - 改用 `RunEveryWithWG`
  - 用计数轮询等待至少执行次数，再用 `WaitGroup` 确认 goroutine 退出
  - `immediate cancel` 场景改成严格断言 `0` 次执行

### 6. `log/root_test.go`

- 问题类型：`global_state_leak` `weak_assertion`
- 原问题：
  - 测试共享全局 `terminal/logManager/logWriter`
  - 使用固定 `/tmp/test_logs`
  - `Start/Stop` 和 `Init` 相关用例断言过弱
- 修正：
  - 增加 logger 全局状态 reset helper
  - 切到 `t.TempDir()`
  - 断言 `cancel` 安装、console-only 不创建文件 writer、file init 产生日志文件
  - 新增 `cleanup` 删除最旧日志文件的行为验证

### 7. `internal/zkprover/service_test.go`

- 问题类型：`weak_assertion` `slow_or_redundant`
- 原问题：
  - `TestService_StartStop` 只 sleep，不验证生命周期状态
- 修正：
  - 直接断言 `running` 状态和 `ctx` 的 live/canceled 变化

### 8. `internal/sync/state_machine_test.go`

- 问题类型：`slow_or_redundant`
- 原问题：
  - `TestSyncMetricsStateDuration` 为了累计 10ms duration 直接睡眠
- 修正：
  - 直接调整 `stateEnterTime` 到过去时间，去掉真实等待

## 已记录但未在本轮直接改动的测试 debt

这些测试不是“坏掉未记录”，但它们依赖外部夹具、平台差异或需要更大范围 mock，已在本轮归档：

- `internal/api/blockscout_test.go`
  - 仍有 `Requires full mock implementation` 占位测试，需要完整 API 链 mock 后单独收口
- `lib/log/v3/log_test.go`
  - `TestCallerStackHandler` 仍是 `t.Skip("fix me")`，属于旧日志库的独立历史 debt
- `lib/kv/mdbx/kv_abstract_test.go`
  - Windows 特定 skip，属于平台兼容性专项
- `lib/kv/remotedbserver/remotedbserver_test.go`
  - Windows 特定 skip，属于平台兼容性专项
- `accounts/keystore/plain_test.go`
  - 依赖外部 `tests/testdata/KeyStoreTests` 子模块夹具；当前 skip 是合理保护
- `tests/*`
  - 多个慢集成测试依赖外部 ethereum state/bls 向量，当前 skip 为环境门控，不属于单元测试失真

## 验证

本轮已通过：

- `go test -count=1 ./utils`
- `go test -count=1 ./log`
- `go test -count=1 ./accounts/keystore`
- `go test -count=1 ./cmd/evmsdk`
- `go test -count=1 ./internal/zkprover`
- `go test -count=1 ./internal/sync`
- `go test -count=1 ./cmd/evmsdk ./conf ./api/protocol/... ./accounts/... ./log/... ./params ./pkg/errors ./tools/... ./utils/...`

收口验证见本轮最终回归命令。
