# Hive engine genesis fixture

`hive_engine_genesis.json` is vendored from:

- Repository: `ethereum/hive`
- Commit: `1f45347ed355cbab17e24f7f0e1594bb59b405b2`
- Upstream path: `simulators/ethereum/engine/init/genesis.json`
- SHA-256: `e63be600b65a48a81fa631f4f2f57f78d195166f1d6dffb18954626794ed3978`

Keep this exact fixture aligned with the pinned hashes in the API and genesis
compatibility tests. Do not update those expected hashes merely to accept a
different upstream genesis.

The optional full Hive checkout under `tests/eth-hive` remains the harness used
by the operational Hive/EEST workflows; it is not required for these unit
regressions.
