# ETH EL 基础补全推进清单

> 目标：先把 `eth-el` 与 `n42` 的基础边界补齐，再进入高成本的 MPT / devp2p / ETH sync 实现；每一步都要求有明确退出条件，避免“架构设计先行、基础约束滞后”。

## 推进原则

1. 先做边界清理，再做大功能引入。
2. 先让 `eth-el` 变成“自洽人格”，再追求真实 Ethereum 网络接入。
3. 每一步都必须有自动化回归或静态证据，不靠主观判断推进。
4. 不在共享执行路径里继续堆散落的链特判。

## Phase 0：范围冻结

### 0.1 明确 `eth-el` 的目标与非目标

- 定义 `eth-el` 的第一阶段目标：
  - 标准 Engine API
  - 标准 `eth_* / debug_* / trace_*`
  - 标准 Hive/EEST
  - 标准 fork 语义
- 定义第一阶段非目标：
  - MPT 主状态后端落地
  - devp2p / ETH 主网接入
  - 所有 N42 特性在 `eth-el` 中的可配置复用

退出条件：

- 有一份固定范围清单，后续设计与实现都以此为准。

### 0.2 列出“共享模块 / Profile 模块 / 禁止混入模块”

- 共享模块：
  - EVM 指令执行
  - Engine API 主流程
  - 通用区块执行
  - 通用 RPC 框架
- `eth-el` 专属模块：
  - Ethereum profile 装配
  - 标准系统合约/预编译注册
  - 标准 ETH payload/body/withdrawals 兼容路径
- `n42` 专属模块：
  - HotStuff/APoS
  - AI/PQ/分布式扩展
  - JMT/LtHash 特定优化

退出条件：

- 每个核心目录都能回答“共享 / eth-el / n42 / 待拆分”归属。

## Phase 1：边界去污染

### 1.1 引入 `ChainProfile` 抽象

- 新建统一 profile 接口。
- 至少准备：
  - `eth` profile
  - `n42` profile
- 禁止新代码直接在共享路径里写 `if isEth` / `if isN42` 分支。

退出条件：

- 新节点装配入口可以显式接收 profile。

### 1.2 清理共识接口污染

- 审核并剥离共识接口中的 N42 特有方法，例如：
  - `GetDepositInfo`
  - `GetAccountRewardUnpaid`
- 把 N42 特有能力从通用 `ChainHeaderReader` / `EngineReader` 中下沉到扩展接口。

退出条件：

- 通用共识接口不再要求所有链都实现 N42 特有方法。

### 1.3 清理状态转换中的遗留链特判

- 盘点并隔离：
  - `isBor`
  - `isParlia`
  - `IsFree`
- 把链特定 gas / fee / service tx 逻辑迁移到 profile policy。

退出条件：

- 共享状态转换路径不再直接引用非 Ethereum/N42 通用语义。

## Phase 2：执行内核收敛

### 2.1 抽共享执行内核

- 统一这些路径的调度入口：
  - `internal/state_processor.go`
  - `internal/parallel_processor.go`
  - `internal/api/engine_payload_stateful.go`
- 形成单一的 block start / tx execute / block end hook 结构。

退出条件：

- 串行与并行路径共享同一套 profile hook 契约。

### 2.2 系统合约与预编译注册表化

- 把注册从“散落逻辑”收敛为 profile registry。
- `eth-el` 默认只打开标准 Ethereum 规范项。
- `n42` profile 再额外叠加 N42 扩展。

退出条件：

- 新增或关闭预编译/系统合约不需要改共享执行主路径。

### 2.3 Header / body / payload 兼容契约收敛

- 固定这些兼容边界：
  - header hash 兼容
  - payload body 提取
  - withdrawals/execution requests
  - block start/block end side effects

退出条件：

- 文档能明确说明每个字段由谁负责、在哪个 profile 生效。

## Phase 3：基础兼容补全

### 3.1 建立 `eth-el` 专属 RPC 暴露清单

- 明确 `eth-el` 只暴露的 namespace。
- 明确 `n42` 专属 namespace：
  - `zk`
  - `bridge`
  - 其他扩展命名空间
- 装配时默认不注册 `n42` 专属模块。

退出条件：

- `eth-el` 启动后没有 N42 扩展 RPC 暴露。

### 3.2 建立状态后端抽象接口

- 在真正实现 MPT 之前，先把接口抽象稳定：
  - state root
  - account/storage proof
  - history access
  - trie/node iteration

退出条件：

- `jmt` 与未来 `mpt` 可以挂在同一套后端接口上。

### 3.3 补全 ETH 兼容缺口清单

- 对以下问题逐条做“代码证据 + 测试证据”核定：
  - `LtHashRoot` 等 N42 头字段兼容边界
  - Protobuf vs RLP 的作用层级
  - payload body 中 withdrawals 的真实兼容情况
  - block/receipt/index/proof 语义是否已有 ETH 兼容路径

退出条件：

- 这些点不再停留在文档判断，而是有代码位置和回归依据。

## Phase 4：重投入项前的决策门

### 4.1 MPT 决策门

- 在真正开做 MPT 之前，先回答：
  - 是完整实现 canonical ETH 状态后端，还是先做最小 proof/root 兼容层？
  - 是否复用现有 trie 组件的一部分，还是独立实现？

退出条件：

- 有明确技术路线和工作量评估，不带模糊预期进入开发。

### 4.2 devp2p / ETH sync 决策门

- 在真正开做 devp2p 前，先回答：
  - 直接引入 go-ethereum p2p 能否降低风险？
  - 自研 devp2p 是否值得？
  - staged sync 是否走 Erigon 式拆阶段，还是最小可用优先？

退出条件：

- 有一份独立的 devp2p/sync 方案比较文档。

## 建议的近期执行顺序

1. 冻结 `eth-el` 范围与非目标。
2. 引入 `ChainProfile` 抽象。
3. 清理共识接口污染。
4. 清理状态转换链特判。
5. 抽共享执行内核与 hook 结构。
6. 完成系统合约/预编译注册表化。
7. 固定 header/body/payload 兼容契约。
8. 建立 `eth-el` RPC 暴露清单。
9. 抽象状态后端接口。
10. 逐项核定 `LtHashRoot` / RLP / withdrawals / proof 等兼容缺口。
11. 在以上基础稳定后，再决定是否进入 MPT。
12. 最后才进入 devp2p / ETH sync。

## 每一步都要有的交付物

- 一份简短设计说明
- 一组最小自动化测试
- 一条清晰的退出条件
- 一条明确的“不会影响 N42 现有链能力”的说明

## 当前建议

当前最稳妥的推进方式不是马上“开做 Ethereum 主网 EL”，而是先把 `eth-el` 作为一个干净、可验证、可测试的运行人格补齐基础边界。只要 Phase 0-3 做扎实，后面的 MPT 与 devp2p 才会是大工程，而不是大返工。
