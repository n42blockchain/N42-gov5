# Hive/EEST 可重复复测说明

## 当前结论

为通过 Hive/EEST 而做的必要代码修改都已提交到当前验证分支或对应的嵌套测试仓库，不只存在于本地工作树。最终复测记录显示三个仓库均无 tracked changes；Hive client 快照和构建目录是预期的未跟踪运行产物。

最近这条和 Hive/EEST 直接相关的基础收口主要包括：

- `102100cfd` `refactor(execution): centralize prague system contract wiring`
- `3a514ab4c` `refactor(execution): unify block start hooks`
- `a50de0e8c` `refactor(execution): unify block end hooks`
- `01a875570` `refactor(system): share prague contract deployment definitions`
- `951ce8a88` `refactor(system): centralize beacon and prague contract code`

这些改动的目标不是“为测试写特判”，而是把 Prague/Cancun 相关的 block hook、system contract 地址、部署字节码和 devnet 初始化收成共享入口，减少今后复测时出现“代码过了，但 Hive 用的还是旧路径/旧副本”的漂移。

## 已确认结果（2026-08-20）

本节只记录有完整结束摘要、退出状态或 Hive JSON 结果可核对的运行。最近一轮结果如下：

| 完成日期 | 范围 | Fixture | 结果 | 耗时 |
|---|---|---|---|---|
| 2026-08-20 | EEST `consume rlp` 全量 | `stable@latest` v5.4.0 | `47,589 / 47,589` 通过，RC `0`，无 failed/error/rerun | `11:30:32` |
| 2026-08-19 | EEST `consume engine` Paris+Shanghai | `stable@latest` v5.4.0 | `3,573 / 3,573` 通过，RC `0` | `0:44:00` |
| 2026-08-19 | EEST `consume engine` Cancun | `stable@latest` v5.4.0 | `17,783 / 17,783` 通过，RC `0` | `3:45:27` |
| 2026-08-19 | EEST `consume engine` Prague | `stable@latest` v5.4.0 | `20,878 / 20,878` 通过，RC `0` | `4:27:11` |
| 2026-08-19 | EEST `consume engine` Osaka | `develop@latest` v5.4.0 | `21,583 / 21,583` 通过，RC `0` | `4:34:31` |
| 2026-08-19 | EEST `consume engine` EIP-2930 access-list 跨 fork | `stable@latest` v5.4.0 | `2,132 / 2,132` 通过，RC `0` | `0:26:57` |
| 2026-08-19 | Hive `ethereum/engine` `eth-el` 全套 | Hive suite | `403 / 403` 通过 | 见 JSON 产物 |
| 2026-08-19 | Hive `ethereum/engine` 主 `n42` 适用集 | Hive suite | `311 / 311` 通过 | 见 JSON 产物 |

Engine shard 之间并非全部互斥，尤其 `engine-access-list` 会与 fork shard 重叠，因此不能把表内数字相加后称为唯一用例总数。`47,589` 只表示完整 stable RLP fixture 集的实际收集和执行数量。

结论边界：上述结果确认 N42 `n42_local` execution-layer client 通过 Hive 执行了完整 stable RLP 集、列出的 Engine broad matrix，以及 `ethereum/engine` simulator 的 `eth-el` 全套和主 `n42` 适用集；不能据此宣称 upstream Hive 仓库中的 `sync`、`rpc-compat`、`graphql` 等所有 simulator 均已全量运行。

## 版本与证据

完整 Hive/EEST 运行固定版本：

- Engine 与 RLP：N42 `36822b5c5cb73640bad3e87941077c9c9fac726c`，Hive `b54317a81e9a226f5899eba2fb27f4a01ff21ffc`，EEST `e78efc220fd6cebdaa131435167f43c3b83236ea`
- Hive `403 / 403` 和原生适用集 `311 / 311`：同一 N42 `36822b5c5cb73640bad3e87941077c9c9fac726c` 与 Hive revision。
- 该 N42 revision 已合入 2026-08-19 fetch 到的 7 个最新 `main` 提交（sender verification、VM、state、rawdb、zk guest），并以 `36822b5c5` 修复同步后暴露的 lint/错误处理问题。

完整矩阵完成后又合入了 10 个较新的 `main` 提交，并提交了 MDBX 日志解耦和 Block-STM scheduler frontier 自恢复修复。代码验证 revision 为 N42 `520fede6f3e735696a8a80c40bb8d774131da561`。`36822b5c5..520fede6f` 的审计没有发现 dedicated eth-el Engine API、EVM 或 RLP decoder 包变更；变化集中在 replay/witness、LtHash bookkeeping、txindex、QMDB/MDBX、HotStuff canonicalization、Block-STM liveness 和 benchmark 文档。该 delta 已通过下述完整项目门禁，但约 26 小时的 Hive/EEST 矩阵没有在 `520fede6f` 上重复，因此不能把 `520fede6f` 写成完整矩阵的实际运行 revision。

关键本地产物：

- Engine 全量目录：`tests/results/eest-shards/20260819-post-sync-full-engine/`
  - `summary.md` SHA-256: `aa2f1a56495eeb6863ca68ee7f6a9fdb5506df52beef4a0b196f480ae74bb706`
- RLP 全量目录：`tests/results/eest-shards/20260819-post-sync-full-rlp/`
  - `rlp.log` SHA-256: `0f308a0eb6361c9f175cec7f6816b514900f963f08a54d9519e45d2dc0133244`
  - `rlp.meta` SHA-256: `7ec6439a1753beffc3fce719679008739f0a10c815d4396d80864078971eeb8c`
  - `summary.md` SHA-256: `7c6141bf7c6e2f2c7edd1559c8ad3400a9913e992dafeab5dbf2b34e8b77b637`
- Hive eth-el 目录：`tests/results/hive-engine-ethel-all-post-sync-20260819/`
  - 五个 suite JSON SHA-256：`df08fe52e78f17af77d50084dfc284edd676fc05bc697afa31fb65a3b8444d48`、`267d9084fce6cab17f1bdd27a7721af7bd2373c42a121f2cc432e2266ce62907`、`796104319d8a8c5a63cbd833043e08d1880a738623d7aaa5347b8b405904cdec`、`d3472e7efd93bce1502acfeeed7b9b36b0fab7dfe4c350efa2c850aa08feb6f9`、`f7d1af20ffb975580fc68356ab13dd029a7aef6ccd7e76653289505a4bc5ac7a`
- Hive 原生适用集目录：`tests/results/hive-engine-native-applicable-post-sync-20260819/`
  - API、withdrawals、Cancun、auth、exchange JSON SHA-256：`303e348f4e69f11d700f2ee12364b22e448a1cb2d8b28fc2531e76e9b3a7f6a7`、`1a0d9e66d36cde5d6e13cabacd8299647ccc9b9ca6afa2a5d108ddfc687fed24`、`4aa03a52316fb7efc38d4f51a970841190e08b14d135d8d30cb57a8ef4033cc6`、`2c5d8f69091613aa4a9e70401cf8488c51d6d551817a517e2c68e0b35ed9179b`、`bf2657dd56d6a4a7ee8fd85ebc127eabf80de531c8279b2d4a2e9f6aa69a4bab`

这些目录按仓库约定属于被忽略的本地测试产物，不提交大体积原始日志；本文保留结果、版本、路径和哈希，供同一测试机复核。

本地复核结果：

- N42 `520fede6f3e735696a8a80c40bb8d774131da561` 的项目门禁 `make build`、`make test-short`、`make test`、`make lint`、`go vet ./...`、`make race-core`、`go test -race -short ./...` 和 `go test ./scripts` 全部通过。日志 `tests/results/final-gates-20260820-latest-fixed/run.log`，SHA-256 `42b71f9bb3d2f7460f5bd646971ee452ef4a9e65faaca86ce0a568d8280acaa1`。这组结果代表整个 Go 项目，不等同于在该 revision 重跑了 EEST/Hive。
- 2026-08-20 的 `make eest-audit` 明确把最新 `20260819-post-sync-full-engine` 和 `20260819-post-sync-full-rlp` 标为 `PASS`。聚合审计仍返回非零，因为结果树保留了 72 个早期失败/中断 run 和 3 个明确跳过的历史 run；这些历史证据不删除、也不改写成通过。
- Hive/EEST 的 Engine API 回归使用宿主机映射端口；Hive 端在容器存活检查后重新读取端口映射，避免 Docker Desktop 端口尚未出现时回退到不可达的 `172.17.x.x`。
- 修复了 Prague payload 校验中 intrinsic gas 与 floor data gas 同时失败时的错误优先级，并用 EEST 异常映射覆盖对应客户端错误文本。
- EEST `e78efc220` 修复 release metadata 缓存：刷新失败时保留有效旧缓存、拒绝空 release 列表并原子替换缓存，避免并发或短暂 GitHub API 异常把缓存写成 `[]`。
- N42 `87db38feb` 在 block-body RLP 边界拒绝带 blob sidecar 的网络传播包装，关闭了完整 RLP 初跑发现的 6 个失败；定向 6 项和随后全量 `47,589` 项均通过。

与 `connect` 相关的关键判断：`tests/eth-hive` 已在本地适配中为非 `python-requests` 客户端返回 `RPCAddress`/`EngineAddress`，EEST/dev 模式通过宿主映射端口访问；容器内的原生 Hive simulator 则继续使用容器 IP。这样避免 macOS 上从宿主机直连 `172.17.x.x`，也避免 simulator Docker module 编译时依赖未发布的本地 Hive API。

完整矩阵 harness 版本：Hive `b54317a8`，EEST `e78efc220`；EEST fixtures 使用 v5.4.0，stable shard 使用 `stable@latest`，Osaka Engine shard 使用 `develop@latest`。

对应接口定义：

- `tests/eth-hive/internal/simapi/simapi.go`：`StartNodeResponse{ID, IP, RPCAddress, EngineAddress, HostPorts}`

## 测试范围边界

需要把“全部测试”和“eth-el 测试”分开记录：

| 范围 | 入口 | 说明 |
|---|---|---|
| 项目全部 Go 包 | `make test`、`make build`、`make lint` | 覆盖仓库级编译、单元测试和静态检查，不依赖 Hive。 |
| `eth-el` 原生 Hive Engine 全套 | `hive --sim ethereum/engine --client n42_ethel --sim.limit 'engine-(auth\|exchange-capabilities\|api\|withdrawals\|cancun)/'` | `403` 项；包含 Fork ID、devp2p 同步/重组、multi-client、payload-body 和 pooled-transaction P2P 场景。 |
| 主 `n42` 原生 Hive 适用集 | 同一 simulator 的精确适用用例选择器 | `311` 项；是 `eth-el` 集的严格子集，无 native-only 项。 |
| eth-el Engine API | `run_eest_shards.sh paris+shanghai cancun prague osaka engine-access-list` | 通过 Hive 启动 N42 execution client，验证 Engine API 和执行语义。 |
| eth-el RLP 启动导入 | `run_eest_shards.sh rlp` | 使用真正的 `consume rlp`，结果单独统计，不与 Engine API access-list 子集混淆。 |
| 原生 Hive simulator | `hive --sim ...` | 每个 simulator 独立统计；EEST-over-Hive 的通过结果不能替代 upstream Hive 全 simulator catalog。 |

EEST/Hive 这些 `consume` 命令都只代表 `n42_local` 这个 execution-layer client 的兼容性，不应写成整个 Go 项目的“全量测试”。反过来，项目级 Go gate 也不能替代 EEST 的 Engine/RLP 测试。

主 `n42` 二进制不提供 `eth-el` 专用的完整 Ethereum devp2p 测试表面，因此不能把不适用项当成原生失败，也不能宣称原生执行了 `403` 项。两个结果集按规范化用例名核对后，原生侧排除项恰好为：

- API `30` 项：Fork ID `6`，missing-ancestor/devp2p syncing `24`
- Withdrawals `11` 项：同步、重组和 payload-body P2P
- Cancun `51` 项：Fork ID `24`，missing-ancestor/devp2p syncing `24`，multi-client `1`，pooled-transaction P2P `2`

合计排除 `92` 项，且 `native-only = 0`。最终结果必须分别写成 `eth-el 403 / 403` 和“原生适用集 `311 / 311`”，不能合并成一个模糊的“全量 Hive”结论。

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

2026-08-19 至 20 日最终全量 RLP 运行使用的等价命令为：

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
