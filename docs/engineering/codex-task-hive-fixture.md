# Codex 任务书：Hive Engine fixture 回归恢复（hive/eest）

## 背景

`tests/eth-hive` 并非子模块，且未 `gitignore`。它存在于部分环境（如 Mac），但 Windows 开发机常不具备该检出路径。当前代码仓内对该路径只有 3 处引用，而且都读同一个文件：

- `internal/api/genesis_hive_compat_test.go`：`internal/api/genesis_hive_compat_test.go:96` `loadHiveEngineGenesisFixture` 的外部路径 fallback
- `internal/api/engine_state_adapter_hive_test.go`：`internal/api/engine_state_adapter_hive_test.go:43` 同上
- `conf/genesis_config_test.go`：`conf/genesis_config_test.go:138` 同上

三处都最终指向：

`tests/eth-hive/simulators/ethereum/engine/init/genesis.json`

## 任务目标（必须保留）

1. 在仓库内 vendor 一个创世文件，不要求/不应克隆整个 `hive` 仓库
   - 目标路径建议：`internal/api/testdata/hive_engine_genesis.json`
2. 三处加载器改为优先读取仓内 vendor 文件；`tests/eth-hive` 外部检出可作为回退路径保留
3. 在 fixture 附近保留来源元数据（路径/commit），用于后续对齐与追溯
4. 不改动任何测试期望哈希（`wantHash / expected*` 常量保持原值）

## 硬约束（绝对不允许）

- 不得通过修改测试期望哈希去适配不同 genesis。哈希是当前这份 fixture 的既定回归锚点（如 `0x34cb47b1...`、`0x76541b14...`、`0x84308e7d...`、`0x67ead97e...`）。
- 不得将 `tests/eth-hive` 完整目录作为必需前置项；它仅作为可选完整 harness 保留路径。
- 修改应保持现有仓内测试语义，不新增 skip 分支掩盖逻辑。

## 当前实现位置（可复核）

- `internal/api/testdata/hive_engine_genesis.json`
- `internal/api/testdata/README.md`：记录来源与提交信息
- `internal/api/testdata/hive_engine_genesis.json.meta`：记录来源文件与上游提交
- `internal/api/genesis_hive_compat_test.go:87-106`
- `internal/api/engine_state_adapter_hive_test.go`：`loadHiveEngineGenesisFixture` 调用位于 `43`，以及在 `1786`、`1814` 的子数据库初始化路径中复用该加载函数
- `conf/genesis_config_test.go:135-149`

## 验收命令

```bash
go test -tags nosqlite,noboltdb -count=1 ./internal/api/ ./conf/
```

要求：

- 两包 pass
- 因缺 fixture 而 skip 的数量为 0（现在为 0）
- `git diff` 中不出现任何 `wantHash` / `expected*` 常量变更
- 证据上有 vendored 文件来源说明

## 独立待排事项（不影响当前收口）

将 16 个测试与 `tests/eth-hive` 代码持续演进的关系独立列入复跑项，不与当前回归恢复耦合：

- 建议在 Mac 上完整执行 `execution-spec-tests`（EEST）复跑，更新 `docs/engineering/HIVE_EEST_REPRODUCIBILITY.md` 日期与结论
- 关注的关键上游/本地改动点包括：
  - `75e99eb4`（07-18）`fix(engine): resolve safe/finalized via state adapter after eth-el restart`
  - `542d319e`（06-03）`fix: harden ethel sync and state execution`
  - `057adf39`（05-01）`fix(ethel): align engine payload compatibility`
  - 07-27 Codex 分支新增 `engine_api_v1.go`、`engine_state_adapter.go`（+43）及依赖升级（mdbx-go/cursor 重构、secp256k1/libsecp256k1、blst C core）
- 结论更新建议写入文档时明确区分“单元回归恢复”和“全量 EEST”两条线，避免将三个月前的通过口径误当当前状态。
