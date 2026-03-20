// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

// Package deferred provides the foundation for deferred block execution.
//
// # Design Rationale
//
// Traditional blockchain nodes execute transactions synchronously during
// consensus: Block N cannot be finalized until all its transactions have
// been executed and a state root computed. This creates a bottleneck where
// consensus throughput is bounded by execution speed.
//
// Deferred execution breaks this coupling:
//
//   - Consensus agrees on transaction ordering without waiting for execution
//   - Execution proceeds asynchronously, using the full block interval
//   - State root for block N is included in block N+1's header
//
// This is the approach used by Monad (10,000 TPS target) and Aptos (250,000 TPS
// target with Raptr consensus).
//
// # Integration with N42
//
// N42's existing architecture supports deferred execution through:
//
//   - HotStuff-2 BFT consensus: Already supports pipelined rounds (Prepare|Commit
//     overlap), which naturally extends to deferred execution.
//   - Block-STM parallel execution: The wave-based executor can process entire
//     blocks asynchronously, maximizing throughput.
//   - JMT state commitment: Content-addressed nodes enable parallel state root
//     computation without blocking consensus.
//
// # Production Deployment Considerations
//
// Before enabling deferred execution in production:
//
//  1. State root delay: Clients querying "latest" state root see a 1-block delay.
//     This affects eth_getBalance at "latest" and similar queries.
//  2. Reorg handling: When a reorg occurs, deferred results for orphaned blocks
//     must be discarded and re-executed on the new canonical chain.
//  3. Error propagation: If execution fails for block N, consensus must be
//     notified to halt or retry. The Pipeline handles this via error tracking.
//  4. Memory pressure: The execution queue and result cache consume memory
//     proportional to the execution lag. PruneResults prevents unbounded growth.
//
// # Related Technologies Assessment
//
// Async I/O (io_uring):
//   - Monad's MonadDB uses Linux io_uring for non-blocking disk I/O.
//   - Go's runtime provides goroutine-based async semantics via netpoller,
//     but filesystem I/O is synchronous (blocking OS threads).
//   - MDBX uses memory-mapped files (mmap), which provide similar benefits
//     to io_uring for read-heavy workloads: the kernel handles page faults
//     asynchronously. Write operations still benefit from io_uring.
//   - Recommendation: Evaluate Go io_uring bindings (iceber/iouring-go) for
//     write-heavy paths (state commit, changeset writes). Read paths are
//     already well-served by mmap.
//
// JIT/AOT EVM Compilation:
//   - reth's revmc compiles EVM bytecode to native x86 via LLVM backend.
//   - JIT mode: 6.9x speedup on compute-intensive contracts (Fibonacci 19x).
//   - Go ecosystem lacks an equivalent LLVM binding with low overhead.
//   - Alternative: Profile hot contracts and hand-optimize via precompiles.
//   - Recommendation: Monitor revmc maturity. Consider CGO-based integration
//     if the performance gap becomes critical for specific workloads.
//
// OP Stack Integration:
//   - Optimism is migrating from op-geth to op-reth (deadline: May 2026).
//   - N42 as an OP Stack execution engine would require:
//     (a) Engine API payload attributes extension for L2 fields
//     (b) Sequencer mode in the consensus layer
//     (c) Fraud proof generation hooks (ExEx framework can support this)
//   - The existing Engine API v1-v4 + ExEx framework provides a foundation.
//   - Recommendation: Build an op-n42 adapter as a separate binary that
//     translates OP Stack payload attributes to N42's Engine API format.
//
// Portal Network:
//   - Decentralized distribution of historical chain data via DHT.
//   - N42 already has a mobile light client with JMT Merkle proofs.
//   - Portal Network could supplement the light client by providing
//     decentralized access to historical blocks/receipts.
//   - N42 uses libp2p (not DevP2P), so Portal's Discovery v5 overlay
//     would need adaptation to libp2p's Kademlia DHT.
//   - Recommendation: Low priority. The existing light client + witness
//     protocol covers the primary use case. Portal Network integration
//     can be deferred until the protocol stabilizes in the Ethereum ecosystem.
package deferred
