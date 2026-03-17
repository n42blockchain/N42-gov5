# Repository Guidelines

## Project Structure & Module Organization
`cmd/` contains entrypoints such as `cmd/n42` (main node), `cmd/zkguest` (RISC-V zkVM guest), and supporting CLIs like `cmd/clef`. Core node logic lives in `internal/` (`consensus/`, `miner/`, `sync/`, `vm/`, `zkprover/`, `zkverifier/`). Reusable primitives and storage code are split across `modules/`, `common/`, and `lib/`. Configuration defaults live in `conf/`; longer-form technical docs are in `docs/`; automation helpers live in `scripts/`; broader integration and protocol suites live in `tests/`. Treat `devtest/`, `mainnet/`, and `n42data/` as runtime data, not normal source directories.

## Build, Test, and Development Commands
Use the `Makefile` first:

- `make build`: compile all Go packages.
- `make n42`: build the main binary at `build/bin/n42`.
- `make zkguest`: cross-build the Linux RISC-V guest binary.
- `make test` or `make test-short`: run full or shortened unit tests.
- `make race-core`: run race detection on the main execution/state packages.
- `make lint`: run `golangci-lint`.
- `make check`: run `gofmt`, `go vet`, and lint together.

For targeted work, `go test ./path/...` is fine, but keep the Make targets green before opening a PR.

## Coding Style & Naming Conventions
This is a Go codebase. Let `gofmt` control indentation and spacing; do not hand-align code. Use `goimports` ordering, with the local prefix `github.com/n42blockchain/N42`. Keep package names lowercase, exported identifiers in `CamelCase`, and unexported helpers in `camelCase`. Match existing subsystem names when adding files, for example `internal/zkprover/prover_client.go` or `modules/state/witness/encoding_binary_test.go`.

## Testing Guidelines
Prefer the standard `testing` package; `testify` is common for assertions, and `ginkgo/gomega` appears only in a legacy formatter package. Name files `*_test.go`, benchmarks `BenchmarkXxx`, and fuzz targets `FuzzXxx`. Use `t.Parallel()` only when the test is isolated from shared chain data or global config. CI runs lint, `make build`, coverage-enabled tests, and `go test -race -short ./...`; coverage must stay above the current 20% threshold.

## Commit & Pull Request Guidelines
Recent history favors short, imperative subjects, often with conventional prefixes: `feat(zkvm): ...`, `fix: ...`, `docs: ...`. Keep commit messages scoped to one logical change. PRs should explain behavior changes, note any config or RPC surface impact, link the relevant issue, and list the commands you ran locally, such as `make test` and `make lint`.

## Security & Configuration Tips
Never commit keystores, JWT secrets, or populated chain data from `mainnet/`, `n42data/`, or `devtest/`. Review RPC port exposure and auth settings before changing defaults in `conf/` or startup scripts.
