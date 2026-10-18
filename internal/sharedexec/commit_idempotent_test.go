package sharedexec

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func certifiedCanonicalFixture(t *testing.T, commit bool) (strictIngressFixture, *block.Block, CertifiedCommitRequest) {
	t.Helper()
	f := newStrictIngressFixture(t, 10, 1<<20)
	status, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.server.AdmitStrictFrame(context.Background(), f.server.Identity(), status.Epoch, f.frames[0]); err != nil {
		t.Fatal(err)
	}
	b := strictExecute(t, f, strictFrameRaws(t, f.frames[0]))
	request := CertifiedCommitRequest{CommitRequest: CommitRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: b.Number64().Uint64()}, Certificate: *commitProof(f.keys, b.Hash(), 5, false)}
	if commit {
		if _, err := f.server.CommitCertified(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	return f, b, request
}

// Reject every writer entrypoint, including a purported read-only helper that
// accidentally joins a failed asynchronous write and reloads state.
type certifiedReadOnlyDB struct {
	kv.RwDB
	writes atomic.Int64
}

func (d *certifiedReadOnlyDB) deny() error {
	d.writes.Add(1)
	return errors.New("unexpected database writer during certified duplicate")
}
func (d *certifiedReadOnlyDB) Update(context.Context, func(kv.RwTx) error) error {
	return d.deny()
}
func (d *certifiedReadOnlyDB) UpdateNosync(context.Context, func(kv.RwTx) error) error {
	return d.deny()
}
func (d *certifiedReadOnlyDB) BeginRw(context.Context) (kv.RwTx, error) {
	return nil, d.deny()
}
func (d *certifiedReadOnlyDB) BeginRwNosync(context.Context) (kv.RwTx, error) {
	return nil, d.deny()
}

func TestCertifiedCanonicalDuplicatePreservesHeldReservationAndNeverWrites(t *testing.T) {
	f, b, request := certifiedCanonicalFixture(t, true)
	ctx := context.Background()
	status, _ := f.server.StrictIngressStatus()
	reservation, known, err := f.server.reserveStrictIngress(f.server.ingress, status.Epoch, f.frames[1], len(f.frames[1].Payload))
	if err != nil || known != nil || reservation == nil {
		t.Fatalf("hold independent frame: %+v %v", known, err)
	}
	defer reservation.release()
	before, _ := f.server.StrictIngressStatus()
	original := f.chain.ChainDB
	readOnly := &certifiedReadOnlyDB{RwDB: original}
	f.chain.ChainDB = readOnly
	defer func() { f.chain.ChainDB = original }()
	for range 7 {
		proof, existing, err := f.chain.VerifyCertifiedCanonicalPrefix(&request.Certificate)
		if err != nil || !existing || proof.HeadHash != b.Hash() || proof.HeadNumber != 1 {
			t.Fatalf("fresh canonical QC: %+v %t %v", proof, existing, err)
		}
		result, err := f.server.CommitCertified(ctx, request)
		if err != nil || !result.Committed || result.HeadHash != b.Hash() || result.HeadNumber != 1 {
			t.Fatalf("authenticated duplicate: %+v %v", result, err)
		}
		after, _ := f.server.StrictIngressStatus()
		if !reflect.DeepEqual(after, before) || reservation.stamp != f.server.ingress.stamp {
			t.Fatal("duplicate invalidated a held reservation or changed queue credits")
		}
	}
	for _, bad := range []*hotstuff.QuorumCertificate{commitProof(f.keys, b.Hash(), 4, false), commitProof(f.keys, b.Hash(), 5, true)} {
		candidate := request
		candidate.Certificate = *bad
		if _, err := f.server.CommitCertified(ctx, candidate); err == nil {
			t.Fatal("duplicate bypassed fresh quorum/domain verification")
		}
	}
	forged := request
	forged.Certificate = request.Certificate.Clone()
	forged.Certificate.AggregateSignature[0] ^= 1
	if _, err := f.server.CommitCertified(ctx, forged); err == nil {
		t.Fatal("duplicate reused a cached certificate")
	}
	wrongView := request
	wrongView.Certificate = request.Certificate.Clone()
	wrongView.Certificate.View++
	if _, err := f.server.CommitCertified(ctx, wrongView); err == nil {
		t.Fatal("duplicate reused signatures from another certificate view")
	}
	wrong := request
	wrong.ParentHash[0] ^= 1
	if _, err := f.server.CommitCertified(ctx, wrong); err == nil {
		t.Fatal("duplicate accepted wrong parent")
	}
	after, _ := f.server.StrictIngressStatus()
	if !reflect.DeepEqual(after, before) || readOnly.writes.Load() != 0 {
		t.Fatal("duplicate rejection touched database or queue state")
	}
	ack, err := f.server.verifyReservedIngress(ctx, f.server.ingress, f.frames[1], reservation)
	if err != nil || ack.NewlyAdmitted != 3 || ack.Status.PendingTransactions != 3 || ack.Status.ReservedTransactions != 0 || ack.Status.ReservedEncodedBytes != 0 {
		t.Fatalf("held grant could not publish after duplicate QCs: %+v %v", ack, err)
	}
	if readOnly.writes.Load() != 0 {
		t.Fatal("admission unexpectedly wrote chain database")
	}
}

func TestCertifiedCanonicalMissingOrWrongTxLookupFallsBackToActualRepair(t *testing.T) {
	for _, wrong := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "wrong-height"}[wrong], func(t *testing.T) {
			f, b, request := certifiedCanonicalFixture(t, true)
			ctx := context.Background()
			key := b.Transactions()[0].Hash().Bytes()
			if err := f.chain.DB().Update(ctx, func(tx kv.RwTx) error {
				if wrong {
					return tx.Put(modules.TxLookup, key, []byte{99})
				}
				return tx.Delete(modules.TxLookup, key)
			}); err != nil {
				t.Fatal(err)
			}
			if _, existing, err := f.chain.VerifyCertifiedCanonicalPrefix(&request.Certificate); err != nil || existing {
				t.Fatalf("legacy lookup repair was skipped: %t %v", existing, err)
			}
			before, _ := f.server.StrictIngressStatus()
			if _, err := f.server.CommitCertified(ctx, request); err != nil {
				t.Fatal(err)
			}
			after, _ := f.server.StrictIngressStatus()
			if after.CanonicalGeneration != before.CanonicalGeneration+1 || after.Reconciling {
				t.Fatal("lookup repair did not use original gated/reconciled path")
			}
			if err := f.chain.DB().View(ctx, func(tx kv.Tx) error {
				got, err := tx.GetOne(modules.TxLookup, key)
				if err != nil {
					return err
				}
				if !bytes.Equal(got, b.Number64().Bytes()) {
					t.Fatal("legacy duplicate did not physically restore transaction index")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if _, existing, err := f.chain.VerifyCertifiedCanonicalPrefix(&request.Certificate); err != nil || !existing {
				t.Fatalf("repaired prefix not recognized: %t %v", existing, err)
			}
		})
	}
}

func TestCertifiedCanonicalDirtyBootstrapOrLaggingQueueStillReconciles(t *testing.T) {
	for _, mode := range []string{"dirty", "bootstrap", "lagging"} {
		t.Run(mode, func(t *testing.T) {
			f, b, request := certifiedCanonicalFixture(t, true)
			q := f.server.ingress
			q.mu.Lock()
			switch mode {
			case "dirty":
				q.reconciling = true
			case "bootstrap":
				q.bootstrapPending = true
			case "lagging":
				q.stamp.hash, q.stamp.number = f.chain.GenesisBlock().Hash(), 0
			}
			generation := q.stamp.generation
			q.mu.Unlock()
			if _, err := f.server.CommitCertified(context.Background(), request); err != nil {
				t.Fatal(err)
			}
			q.mu.Lock()
			defer q.mu.Unlock()
			if q.stamp.generation != generation+1 || q.reconciling || q.bootstrapPending || q.stamp.hash != b.Hash() || q.stamp.number != 1 {
				t.Fatal("duplicate left original startup/failed-reconcile recovery gated")
			}
		})
	}
}

func TestCertifiedCanonicalSevenConcurrentQCsAdvanceOnce(t *testing.T) {
	f, b, request := certifiedCanonicalFixture(t, false)
	before, _ := f.server.StrictIngressStatus()
	start := make(chan struct{})
	results := make(chan error, 7)
	var workers sync.WaitGroup
	for range 7 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			result, err := f.server.CommitCertified(context.Background(), request)
			if err == nil && (!result.Committed || result.HeadHash != b.Hash() || result.HeadNumber != 1) {
				err = errors.New("incorrect committed canonical head")
			}
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	after, _ := f.server.StrictIngressStatus()
	if after.CanonicalGeneration != before.CanonicalGeneration+1 || after.PendingTransactions != 0 || after.Reconciling {
		t.Fatal("concurrent duplicate QCs repeatedly mutated ingress generation")
	}
}

func TestCertifiedCanonicalProofRequiresFinalityAndActualAppliedLineage(t *testing.T) {
	for _, mode := range []string{"advance", "no-finality", "behind-finality", "bad-finality", "no-applied", "behind-applied", "sibling-applied", "descendant-applied"} {
		t.Run(mode, func(t *testing.T) {
			f, b, request := certifiedCanonicalFixture(t, mode != "advance")
			if err := f.chain.DB().Update(context.Background(), func(tx kv.RwTx) error {
				switch mode {
				case "no-finality":
					return rawdb.WriteHotStuffCommittedHead(tx, types.Hash{})
				case "behind-finality":
					return rawdb.WriteHotStuffCommittedHead(tx, f.chain.GenesisBlock().Hash())
				case "bad-finality":
					return rawdb.WriteHotStuffCommittedHead(tx, types.Hash{0xff})
				case "no-applied":
					return tx.Delete(modules.DatabaseInfo, []byte("qmdbAppliedHead"))
				case "behind-applied":
					return rawdb.WriteQMDBApplied(tx, 0, f.chain.GenesisBlock().Hash())
				case "sibling-applied", "descendant-applied":
					h := block.CopyHeader(b.Header().(*block.Header))
					if mode == "sibling-applied" {
						h.Root[0] ^= 1
					} else {
						h.ParentHash, h.Number = b.Hash(), uint256.NewInt(2)
					}
					h.ResetHashCache()
					rawdb.WriteHeader(tx, h)
					return rawdb.WriteQMDBApplied(tx, h.Number.Uint64(), h.Hash())
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			proof, existing, err := f.chain.VerifyCertifiedCanonicalPrefix(&request.Certificate)
			if mode == "descendant-applied" {
				if err != nil || !existing || proof.HeadHash != b.Hash() {
					t.Fatalf("legitimate applied descendant rejected: %+v %t %v", proof, existing, err)
				}
			} else if mode == "advance" || mode == "no-finality" || mode == "behind-finality" {
				if err != nil || existing || proof.HeadHash != (types.Hash{}) || proof.HeadNumber != 0 {
					t.Fatalf("writer fallback expected: %+v %t %v", proof, existing, err)
				}
			} else if err == nil || existing || proof.HeadHash != (types.Hash{}) || proof.HeadNumber != 0 {
				t.Fatalf("invalid finality/applied evidence accepted: %+v %t %v", proof, existing, err)
			}
		})
	}
}

type certifiedLookupErrorDB struct {
	kv.RwDB
	err error
}

type certifiedLookupErrorTx struct {
	kv.Tx
	err error
}

func (d *certifiedLookupErrorDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	return d.RwDB.View(ctx, func(tx kv.Tx) error { return fn(&certifiedLookupErrorTx{Tx: tx, err: d.err}) })
}
func (t *certifiedLookupErrorTx) GetOne(table string, key []byte) ([]byte, error) {
	if table == modules.TxLookup {
		return nil, t.err
	}
	return t.Tx.GetOne(table, key)
}

func TestCertifiedCanonicalTxLookupReadErrorDoesNotMutateQueue(t *testing.T) {
	f, _, request := certifiedCanonicalFixture(t, true)
	before, _ := f.server.StrictIngressStatus()
	original := f.chain.ChainDB
	sentinel := errors.New("physical lookup read failed")
	f.chain.ChainDB = &certifiedLookupErrorDB{RwDB: original, err: sentinel}
	defer func() { f.chain.ChainDB = original }()
	if _, existing, err := f.chain.VerifyCertifiedCanonicalPrefix(&request.Certificate); !errors.Is(err, sentinel) || existing {
		t.Fatalf("physical lookup IO error hidden: %t %v", existing, err)
	}
	if _, err := f.server.CommitCertified(context.Background(), request); !errors.Is(err, sentinel) {
		t.Fatalf("owner hid physical lookup IO error: %v", err)
	}
	after, _ := f.server.StrictIngressStatus()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("failed read invalidated queue reservations/generation")
	}
}
