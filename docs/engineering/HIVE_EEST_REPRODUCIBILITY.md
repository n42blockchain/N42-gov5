# Hive/EEST 可重复复测说明

## 当前结论

为通过 Hive/EEST 而做的**必要代码修改**已经进入 `main`，不是只留在本地工作树。

最近这条和 Hive/EEST 直接相关的基础收口主要包括：

- `102100cfd` `refactor(execution): centralize prague system contract wiring`
- `3a514ab4c` `refactor(execution): unify block start hooks`
- `a50de0e8c` `refactor(execution): unify block end hooks`
- `01a875570` `refactor(system): share prague contract deployment definitions`
- `951ce8a88` `refactor(system): centralize beacon and prague contract code`

这些改动的目标不是“为测试写特判”，而是把 Prague/Cancun 相关的 block hook、system contract 地址、部署字节码和 devnet 初始化收成共享入口，减少今后复测时出现“代码过了，但 Hive 用的还是旧路径/旧副本”的漂移。

## 当前记录状态（2026-07-28）

已同步到最新：

- `tests/eth-hive`: `a74a5a70`
- `tests/eth-tests/execution-spec-tests`: `main`（`10eaa63d`）

本地复核结果：

- `go test -tags nosqlite,noboltdb -count=1 ./internal/api/ ./conf/` 通过，16 个 hive fixture 相关测试已恢复可跑。
- `execution-spec-tests` 单条 `consume-engine` 用例重跑（`stable@latest`）在 6 分多失败，失败点为执行期连接超时：
  - `HTTPConnectionPool(host='172.17.0.4', port=8551): ConnectTimeout`

与 `connect` 相关的关键判断：`tests/eth-hive` 当前 API 的 `StartNodeResponse` 仅返回 `id` 与 `ip`（未包含 `rpcUrl`/`engineUrl`），在 macOS 上容器 `172.17.x.x` 直连仍不稳定；当前最小复现路径不在测试期望哈希层面。

对应接口定义：

- `tests/eth-hive/internal/simapi/simapi.go`：`StartNodeResponse{ID, IP}`

## 复测时必须满足的前提

### 1. 每次启动 Hive 前先同步 `n42-local`

必须先运行：

```bash
./scripts/prepare_hive_n42_client.sh
```

或者直接通过：

```bash
./scripts/start_hive_dev_n42.sh
```

原因：

- Hive 本地 client 镜像使用的是 `tests/eth-hive/clients/n42/n42-local`
- 如果不重新同步，这个目录可能还是旧源码快照
- 旧快照会导致“主仓代码已修复，但 Hive 仍然复现旧失败”的假阴性

### 2. 固定 EEST 运行入口

优先使用现有脚本，不要临时手敲不同口径：

- `./scripts/run_eest_local.sh`
- `./scripts/run_eest_shards.sh`
- `make eest-watch`
- `make eest-cycle`
- `make eest-audit`
- `make eest-repair`

这些入口已经把：

- shard selector
- `consume engine` / `fill` 的口径差异
- 日志输出目录
- watcher / cycle 状态文件

固定了下来。

其中：

```bash
make eest-audit
```

会审计 `tests/results/eest-shards/` 下的结果目录，直接标出缺 `summary.md`、缺 `rc/duration`、缺 `.log` 的不完整 run。

如果要把显式忽略的历史目录也视为失败，可以改用：

```bash
make eest-audit FAIL_ON_SKIP=1
```

这会把 `.eest-audit-ignore` 标记的 run 继续显示为 `SKIP`，但整体退出码改成失败，适合更严格的本地 gate 或临时 CI 验证。

```bash
make eest-repair
```

会对历史结果目录执行最小修复：

- 缺 `summary.md` 时按现有 `.meta` 回填摘要
- 缺 `rc/duration_seconds` 时显式补成 `incomplete` / `-`
- 对完全空的历史目录写入 `.eest-audit-ignore`，避免后续审计反复报同一个废弃产物

### 3. 固定 Python 版本

EEST 相关命令统一走 `uv run --python 3.13`。

当前环境里，`3.14` 可能触发 `PyO3/pydantic-core` 一类构建问题，导致不是协议失败而是工具链失败。

## 推荐复测流程

### 启 Hive dev backend

```bash
./scripts/start_hive_dev_n42.sh
```

默认会：

- 同步 `n42-local`
- 生成 `tests/eth-hive/n42-clients.yaml`
- 启动 `hive --dev`

这里需要区分两层：

- Hive 自己跑的是 `hive --dev`
- N42 作为 Ethereum EL 私链节点时，当前固定入口是 `n42 --ethdev`

不要把 Hive 的 `--dev` 和 N42 的 `--dev` 混成同一个模式。当前 Hive/EEST 互操作口径下，N42 应使用 `--ethdev`，而不是 N42 的 HotStuff/JMT `--dev`

### 跑 broad shards

```bash
./scripts/run_eest_shards.sh paris+shanghai cancun prague osaka
```

或按需单跑某个 shard：

```bash
./scripts/run_eest_shards.sh prague
```

### 挂巡检

```bash
make eest-watch LOG=/abs/path/to/pytest.log INTERVAL=300
```

### 挂自动循环

```bash
make eest-cycle NAME=prague RUN='...'
```

## 哪些目录是结果，不是源码

下面这些目录只用于产物和本地验证，不应当被当成“待提交源码改动”：

- `tests/results/eest-shards/`
- `tests/eth-tests/execution-spec-tests/logs/`
- `logs/`

## 复测失败时先检查什么

优先检查下面 3 件事，而不是马上怀疑执行逻辑回退：

1. `n42-local` 是否已重新同步
2. Hive 是否真的重建了最新 client 镜像
3. 本次跑的是不是和上次一致的 shard / `sim.limit` / Python 版本

只有这三件事都确认无误，再开始判定是否是新的执行语义回归。
