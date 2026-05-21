// n42-proof-debug runs HistoricalProof against the production
// archive with defer/recover to capture the panic value that the
// `go test` runner kept losing (output got truncated).
//
// Usage (will take ~10 min — full reth scan per inline sibling):
//
//	n42-proof-debug --addr 0xa0b8...eb48 --slots 0,1,11 --block 25000000
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"github.com/n42blockchain/N42/internal/mptproof"
)

const (
	defaultAccountsDir = `D:\n42-mpt\accounts-mptcache`
	defaultStorageDir  = `D:\n42-mpt\storage-mptcache`
	defaultHistoryDir  = `D:\n42-history-full`
	defaultRethDB      = `D:\reth2k\db`
)

func main() {
	addrHex := flag.String("addr", "a0b86991c6218b36c1d19d4a2e9eb0ce3606eb48", "address (hex, no 0x)")
	slotsCSV := flag.String("slots", "", "comma-separated slot numbers (decimal) to include in proof")
	blockN := flag.Uint64("block", 25_000_000, "historical block N")
	accountsDir := flag.String("accounts", defaultAccountsDir, "")
	storageDir := flag.String("storage", defaultStorageDir, "")
	historyDir := flag.String("history", defaultHistoryDir, "")
	rethDB := flag.String("reth", defaultRethDB, "")
	skipFullProof := flag.Bool("skip-full-proof", false, "only test cheap account proof, skip FullAccountProofBytes (which is what panics)")
	flag.Parse()

	t0 := time.Now()

	addrBytes, err := hex.DecodeString(strings.TrimPrefix(*addrHex, "0x"))
	if err != nil || len(addrBytes) != 20 {
		fatal("addr: bad hex (need 40 chars): %v", err)
	}
	var addr [20]byte
	copy(addr[:], addrBytes)
	fmt.Printf("addr        : 0x%x\n", addr)
	fmt.Printf("block N     : %d\n", *blockN)

	var slots [][32]byte
	if *slotsCSV != "" {
		for _, s := range strings.Split(*slotsCSV, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			var slot [32]byte
			// Decimal slot index → big-endian 32B
			var n uint64
			if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
				fatal("slots: parse %q: %v", s, err)
			}
			slot[31] = byte(n)
			slot[30] = byte(n >> 8)
			slot[29] = byte(n >> 16)
			slot[28] = byte(n >> 24)
			slots = append(slots, slot)
		}
		fmt.Printf("slots       : %d (decimal)\n", len(slots))
	}

	fmt.Printf("opening leaf source from %s...\n", *rethDB)
	leaves, err := mptproof.NewRethLeafSource(*rethDB, "PlainAccountState", "PlainStorageState", 4096)
	if err != nil {
		fatal("NewRethLeafSource: %v", err)
	}
	defer leaves.Close()

	fmt.Printf("opening MPT readers + history...\n")
	g, err := mptproof.New(mptproof.Config{
		AccountsTrieDir: *accountsDir,
		StorageTrieDir:  *storageDir,
		HistoryDir:      *historyDir,
		Leaves:          leaves,
	})
	if err != nil {
		fatal("Generator.New: %v", err)
	}
	defer g.Close()

	fmt.Printf("=== Step 1: cheap LatestAccountProof ===\n")
	proof, err := g.LatestAccountProof(addr)
	if err != nil {
		fatal("LatestAccountProof: %v", err)
	}
	fmt.Printf("  walk outcome=%v leafDepth=%d hops=%d siblings=%d\n",
		proof.Walk.Outcome, proof.Walk.LeafDepth,
		len(proof.Walk.Hops), len(proof.Walk.CollectSiblings()))
	fmt.Printf("  leaf value: %d bytes  found=%v\n", len(proof.LeafValue), proof.LeafFound)
	for i, hop := range proof.Walk.Hops {
		// Count inline siblings (state set, hash not set) for this hop.
		state := hop.Branch.HasState
		hashMask := hop.Branch.HasHash
		inlineSibs := 0
		for n := byte(0); n < 16; n++ {
			bit := uint16(1) << n
			if state&bit != 0 && hashMask&bit == 0 {
				inlineSibs++
			}
		}
		fmt.Printf("  hop %d  depth=%d targetNib=0x%x  state=%016b  hashMask=%016b  inlineSibs=%d\n",
			i, hop.PrefixDepth, hop.TargetNibble, state, hashMask, inlineSibs)
	}

	if *skipFullProof {
		fmt.Printf("\n--skip-full-proof set; exiting before the slow step that panics\n")
		fmt.Printf("total elapsed %s\n", time.Since(t0).Truncate(time.Second))
		return
	}

	fmt.Printf("\n=== Step 2: FullAccountProofBytes (THE SLOW STEP, panics in prior runs) ===\n")
	fmt.Printf("starting at %s — expect ~30s per inline sibling × 7 hops = several minutes\n",
		time.Now().Format(time.RFC3339))

	// Wrap with recover + ALWAYS flush stderr/stdout before exit.
	func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "\n\n=== PANIC CAPTURED ===\n")
				fmt.Fprintf(os.Stderr, "panic value: %v\n", r)
				fmt.Fprintf(os.Stderr, "panic type : %T\n", r)
				fmt.Fprintf(os.Stderr, "\n=== STACK ===\n%s\n", debug.Stack())
				os.Stderr.Sync()
				os.Stdout.Sync()
				os.Exit(2)
			}
		}()

		pb, err := g.FullAccountProofBytes(proof)
		if err != nil {
			fatal("FullAccountProofBytes: %v", err)
		}
		fmt.Printf("✓ FullAccountProofBytes returned %d nodes, total %d bytes\n",
			len(pb), totalSize(pb))
		for i, n := range pb {
			fmt.Printf("  node %d: %d bytes\n", i, len(n))
		}

		// Verify the proof bytes pass the independent standard oracle
		// against the trie's stateRoot. This is the real end-to-end
		// correctness gate for the production data.
		fmt.Printf("\n=== Step 3: VerifyStandardProof (independent oracle) ===\n")
		val, found, verr := mptproof.VerifyStandardProof(pb, proof.StateRoot, proof.HashedAddr[:])
		if verr != nil {
			fmt.Fprintf(os.Stderr, "ORACLE ERROR: %v\n", verr)
			return
		}
		if !found {
			fmt.Fprintf(os.Stderr, "ORACLE: not found in proof — proof claims non-inclusion?\n")
			return
		}
		fmt.Printf("✓ Oracle: proof verifies against stateRoot 0x%x\n", proof.StateRoot[:8])
		fmt.Printf("  proven value: %d bytes (matches leaf: %v)\n",
			len(val), bytesEqualSafe(val, proof.LeafValue))
	}()

	fmt.Printf("\ntotal elapsed %s\n", time.Since(t0).Truncate(time.Second))
}

func totalSize(pb [][]byte) int {
	total := 0
	for _, x := range pb {
		total += len(x)
	}
	return total
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}

func bytesEqualSafe(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

var _ = context.Background
