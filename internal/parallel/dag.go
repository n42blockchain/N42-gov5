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

package parallel

import (
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

// TxAccessInfo represents the pre-execution access pattern of a transaction,
// derived from EIP-2930 access lists and implicit sender/recipient accesses.
type TxAccessInfo struct {
	Index         int
	From          types.Address    // sender address (always known)
	To            *types.Address   // recipient (nil for contract creation)
	AccessList    transaction.AccessList
	HasAccessList bool // false means the tx has no access list — treat as unknown
}

// ExtractAccessInfo builds a TxAccessInfo from a transaction and its index.
// The sender address must be provided separately (it requires signature recovery).
func ExtractAccessInfo(txIndex int, sender types.Address, tx *transaction.Transaction) TxAccessInfo {
	info := TxAccessInfo{
		Index:      txIndex,
		From:       sender,
		To:         tx.To(),
		AccessList: tx.AccessList(),
	}
	// A transaction has a meaningful access list if it's an AccessListTx or DynamicFeeTx
	// and the list is non-empty, OR if the type itself supports access lists.
	// We consider the access list present if tx.AccessList() returns a non-nil, non-empty list.
	info.HasAccessList = len(info.AccessList) > 0
	return info
}

// locationKey is a lightweight key for conflict detection during DAG construction.
// It combines address + optional storage slot to detect overlapping state accesses.
type locationKey struct {
	addr types.Address
	slot types.Hash
	isStorage bool
}

// TxDependency represents a directed edge in the transaction dependency graph:
// transaction From must complete before transaction To can begin.
type TxDependency struct {
	From int // tx index that must execute first
	To   int // tx index that depends on From
}

// TxDAG represents the transaction dependency graph for a block.
// It encodes which transactions may conflict and must be ordered,
// enabling the executor to parallelize truly independent work.
type TxDAG struct {
	NumTxs       int
	Dependencies []TxDependency

	// deps[i] = set of tx indices that tx i depends on (must execute before i).
	deps [][]int

	// revDeps[i] = set of tx indices that depend on tx i (i must execute before them).
	revDeps [][]int

	// Independent is the list of tx indices with no dependencies.
	Independent []int

	// Unknown is the list of tx indices without access lists (conservative).
	Unknown []int
}

// BuildDAG analyzes transactions' access patterns and constructs a dependency graph.
//
// Algorithm:
//  1. For each transaction, collect all state locations it accesses:
//     - Sender address (balance/nonce are always read/written)
//     - Recipient address (balance is always written for value transfers)
//     - All addresses and storage keys in the EIP-2930 access list
//  2. Build a conflict map: location -> list of tx indices that access it.
//  3. For each location, create dependency edges between all accessing transactions
//     in index order (earlier tx -> later tx).
//  4. Transactions without access lists are treated conservatively: they depend on
//     all earlier transactions and all later transactions depend on them.
//
// The resulting DAG is used by DAGExecutor to schedule execution batches that
// maximize parallelism while respecting known dependencies.
func BuildDAG(txInfos []TxAccessInfo) *TxDAG {
	n := len(txInfos)
	if n == 0 {
		return &TxDAG{NumTxs: 0}
	}

	dag := &TxDAG{
		NumTxs:  n,
		deps:    make([][]int, n),
		revDeps: make([][]int, n),
	}

	// Track which transactions have unknown access patterns.
	isUnknown := make([]bool, n)

	// conflictMap: location -> sorted list of tx indices that access this location.
	conflictMap := make(map[locationKey][]int)

	for i := range txInfos {
		info := &txInfos[i]

		if !info.HasAccessList {
			isUnknown[i] = true
			dag.Unknown = append(dag.Unknown, i)
			continue
		}

		// Sender address: balance and nonce are always accessed.
		addToConflictMap(conflictMap, locationKey{addr: info.From}, i)

		// Recipient address: balance is always accessed for value transfers.
		if info.To != nil {
			addToConflictMap(conflictMap, locationKey{addr: *info.To}, i)
		}

		// EIP-2930 access list entries.
		for _, tuple := range info.AccessList {
			// Account-level access.
			addToConflictMap(conflictMap, locationKey{addr: tuple.Address}, i)

			// Storage slot-level access.
			for _, slot := range tuple.StorageKeys {
				addToConflictMap(conflictMap, locationKey{
					addr:      tuple.Address,
					slot:      slot,
					isStorage: true,
				}, i)
			}
		}
	}

	// Build dependency edges from conflict map.
	// For each location, if txs [a, b, c] all access it (a < b < c),
	// we only need edges a->b and b->c (transitive reduction).
	depSet := make([]map[int]struct{}, n)
	for i := range depSet {
		depSet[i] = make(map[int]struct{})
	}

	for _, txIndices := range conflictMap {
		if len(txIndices) < 2 {
			continue
		}
		for j := 1; j < len(txIndices); j++ {
			from := txIndices[j-1]
			to := txIndices[j]
			depSet[to][from] = struct{}{}
		}
	}

	// Handle unknown transactions: they create barriers.
	// An unknown tx depends on all earlier known txs and all later known txs depend on it.
	// More precisely: unknown tx i depends on all txs j < i, and all txs k > i depend on i.
	for _, unknownIdx := range dag.Unknown {
		// Unknown tx depends on ALL earlier transactions.
		for j := 0; j < unknownIdx; j++ {
			depSet[unknownIdx][j] = struct{}{}
		}
		// ALL later transactions depend on this unknown tx.
		for k := unknownIdx + 1; k < n; k++ {
			depSet[k][unknownIdx] = struct{}{}
		}
	}

	// Convert dep sets to sorted slices and build the DAG edges.
	for i := 0; i < n; i++ {
		if len(depSet[i]) == 0 {
			dag.Independent = append(dag.Independent, i)
			continue
		}
		deps := make([]int, 0, len(depSet[i]))
		for dep := range depSet[i] {
			deps = append(deps, dep)
		}
		// Sort for deterministic ordering.
		sortInts(deps)
		dag.deps[i] = deps

		for _, dep := range deps {
			dag.Dependencies = append(dag.Dependencies, TxDependency{From: dep, To: i})
			dag.revDeps[dep] = append(dag.revDeps[dep], i)
		}
	}

	return dag
}

// Schedule returns batches of transaction indices that can be executed in parallel.
// Each batch contains transactions whose dependencies are all satisfied by previous batches.
// This is essentially Kahn's algorithm for topological level ordering.
func (dag *TxDAG) Schedule(numWorkers int) [][]int {
	if dag.NumTxs == 0 {
		return nil
	}

	// Compute in-degree for each transaction.
	inDegree := make([]int, dag.NumTxs)
	for i := 0; i < dag.NumTxs; i++ {
		inDegree[i] = len(dag.deps[i])
	}

	var batches [][]int
	remaining := dag.NumTxs

	for remaining > 0 {
		// Collect all txs with zero in-degree.
		var batch []int
		for i := 0; i < dag.NumTxs; i++ {
			if inDegree[i] == 0 {
				batch = append(batch, i)
			}
		}

		if len(batch) == 0 {
			// This should never happen in a valid DAG. If it does,
			// schedule all remaining as a single sequential batch.
			var fallback []int
			for i := 0; i < dag.NumTxs; i++ {
				if inDegree[i] >= 0 {
					fallback = append(fallback, i)
				}
			}
			batches = append(batches, fallback)
			break
		}

		batches = append(batches, batch)
		remaining -= len(batch)

		// "Remove" these txs from the graph by setting in-degree to -1
		// and decrementing dependents' in-degree.
		for _, idx := range batch {
			inDegree[idx] = -1
			for _, dep := range dag.revDeps[idx] {
				if inDegree[dep] > 0 {
					inDegree[dep]--
				}
			}
		}
	}

	return batches
}

// Deps returns the list of transaction indices that tx i depends on.
func (dag *TxDAG) Deps(txIndex int) []int {
	if txIndex < 0 || txIndex >= dag.NumTxs {
		return nil
	}
	return dag.deps[txIndex]
}

// HasDependency returns true if txIndex depends on depIndex.
func (dag *TxDAG) HasDependency(txIndex, depIndex int) bool {
	for _, d := range dag.deps[txIndex] {
		if d == depIndex {
			return true
		}
	}
	return false
}

// addToConflictMap appends a tx index to the conflict map for a given location.
func addToConflictMap(m map[locationKey][]int, key locationKey, txIndex int) {
	existing := m[key]
	// Avoid duplicates (same tx might access address both directly and via access list).
	if len(existing) > 0 && existing[len(existing)-1] == txIndex {
		return
	}
	m[key] = append(existing, txIndex)
}

// sortInts sorts a slice of ints in ascending order (insertion sort for small slices).
func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		key := a[i]
		j := i - 1
		for j >= 0 && a[j] > key {
			a[j+1] = a[j]
			j--
		}
		a[j+1] = key
	}
}
