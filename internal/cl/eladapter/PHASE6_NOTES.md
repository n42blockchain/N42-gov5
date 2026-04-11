# eladapter — what is wired and what is not

This file documents the state of the Caplin ↔ N42-EL seam after Phase 6 and
explains the architectural constraints that shape it. Read this before
attempting to flip any of the still-stubbed methods.

## Wired (real implementations)

| `Adapter` method      | Backed by                                          |
| --------------------- | -------------------------------------------------- |
| `IsCanonicalHash`     | `cmd/ethexec/beacon_backend.go` → `modules/rawdb`  |
| `HasBlock`            | `cmd/ethexec/beacon_backend.go` → `modules/rawdb`  |
| `Ready`               | non-nil executor + db                              |

The `Backend.CurrentHeadNumber` accessor is also live and reads from
`modules/rawdb.ReadHeadBlockHash + ReadHeaderNumber`.

## Stubbed (return `ErrNotImplemented`)

| `Adapter` method            | Why it is still a stub                                                |
| --------------------------- | --------------------------------------------------------------------- |
| `NewPayload`                | Needs `*api.EngineAPIv4`, which requires a full `*node.Node`          |
| `ForkChoiceUpdate`          | Same — depends on `BlockChainAPI` overlay state                       |
| `CurrentHeader`             | `depshim/types.Header` is a field-shape stub, cannot carry real data  |
| `InsertBlock(s)`            | Needs Caplin → `internal/ethel/executor` historical-import bridge     |
| `GetBodiesByRange/Hashes`   | Needs body decoder integrated with the chaindata layout               |
| `GetAssembledBlock`         | Block-production path — depends on a live `EngineAPIv4` builder       |
| `GetBlobs`                  | Depends on a blob store, which `cmd/ethexec` does not maintain        |

`SupportInsertion()` returns `false`, which forces Caplin's stage loop down
the `NewPayload` path once Phase 7+ wires up the rest of the cl/ tree.

## Why the Engine API methods are hard

`internal/api/engine_api_v4.go` lives on `*BlockChainAPI`, which lives on
`*node.Node`. `cmd/ethexec` is a deliberately minimal binary: it
constructs an `*ethel.Executor` (a batch replayer) and a chaindata MDBX,
and nothing else. There is no `BlockChain`, no overlay, no payload
builder.

There are three viable paths to fix this — each is a meaningful project
on its own:

1. **Spawn a minimal Node inside cmd/ethexec.** Pulls in the entire
   `internal/node` graph, defeats the "Caplin is opt-in for n42-el" budget.
2. **Add a thin `payload-execute` mode to `internal/ethel/executor`** that
   takes one CL `Eth1Block`, runs the existing per-block EVM path, and
   reports back via the same `PayloadStatusV1` shape. Smallest delta, but
   needs a freshly-written `Eth1Block → block.Block` converter that does
   not go through the `depshim/types.Header` stub.
3. **Run Caplin out-of-process** and let `cmd/ethexec` serve the HTTP
   Engine API on port 20014 the same way the native `cmd/n42` binary
   already does. Caplin then talks to itself over JSON-RPC, exactly as
   Lighthouse / Prysm do today. Highest fidelity, lowest invasiveness for
   ethexec.

Picking between 2 and 3 is the next architectural decision; until then
the stubs document the gap precisely.

## Why the read-only methods *are* live

`HasBlock`, `IsCanonicalHash`, and the head-number probe only need
`modules/rawdb` access. `cmd/ethexec` already opens the chaindata MDBX
in read-write mode for the executor, so handing the `kv.RoDB` view to the
Caplin backend is free. These three methods are enough for a future
phase1 stage loop to walk historical canonical hashes — which is the
*first* thing Caplin does on startup.

## Type identity reminder

Caplin operates on `internal/cl/depshim/common.Hash`, which is a Go type
alias of `lib/common.Hash`. `modules/rawdb` operates on `common/types.Hash`.
Both are `[32]byte` underneath but they are distinct named types in Go's
type system. The `chainHash` helper in `cmd/ethexec/beacon_backend.go`
performs the (free) named-type conversion at the seam.
