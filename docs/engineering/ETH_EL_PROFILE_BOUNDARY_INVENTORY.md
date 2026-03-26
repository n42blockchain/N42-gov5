# ETH EL Profile 边界盘点（Phase 0 起步）

> 目标：把当前代码库中“共享执行内核”、“`eth-el` 专属能力”、“`n42` 专属能力”、“待拆分污染项”先盘清楚，再进入真正的模块改造。

## 使用方法

这份 inventory 不解决所有问题，只回答四件事：

1. 这个模块今天主要服务谁？
2. 未来应归属 `shared / eth-el / n42 / split` 哪一类？
3. 最先要补的边界是什么？
4. 什么不应该在第一阶段动？

## 分类原则

- `shared`
  - 两种 profile 都要用，且没有必要复制实现。
- `eth-el`
  - 标准 Ethereum EL 运行人格专属或优先。
- `n42`
  - N42 链专属能力或只对 N42 有意义。
- `split`
  - 当前耦合在一起，后续必须拆分。

## 模块盘点

| 模块 | 当前职责 | 目标归属 | 当前判断 | 第一阶段动作 |
|---|---|---|---|---|
| `internal/api/engine_api_*` | Engine API v1-v4 | `shared` | 主体可共享，少量输出/兼容层需 profile 化 | 固定 `eth-el` 与 `n42` 的 payload/body/withdrawals 契约 |
| `internal/api/engine_payload_stateful.go` | payload 执行态接线 | `shared` | 是共享执行主路径的一部分 | 抽统一 hook，减少外部隐式依赖 |
| `internal/state_processor.go` | 串行区块执行 | `shared` | 标准 EVM 主路径，适合做公共内核 | 与并行路径统一 hook 契约 |
| `internal/parallel_processor.go` | 并行区块执行 | `shared` | 共享，但必须服从相同语义 | 先对齐 block-start/block-end 与 fork hook |
| `internal/blockhelp.go` | Prague 系统合约 / block start/end | `split` | 逻辑大体标准，但注册和生效条件应 profile 化 | 把 system contracts 移到 registry |
| `internal/vm/` | EVM 指令与预编译 | `shared + split` | 指令主路径共享，预编译注册需要拆分 | 做 precompile registry，默认 `eth-el` 只开标准项 |
| `common/transaction/` | 交易类型与编码 | `split` | 主体共享，但存在链特定 tx type / policy | 把链特定交易能力挂到 profile policy |
| `params/config.go` / `params/*` | fork / 链配置 | `split` | 配置结构可共享，但链特定字段过多 | 拆 profile config view，避免业务直接读全量字段 |
| `internal/node/` | 节点装配 | `split` | 当前是“全功能 N42 节点”装配中心 | 引入 `ChainProfile` 和 `NodeFactory` |
| `internal/consensus/` | 共识接口与引擎 | `split` | 通用接口被 N42 能力污染 | 先清理接口，再拆 `eth-el` 外部 CL 模式 |
| `internal/sync/` | 自有同步 | `split` | 共享价值有限，ETH sync 需求不同 | 先隔离接口，不急着直接做 ETH sync |
| `internal/p2p/` | 当前网络栈 | `n42` | 主要服务 N42，不能直接当 ETH devp2p 用 | 第一阶段只隔离，不直接重写 |
| `modules/state` | 状态读写与 changeset | `shared + split` | 状态转换路径共享，但承诺后端必须拆 | 先定义 state backend 接口 |
| `modules/rawdb` | 底层数据访问 | `shared` | 适合保留为共享基础设施 | 不先重写，只补 profile 约束 |
| `cmd/n42` | 主入口 | `split` | 当前偏 N42 全功能 | 第一阶段增加 profile 入口，不急着拆二进制 |
| `cmd/rpcdaemon` | 独立 RPC 入口 | `shared` | 有利于 `eth-el` 形态演进 | 补 profile-aware namespace 装配 |

## 当前最值得先拆的 6 个污染点

### 1. 共识接口污染

代表项：

- `GetDepositInfo`
- `GetAccountRewardUnpaid`

问题：

- 它们不属于通用 Ethereum EL 共识接口，却进入了所有引擎共同依赖的 reader 接口。

第一步：

- 先拆成 N42 扩展接口，不要求所有 profile 实现。

### 2. 状态转换链特判

代表项：

- `IsFree`
- `isBor`
- `isParlia`

问题：

- gas / fee / service tx 语义被链特定逻辑污染。

第一步：

- 把这些判断收进交易/执行 policy。

### 3. 系统合约生效条件

代表项：

- Prague block-start / block-end
- EIP-2935 / 7002 / 7251

问题：

- 逻辑本身标准，但当前仍偏“直接写死在主路径”。

第一步：

- 抽成 `SystemContractRegistry`。

### 4. 预编译注册

问题：

- EVM 指令主路径共享没问题，但预编译集合不能混在一起长期维护。

第一步：

- `eth-el` 默认只注册标准预编译。
- `n42` 在 registry 上额外叠加。

### 5. 节点装配过载

问题：

- `internal/node` 现在同时装配 EL、N42 共识、AI、分布式、桥接、zk 等大量服务。

第一步：

- 引入 `ChainProfile`，先让装配过程能显式区分 `eth-el` 和 `n42`。

### 6. 状态承诺边界不清

问题：

- 现在执行路径、状态读写路径、状态承诺路径还没有被明确隔开。

第一步：

- 先定义 state backend 接口，暂时不直接进入 MPT 实现。

## 明确什么先不动

第一阶段不建议直接动这些大块：

- 完整 devp2p
- 完整 ETH snap sync
- 完整 MPT 主状态后端
- `cmd/n42el` 独立新二进制
- 大规模复制 geth/erigon 目录结构

原因：

- 这些都属于高成本实现项。
- 在 profile 边界没收稳之前，越早动，返工概率越高。

## 建议的起步顺序

1. 定义 `ChainProfile` 最小接口。
2. 在 `internal/node` 上做第一层 profile-aware 装配。
3. 从共识接口中拿掉 N42 专属 reader 方法。
4. 从状态转换里抽出链特定 policy。
5. 把系统合约和预编译改成 registry。
6. 再开始定义 state backend 抽象。

## 完成标准

这份 inventory 的价值不在于“列得全”，而在于后续每一笔改动都可以对照：

- 它是在减少耦合，还是在继续堆耦合？
- 它是共享内核的一部分，还是 profile 应该承接的部分？
- 它是否会让 `eth-el` 更自洽，而不伤害 `n42`？

只要这三件事能持续回答清楚，后续推进就会踏实很多。
