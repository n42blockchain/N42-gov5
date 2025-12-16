# N42 节点快速启动指南

## 安装

```bash
# 克隆代码
git clone https://github.com/n42blockchain/N42.git
cd N42

# 编译
go build -o n42 ./cmd/n42

# 或者直接运行
go run ./cmd/n42
```

## 基本启动

### 启动主网全节点

```bash
# 最简单的启动方式
./n42

# 等同于
./n42 --chain mainnet --data.dir ./n42data
```

### 启动测试网节点

```bash
./n42 --testnet

# 等同于
./n42 --chain testnet
```

### 开发者模式（本地单节点）

```bash
./n42 --dev

# 等同于
./n42 --chain private --p2p.no-discovery --p2p.max-peers 0
```

## 启用 RPC

### HTTP RPC（本地访问）

```bash
./n42 --http

# RPC 服务将在 127.0.0.1:8545 启动
# 支持的方法：eth_*, web3_*, net_*
```

### HTTP RPC（对外开放）

```bash
./n42 --http --http.addr 0.0.0.0

# ⚠️ 警告：对外开放 RPC 存在安全风险
# 建议配合防火墙使用
```

### WebSocket RPC

```bash
./n42 --ws

# WS 服务将在 127.0.0.1:8546 启动
```

### 开放更多 API 模块

```bash
./n42 --http --http.api eth,web3,net,debug,txpool
```

## 数据存储

### 指定数据目录

```bash
./n42 --data.dir /path/to/data

# 或使用别名
./n42 --datadir /path/to/data
```

### 监控磁盘空间

```bash
# 当磁盘空间低于 20GB 时自动关闭节点
./n42 --data.minfreedisk 20
```

## 挖矿/验证

### 启用挖矿

```bash
./n42 --mine --etherbase 0xYourAddress

# 或者
./n42 --engine.miner --engine.etherbase 0xYourAddress
```

## P2P 网络

### 指定端口

```bash
# 同时设置 TCP 和 UDP 端口
./n42 --port 30304

# 或分别设置
./n42 --p2p.tcp-port 30304 --p2p.udp-port 30304
```

### 连接指定节点

```bash
# 添加引导节点
./n42 --p2p.bootnodes enode://...@ip:port

# 添加静态节点（始终保持连接）
./n42 --p2p.staticnodes enode://...@ip:port
```

### 设置最大连接数

```bash
./n42 --p2p.maxpeers 100
```

## 日志配置

### 设置日志级别

```bash
# 可选：trace, debug, info, warn, error, fatal
./n42 --log.level info

# 或使用别名
./n42 --verbosity debug
```

### 输出到文件

```bash
# 输出到文件（同时也输出到控制台）
./n42 --log.file n42.log

# 仅输出到文件（不显示在控制台）
./n42 --log.file n42.log --log.console=false
```

### 日志文件自动管理

日志系统支持自动分段、清理和压缩，防止日志占满磁盘：

```bash
# 推荐的生产环境配置
./n42 --log.file n42.log \
  --log.maxsize 100 \      # 单文件最大 100MB，超过自动切分
  --log.maxbackups 10 \    # 保留 10 个旧文件
  --log.maxage 30 \        # 保留 30 天
  --log.compress           # 压缩旧文件，节省约 90% 空间

# 磁盘紧张时的配置
./n42 --log.file n42.log \
  --log.maxsize 50 \       # 单文件最大 50MB
  --log.maxbackups 5 \     # 只保留 5 个旧文件
  --log.maxage 7 \         # 只保留 7 天
  --log.compress \         # 压缩旧文件
  --log.totalsize 500      # 所有日志文件总大小不超过 500MB

# 使用 JSON 格式（便于 ELK 等日志分析系统）
./n42 --log.file n42.log --log.json

# 使用文本格式（更易读）
./n42 --log.file n42.log --log.json=false
```

### 日志文件路径

日志文件默认保存在 `{data.dir}/log/` 目录下：
- 主日志：`{data.dir}/log/n42.log`
- 旧日志：`{data.dir}/log/n42-2024-01-15T10-30-00.log.gz`

## 调试与性能分析

### 启用调试模式

```bash
./n42 --debug

# 等同于 --log.level debug --pprof
```

### 启用 pprof

```bash
./n42 --pprof --pprof.port 6060

# 访问 http://localhost:6060/debug/pprof/
```

### 启用指标收集

```bash
./n42 --metrics --metrics.port 6061

# Prometheus 格式指标：http://localhost:6061/metrics
```

## 使用配置文件

```bash
./n42 --config /path/to/config.toml

# 或
./n42 -c /path/to/config.yaml
```

示例配置文件 (config.toml):

```toml
[node]
datadir = "/data/n42"
chain = "mainnet"

[http]
enabled = true
addr = "127.0.0.1"
port = 8545
api = "eth,web3,net"

[p2p]
port = 30303
maxpeers = 50

[log]
level = "info"
```

## 常用组合示例

### 公共 RPC 节点

```bash
./n42 \
  --http --http.addr 0.0.0.0 --http.port 8545 \
  --ws --ws.addr 0.0.0.0 --ws.port 8546 \
  --http.corsdomain "*" \
  --p2p.maxpeers 100 \
  --data.dir /data/n42
```

### 验证者节点

```bash
./n42 \
  --mine --etherbase 0xYourAddress \
  --unlock 0xYourAddress --password /path/to/password \
  --data.dir /data/n42 \
  --log.file /var/log/n42.log
```

### 轻量级同步节点

```bash
./n42 \
  --syncmode fast \
  --p2p.maxpeers 25 \
  --data.dir /data/n42
```

### 开发测试

```bash
./n42 \
  --dev \
  --http --http.addr 0.0.0.0 \
  --debug
```

## 环境变量

支持通过环境变量设置常用参数：

```bash
export N42_CHAIN=mainnet
export N42_DATA_DIR=/data/n42
export N42_HTTP_ENABLED=true
export N42_HTTP_ADDR=0.0.0.0
export N42_LOG_LEVEL=info
export N42_MAX_PEERS=50

./n42
```

## 帮助

```bash
# 查看所有选项
./n42 --help

# 查看子命令帮助
./n42 account --help
./n42 init --help
```

## 常见问题

### Q: 节点同步很慢？

A: 尝试以下方法：
1. 增加最大连接数：`--p2p.maxpeers 100`
2. 添加更多引导节点：`--p2p.bootnodes ...`
3. 使用快速同步：`--syncmode fast`

### Q: RPC 无法从外部访问？

A: 检查以下配置：
1. 监听地址：`--http.addr 0.0.0.0`
2. 防火墙设置：确保端口 8545 开放
3. 云服务安全组：添加入站规则

### Q: 磁盘空间不足？

A: 
1. 设置自动关闭阈值：`--data.minfreedisk 20`
2. 定期清理日志：`--log.maxbackups 5`
3. 迁移数据目录到更大磁盘

### Q: 如何查看同步进度？

A: 使用 RPC 调用：
```bash
curl -X POST -H "Content-Type: application/json" \
  --data '{"jsonrpc":"2.0","method":"eth_syncing","params":[],"id":1}' \
  http://localhost:8545
```

