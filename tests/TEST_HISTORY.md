# Ethereum Execution Layer Test History

## Current Status (2026-08-16)

```
Hive ethereum/engine with eth-el: 403 / 403 passed
Hive ethereum/engine native applicable set: 311 / 311 passed

Latest completed EEST broad consume-engine shard reruns: ALL GREEN
Paris+Shanghai:  3,573 passed
Cancun:         17,783 passed
Prague:         20,878 passed
Osaka:          21,583 passed
Access-list:     2,132 passed

Latest completed EEST consume-rlp stable fixture set:
RLP:            47,589 / 47,589 passed
```

The EEST rows are the most recent completed evidence (Engine on 2026-08-02,
RLP on 2026-08-12) while the 2026-08-16 latest-revision rerun is in progress.
Engine shards overlap, especially the cross-fork access-list shard, so their
counts must not be added and reported as a unique-test total. The native Hive
set is the subset applicable to the main `n42` binary; devp2p, multi-client,
and P2P syncing scenarios that require `eth-el` are reported separately.

See: README.md, docs/DEVLOG.md, docs/GAP.md

---

## Historical Fix Timeline

### Phase 1 — Baseline (2026-01-08)

Pass rate: **95.0%** (35,843 / 37,724)

Major fixes applied:
- SSTORE gas calculation — fixed ~9,000 tests
- SELFDESTRUCT created flag — fixed ~1,881 tests

Known issues: CREATE2+SELFDESTRUCT interactions, Prague gas schedules, CALLCODE edge cases.

### Phase 2 — Rapid Fix Sessions (2026-01-09)

Pass rate: **98.1% → 98.4%** (37,006 → 37,115 / 37,724)

| Fix | Tests Fixed |
|-----|------------|
| BLS precompiles activation | 7 |
| Exception validation OR logic | 92 |
| GASLIMIT price product overflow | 2 |
| Legacy transaction gas validation | 4 |
| NONCE_IS_MAX validation | 4 |
| EIP-1559 access list indexing (gasIndex → dataIndex) | 546 |
| **Session total** | **+109** |

### Phase 3 — Full Pass (2026-01-10)

Pass rate: **100.0%** (37,724 / 37,724), 0 failures

Key fixes that closed the gap:

| Fix | Tests Fixed | Detail |
|-----|------------|--------|
| EIP-7623 calldata cost | 59 | `TxDataNonZeroGasEIP7623=40`, `TxDataZeroGasEIP7623=10`, `FloorDataGas()` |
| EIP-6780 SELFDESTRUCT gas + balance | 20+ | Prague behavior change |
| CREATE2 collision detection | 4+ | Nonce/code check before creation |
| Precompile fork order | 18 | Critical: fork activation ordering bug |
| **Total improvement** | **+609** (+1.6%) |

### Phase 4 — EEST Broad Matrix (2026-03 ~ 2026-03-26)

Extended to 4-fork broad matrix with execution-spec-tests v5.4.0+.

Blockers fixed:
- Fork inheritance chain (`IsCancun` / `IsShanghai` must imply all earlier forks)
- Prague system contracts (EIP-2935 history storage, skip undeployed)
- Cancun `modexp d30` edge case
- SELFDESTRUCT / CREATE2 transaction boundaries
- Hive timestamp handling (`HIVE_CANCUN_TIMESTAMP` without Berlin → gas explosion)

Final: all collected tests were green across Paris+Shanghai, Cancun, Prague,
and Osaka. These shard counts overlap and therefore have no valid summed
unique-test total.

---

## Lessons Learned

1. Test failures were implementation bugs, not test issues — never skip tests, investigate instead.
2. Prague uses timestamp-based fork activation vs earlier block-based — requires separate handling.
3. Fork inheritance must be transitive: setting Cancun implies Shanghai implies Berlin, etc.
4. Precompile activation order matters — wrong ordering silently breaks gas calculations.
