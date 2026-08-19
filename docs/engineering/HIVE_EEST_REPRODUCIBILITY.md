# Hive/EEST 可重复复测说明

## 当前结论

为通过 Hive/EEST 而做的必要代码修改都已提交到当前验证分支或对应的嵌套测试仓库，不只存在于本地工作树。最终复测时三个仓库均为 clean。

最近这条和 Hive/EEST 直接相关的基础收口主要包括：

- `102100cfd` `refactor(execution): centralize prague system contract wiring`
- `3a514ab4c` `refactor(execution): unify block start hooks`
- `a50de0e8c` `refactor(execution): unify block end hooks`
- `01a875570` `refactor(system): share prague contract deployment definitions`
- `951ce8a88` `refactor(system): centralize beacon and prague contract code`

这些改动的目标不是“为测试写特判”，而是把 Prague/Cancun 相关的 block hook、system contract 地址、部署字节码和 devnet 初始化收成共享入口，减少今后复测时出现“代码过了，但 Hive 用的还是旧路径/旧副本”的漂移。

## 已确认结果（2026-08-19）

本节只记录有完整结束摘要、退出状态或 Hive JSON 结果可核对的运行。最近一轮结果如下：

| 完成日期 | 范围 | Fixture | 结果 | 耗时 |
|---|---|---|---|---|
| 2026-08-19 | EEST `consume rlp` 全量 | `stable@latest` v5.4.0 | `47,589 / 47,589` 通过，RC `0`，无 failed/error/rerun | `10:36:15` |
| 2026-08-17 | EEST `consume engine` Paris+Shanghai | `stable@latest` v5.4.0 | `3,573 / 3,573` 通过，RC `0` | `0:52:51` |
| 2026-08-17 | EEST `consume engine` Cancun | `stable@latest` v5.4.0 | `17,783 / 17,783` 通过，RC `0` | `3:56:13` |
| 2026-08-17 | EEST `consume engine` Prague | `stable@latest` v5.4.0 | `20,878 / 20,878` 通过，RC `0` | `4:41:44` |
| 2026-08-17 | EEST `consume engine` Osaka | `develop@latest` v5.4.0 | `21,583 / 21,583` 通过，RC `0` | `5:04:19` |
| 2026-08-17 | EEST `consume engine` EIP-2930 access-list 跨 fork | `stable@latest` v5.4.0 | `2,132 / 2,132` 通过，RC `0` | `0:28:05` |
| 2026-08-17 | Hive `ethereum/engine` `eth-el` 全套 | Hive suite | `403 / 403` 通过 | 见 JSON 产物 |
| 2026-08-17 | Hive `ethereum/engine` 主 `n42` 适用集 | Hive suite | `311 / 311` 通过 | 见 JSON 产物 |

Engine shard 之间并非全部互斥，尤其 `engine-access-list` 会与 fork shard 重叠，因此不能把表内数字相加后称为唯一用例总数。`47,589` 只表示完整 stable RLP fixture 集的实际收集和执行数量。

结论边界：上述结果确认 N42 `n42_local` execution-layer client 通过 Hive 执行了完整 stable RLP 集、列出的 Engine broad matrix，以及 `ethereum/engine` simulator 的 `eth-el` 全套和主 `n42` 适用集；不能据此宣称 upstream Hive 仓库中的 `sync`、`rpc-compat`、`graphql` 等所有 simulator 均已全量运行。

## 版本与证据

最终运行固定版本：

- Engine：N42 `9c0ff307624698916f078e1fc057bc3125b93d9d`，Hive `b54317a81e9a226f5899eba2fb27f4a01ff21ffc`，EEST `e78efc220fd6cebdaa131435167f43c3b83236ea`
- RLP：N42 `87db38feb03c94332b0a99d969dfa169f970d8e0`，Hive/EEST 同上
- Hive `403 / 403` 和原生适用集 `311 / 311`：N42 `ed995d69ff4e5013ebacfac411373250df33be56`，Hive `b54317a81e9a226f5899eba2fb27f4a01ff21ffc`
- 之后合入的 N42 `9c0ff3076` 只修改 manifest 工具和文档，不影响 Hive execution path，因此没有把早一版 Hive 证据错误标记为在该 SHA 上运行。

关键本地产物：

- Engine 全量目录：`tests/results/eest-shards/20260817-latest-main-sync-full-engine-cachefix/`
  - `summary.md` SHA-256: `575d9e5448094bdd44c327cde2d383f935b0bdaa0dcb05066c2ac26d46f73936`
- RLP 全量目录：`tests/results/eest-shards/20260818-latest-main-sync-full-rlp-sidecarfix/`
  - `rlp.log` SHA-256: `410287f218e2ff64e04cbee5a14320ec61878fe069230a4a9177548392eead35`
  - `rlp.meta` SHA-256: `eabd798d3bfb759125ad976234f31577a90c992163e4debb5b7778d78c1536ba`
  - `summary.md` SHA-256: `0867d6dd94efd531762a44d515c918a697c57dd1692cdb5902d0834d8398e722`
- Hive eth-el 目录：`tests/results/hive-engine-ethel-all-latest-head-20260817/`
  - 五个 suite JSON SHA-256：`769269bd43d3804326403303722edcddb73845dfa78ac590ffe6aa59d6199888`、`360d008950588d3b5cbb82d8fd5ee63a15131981a005e4f325b9d6263ceabc40`、`e333f420ad56c09fb36c0653d359a3f7aa391b27f1875ff14c64467ff3b829f6`、`f87ae9504ec6842fd975ecb16eccce1ed9f076b4470a7f016705c7918b7dc112`、`7cfce2d87767a8ac2044f4958ff7358e53d9f4994fadb3192fd9d46cc68d0df2`
- Hive 原生适用集目录：`tests/results/hive-engine-native-applicable-latest-head-20260817/`
  - 五个 suite JSON SHA-256：`fde706e0834b27c210b86a397a89069d11517f44c9dc07cc11bf7e534cedf3f6`、`72670a3f44737c7241a497ff5e5b89bd11e6240ee5dfd387ffee2837fcebcdef`、`6803fb7f4ee76c2780dbc148c06b7025511bb420f50d0a565dfa9f850035eed9`、`e9a9f9126051470acd5b588acc431ca4c7ab783b2223280bc8c0e9b5e7d45f43`、`f2afd081aec35bf0a2175a6cc5391bd16ee06dee9a6e78546a8ffa135917efa6`

这些目录按仓库约定属于被忽略的本地测试产物，不提交大体积原始日志；本文保留结果、版本、路径和哈希，供同一测试机复核。

本地复核结果：

- 当前 HEAD 项目门禁 `make build`、`make test-short`、`make test`、`make lint`、`go vet ./...`、`make race-core`、`go test -race -short ./...` 和 `go test ./scripts` 全部通过。日志 `tests/results/final-gates-20260819/run.log`，SHA-256 `619cda463bc322b42a85e51f931885df30aa9f9e9c6e1a3409133c753dbbb8c1`。这组结果代表整个 Go 项目，不等同于 EEST 的 eth-el 兼容性结果。
- Hive/EEST 的 Engine API 回归使用宿主机映射端口；Hive 端在容器存活检查后重新读取端口映射，避免 Docker Desktop 端口尚未出现时回退到不可达的 `172.17.x.x`。
- 修复了 Prague payload 校验中 intrinsic gas 与 floor data gas 同时失败时的错误优先级，并用 EEST 异常映射覆盖对应客户端错误文本。
- EEST `e78efc220` 修复 release metadata 缓存：刷新失败时保留有效旧缓存、拒绝空 release 列表并原子替换缓存，避免并发或短暂 GitHub API 异常把缓存写成 `[]`。
- N42 `87db38feb` 在 block-body RLP 边界拒绝带 blob sidecar 的网络传播包装，关闭了完整 RLP 初跑发现的 6 个失败；定向 6 项和随后全量 `47,589` 项均通过。

与 `connect` 相关的关键判断：`tests/eth-hive` 已在本地适配中为非 `python-requests` 客户端返回 `RPCAddress`/`EngineAddress`，EEST/dev 模式通过宿主映射端口访问；容器内的原生 Hive simulator 则继续使用容器 IP。这样避免 macOS 上从宿主机直连 `172.17.x.x`，也避免 simulator Docker module 编译时依赖未发布的本地 Hive API。

当前验证版本：Hive `b54317a8`，EEST `e78efc220`；EEST fixtures 使用 v5.4.0，stable shard 使用 `stable@latest`，Osaka Engine shard 使用 `develop@latest`。

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

2026-08-18 至 19 日最终全量 RLP 运行使用的等价命令为：

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
