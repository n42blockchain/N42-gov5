package sharedexec

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type certifiedReadCountingDB struct {
	*certifiedReadOnlyDB
	bodyKey, receiptKey                []byte
	bodies, receipts, senders, lookups atomic.Int64
	failTable                          string
	failErr                            error
}

type certifiedReadCountingTx struct {
	kv.Tx
	db *certifiedReadCountingDB
}

func (d *certifiedReadCountingDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	return d.RwDB.View(ctx, func(tx kv.Tx) error { return fn(&certifiedReadCountingTx{Tx: tx, db: d}) })
}

func (t *certifiedReadCountingTx) GetOne(table string, key []byte) ([]byte, error) {
	switch {
	case table == modules.BlockBody && bytes.Equal(key, t.db.bodyKey):
		t.db.bodies.Add(1)
	case table == modules.Receipts && bytes.Equal(key, t.db.receiptKey):
		t.db.receipts.Add(1)
	case table == modules.Senders && bytes.Equal(key, t.db.bodyKey):
		t.db.senders.Add(1)
	case table == modules.TxLookup:
		t.db.lookups.Add(1)
	}
	if table == t.db.failTable {
		return nil, t.db.failErr
	}
	return t.Tx.GetOne(table, key)
}

func countedCertifiedReads(t *testing.T, f strictIngressFixture, b *block.Block) *certifiedReadCountingDB {
	t.Helper()
	original := f.chain.ChainDB
	db := &certifiedReadCountingDB{certifiedReadOnlyDB: &certifiedReadOnlyDB{RwDB: original}, bodyKey: modules.BlockBodyKey(b.Number64().Uint64(), b.Hash()), receiptKey: modules.EncodeBlockNumber(b.Number64().Uint64())}
	f.chain.ChainDB = db
	t.Cleanup(func() { f.chain.ChainDB = original })
	return db
}

func TestCertifiedCanonicalExecutionReadsOneBodyAndReceiptPerRequest(t *testing.T) {
	f, b, request := certifiedCanonicalFixture(t, true)
	status, _ := f.server.StrictIngressStatus()
	reservation, known, err := f.server.reserveStrictIngress(f.server.ingress, status.Epoch, f.frames[1], len(f.frames[1].Payload))
	if err != nil || reservation == nil || known != nil {
		t.Fatalf("hold grant: %+v %v", known, err)
	}
	defer reservation.release()
	before, _ := f.server.StrictIngressStatus()
	db := countedCertifiedReads(t, f, b)
	for i := int64(1); i <= 2; i++ {
		result, err := f.server.CommitCertified(context.Background(), request)
		if err != nil || !result.Committed || result.HeadHash != b.Hash() || result.HeadNumber != 1 {
			t.Fatalf("fresh certified duplicate: %+v %v", result, err)
		}
		if db.bodies.Load() != i || db.receipts.Load() != i || db.senders.Load() != i || db.lookups.Load() != i*int64(len(b.Transactions())) {
			t.Fatalf("request %d reads body=%d receipt=%d senders=%d physical-index=%d", i, db.bodies.Load(), db.receipts.Load(), db.senders.Load(), db.lookups.Load())
		}
		after, _ := f.server.StrictIngressStatus()
		if !reflect.DeepEqual(before, after) || db.writes.Load() != 0 {
			t.Fatal("read reuse mutated database, queue generation or held grant")
		}
	}
	ack, err := f.server.verifyReservedIngress(context.Background(), f.server.ingress, f.frames[1], reservation)
	if err != nil || ack.NewlyAdmitted != 3 || ack.Status.ReservedTransactions != 0 || ack.Status.ReservedEncodedBytes != 0 || db.writes.Load() != 0 {
		t.Fatalf("held grant invalidated by duplicate: %+v %v", ack, err)
	}
}

func TestCertifiedCanonicalExecutionRejectsFreshStoredBodyOrReceiptDamage(t *testing.T) {
	for _, mode := range []string{"missing-body", "missing-receipts", "truncated-receipts", "malformed-receipts", "changed-receipt"} {
		t.Run(mode, func(t *testing.T) {
			f, b, request := certifiedCanonicalFixture(t, true)
			if _, err := f.server.CommitCertified(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			if err := f.chain.DB().Update(context.Background(), func(tx kv.RwTx) error {
				if mode == "missing-body" {
					return tx.Delete(modules.BlockBody, modules.BlockBodyKey(b.Number64().Uint64(), b.Hash()))
				}
				key := modules.EncodeBlockNumber(b.Number64().Uint64())
				switch mode {
				case "missing-receipts":
					return tx.Delete(modules.Receipts, key)
				case "malformed-receipts":
					return tx.Put(modules.Receipts, key, []byte{0xff})
				}
				receipts := rawdb.ReadRawReceipts(tx, b.Number64().Uint64())
				if len(receipts) != 3 {
					t.Fatal("fixture needs actual three execution receipts")
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
			before, _ := f.server.StrictIngressStatus()
			db := countedCertifiedReads(t, f, b)
			if _, err := f.server.CommitCertified(context.Background(), request); err == nil {
				t.Fatal("fresh request reused a previous successful execution check")
			}
			after, _ := f.server.StrictIngressStatus()
			if !reflect.DeepEqual(before, after) || db.writes.Load() != 0 {
				t.Fatal("failed evidence check changed queue or database")
			}
		})
	}
}

func emptyCertifiedCanonicalFixture(t *testing.T) (strictIngressFixture, *block.Block, CertifiedCommitRequest) {
	t.Helper()
	f := newStrictIngressFixture(t, 10, 1<<20)
	b := strictExecute(t, f, nil)
	request := CertifiedCommitRequest{CommitRequest: CommitRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: b.Number64().Uint64()}, Certificate: *commitProof(f.keys, b.Hash(), 5, false)}
	if _, err := f.server.CommitCertified(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	return f, b, request
}

func TestCertifiedCanonicalExecutionPropagatesSenderAndReceiptReadErrors(t *testing.T) {
	for _, empty := range []bool{false, true} {
		for _, table := range []string{modules.Senders, modules.Receipts} {
			t.Run(map[bool]string{false: "nonempty", true: "empty"}[empty]+"/"+table, func(t *testing.T) {
				var f strictIngressFixture
				var b *block.Block
				var request CertifiedCommitRequest
				if empty {
					f, b, request = emptyCertifiedCanonicalFixture(t)
				} else {
					f, b, request = certifiedCanonicalFixture(t, true)
				}
				// Successful empty and nonempty duplicate requests both remain valid
				// before injecting a storage error on the following fresh read.
				if _, err := f.server.CommitCertified(context.Background(), request); err != nil {
					t.Fatal(err)
				}
				before, _ := f.server.StrictIngressStatus()
				db := countedCertifiedReads(t, f, b)
				sentinel := errors.New("stored execution read failed")
				db.failTable, db.failErr = table, sentinel
				if _, err := f.server.CommitCertified(context.Background(), request); !errors.Is(err, sentinel) {
					t.Fatalf("%s error hidden, empty=%t: %v", table, empty, err)
				}
				after, _ := f.server.StrictIngressStatus()
				if !reflect.DeepEqual(before, after) || db.writes.Load() != 0 {
					t.Fatal("IO failure changed queue or entered a writer")
				}
			})
		}
	}
}
