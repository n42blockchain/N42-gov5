# N42 Verifier iOS Sample — Build Guide

## Prerequisites

- macOS with Xcode 15+
- Go 1.21+
- gomobile installed:
  ```bash
  go install golang.org/x/mobile/cmd/gomobile@latest
  go install golang.org/x/mobile/cmd/gobind@latest
  gomobile init
  ```

## 1. Build the gomobile bind

From the N42-gov5 repo root:

```bash
make ios
# or, equivalently:
GOOS=ios CGO_ENABLED=1 GOARCH=arm64 \
  gomobile bind -tags nosqlite,noboltdb \
    -o build/mobile/Evmsdk.xcframework \
    -target=ios/arm64 \
    github.com/n42blockchain/N42/cmd/evmsdk
```

This produces `build/mobile/Evmsdk.xcframework`.

For the iOS Simulator (arm64 macs), use `-target=ios/arm64,iossimulator`
or build a separate xcframework with `-target=iossimulator/arm64`.

## 2. Add the xcframework to the sample app

Open Xcode and create a new project at `cmd/evmsdk/sample-ios/N42Verifier`
(SwiftUI App template). Then:

1. **General → Frameworks, Libraries, and Embedded Content** → drag
   `Evmsdk.xcframework` in. Set **Embed & Sign**.
2. **Build Settings → Other Linker Flags** → ensure `-ObjC` is present
   (gomobile sometimes needs it for Objective-C method registration).
3. The Swift wrapper imports it as `import Evmsdk` — this is the
   gomobile-generated module name.

## 3. Drop in the source files

The 3 source files in this directory:

- `N42VerifierApp.swift` — `@main` entry
- `EvmsdkWrapper.swift` — calls `EvmsdkMobileInit`, `EvmsdkMobileStart`,
  etc., parses the JSON stats blobs, exposes `@Published` SwiftUI bindings
- `ContentView.swift` — UI: status / key entry / server config /
  start-stop / stats / latest block / verify log

Add these to the Xcode project (drag into the Project Navigator, copy
items if needed). Make sure **Target Membership** is checked for the
iOS target.

## 4. Run

Connect a device or pick the simulator → Cmd+R.

The app will:

1. Call `EvmsdkMobileInit(4242)` on launch → status "Initialized"
2. Wait for you to enter a 64-hex-char BLS private key → "Set Key"
3. Wait for you to enter the IDC server host/port and verifier account
4. "Start" → calls `EvmsdkMobileSetServer` then `EvmsdkMobileStart`
5. Polls `EvmsdkMobileGetStats` and `EvmsdkMobileGetLastVerifyInfo`
   every 1s, updates the SwiftUI bindings → real-time stats display

## 5. Troubleshooting

### `Symbols not found for architecture arm64`

The xcframework wasn't built for the right slice. Check
`-target=ios/arm64` for device builds or
`-target=iossimulator/arm64` for simulator builds. You can build a
multi-slice xcframework by passing both:

```bash
gomobile bind -target=ios/arm64,iossimulator/arm64 ...
```

### `BLST_PORTABLE` build errors

The blst BLS library has Darwin assembly that occasionally fails on
older toolchains:

```bash
BLST_PORTABLE=1 make ios
```

### Status stuck at "Connected" but no blocks verified

The IDC node's WebSocket subscriber service is not yet wired into the
N42-gov5 main binary (Phase 2b — pending). Until it lands, use offline
mode: pass packet bytes from disk to `EvmsdkMobileVerifyPacket(_:)`
directly. See `cmd/evmsdk/mobile_facade_test.go::TestMobileFacade_StatsAfterVerify`
for the calling pattern.

## 6. Notes

- **Minimum iOS deployment target:** 16.0 (matches the SwiftUI
  features used in `ContentView.swift`)
- **The xcframework is self-contained**: includes all transitive Go
  deps (BLS, secp256k1, EVM, etc.). Expect ~30-40 MB binary size.
- **Backgrounding behavior**: gomobile goroutines run in a separate
  thread group; iOS may suspend them when the app is backgrounded.
  Production apps should call `EvmsdkMobileStop()` on
  `applicationDidEnterBackground` and restart on `willEnterForeground`.
