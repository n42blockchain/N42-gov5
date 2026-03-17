# EEST Shard Run Summary

- Generated: `20260316-203112Z`
- Mode: `consume-engine`
- Python: `3.13`
- Pytest workers: `auto`
- Shard jobs: `1`
- Dry run: `1`

| Shard | Selector | Target ~Tests | RC | Duration (s) | Log |
|-------|----------|---------------|----|--------------|-----|
| paris+shanghai | `.*/.*fork_(Paris\|Shanghai)` | ~2,600 | `0` | `0` | `paris+shanghai.log` |
| cancun | `.*/.*fork_Cancun` | ~17,250 | `0` | `0` | `cancun.log` |
| prague | `.*/.*fork_Prague` | ~20,500 | `0` | `0` | `prague.log` |
| osaka | `.*/.*fork_Osaka` | ~21,000 | `0` | `0` | `osaka.log` |
| rlp | `.*eip2930_access_list.*` | unchanged | `0` | `0` | `rlp.log` |
