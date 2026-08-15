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

## 已确认结果（2026-08-15）

本节只记录有完整结束摘要、退出状态或 Hive JSON 结果可核对的运行。最近一轮结果如下：

| 完成日期 | 范围 | Fixture | 结果 | 耗时 |
|---|---|---|---|---|
| 2026-08-12 | EEST `consume rlp` 全量 | `stable@latest` v5.4.0 | `47,589 / 47,589` 通过，`0` failed，`0` errors | `16:16:34` |
| 2026-08-02 | EEST `consume engine` Paris+Shanghai | `stable@latest` v5.4.0 | `3,573` 通过，RC `0` | `0:39:20` |
| 2026-08-02 | EEST `consume engine` Cancun | `stable@latest` v5.4.0 | `17,783` 通过，RC `0` | `3:01:04` |
| 2026-08-02 | EEST `consume engine` Prague | `stable@latest` v5.4.0 | `20,878` 通过，RC `0` | `3:45:17` |
| 2026-08-02 | EEST `consume engine` Osaka | `develop@latest` v5.4.0 | `21,583` 通过，RC `0` | `3:52:31` |
| 2026-08-02 | EEST `consume engine` EIP-2930 access-list 跨 fork | `stable@latest` v5.4.0 | `2,132` 通过，RC `0` | `0:24:02` |
| 2026-07-31 | 原生 Hive `ethereum/engine` `engine-auth/` | Hive suite | `1` suite、`8 / 8` cases 通过 | 约 `19s` |

Engine shard 之间并非全部互斥，尤其 `engine-access-list` 会与 fork shard 重叠，因此不能把表内数字相加后称为唯一用例总数。`47,589` 只表示完整 stable RLP fixture 集的实际收集和执行数量。

结论边界：上述结果确认 N42 `n42_local` execution-layer client 通过 Hive 执行了完整 stable RLP 集和列出的 Engine broad matrix；原生 Hive 自身有完整结果证据的是 `engine-auth`，不能据此宣称 upstream Hive 仓库中的 `sync`、`rpc-compat`、`graphql`、`devp2p` 等所有 simulator 均已全量运行。

## 版本与证据

已同步到最新：

- `tests/eth-hive`: `f28302b5`（2026-08-02）
- `tests/eth-tests/execution-spec-tests`: `main`（`bb5ca80f`；基于 `0eb24a2b`，补充 v5.4.0 legacy typed-transaction exception 解析兼容）
- 2026-08-12 RLP 全量运行的 N42 验证代码：`be09df4e2`

关键本地产物：

- RLP 全量日志：`tests/eth-tests/execution-spec-tests/logs/consume-rlp-20260812-071509-main.log`
  - SHA-256: `ddab5e929579cd3c4e689c892977e8c53e1d690f381aa4c4ed56448e2217ed87`
  - 日志内 `START TEST`、`END TEST`、`PASSED` 均为 `47,589`
- Engine broad 摘要：`tests/results/eest-engine-full-rerun-20260802-host-retry/summary.md`
  - SHA-256: `d4043ab64c02c98375d004d2f00511c21fa9389b3399666c1d2574e3b1070402`
  - 该历史 runner 未把 N42 源码 SHA 写进产物，因此 Engine 结果按完成日期、EEST ref、fixture 版本和日志哈希固定，不能反推一个未记录的 N42 commit
- Hive `engine-auth` JSON：`tests/results/hive-engine-smoke-20260731-rerun/1785481958-17cff0979259e19ee35375d7122a4203.json`
  - SHA-256: `a430fa56e560f7ba0863121794a8345454ce17a5d7c1d22a91b0c039c783b017`

这些目录按仓库约定属于被忽略的本地测试产物，不提交大体积原始日志；本文保留结果、版本、路径和哈希，供同一测试机复核。

本地复核结果：

- 项目级 `make test`、`make build`、`make lint` 分开通过；这组结果代表整个 Go 项目，不等同于 EEST 的 eth-el 兼容性结果。
- Hive/EEST 的 Engine API 回归使用宿主机映射端口；Hive 端在容器存活检查后重新读取端口映射，避免 Docker Desktop 端口尚未出现时回退到不可达的 `172.17.x.x`。
- 修复了 Prague payload 校验中 intrinsic gas 与 floor data gas 同时失败时的错误优先级，并用 EEST 异常映射覆盖对应客户端错误文本。

与 `connect` 相关的关键判断：`tests/eth-hive` 已在本地适配中为非 `python-requests` 客户端返回 `RPCAddress`/`EngineAddress`，EEST/dev 模式通过宿主映射端口访问；容器内的原生 Hive simulator 则继续使用容器 IP。这样避免 macOS 上从宿主机直连 `172.17.x.x`，也避免 simulator Docker module 编译时依赖未发布的本地 Hive API。

当前依赖版本：Hive `f28302b5`（2026-08-02），EEST `bb5ca80f`；EEST fixtures 使用 v5.4.0，stable shard 使用 `stable@latest`，Osaka Engine shard 使用 `develop@latest`。

对应接口定义：

- `tests/eth-hive/internal/simapi/simapi.go`：`StartNodeResponse{ID, IP, RPCAddress, EngineAddress, HostPorts}`

## 测试范围边界

需要把“全部测试”和“eth-el 测试”分开记录：

| 范围 | 入口 | 说明 |
|---|---|---|
| 项目全部 Go 包 | `make test`、`make build`、`make lint` | 覆盖仓库级编译、单元测试和静态检查，不依赖 Hive。 |
| eth-el Engine API | `run_eest_shards.sh paris+shanghai cancun prague osaka engine-access-list` | 通过 Hive 启动 N42 execution client，验证 Engine API 和执行语义。 |
| eth-el RLP 启动导入 | `run_eest_shards.sh rlp` | 使用真正的 `consume rlp`，结果单独统计，不与 Engine API access-list 子集混淆。 |
| 原生 Hive simulator | `hive --sim ...` | 每个 simulator 独立统计；EEST-over-Hive 的通过结果不能替代 upstream Hive 全 simulator catalog。 |

EEST/Hive 这些 `consume` 命令都只代表 `n42_local` 这个 execution-layer client 的兼容性，不应写成整个 Go 项目的“全量测试”。反过来，项目级 Go gate 也不能替代 EEST 的 Engine/RLP 测试。

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
- `EEST_RPC_TIMEOUT`（默认 90 秒，避免电脑休眠或网络异常让单个 HTTP 请求无限阻塞）
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
./scripts/run_eest_shards.sh paris+shanghai cancun prague osaka engine-access-list
```

或按需单跑某个 shard：

```bash
./scripts/run_eest_shards.sh prague
```

真正的 RLP 启动导入测试单独运行：

```bash
./scripts/run_eest_shards.sh rlp
```

2026-08-12 最终全量 RLP 运行使用的等价命令为：

```bash
HIVE_SIMULATOR=http://127.0.0.1:3003 \
EEST_RPC_TIMEOUT=120 \
uv run --python 3.13 consume rlp \
  --input stable@latest -n 1 --no-html
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
