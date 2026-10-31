# Plan: move n42appv2 mining onto /mobileverify

Status: phase 1 done (2026-10-31), see "Progress" at the end; the rest is still a plan.

## Where the app is today

Checked in `C:\N42\n42appv2` (head `e9f843d6`):

- Mining (`lib/features/mining_v2`) calls `com.mobileSdk.Api.runClient(wsUrl, validatorPrivateKey)`
  through the `trustdart` / `flutter_mining` method channels. That library is the Rust
  `crates/n42/mobile-sdk` (reth-based, from the n42-26 Rust chain), shipped as
  `plugins/flutter_mining/android/libs/mobile-sdk-release.aar` (Android) and
  `MobileSdk.xcframework` (iOS).
- Its protocol is WebSocket JSON-RPC: `consensusBeaconExt_subscribeToVerificationRequest`
  (pushes `UnverifiedBlock{block, td, committee_index}`) and
  `consensusBeaconExt_submitVerification`, plus deposit / exit transactions against a deposit
  contract. **The Go node implements none of `consensusBeaconExt_*`**, so the current app
  cannot mine against an N42-gov5 fleet.
- `evm.aar` (gomobile build of `cmd/evmsdk`, 2026-03-22) is linked on Android only and is used
  through `Evmsdk.emit` for transaction signing; it predates `MobileVerifyClient`
  (added 2026-07-16). iOS links no evmsdk at all.
- The mining endpoint is compile-time only: `--dart-define=MINING_WS_URL` (default
  `wss://testws2.n42.world`); there is no in-app setting.

## Target

The phone runs the Go fleet's mobile verification loop, the same one
`cmd/mobileverify-device` exercises on the desktop:

1. Generate or load a BLS identity (`GenerateMobileBLSKey`), keep the secret in the platform
   keystore (Android Keystore-wrapped / iOS Keychain), never in SharedPreferences.
2. `POST /mobileverify/register` with proof of possession (and the PoW retry on HTTP 428).
3. For each new block: `GET /mobileverify/packet/<sealed hash>`, re-execute with
   `ExecuteAndVerifyV2`, sign, `POST /mobileverify/receipt`.
4. Show status from `/mobileverify/health` and the block's `/mobileverify/cert/<hash>`.

## Work items

### Go side (N42-gov5)

1. **gomobile facade.** `MobileVerifyClient`'s methods take `context.Context`, `[48]byte`,
   `*CodeCache`, `*params.ChainConfig` and `*StreamPacket`; gomobile skips methods with
   such types, so none of them reach Java/Swift. Add a bindable wrapper, strings and ints
   only, e.g.
   `NewMobileVerifySession(baseURL, privKeyHex, chainName string) (*MobileVerifySession, error)`,
   `Register() (int64, error)`, `VerifyBlock(blockHashHex string) (string /*JSON result*/, error)`,
   `Health() (string, error)`, `Cert(blockHashHex string) (string, error)`, `Close()`.
   The session owns a long-lived `CodeCache` and a timeout per call. Unit-test it against
   `httptest` the way `internal/mobileverify/client_e2e_test.go` does.
2. **Block discovery.** The phone needs the next block hash. Options: poll
   `eth_getBlockByNumber("latest")` on the node's JSON-RPC, or add a small
   `/mobileverify/latest` (number + sealed hash) so the phone talks to one surface only.
   Prefer the latter; it also avoids exposing the full JSON-RPC to phones.
3. **Build targets.** `make android` builds `-target=android/arm` only. The app ships
   `arm64-v8a`, `armeabi-v7a` and `x86` (emulator). Change to
   `-target=android/arm,android/arm64,android/amd64` (`amd64` for the emulator) and add an
   iOS simulator slice to `make ios` (`ios/arm64,iossimulator/arm64`).
4. **Public exposure.** Today `--mobileverify.http` is bound to `127.0.0.1` on every fleet
   node. A phone needs a TLS endpoint (the app's debug checks reject `ws://`/`http://` and
   bare IPs): put a reverse proxy with a real certificate in front of one or more nodes'
   mobileverify ports. The per-IP registration rate limit (10/min) then sees the proxy's IP;
   forward the client address (`X-Forwarded-For` with a trusted-proxy list) or raise the limit
   at the proxy.

### App side (n42appv2)

5. Replace `evm.aar` / add `evmsdk.xcframework` from the facade build; drop the duplicate
   copies (`android/app/libs` and `plugins/flutter_mining/android/libs` both carry one).
6. New method-channel calls in `flutter_mining` (Kotlin + Swift): `mvCreateSession`,
   `mvRegister`, `mvVerifyBlock`, `mvHealth`, `mvCert`, `mvStop`. Run the loop on a
   background thread / foreground service (Android) and the existing background-mining
   mode (iOS LiveActivity); the Go side is blocking.
7. `mining_v2` provider: a new `MobileVerifyMiningSource` next to the Rust one, chosen by
   config, so both can coexist while the Rust chain is still in service. Endpoint becomes a
   runtime setting (with the compile-time value as default) so testers can point at a fleet.
8. Rewards: the Rust flow pays through deposit / exit transactions; `/mobileverify` has no
   reward path yet (receipts and certificates only). Decide how verified receipts turn into
   rewards (on-chain anchor of certificates, or an off-chain ledger) before shipping it as
   "mining" to users.

## Test plan

- Desktop: `cmd/mobileverify-device` against the 7-node fleet (register, packet, re-execute,
  receipt, certificate), several devices, each submitting to one node.
- Android emulator (x86_64 slice) against a LAN-exposed node, then a real arm64 device
  through the TLS proxy.
- Real iOS device (no simulator slice for the Rust SDK today; the evmsdk facade should get
  one).
- Negative: wrong chain spec, packet window expired (`packet not in window`), duplicate
  submission to two nodes (must be excluded from the certificate), app relaunch
  (re-register returns the same index).

## Open decisions for the owner

- Keep the Rust (`consensusBeaconExt`) path for the n42-26 chain, or retire it.
- Reward model for `/mobileverify` receipts (item 8).
- Who operates the TLS proxy and which nodes are exposed (item 4).

## Progress

### Phase 1 (2026-10-31): SDK facade, head endpoint, Android probe

Go side (N42):

- `GET /mobileverify/latest` returns the canonical head (number, hash) and whether the node
  already caches its packet (item 2).
- `cmd/evmsdk/mobileverify_session.go`: `MobileVerifySession`, the gomobile face of the client
  (item 1): `NewMobileVerifySession(baseURL, privKeyHex, chainName)`, `Register`, `Latest`,
  `VerifyBlock`, `Cert`, `Health`, `PubkeyHex`, `SetTimeoutMs`, `Close`, JSON results. The chain
  is required: the nil default would re-execute `mainnet_qmdb_staggered` packets under the
  mainnet rules and fail every block.
- `make android` / `make ios` build again (item 3). They had been broken since 2026-03 by two
  unbindable exported functions (gobind rejects the whole package), `lib/mmap` not compiling
  for 32-bit, the `wlynxg/anet` linkname, blst under NDK 26+, and no `golang.org/x/mobile` in
  `go.mod` (now tool directives). `make android` builds all four ABIs the app ships.

App side (n42appv2, branch `feat/mining-v3-mobileverify`, not pushed):

- `evm.aar` rebuilt from N42 `d2fae7b3` in both places (item 5); `Evmsdk.emit` kept for v1.
- `flutter_mining` gains the `n42/mobileverify` channel (`MobileVerifyHandler.kt`) and
  `lib/mobileverify.dart` (item 6).
- `lib/features/mining_v3`: `MobileVerifyProbe` and a debug-only settings page that registers,
  waits for the epoch commit, verifies the head and waits for its certificate.

Not yet verified on a device: there is no emulator on the build machine, and the fleet needs
a roll before it serves `/latest`. The session itself ran against the live 7-node fleet from
the desktop (head taken from JSON-RPC instead): registered and committed within a minute, then
3 blocks (25, 2 and 21 transactions) re-executed under `mainnet_qmdb_staggered`, submitted, and
certified with its signature.

Next: phase 2 (`MiningEngineKind{v1,v2,v3}` replacing `miningUseV2Provider`, behind a
`MiningEngine` interface), phase 3 (foreground service, iOS), phase 4 (reward model, remote
flag, TLS endpoint: items 4 and 8).
