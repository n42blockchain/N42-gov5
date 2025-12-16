# N42 Benchmark & Metrics Baseline Tools

This directory contains tools for establishing performance baselines and running smoke tests for the N42 blockchain.

## Tools

### 1. `run_smoke.sh` - Smoke Test Script

Basic smoke test to verify the node is functioning correctly.

```bash
# Run with default settings (localhost:8545)
./run_smoke.sh

# Run with custom RPC URL
./run_smoke.sh http://192.168.1.100:8545
```

### 2. `cmd/rpc/main.go` - RPC Benchmark Tool

Benchmark key RPC methods and report P50/P95 latencies.

```bash
# Build and run
go run ./cmd/rpc -url http://localhost:8545 -n 100

# Or build first
go build -o bench_rpc ./cmd/rpc
./bench_rpc -url http://localhost:8545 -n 100

# Options:
#   -url     RPC URL (default: http://localhost:8545)
#   -n       Number of iterations per method (default: 100)
#   -methods Comma-separated list of methods to test
```

### 3. `cmd/metrics/main.go` - Metrics Collection Tool

Collect and report system metrics including:
- Database disk usage
- Memory heap profile (pprof)
- Sync progress

```bash
# Build and run
go run ./cmd/metrics -datadir /path/to/n42/data -pprof http://localhost:6060

# Or build first
go build -o bench_metrics ./cmd/metrics
./bench_metrics -datadir /path/to/n42/data -pprof http://localhost:6060

# Options:
#   -datadir  N42 data directory
#   -pprof    pprof endpoint URL
#   -output   Output file for metrics (JSON)
```

## Metrics Baseline Procedure

### 1. Pre-Deployment Baseline

Before deploying new code, run:

```bash
# 1. Start the node
./n42 --rpc --pprof

# 2. Wait for sync to complete

# 3. Run baseline collection
./run_smoke.sh
go run ./cmd/rpc -n 100 > baseline_before.txt
go run ./cmd/metrics -output metrics_before.json
```

### 2. Post-Deployment Verification

After deploying new code:

```bash
# 1. Run the same tests
./run_smoke.sh
go run ./cmd/rpc -n 100 > baseline_after.txt
go run ./cmd/metrics -output metrics_after.json

# 2. Compare results
diff baseline_before.txt baseline_after.txt
```

## Key Metrics to Track

### Sync Performance
- **Initial sync time**: Time to sync from genesis to head
- **Blocks/second**: Block import rate during sync
- **State sync time**: Time for state synchronization

### RPC Performance
| Method | Target P50 | Target P95 |
|--------|------------|------------|
| eth_blockNumber | < 5ms | < 20ms |
| eth_getBlockByNumber | < 50ms | < 200ms |
| eth_sendRawTransaction | < 100ms | < 500ms |
| eth_getLogs | < 100ms | < 1s |
| eth_call | < 100ms | < 500ms |

### Reorg Performance
- **Depth 1 reorg**: < 100ms
- **Depth 5 reorg**: < 500ms
- **Depth 10 reorg**: < 2s

### Resource Usage
- **Memory**: Heap usage during steady state
- **Disk**: Database size growth rate
- **CPU**: Average utilization during sync/steady state

## Reorg Testing

Test reorg handling with different depths:

```bash
# These tests require a test network setup
# Depth 1: Common case
# Depth 5: Moderate reorg
# Depth 10: Large reorg (should trigger warnings)

# Check reorg audit logs:
grep "Reorg audit" /path/to/n42/logs/*.log
```

## Automated CI Integration

Add to CI pipeline:

```yaml
# .github/workflows/benchmark.yml
- name: Run Smoke Tests
  run: |
    cd tools/bench
    ./run_smoke.sh ${{ env.RPC_URL }}

- name: Run RPC Benchmarks
  run: |
    cd tools/bench
    go run bench_rpc.go -n 50 | tee benchmark_results.txt

- name: Check Performance Regression
  run: |
    # Compare with baseline
    # Fail if P95 > 2x baseline
```

## Troubleshooting

### High RPC Latency
1. Check node sync status
2. Check database performance
3. Review recent code changes affecting RPC handlers

### Reorg Issues
1. Check reorg audit logs for depth/duration
2. Verify state root consistency
3. Check for consensus issues

### Memory Leaks
1. Take heap profile: `go tool pprof http://localhost:6060/debug/pprof/heap`
2. Compare with baseline profile
3. Check for goroutine leaks

## Contributing

When adding new benchmarks:
1. Document expected baseline values
2. Add comparison logic
3. Update CI integration

