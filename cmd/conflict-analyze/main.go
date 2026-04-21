// conflict-analyze: estimate parallelizability of historical ETH mainnet
// transactions for a Go Block-STM port feasibility study.
//
// Methodology — static, pessimistic account-level analysis:
//
// For each tx in each block, compute a (read_set, write_set) based on
// information available WITHOUT running EVM:
//
//   - Sender always reads & writes its account (nonce + balance).
//   - Tx.To always reads; writes only if value > 0 OR data is non-empty
//     (the contract's state may change). For simple value-only transfers
//     this is the destination account.
//   - EIP-2930 AccessList entries: contribute address + slots. Addresses
//     go in both reads and writes (contract may modify them). Slots go
//     in reads only (access-list semantics per EIP).
//
// Two txs are a "conflict pair" iff their account-level sets intersect:
//
//   A.write ∩ B.(read ∪ write) ≠ ∅   OR   B.write ∩ A.(read ∪ write) ≠ ∅
//
// The conflict graph's chromatic number approximates parallel width.
// Greedy coloring (Welsh-Powell by degree) gives an UPPER BOUND on
// chromatic number → a LOWER BOUND on parallelism.
//
// Per-block metric: parallel_ratio = 1 - chromatic_number / N_txs.
// Ideal speedup at N_cores: N_txs / max(chromatic_number, N_txs / N_cores).
//
// Caveats that will bias the estimate LOW (pessimistic):
//
//   - We don't know which storage slots a contract call actually touches,
//     so we assume any two calls to the SAME contract conflict (account
//     level). Real Block-STM operates slot-level and often discovers
//     disjoint slot sets → more parallelism.
//   - ERC20 transfer(x, y) has known predictable slots (balanceOf mapping)
//     — not modeled here; our estimate treats ERC20 token contract as
//     "hot" (everyone conflicts).
//   - We don't see internal CREATE / CALL to other contracts (would need
//     EVM trace). True write set is larger → more conflicts, less
//     parallelism. So actually this might bias us HIGH on the contract
//     side. Net direction ambiguous; treat numbers as ballpark.
//
// Output: per-block histogram + aggregate parallelism stats.
package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/params"
)

type txAccess struct {
	reads  map[types.Address]struct{}
	writes map[types.Address]struct{}
}

func (a *txAccess) conflicts(b *txAccess) bool {
	// A.write ∩ B.(read ∪ write)
	for addr := range a.writes {
		if _, ok := b.writes[addr]; ok {
			return true
		}
		if _, ok := b.reads[addr]; ok {
			return true
		}
	}
	// B.write ∩ A.read
	for addr := range b.writes {
		if _, ok := a.reads[addr]; ok {
			return true
		}
	}
	return false
}

func main() {
	ancient := flag.String("ancient", `d:\geth\geth\chaindata\ancient\chain`, "geth ancient chain dir")
	fromBlock := flag.Uint64("from", 13000000, "start block")
	toBlock := flag.Uint64("to", 13100000, "end block")
	sample := flag.Uint64("sample", 1, "sample 1 block every N (1 = every block)")
	topHot := flag.Int("top-hot", 20, "show top N hot addresses")
	flag.Parse()

	f, err := freezer.New(*ancient, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open freezer:", err)
		os.Exit(1)
	}
	defer f.Close()

	fmt.Printf("Analyzing blocks [%d, %d] sample 1/%d (ancient=%s)\n",
		*fromBlock, *toBlock, *sample, *ancient)

	// Aggregates
	var (
		blocksAnalyzed int
		totalTxs       int
		totalPairs     int
		conflictPairs  int
		widthHist      = make(map[int]int)     // parallel width buckets
		paralRatioSum  float64                 // sum of per-block (1 - width/N)
		hotWrites      = make(map[types.Address]uint64)
		speedup4Sum    float64
		speedup8Sum    float64
		speedup16Sum   float64
		startTime      = time.Now()
	)

	chainCfg := params.EthereumMainnetChainConfig

	for blk := *fromBlock; blk <= *toBlock; blk += *sample {
		bodyData, err := f.Ancient(freezer.TableBodies, blk)
		if err != nil {
			continue
		}
		body, err := ethel.DecodeGethBody(bodyData)
		if err != nil || len(body.Transactions) < 2 {
			continue
		}
		headerData, err := f.Ancient(freezer.TableHeaders, blk)
		if err != nil {
			continue
		}
		header, err := ethel.DecodeGethHeader(headerData)
		if err != nil {
			continue
		}
		signer := transaction.MakeSigner(chainCfg, header.Number.ToBig())

		n := len(body.Transactions)
		accesses := make([]*txAccess, n)
		for i, tx := range body.Transactions {
			ta := &txAccess{
				reads:  make(map[types.Address]struct{}, 4),
				writes: make(map[types.Address]struct{}, 4),
			}
			// Sender: reads + writes (nonce + balance).
			if sender, err := transaction.Sender(signer, tx); err == nil {
				ta.reads[sender] = struct{}{}
				ta.writes[sender] = struct{}{}
			}
			// Destination.
			if to := tx.To(); to != nil {
				ta.reads[*to] = struct{}{}
				hasValue := tx.Value() != nil && !tx.Value().IsZero()
				hasData := len(tx.Data()) > 0
				if hasValue || hasData {
					ta.writes[*to] = struct{}{}
				}
			} else {
				// Contract creation — the deployer writes to computed
				// address. Approximate as "always conflicts with nothing
				// static" (new address). Skip.
			}
			// Access list — explicit read hints.
			for _, entry := range tx.AccessList() {
				ta.reads[entry.Address] = struct{}{}
				// access-list DOES NOT imply write; contracts MAY write
				// to these on the call. Conservative: mark as write too.
				ta.writes[entry.Address] = struct{}{}
			}
			accesses[i] = ta
			for addr := range ta.writes {
				hotWrites[addr]++
			}
		}

		// Pairwise conflict matrix. Build adjacency list for coloring.
		adj := make([][]int, n)
		numPairs := n * (n - 1) / 2
		numConflicts := 0
		for i := 0; i < n; i++ {
			for j := i + 1; j < n; j++ {
				if accesses[i].conflicts(accesses[j]) {
					adj[i] = append(adj[i], j)
					adj[j] = append(adj[j], i)
					numConflicts++
				}
			}
		}

		// Greedy coloring by degree (Welsh-Powell): order vertices by
		// descending degree, assign each the smallest color not used by
		// its already-colored neighbors. Upper bound on chromatic number.
		order := make([]int, n)
		for i := range order {
			order[i] = i
		}
		sort.Slice(order, func(a, b int) bool {
			return len(adj[order[a]]) > len(adj[order[b]])
		})
		colors := make([]int, n)
		for i := range colors {
			colors[i] = -1
		}
		maxColor := 0
		for _, v := range order {
			used := make(map[int]bool)
			for _, u := range adj[v] {
				if colors[u] >= 0 {
					used[colors[u]] = true
				}
			}
			c := 0
			for used[c] {
				c++
			}
			colors[v] = c
			if c > maxColor {
				maxColor = c
			}
		}
		width := maxColor + 1

		blocksAnalyzed++
		totalTxs += n
		totalPairs += numPairs
		conflictPairs += numConflicts
		widthHist[width]++
		// parallel_ratio: how many txs "fit" per parallel lane.
		paralRatioSum += 1.0 - float64(width)/float64(n)

		// Speedup at N cores: N_txs / max(width, ceil(N_txs / N))
		// With N cores, we're bound by BOTH chromatic number AND core count.
		// Effective time = max(chromatic_number, N_txs / N_cores) relative to N_txs.
		sp := func(N int) float64 {
			byCores := float64(n) / float64(N)
			bound := float64(width)
			if byCores > bound {
				bound = byCores
			}
			return float64(n) / bound
		}
		speedup4Sum += sp(4)
		speedup8Sum += sp(8)
		speedup16Sum += sp(16)

		if blocksAnalyzed%5000 == 0 {
			fmt.Fprintf(os.Stderr, "  processed %d blocks, %.1fs elapsed\n",
				blocksAnalyzed, time.Since(startTime).Seconds())
		}
	}

	// Report
	fmt.Printf("\n=== Conflict analysis [%d, %d] ===\n", *fromBlock, *toBlock)
	fmt.Printf("Blocks analyzed:   %d\n", blocksAnalyzed)
	fmt.Printf("Total txs:         %d\n", totalTxs)
	if blocksAnalyzed == 0 {
		fmt.Println("No blocks analyzed.")
		return
	}
	fmt.Printf("Avg txs/block:     %.1f\n", float64(totalTxs)/float64(blocksAnalyzed))
	fmt.Printf("Total pair checks: %d\n", totalPairs)
	fmt.Printf("Conflict pairs:    %d (%.2f%%)\n",
		conflictPairs, 100*float64(conflictPairs)/float64(totalPairs))
	fmt.Printf("Avg parallel ratio: %.3f (1 = fully parallel, 0 = fully serial)\n",
		paralRatioSum/float64(blocksAnalyzed))
	fmt.Printf("\nIdeal speedup at N cores:\n")
	fmt.Printf("  N=4:   %.2fx\n", speedup4Sum/float64(blocksAnalyzed))
	fmt.Printf("  N=8:   %.2fx\n", speedup8Sum/float64(blocksAnalyzed))
	fmt.Printf("  N=16:  %.2fx\n", speedup16Sum/float64(blocksAnalyzed))

	fmt.Printf("\nParallel-width distribution (# colors / block):\n")
	widthBuckets := []struct {
		lo, hi int
		count  int
	}{
		{1, 1, 0},
		{2, 3, 0},
		{4, 7, 0},
		{8, 15, 0},
		{16, 31, 0},
		{32, 63, 0},
		{64, 127, 0},
		{128, 1 << 30, 0},
	}
	for w, c := range widthHist {
		for i := range widthBuckets {
			if w >= widthBuckets[i].lo && w <= widthBuckets[i].hi {
				widthBuckets[i].count += c
				break
			}
		}
	}
	for _, b := range widthBuckets {
		if b.count == 0 {
			continue
		}
		pct := 100 * float64(b.count) / float64(blocksAnalyzed)
		fmt.Printf("  width %3d-%3d: %6d blocks (%.2f%%)\n", b.lo, b.hi, b.count, pct)
	}

	fmt.Printf("\nTop %d hot writer addresses (appear in write sets):\n", *topHot)
	type hotEntry struct {
		addr  types.Address
		count uint64
	}
	sortedHot := make([]hotEntry, 0, len(hotWrites))
	for a, c := range hotWrites {
		sortedHot = append(sortedHot, hotEntry{a, c})
	}
	sort.Slice(sortedHot, func(i, j int) bool { return sortedHot[i].count > sortedHot[j].count })
	if len(sortedHot) > *topHot {
		sortedHot = sortedHot[:*topHot]
	}
	for i, h := range sortedHot {
		pct := 100 * float64(h.count) / float64(totalTxs)
		fmt.Printf("  %2d. %s  %6d  (%.2f%% of txs)\n",
			i+1, hex.EncodeToString(h.addr[:]), h.count, pct)
	}

	fmt.Printf("\nTotal time: %s\n", time.Since(startTime).Truncate(time.Second))
}
