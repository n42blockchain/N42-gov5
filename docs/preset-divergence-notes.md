# Chain-preset divergence notes

Known, deliberate divergences between chainspec presets and the code's
current fork semantics. Check here before re-executing or extending an
old library.

## mainnet_v2 / mainnet_mpt: EIP-7904 gas window (2026-03 → 2026-08-12)

Both presets carried `"glamsterdamTime": 0` while `IsGlamsterdam` gated
the (since upstream-demoted) EIP-7904 gas repricing — intrinsic 4500,
zero-data 1 gas, CREATE 8000, etc. Every block executed into libraries
built on these presets between commit f25cc895 (2026-03-22) and commit
b37f5ecd (2026-08-12) carries 7904 gas figures in its sealed
receipts/headers.

b37f5ecd rebound `IsGlamsterdam` to the real Amsterdam schedule and
removed the 7904 implementation; the audit follow-up set both presets'
`glamsterdamTime` to `null`. Consequences:

- **Re-executing the 2026-03→08 ranges of those libraries can NOT
  reproduce their stored gas under any current rule set** — neither
  pre-Glamsterdam (intrinsic 21000) nor Amsterdam (2780 components)
  matches 7904 figures. `--verify` ladders and witness replays over
  those ranges will report gas mismatches by construction.
- Blocks **appended from now on** use standard pre-Glamsterdam semantics
  (the gate is null) and are internally consistent going forward.
- If byte-accurate re-execution of the historical window is ever needed,
  the library must be rebuilt from its source with current code — do not
  resurrect the 7904 preset.

Unaffected: `mainnet_qmdb_staggered` (qs fleet — glamsterdamTime
2027-01-01, never active), `mainnet_qmdb` (null), and all eth-el work
(Ethereum mainnet config has no Glamsterdam time).

## Randomness precompile 0x0302 (redesigned 2026-08-16)

The beacon now reads the block header's PrevRanDao (consensus-committed,
replay-reproducible). Before setting `RandomnessTime` on ANY chain,
verify that chain's headers actually carry a consensus-derived
PrevRandao — the precompile fails deterministically when absent. The old
process-local QC ring was removed: it diverged between leader and
followers and could never replay.
