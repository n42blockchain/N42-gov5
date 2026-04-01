// sszdiag: diagnose SSZ format from an existing MDBX database.
// Reads a few blocks, marshals them to SSZ, and reports sizes.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/n42blockchain/N42/proto/types_pb"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	internalcore "github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: sszdiag <datadir/chaindata>\n")
		os.Exit(1)
	}
	dbPath := os.Args[1]

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg

	logger := log.New()
	db, err := mdbx.NewMDBX(logger).
		Path(dbPath).
		Readonly().
		Open(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	if err := db.View(context.Background(), func(tx kv.Tx) error {
		// Read a few blocks: 0, 1, 2, 100
		// Genesis hash analysis
		fmt.Println("=== Genesis Hash Analysis ===")
		canonicalHash, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			fmt.Printf("ReadCanonicalHash(0) error: %v\n", err)
		} else {
			fmt.Printf("Canonical hash at block 0: %s\n", canonicalHash.Hex())
		}
		genesisBlk := rawdb.ReadBlock(tx, canonicalHash, 0)
		if genesisBlk != nil {
			fmt.Printf("Genesis block hash (computed): %s\n", genesisBlk.Hash().Hex())
			fmt.Printf("Genesis block header hash:     %s\n", genesisBlk.Header().Hash().Hex())
		}
		// Check block #1 parent hash
		hash1, _ := rawdb.ReadCanonicalHash(tx, 1)
		if hash1 != (types.Hash{}) {
			blk1 := rawdb.ReadBlock(tx, hash1, 1)
			if blk1 != nil {
				fmt.Printf("Block #1 parentHash:           %s\n", blk1.ParentHash().Hex())
			}
		}
		fmt.Printf("Expected mainnet genesis:       %s\n", "0x138734b7044254e5ecbabf8056f5c2b73cd0847aaa5acac7345507cbeab387b8")

		// Compare transaction encoding for blocks with txs
		fmt.Println("\n=== Transaction Encoding Analysis ===")
		// Detailed state root analysis for block 3304451
		fmt.Println("=== Block #3304451 State Root Deep Dive ===")
		{
			badNum := uint64(3304451)
			h, _ := rawdb.ReadCanonicalHash(tx, badNum)
			blk := rawdb.ReadBlock(tx, h, badNum)
			if blk != nil {
				hdr := blk.Header().(*block.Header)
				fmt.Printf("Block #%d: hash=%s\n", badNum, h.Hex()[:18])
				fmt.Printf("  StateRoot:   %s\n", hdr.Root.Hex())
				fmt.Printf("  TxHash:      %s\n", hdr.TxHash.Hex()[:18])
				fmt.Printf("  ReceiptHash: %s\n", hdr.ReceiptHash.Hex()[:18])
				fmt.Printf("  GasUsed:     %d\n", hdr.GasUsed)
				fmt.Printf("  Txs:         %d\n", len(blk.Transactions()))
				for i, t := range blk.Transactions() {
					fmt.Printf("  tx[%d]: type=%d to=%v gas=%d nonce=%d\n",
						i, t.Type(), t.To(), t.Gas(), t.Nonce())
				}
			} else {
				fmt.Printf("Block #%d not found\n", badNum)
			}
			// Check parent
			parentNum := badNum - 1
			ph, _ := rawdb.ReadCanonicalHash(tx, parentNum)
			pblk := rawdb.ReadBlock(tx, ph, parentNum)
			if pblk != nil {
				phdr := pblk.Header().(*block.Header)
				fmt.Printf("Parent #%d: StateRoot=%s\n", parentNum, phdr.Root.Hex())
				fmt.Printf("Parent #%d: MixDigest=%s\n", parentNum, phdr.MixDigest.Hex()[:18])
			}
			// Check what Finalize modifies
			if blk != nil {
				hdr := blk.Header().(*block.Header)
				var bgu, ebg uint64
				if hdr.BlobGasUsed != nil {
					bgu = *hdr.BlobGasUsed
				}
				if hdr.ExcessBlobGas != nil {
					ebg = *hdr.ExcessBlobGas
				}
				fmt.Printf("Block header BlobGasUsed=%d ExcessBlobGas=%d\n", bgu, ebg)
				// LtHashRoot is now encoded in Extra, not a Header field
				// fmt.Printf("Block header LtHashRoot=%s\n", hdr.LtHashRoot.Hex()[:18])
				fmt.Printf("Block header MixDigest=%s\n", hdr.MixDigest.Hex()[:18])
			}
		}

		// Check Shanghai block timestamp
		fmt.Println("\n=== Shanghai Block Timestamp ===")
		for _, num := range []uint64{11907215, 11907216, 11907217} {
			h, _ := rawdb.ReadCanonicalHash(tx, num)
			blk := rawdb.ReadBlock(tx, h, num)
			if blk != nil {
				hdr := blk.Header().(*block.Header)
				fmt.Printf("Block #%d: timestamp=%d (0x%x)\n", num, hdr.Time, hdr.Time)
			} else {
				fmt.Printf("Block #%d: not found\n", num)
			}
		}
		// Also check block #1
		{
			h, _ := rawdb.ReadCanonicalHash(tx, 1)
			blk := rawdb.ReadBlock(tx, h, 1)
			if blk != nil {
				fmt.Printf("Block #1: timestamp=%d\n", blk.Header().(*block.Header).Time)
			}
		}

		// Full scan: verify tx root for every block with transactions 0-10M
		fmt.Println("\n=== Full TX Root Scan 0-10M ===")
		maxBlock := uint64(10_000_000)
		scanned := 0
		matched := 0
		failed := 0
		for num := uint64(0); num <= maxBlock; num++ {
			hash, err := rawdb.ReadCanonicalHash(tx, num)
			if err != nil || hash == (types.Hash{}) {
				break // end of chain
			}
			blk := rawdb.ReadBlock(tx, hash, num)
			if blk == nil {
				break
			}
			scanned++

			// Simulate SSZ round-trip (P2P path)
			proto1 := blk.ToProtoMessage().(*types_pb.Block)
			sszBytes, err := proto1.MarshalSSZ()
			if err != nil {
				fmt.Printf("Block #%d: MarshalSSZ error: %v\n", num, err)
				failed++
				continue
			}
			proto2 := &types_pb.Block{}
			if err := proto2.UnmarshalSSZ(sszBytes); err != nil {
				fmt.Printf("Block #%d: UnmarshalSSZ error: %v\n", num, err)
				failed++
				continue
			}
			blk2 := new(block.Block)
			if err := blk2.FromProtoMessage(proto2); err != nil {
				fmt.Printf("Block #%d: FromProtoMessage error: %v\n", num, err)
				failed++
				continue
			}

			// Check tx root
			txRoot := internalcore.DeriveSha(transaction.Transactions(blk2.Transactions()))
			headerTxHash := blk2.Header().(*block.Header).TxHash
			if txRoot != headerTxHash {
				fmt.Printf("FAIL Block #%d: txRoot have=%s want=%s txs=%d\n",
					num, txRoot.Hex()[:18], headerTxHash.Hex()[:18], len(blk2.Transactions()))
				failed++
				if failed >= 10 {
					fmt.Println("... stopping after 10 failures")
					break
				}
				continue
			}
			matched++

			if num%1_000_000 == 0 {
				fmt.Printf("  ... scanned %d blocks, %d matched, %d failed\n", scanned, matched, failed)
			}
		}
		fmt.Printf("=== Scan complete: scanned=%d matched=%d failed=%d ===\n\n", scanned, matched, failed)

		// Test block #1 empty tx root (the exact error scenario)
		fmt.Println("=== Block #1 Empty TX Root Test ===")
		{
			hash1, _ := rawdb.ReadCanonicalHash(tx, 1)
			blk1 := rawdb.ReadBlock(tx, hash1, 1)
			if blk1 != nil {
				txs := blk1.Transactions()
				fmt.Printf("Block #1: txs=%d headerTxHash=%s\n", len(txs), blk1.Header().(*block.Header).TxHash.Hex()[:18])
				// Simulate what block_validator does
				legacyHash := internalcore.DeriveSha(transaction.Transactions(txs))
				fmt.Printf("  internal.DeriveSha (legacy) = %s\n", legacyHash.Hex()[:18])
				v2Hash := internalcore.DeriveShaV2(transaction.Transactions(txs))
				fmt.Printf("  internal.DeriveShaV2        = %s\n", v2Hash.Hex()[:18])
				if legacyHash == blk1.Header().(*block.Header).TxHash {
					fmt.Printf("  Legacy MATCH ✓\n")
				} else if v2Hash == blk1.Header().(*block.Header).TxHash {
					fmt.Printf("  V2 MATCH (wrong path selected!)\n")
				} else {
					fmt.Printf("  NEITHER MATCH!\n")
				}
			}
		}

		// Simulate BOOTNODE: marshal with OLD 336-byte SSZ format
		fmt.Println("\n=== Bootnode Simulation (OLD 336-byte SSZ) ===")
		for _, num := range []uint64{1, 2642} {
			hash, _ := rawdb.ReadCanonicalHash(tx, num)
			blk := rawdb.ReadBlock(tx, hash, num)
			if blk == nil {
				continue
			}
			proto1 := blk.ToProtoMessage().(*types_pb.Block)
			// Current SSZ marshal (392 bytes per tx)
			currentSSZ, _ := proto1.MarshalSSZ()

			// Truncate each tx in SSZ to simulate 336-byte bootnode format
			// We can't easily re-marshal with old code, so let's check
			// what happens when we use 392 vs what bootnode would send
			fmt.Printf("Block #%d: currentSSZ=%d bytes, txs=%d\n", num, len(currentSSZ), len(proto1.Body.Txs))
			if len(proto1.Body.Txs) > 0 {
				tx0ssz, _ := proto1.Body.Txs[0].MarshalSSZ()
				fmt.Printf("  tx[0] SSZ size = %d (should be 392 for new, bootnode sends 336)\n", len(tx0ssz))
				// Check what data is in fields 16-22 (bytes 336-392)
				if len(tx0ssz) >= 392 {
					allZero := true
					for _, b := range tx0ssz[336:392] {
						if b != 0 {
							allZero = false
							break
						}
					}
					fmt.Printf("  fields 16-22 (bytes 336-392) all zero? %v\n", allZero)
				}
				// Now simulate: manually create a 336-byte tx by truncating
				// Check if tx has AccessList
				tx0 := blk.Transactions()[0]
				al := tx0.AccessList()
				fmt.Printf("  tx type=%d AccessList entries=%d\n", tx0.Type(), len(al))
				if len(al) > 0 {
					fmt.Printf("  *** AccessList present! Bootnode 336-byte SSZ DROPS this ***\n")
					// Simulate: what happens if we remove AccessList from Marshal?
					origBytes, _ := tx0.Marshal()
					fmt.Printf("  Marshal with AccessList:    %d bytes\n", len(origBytes))

					// Create a copy without AccessList to simulate bootnode
					// (bootnode sends SSZ without AccessList, we decode, then Marshal)
				}
			}
		}

		// SSZ round-trip test: simulate P2P send/receive
		fmt.Println("\n=== SSZ Round-Trip Test (simulating P2P) ===")
		for _, num := range []uint64{2642, 5000000} {
			hash, _ := rawdb.ReadCanonicalHash(tx, num)
			blk := rawdb.ReadBlock(tx, hash, num)
			if blk == nil || len(blk.Transactions()) == 0 {
				continue
			}
			// Step 1: Convert to proto (what bootnode does)
			proto1 := blk.ToProtoMessage().(*types_pb.Block)
			// Step 2: SSZ marshal (bootnode sends)
			sszBytes, _ := proto1.MarshalSSZ()
			// Step 3: SSZ unmarshal (we receive)
			proto2 := &types_pb.Block{}
			if err := proto2.UnmarshalSSZ(sszBytes); err != nil {
				fmt.Printf("Block #%d: SSZ unmarshal failed: %v\n", num, err)
				continue
			}
			// Step 4: Convert to internal block
			blk2 := new(block.Block)
			if err := blk2.FromProtoMessage(proto2); err != nil {
				fmt.Printf("Block #%d: FromProtoMessage failed: %v\n", num, err)
				continue
			}
			// Step 5: Compute DeriveSha
			txRoot := internalcore.DeriveSha(transaction.Transactions(blk2.Transactions()))
			headerTxHash := blk2.Header().(*block.Header).TxHash
			if txRoot == headerTxHash {
				fmt.Printf("Block #%d: SSZ round-trip MATCH (txRoot=%s)\n", num, txRoot.Hex()[:18])
			} else {
				fmt.Printf("Block #%d: SSZ round-trip MISMATCH!\n  header=%s\n  computed=%s\n", num, headerTxHash.Hex()[:18], txRoot.Hex()[:18])
				// Compare first tx marshal bytes
				if len(blk.Transactions()) > 0 && len(blk2.Transactions()) > 0 {
					b1, _ := blk.Transactions()[0].Marshal()
					b2, _ := blk2.Transactions()[0].Marshal()
					fmt.Printf("  tx[0] original marshal=%d bytes, roundtrip marshal=%d bytes\n", len(b1), len(b2))
					if len(b1) != len(b2) {
						fmt.Printf("  SIZE DIFFERS! (+%d bytes)\n", len(b2)-len(b1))
					// Dump proto fields comparison
					p1 := blk.Transactions()[0].ToProtoMessage().(*types_pb.Transaction)
					p2 := blk2.Transactions()[0].ToProtoMessage().(*types_pb.Transaction)
					fmt.Printf("  orig: From=%v To=%v V=%v R=%v S=%v Sign=%d\n", p1.From!=nil, p1.To!=nil, p1.V!=nil, p1.R!=nil, p1.S!=nil, len(p1.Sign))
					fmt.Printf("  rt:   From=%v To=%v V=%v R=%v S=%v Sign=%d\n", p2.From!=nil, p2.To!=nil, p2.V!=nil, p2.R!=nil, p2.S!=nil, len(p2.Sign))
					fmt.Printf("  orig: AccessList=%d BlobHashes=%d PqSig=%d\n", len(p1.AccessList), len(p1.BlobHashes), p1.PqSigAlgo)
					fmt.Printf("  rt:   AccessList=%d BlobHashes=%d PqSig=%d\n", len(p2.AccessList), len(p2.BlobHashes), p2.PqSigAlgo)
					} else {
						for i := range b1 {
							if b1[i] != b2[i] {
								fmt.Printf("  first diff at byte %d: orig=0x%02x rt=0x%02x\n", i, b1[i], b2[i])
								break
							}
						}
					}
				}
			}
		}

		fmt.Println("\n=== Direct TX Root Test ===")
		for _, num := range []uint64{2642, 5000000} {
			hash, _ := rawdb.ReadCanonicalHash(tx, num)
			blk := rawdb.ReadBlock(tx, hash, num)
			if blk == nil || len(blk.Transactions()) == 0 {
				fmt.Printf("Block #%d: no txs or not found\n", num)
				continue
			}
			fmt.Printf("Block #%d: txHash in header = %s\n", num, blk.Header().(*block.Header).TxHash.Hex()[:18])

			// Compute tx root the way our code does it
			txRoot := internalcore.DeriveSha(transaction.Transactions(blk.Transactions()))
			fmt.Printf("  DeriveSha computed = %s\n", txRoot.Hex()[:18])

			// Show first tx proto encoding size
			tx0 := blk.Transactions()[0]
			b, _ := tx0.Marshal()
			fmt.Printf("  tx[0] proto.Marshal = %d bytes (type=%d nonce=%d)\n", len(b), tx0.Type(), tx0.Nonce())

			// Compare with stored hash
			if txRoot == blk.Header().(*block.Header).TxHash {
				fmt.Printf("  MATCH!\n")
			} else {
				fmt.Printf("  MISMATCH! header=%s computed=%s\n", blk.Header().(*block.Header).TxHash.Hex()[:18], txRoot.Hex()[:18])
			}
		}

		fmt.Println("\n=== Block analysis ===")
		for _, num := range []uint64{0, 1, 5000000, 10000000} {
			hash, err := rawdb.ReadCanonicalHash(tx, num)
			if err != nil || hash == (types.Hash{}) {
				fmt.Printf("Block #%d: not found\n", num)
				continue
			}

			blk := rawdb.ReadBlock(tx, hash, num)
			if blk == nil {
				fmt.Printf("Block #%d: hash=%s but block nil\n", num, hash.Hex()[:16])
				continue
			}

			// Convert to proto and SSZ-marshal (same path as P2P sender)
			proto := blk.ToProtoMessage().(*types_pb.Block)
			sszData, err := proto.MarshalSSZ()
			if err != nil {
				fmt.Printf("Block #%d: MarshalSSZ error: %v\n", num, err)
				continue
			}

			// Report sizes
			hdrSize := 0
			bodySize := 0
			if proto.Header != nil {
				hdrSSZ, _ := proto.Header.MarshalSSZ()
				hdrSize = len(hdrSSZ)
				// Analyze header: read Extra offset (bytes 276-280) to detect format
				if len(hdrSSZ) >= 280 {
					extraOff := uint64(hdrSSZ[276]) | uint64(hdrSSZ[277])<<8 | uint64(hdrSSZ[278])<<16 | uint64(hdrSSZ[279])<<24
					extraLen := len(proto.Header.Extra)
					fmt.Printf("  Header detail: sszLen=%d extraOffset=%d extraLen=%d fixedPart=%d\n",
						len(hdrSSZ), extraOff, extraLen, extraOff)
				}
			}
			if proto.Body != nil {
				bodySSZ, _ := proto.Body.MarshalSSZ()
				bodySize = len(bodySSZ)
			}

			txCount := 0
			txSizes := ""
			if proto.Body != nil {
				txCount = len(proto.Body.Txs)
				if txCount > 0 && txCount <= 3 {
					for i, t := range proto.Body.Txs {
						tssz, _ := t.MarshalSSZ()
						txSizes += fmt.Sprintf(" tx[%d]=%d", i, len(tssz))
					}
				} else if txCount > 3 {
					t0, _ := proto.Body.Txs[0].MarshalSSZ()
					txSizes = fmt.Sprintf(" tx[0]=%d ...(%d more)", len(t0), txCount-1)
				}
			}

			verCount := 0
			rewCount := 0
			if proto.Body != nil {
				verCount = len(proto.Body.Verifiers)
				rewCount = len(proto.Body.Rewards)
			}

			fmt.Printf("Block #%d: total=%d header=%d body=%d txs=%d vers=%d rews=%d%s\n",
				num, len(sszData), hdrSize, bodySize, txCount, verCount, rewCount, txSizes)

			// Try round-trip: unmarshal the SSZ data
			blk2 := &types_pb.Block{}
			if err := blk2.UnmarshalSSZ(sszData); err != nil {
				fmt.Printf("  !! Round-trip UnmarshalSSZ FAILED: %v\n", err)
			} else {
				fmt.Printf("  OK round-trip\n")
			}
		}
		return nil
	}); err != nil {
		fmt.Fprintf(os.Stderr, "view: %v\n", err)
		os.Exit(1)
	}
}
