package sharedexec

import (
	"context"
	"reflect"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func TestFreshCommitValidatesStoredDurability(t *testing.T) {
	for _, certified := range []bool{true, false} {
		t.Run(map[bool]string{true: "certified", false: "trusted-owner"}[certified], func(t *testing.T) {
			f, b, request := certifiedCanonicalFixture(t, false)
			var result CommitResult
			var err error
			if certified {
				result, err = f.server.CommitCertified(context.Background(), request)
			} else {
				owner := [32]byte{1}
				f.server.commitOwner = owner
				result, err = f.server.Commit(context.Background(), request.CommitRequest, owner)
			}
			if err != nil || !result.Committed || result.HeadHash != b.Hash() || result.HeadNumber != 1 {
				t.Fatalf("fresh commit: %+v %v", result, err)
			}
			if err := f.chain.DB().View(context.Background(), func(tx kv.Tx) error {
				if rawdb.ReadHeadBlockHash(tx) != b.Hash() || rawdb.ReadHotStuffCommittedHead(tx) != b.Hash() {
					t.Fatal("fresh commit did not persist canonical finality")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}

		})
	}
}

func damageFreshCommitEvidence(t *testing.T, f strictIngressFixture, b *block.Block, mode string) {
	t.Helper()
	if err := f.chain.DB().Update(context.Background(), func(tx kv.RwTx) error {
		bodyKey := modules.BlockBodyKey(b.Number64().Uint64(), b.Hash())
		receiptKey := modules.EncodeBlockNumber(b.Number64().Uint64())
		switch mode {
		case "missing-body":
			return tx.Delete(modules.BlockBody, bodyKey)
		case "malformed-body":
			return tx.Put(modules.BlockBody, bodyKey, []byte{0xff})
		case "missing-receipts":
			return tx.Delete(modules.Receipts, receiptKey)
		case "malformed-receipts":
			return tx.Put(modules.Receipts, receiptKey, []byte{0xff})
		case "wrong-canonical-sibling":
			return rawdb.WriteCanonicalHash(tx, types.Hash{1}, b.Number64().Uint64())
		}
		receipts := rawdb.ReadRawReceipts(tx, b.Number64().Uint64())
		if len(receipts) != 3 {
			t.Fatal("fixture requires three actual receipts")
		}
		if mode == "truncated-receipts" {
			receipts = receipts[:2]
		} else {
			receipts[2].CumulativeGasUsed++
		}
		return rawdb.WriteReceipts(tx, b.Number64().Uint64(), receipts)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFreshCommitRejectsDamagedPhysicalEvidence(t *testing.T) {
	for _, mode := range []string{"missing-body", "malformed-body", "missing-receipts", "malformed-receipts", "truncated-receipts", "changed-receipt", "wrong-canonical-sibling"} {
		t.Run(mode, func(t *testing.T) {
			f, b, request := certifiedCanonicalFixture(t, false)
			damageFreshCommitEvidence(t, f, b, mode)
			before, _ := f.server.StrictIngressStatus()

			if result, err := f.server.CommitCertified(context.Background(), request); err == nil || result.Committed {
				t.Fatalf("damaged evidence committed: %+v %v", result, err)
			}
			after, _ := f.server.StrictIngressStatus()
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failed durability check changed queue or database")
			}
			if err := f.chain.DB().View(context.Background(), func(tx kv.Tx) error {
				if rawdb.ReadHeadBlockHash(tx) != b.ParentHash() || rawdb.ReadHotStuffCommittedHead(tx) == b.Hash() {
					t.Fatal("damaged evidence changed canonical finality")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestImportStillChecksPhysicalBodyBeforeDurableAcknowledgement(t *testing.T) {
	f, b, _ := certifiedCanonicalFixture(t, false)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	damageFreshCommitEvidence(t, f, b, "missing-body")
	server, err := New(f.chain, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	result, err := server.Import(context.Background(), ImportRequest{Identity: server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
	if err != nil || result.Durable || result.State != "failed" {
		t.Fatalf("ordinary import trusted its request body: %+v %v", result, err)
	}
}
