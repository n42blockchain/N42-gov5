// n42-mpt-build-spike: Phase A 2-3 day spike validating that we can
// build a standard Ethereum MPT from N42's existing data using N42's
// existing lib/trie HashBuilder + GenStructStep. Doesn't compete with
// reth on size yet — just proves the pipeline works end-to-end so we
// can commit to the 4-5 week full implementation with confidence.
//
// Pipeline:
//
//  1. Open reth's PlainAccountState (the data source N42 snapshot was
//     itself built from).
//  2. Sample N (addr, account_value) pairs.
//  3. For each: compute keccak(addr) → hashedKey, convert to nibble
//     path (64 nibbles + 0x10 terminator).
//  4. Sort entries by nibble path.
//  5. Feed sorted entries to HashBuilder via GenStructStep with a
//     HashCollector that captures each branch (path → encoding).
//  6. Report:
//     - State root (deterministic across runs on same input)
//     - Count + total bytes of persisted branch nodes
//     - Wall time
//     - Bytes per leaf (= MPT efficiency at this sample size)
//
// Pattern follows common/hash/derive_sha_erigon.go (DeriveShaErigon).
// On a successful spike we know lib/trie is usable for full MPT
// building from snapshot data → green-light Phase A real implementation.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/c2h5oh/datasize"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/rlphacks"
	"github.com/n42blockchain/N42/lib/trie"
)

func tableCfg(table string) func(kv.TableCfg) kv.TableCfg {
	return func(d kv.TableCfg) kv.TableCfg {
		d[table] = kv.TableCfgItem{}
		return d
	}
}

type leafEntry struct {
	keyHex []byte // nibble path with 0x10 terminator
	value  []byte
}

func main() {
	dbPath := flag.String("db", `D:\reth2k\db`, "reth MDBX dir (readonly)")
	table := flag.String("table", "PlainAccountState", "source table (PlainAccountState)")
	samples := flag.Int("samples", 1000, "leaves to build the MPT from")
	mapSizeGB := flag.Int("mapsize-gb", 4096, "DB MapSize cap")
	traceBuild := flag.Bool("trace", false, "trace HashBuilder")
	verify := flag.Bool("verify", true, "rebuild a second time and assert root is identical")
	flag.Parse()

	logger := log.New()
	t0 := time.Now()
	db, err := mdbxkv.NewMDBX(logger).
		Path(*dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(datasize.ByteSize(*mapSizeGB) * datasize.GB).
		Readonly().
		WithTableCfg(tableCfg(*table)).
		Open(context.Background())
	if err != nil {
		fatal("open: %v", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fatal("tx: %v", err)
	}
	defer tx.Rollback()

	c, err := tx.Cursor(*table)
	if err != nil {
		fatal("cursor: %v", err)
	}
	defer c.Close()

	hasher := sha3.NewLegacyKeccak256()
	entries := make([]leafEntry, 0, *samples)
	for k, v, err := c.First(); err == nil && k != nil && len(entries) < *samples; k, v, err = c.Next() {
		hasher.Reset()
		hasher.Write(k)
		hashed := hasher.Sum(nil)
		// nibble path: 64 nibbles + terminator (0x10)
		keyHex := make([]byte, len(hashed)*2+1)
		for i, b := range hashed {
			keyHex[i*2] = b >> 4
			keyHex[i*2+1] = b & 0x0f
		}
		keyHex[len(keyHex)-1] = 0x10
		val := make([]byte, len(v))
		copy(val, v)
		entries = append(entries, leafEntry{keyHex: keyHex, value: val})
	}
	if len(entries) == 0 {
		fatal("no entries read from %s", *table)
	}
	fmt.Printf("collected %d entries (read %s)\n", len(entries), time.Since(t0).Truncate(time.Millisecond))

	root, branchCount, branchBytes := buildMPT(entries, *traceBuild)

	fmt.Println()
	fmt.Println("=== MPT build result ===")
	fmt.Printf("  leaves                %d\n", len(entries))
	fmt.Printf("  state root            0x%s\n", hex.EncodeToString(root[:]))
	fmt.Printf("  branch nodes          %d\n", branchCount)
	fmt.Printf("  branch total bytes    %d  (%.2f KB)\n", branchBytes, float64(branchBytes)/1024)
	if branchCount > 0 {
		fmt.Printf("  bytes/branch          %.1f\n", float64(branchBytes)/float64(branchCount))
	}
	fmt.Printf("  bytes/leaf (amortised) %.1f\n", float64(branchBytes)/float64(len(entries)))
	fmt.Printf("  build elapsed         %s\n", time.Since(t0).Truncate(time.Millisecond))

	if *verify {
		root2, _, _ := buildMPT(entries, false)
		if root != root2 {
			fatal("DETERMINISM FAILURE: rebuild root 0x%x != first 0x%x", root2[:], root[:])
		}
		fmt.Println()
		fmt.Println("  determinism check     ✓ passed (rebuild matches)")
	}

	// Extrapolate to full account table (386M).
	if branchCount > 0 {
		const fullAccts = 386_066_282
		extrapBranchCount := int64(branchCount) * fullAccts / int64(len(entries))
		extrapBytes := int64(branchBytes) * fullAccts / int64(len(entries))
		fmt.Println()
		fmt.Println("=== Extrapolation to full PlainAccountState (386M leaves) ===")
		fmt.Printf("  est. branch nodes     %d (%.1fM)\n", extrapBranchCount, float64(extrapBranchCount)/1e6)
		fmt.Printf("  est. branch bytes     %.2f GB\n", float64(extrapBytes)/1e9)
		fmt.Printf("  est. bytes/leaf       %.1f\n", float64(extrapBytes)/float64(fullAccts))
		fmt.Printf("  reth AccountsTrie     5.40 GB / 14.0 B/leaf  (reference)\n")
	}
}

// buildMPT feeds sorted leaves into HashBuilder via GenStructStep,
// returns (root, branchCount, branchBytes). Mirrors the structure of
// common/hash/derive_sha_erigon.go's DeriveShaErigon.
func buildMPT(entries []leafEntry, trace bool) (root [32]byte, branchCount int, branchBytes int) {
	// Sort by nibble path.
	sort.Slice(entries, func(i, j int) bool {
		return bytes.Compare(entries[i].keyHex, entries[j].keyHex) < 0
	})

	hb := trie.NewHashBuilder(trace)
	var (
		curr, succ, currVal []byte
		groups              []uint16
		hasTree             []uint16
		hasHash             []uint16
		leafData            trie.GenStructStepLeafData
	)
	retain := func(_ []byte) bool { return false }

	// HashCollector2 captures persisted branches.
	hc := func(keyHex []byte, hasState, hasTree, hasHash uint16, hashes, rootHash []byte) error {
		if hasState == 0 {
			return nil // pruned / deleted
		}
		branchCount++
		// Branch encoding = 6B masks + hashes + optional 32B root.
		branchBytes += 6 + len(hashes) + len(rootHash)
		return nil
	}

	for _, e := range entries {
		succ = e.keyHex
		if len(curr) > 0 {
			leafData.Value = rlphacks.RlpEncodedBytes(currVal)
			var err error
			groups, hasTree, hasHash, err = trie.GenStructStep(
				retain, curr, succ, hb, hc, &leafData,
				groups, hasTree, hasHash, trace,
			)
			if err != nil {
				panic(err)
			}
		}
		curr = succ
		currVal = e.value
	}
	// Final step with empty succ.
	leafData.Value = rlphacks.RlpEncodedBytes(currVal)
	if _, _, _, err := trie.GenStructStep(
		retain, curr, []byte{}, hb, hc, &leafData,
		groups, hasTree, hasHash, trace,
	); err != nil {
		panic(err)
	}

	r, err := hb.RootHash()
	if err != nil {
		panic(err)
	}
	copy(root[:], r[:])
	return root, branchCount, branchBytes
}

func fatal(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}
