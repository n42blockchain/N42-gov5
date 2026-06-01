# Minimal Node UI (iOS SwiftUI + Android Compose)

A zero-trust **minimal stateless client** screen, added alongside the existing
verifier sample. It follows the chain tip with ~1 day of rolling data and verifies
account balances against the header-chain-trusted state root — no full node, no
full-state download.

## What it calls

The gomobile bind now includes the minimal facade (`cmd/evmsdk/mobile_minimal.go`),
exposed as `EvmsdkMobileMinimal*` (iOS) / `Evmsdk.mobileMinimal*` (Android):

| Facade | Trust layer | Cost |
|---|---|---|
| `MobileMinimalInit(idcURL, cpBlock, cpHash, retention)` | bootstrap from a social checkpoint | — |
| `MobileMinimalSync` / `MobileMinimalSyncTo(maxBlock)` | ① header chain (parentHash → tip) | 136 B/block |
| `MobileMinimalState` | — (query) | — |
| `MobileBalanceOf(addr)` | ③ per-account EIP-1186 proof vs trusted stateRoot | **~2 KB / call** |

Layer ② (witness EVM replay → receiptRoot) reuses the existing on-device verifier
(`verify_v2_exec.go`); wiring the producer witness stream to it is the next step.

## Build

1. **Rebuild the framework** so the bind picks up `mobile_minimal.go`:
   - iOS:  `make ios`   → `build/mobile/evmsdk.xcframework`
   - Android: `make android` → `build/mobile/android/evmsdk.aar`
2. iOS: add `MinimalWrapper.swift` + `MinimalView.swift` to the Xcode target. The
   app root (`N42VerifierApp.swift`) now shows a `TabView` with **Minimal** +
   **Verifier**.
3. Android: `MinimalClient.kt` + `MinimalScreen.kt` are in the package; `MainActivity`
   now shows a **Minimal / Verifier** `TabRow`.

## Run against a producer

Start an IDC serving RPC with a state trie so `/account-proof` works:

```
n42-stateless-serve --addr 0.0.0.0:8555 \
  --headers <headerc-dir> --bodies <bodyc-dir> --witness <witness-dir> \
  --anchors <anchor-dir> --trie <trie-dir> [--trie-head <N>]
```

In the app: set the IDC URL (Android emulator → `http://10.0.2.2:8555`), a trusted
checkpoint `{block, hash}` (hard-coded / PoS-finalized), tap **Connect**, then
**Follow tip ▶**. Enter an address and tap **Verify ③** for a trustless balance
(🛡 = proven against the trusted state root; the byte count shows how small the
proof is).

## Resource envelope (measured, real mainnet data)

- ① header: 136 B/block → ~1 MB/day. ③ per-account: ~2 KB / 5 nodes, RAM a few KB.
- 1-day rolling: ~50–150 MB SSD (headers + witness; bodies replay-and-discard).
- Peak RAM < 150 MB. The full-window MPT anchor (1 MB–207 MB) is **never** fetched
  on the phone — that is what `--anchors` serves to archive/full nodes; the phone
  uses bounded per-account proofs instead.
