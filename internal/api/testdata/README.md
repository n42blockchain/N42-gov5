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

# eth_config (EIP-7910) fixtures

`eth_config-hoodi-next-and-last.json` and `eth_config-hoodi-current.json` are
copied unchanged from go-ethereum:

- Upstream paths: `internal/ethapi/testdata/eth_config-next-and-last.json`,
  `internal/ethapi/testdata/eth_config-current.json`
- Last changed upstream in commit `ffe758c7a783ef2f4fefbb72200a740adce015c1`
- SHA-256: `0415f266f2d5dbcd1a15c453ee65cf9648034de466937993adba80437c288b61`
  (next-and-last), `d8aea3befdc9a12261e3963aca35cf5c765b699ba71ca29e56fbd1a065e15ea2`
  (current)

They are geth's answer for its Hoodi snapshot config (Prague scheduled,
nothing after) at time 0 and at the Prague time;
`TestEthConfigMatchesGethHoodiVectors` reproduces them.
