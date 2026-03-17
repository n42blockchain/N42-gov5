# Benchmark Baselines

This directory tracks the repository-owned benchmark workflow, not a separate benchmark codebase.

Current layout:

- `results/`: local raw benchmark captures produced by `scripts/run_perf_baseline.sh`

Current curated baseline scope:

- `./tools/tpsbench`
- `./modules/rawdb`
- `./internal/vm`

Usage:

```bash
make perf-baseline
```

or

```bash
bash scripts/run_perf_baseline.sh
```

Notes:

- The benchmark implementations remain in package-local `*_test.go` files.
- Raw outputs under `benchmarks/results/` are local artifacts and are ignored by git by default.
- If a result is important enough to cite in docs, it should be curated into a checked-in report with the exact command and date.
