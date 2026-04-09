// verify_senders.go — spot-check pre-computed senders against ecrecover.

package main

import (
	"fmt"
	"math/big"
	"math/rand"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/params"
)

// verifySenders spot-checks N random blocks from the senders store.
func verifySenders(chainDir string, ancientPath string, chainCfg *params.ChainConfig, sampleCount int) error {
	store, err := ethel.OpenSenderStore(chainDir)
	if err != nil {
		return fmt.Errorf("open sender store: %w", err)
	}
	if store == nil {
		return fmt.Errorf("no sender store found in %s", chainDir)
	}
	defer store.Close()

	maxBlock := store.MaxBlock()
	if maxBlock == 0 {
		return fmt.Errorf("sender store is empty")
	}
	fmt.Printf("Sender store: maxBlock=%d\n", maxBlock)

	// Open bodies from compact format (v2: bodies.cidx + .cdat).
	bodyReader, err := ethel.OpenBodyCompact(ancientPath)
	if err != nil {
		return fmt.Errorf("open body compact: %w", err)
	}
	defer bodyReader.Close()
	fmt.Printf("Body reader: maxBlock=%d\n", bodyReader.MaxBlock())

	bodyMax := bodyReader.MaxBlock()
	if maxBlock > bodyMax {
		maxBlock = bodyMax
	}

	rng := rand.New(rand.NewSource(42))
	checked, mismatches, skipped := 0, 0, 0

	for i := 0; i < sampleCount; i++ {
		blockNum := uint64(rng.Int63n(int64(maxBlock)))
		if blockNum == 0 {
			blockNum = 1
		}

		// Read body from compact reader.
		body, err := bodyReader.ReadBody(blockNum)
		if err != nil || body == nil {
			skipped++
			continue
		}
		if len(body.Txs) == 0 {
			continue // empty block
		}

		// Read pre-computed senders.
		senderData, err := store.ReadBlock(blockNum)
		if err != nil {
			fmt.Printf("  block %d: read err: %v\n", blockNum, err)
			continue
		}
		if len(senderData) == 0 {
			skipped++
			continue
		}
		senderCount := len(senderData) / 20
		if senderCount != len(body.Txs) {
			fmt.Printf("  block %d: COUNT MISMATCH senders=%d txs=%d\n",
				blockNum, senderCount, len(body.Txs))
			mismatches++
			continue
		}

		// ecrecover each TX and compare.
		signer := transaction.MakeSigner(chainCfg, new(big.Int).SetUint64(blockNum))
		blockOk := true
		for j, tx := range body.Txs {
			recovered, err := transaction.Sender(signer, tx)
			if err != nil {
				fmt.Printf("  block %d tx %d: ecrecover err: %v\n", blockNum, j, err)
				blockOk = false
				break
			}
			var stored types.Address
			copy(stored[:], senderData[j*20:(j+1)*20])
			if recovered != stored {
				fmt.Printf("  block %d tx %d: MISMATCH recovered=%s stored=%s\n",
					blockNum, j, recovered.Hex()[:12], stored.Hex()[:12])
				blockOk = false
				mismatches++
				break
			}
		}
		if blockOk {
			checked++
		}
	}

	fmt.Printf("\nSender verification: %d blocks checked, %d mismatches, %d skipped\n", checked, mismatches, skipped)
	if mismatches > 0 {
		return fmt.Errorf("%d sender mismatches found", mismatches)
	}
	fmt.Println("All OK")
	return nil
}
