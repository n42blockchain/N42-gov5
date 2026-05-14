// acctcs-extract-wipes: extract per-block list of contract addresses
// whose storage must be prefix-wiped (SELFDESTRUCT path) from a
// witness-replay acctcs freezer. Writes a compact "addr-only" sidecar
// that rebuild-state expands at apply time via an MDBX prefix scan on
// the Storage table — covering the gap left by witness-replay's
// per-slot storcs, which only sees slots the EVM read.
//
// Per-block payload format:
//
//	addr20 × N  (N = len(blob)/20; len(blob) % 20 == 0)
//
// Empty blocks have a zero-byte payload. cidx stays aligned with the
// source acctcs (one entry per block).
//
// Why scan witness-replay's own acctcs (not forward-replay)
// ----------------------------------------------------------
// witness-replay's acctcs records (oldAccount, newAccount) for every
// address touched in a block, with the same fidelity as forward-replay
// — DeleteAccount and account-creation calls go through the regular
// stateWriter path regardless of whether the storage reader is backed
// by MDBX or by the witness stream. So acctcs is the right place to
// look for the wipe signal even when storcs is structurally lossy.
//
// Signal (cross-referenced via account codeHash):
//
//   forward-replay calls stateWriter.CreateContract(addr) — which is
//   what triggers collectPreWipeSlots — from TWO paths in
//   intra_block_state.updateAccountWithWipe:
//
//     path 1  shouldDelete    (SELFDESTRUCT, no recreate same block)
//             → acctcs: newValue=empty
//     path 2  needsWipe       (SELFDESTRUCT + CREATE2 same block)
//             → acctcs: oldValue != newValue, both non-empty, with the
//               contract codeHash CHANGING (CREATE2 redeploys at the
//               same address with new code) — and the redeployed
//               account's nonce typically resets to 0.
//
//   We decode the account-storage v2 fieldBits to extract codeHash from
//   both oldValue and newValue, then flag addr iff EITHER:
//
//     Signal A (path 1): newValue=empty AND old.codeHash != emptyHash
//                        i.e. a non-EOA account was deleted.
//                        The "non-EOA" filter is critical: EIP-161
//                        empty-account cleanups (Spurious Dragon+) fire
//                        DeleteAccount on touched empty EOAs across
//                        millions of blocks, but those have empty
//                        codeHash and never had any storage rows — so
//                        flagging them inflates the sidecar by ~1000×
//                        with no correctness benefit (prefix-scan
//                        finds nothing to delete at apply time).
//
//     Signal B (path 2): old.codeHash != emptyHash AND
//                        new.codeHash != emptyHash AND
//                        old.codeHash != new.codeHash
//                        i.e. a contract was destroyed and replaced
//                        with a different contract in the same block.
//
// No external comparison or threshold needed — the signal is exact
// because codeHash is part of the account's canonical encoding and
// reflects the EVM's view of "is this still the same contract".
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
)

// emptyCodeHash is keccak256(nil) — the canonical "no code" marker on
// every EOA. Mirrors account.IsEmptyCodeHash without taking a dependency
// on its return path.
var emptyCodeHash = crypto.Keccak256Hash(nil)

// accountCodeHash extracts the codeHash field from a StateAccount V2
// encoding. Returns (Hash{}, true) for EOAs (no codeHash bit set, or
// codeHash byte string == emptyCodeHash). Returns (?, false) only on a
// truncated/malformed encoding — callers should treat that as "not a
// contract" so corruption stays as a no-op rather than a false flag.
//
// V2 layout (mirrors common/account/state_account.go DecodeForStorageV2):
//
//	byte 0:     fieldBits — bit 0=Nonce, bit 1=Balance, bit 2=Incarnation,
//	                        bit 3=CodeHash
//	bytes 1...: varint Nonce             (when bit 0)
//	            1B balanceLen + balance  (when bit 1)
//	            varint Incarnation       (when bit 2 — skipped, legacy)
//	            32B CodeHash             (when bit 3)
func accountCodeHash(enc []byte) (types.Hash, bool) {
	if len(enc) == 0 {
		return types.Hash{}, false
	}
	bits := enc[0]
	pos := 1
	if bits&1 != 0 {
		_, n := binary.Uvarint(enc[pos:])
		if n <= 0 {
			return types.Hash{}, false
		}
		pos += n
	}
	if bits&2 != 0 {
		if pos >= len(enc) {
			return types.Hash{}, false
		}
		bl := int(enc[pos])
		pos++
		if bl > 32 || pos+bl > len(enc) {
			return types.Hash{}, false
		}
		pos += bl
	}
	if bits&4 != 0 {
		_, n := binary.Uvarint(enc[pos:])
		if n <= 0 {
			return types.Hash{}, false
		}
		pos += n
	}
	if bits&8 == 0 {
		// No codeHash field == EOA. Treat as empty.
		return types.Hash{}, true
	}
	if pos+32 > len(enc) {
		return types.Hash{}, false
	}
	var ch types.Hash
	copy(ch[:], enc[pos:pos+32])
	return ch, true
}

// isContractCodeHash returns true when ch is set to a real contract
// codeHash (i.e. NOT empty and NOT the zero hash). EOAs return false.
func isContractCodeHash(ch types.Hash) bool {
	return ch != (types.Hash{}) && ch != emptyCodeHash
}

func main() {
	src := flag.String("acctcs-dir", "", "freezer dir containing witness-replay acctcs.cdat (REQUIRED)")
	dst := flag.String("dst", "", "destination freezer dir for the addr-only wipes sidecar (REQUIRED)")
	fromBlock := flag.Uint64("from", 0, "start block (inclusive); 0 = auto-resume from dst's existing items")
	toBlock := flag.Uint64("to", 0, "end block (exclusive); 0 = acctcs.Items()")
	progressEvery := flag.Uint64("progress", 100000, "log progress every N blocks (0 = silent)")
	flag.Parse()

	if *src == "" || *dst == "" {
		fmt.Fprintln(os.Stderr, "ERROR: --acctcs-dir and --dst are required")
		os.Exit(2)
	}

	acctTbl, err := freezer.NewFreezerTableCompressedReadOnly(*src, freezer.TableAccountChanges, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open acctcs:", err)
		os.Exit(1)
	}
	defer acctTbl.Close()
	acctTbl.ForceBatchSize(freezer.BatchSize)

	if err := os.MkdirAll(*dst, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir dst:", err)
		os.Exit(1)
	}
	dstTbl, err := freezer.NewFreezerTableCompressed(*dst, freezer.TableWipes, "c")
	if err != nil {
		fmt.Fprintln(os.Stderr, "open dst wipes:", err)
		os.Exit(1)
	}
	defer dstTbl.Close()

	enc, err := zstd.NewWriter(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "zstd writer:", err)
		os.Exit(1)
	}
	defer enc.Close()

	end := *toBlock
	if items := acctTbl.Items(); end == 0 || end > items {
		end = items
	}
	start := *fromBlock
	if dstItems := dstTbl.Items(); dstItems > start {
		fmt.Fprintf(os.Stderr, "resuming: dst already has %d entries, --from set to %d\n", dstItems, dstItems)
		start = dstItems
	}
	if start >= end {
		fmt.Fprintf(os.Stderr, "nothing to do: start=%d >= end=%d\n", start, end)
		return
	}

	fmt.Printf("Extracting addr-only wipes from blocks [%d, %d)\n", start, end)
	fmt.Printf("acctcs=%s\n", *src)
	fmt.Printf("dst=%s/%s.cdat\n", *dst, freezer.TableWipes)
	fmt.Printf("emptyCodeHash=%x\n", emptyCodeHash)

	var (
		pathACount     uint64 // signal A: contract deleted (path 1)
		pathBCount     uint64 // signal B: contract replaced (path 2)
		pathCCount     uint64 // signal C: contract code stripped, account survives (pre-Spurious-Dragon SELFDESTRUCT)
		nonEmptyBlocks uint64
		totalAddrsOut  uint64
		batchEntries   [][]byte
	)
	t0 := time.Now()

	flushBatch := func() error {
		if len(batchEntries) == 0 {
			return nil
		}
		encoded := freezer.EncodeBatch(batchEntries, enc)
		if err := freezer.WriteBatch(dstTbl, batchEntries, encoded); err != nil {
			return err
		}
		batchEntries = batchEntries[:0]
		return nil
	}

	// Per-block scratch: set of addrs flagged this block (dedup
	// path A vs path B fires on the same addr).
	wipes := make(map[types.Address]uint8, 32)
	// Reuse account decoder to amortize ~5ns/decode across 24M blocks.
	var oldAcc, newAcc account.StateAccount
	_ = oldAcc
	_ = newAcc

	for b := start; b < end; b++ {
		acctData, err := acctTbl.Retrieve(b)
		if err != nil {
			fmt.Fprintf(os.Stderr, "block %d: retrieve acctcs: %v\n", b, err)
			os.Exit(1)
		}

		clear(wipes)

		if len(acctData) > 0 {
			entries, err := ethel.DecodeAccountChanges(acctData)
			if err != nil {
				fmt.Fprintf(os.Stderr, "WARN block %d: decode acctcs: %v\n", b, err)
			} else {
				for _, e := range entries {
					oldCH, oldOK := accountCodeHash(e.OldValue)
					if !oldOK {
						continue
					}
					oldIsContract := isContractCodeHash(oldCH)

					if len(e.NewValue) == 0 {
						// Path 1: account deleted. Flag only when it WAS
						// a contract — EOAs have no storage rows for
						// prefix-scan to delete.
						if oldIsContract {
							wipes[e.Address] |= 0x01
						}
						continue
					}

					newCH, newOK := accountCodeHash(e.NewValue)
					if !newOK {
						continue
					}
					newIsContract := isContractCodeHash(newCH)

					// Path 2: contract replaced. Old and new BOTH
					// contracts (anchored on codeHash to filter
					// EOA→contract first-deploys, which never wipe),
					// codeHash differs (CREATE2 redeployed with new
					// bytecode in the same block).
					if oldIsContract && newIsContract && oldCH != newCH {
						wipes[e.Address] |= 0x02
					}

					// Path 3: contract SELFDESTRUCT'd pre-Spurious-Dragon
					// (block < 2,675,000). EIP-161 only removes empty
					// accounts when they're touched POST-Spurious-Dragon;
					// before that, a SELFDESTRUCT'd contract leaves
					// behind an empty-but-existing account (newValue
					// encodes as [0x00] — fieldBits=0, no fields). Old
					// had codeHash; new has no codeHash bit (account
					// became EOA-shaped). Storage rows of the old
					// contract MUST be prefix-wiped — forward-replay
					// would have emitted them via collectPreWipeSlots;
					// witness-replay missed that step, and Path A above
					// doesn't catch this case because newValue isn't
					// empty (it's a 1-byte all-zeros account).
					if oldIsContract && !newIsContract {
						wipes[e.Address] |= 0x04
					}
				}
			}
		}

		var blob []byte
		if len(wipes) > 0 {
			blob = make([]byte, 0, 20*len(wipes))
			for addr, src := range wipes {
				blob = append(blob, addr[:]...)
				if src&0x01 != 0 {
					pathACount++
				}
				if src&0x02 != 0 {
					pathBCount++
				}
				if src&0x04 != 0 {
					pathCCount++
				}
			}
			totalAddrsOut += uint64(len(wipes))
			nonEmptyBlocks++
		}
		batchEntries = append(batchEntries, blob)

		if len(batchEntries) >= freezer.BatchSize {
			if err := flushBatch(); err != nil {
				fmt.Fprintf(os.Stderr, "write batch at block %d: %v\n", b, err)
				os.Exit(1)
			}
		}
		if *progressEvery > 0 && b > start && (b-start)%*progressEvery == 0 {
			elapsed := time.Since(t0).Seconds()
			rate := float64(b-start) / elapsed
			fmt.Fprintf(os.Stderr, "  block %d  %.0f blk/s  addrs=%d (pathA=%d pathB=%d pathC=%d)  nonEmptyBlocks=%d\n",
				b, rate, totalAddrsOut, pathACount, pathBCount, pathCCount, nonEmptyBlocks)
		}
	}
	if err := flushBatch(); err != nil {
		fmt.Fprintln(os.Stderr, "final batch:", err)
		os.Exit(1)
	}
	if err := dstTbl.Sync(); err != nil {
		fmt.Fprintln(os.Stderr, "sync dst:", err)
		os.Exit(1)
	}

	fmt.Printf("\n=== done ===\n")
	fmt.Printf("scanned blocks:        %d (%d..%d)\n", end-start, start, end-1)
	fmt.Printf("total wipe addrs:      %d\n", totalAddrsOut)
	fmt.Printf("  path A (deleted):    %d\n", pathACount)
	fmt.Printf("  path B (replaced):   %d\n", pathBCount)
	fmt.Printf("  path C (code-stripped, pre-SD): %d\n", pathCCount)
	fmt.Printf("non-empty wipe blocks: %d / %d (%.4f%%)\n",
		nonEmptyBlocks, end-start, 100*float64(nonEmptyBlocks)/float64(end-start))
	fmt.Printf("elapsed: %v\n", time.Since(t0).Truncate(time.Second))
}
