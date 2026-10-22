package sharedexec

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// This context records entry into the writer-admission select after the first
// canonical probe has returned. DB View calls are marked so a backend's own
// context handling cannot be mistaken for admission.
type admissionObservedContext struct {
	context.Context
	inView       atomic.Bool
	probeDone    atomic.Bool
	waitOnce     sync.Once
	admissionTry chan struct{}
}

func newAdmissionObservedContext(parent context.Context) *admissionObservedContext {
	return &admissionObservedContext{Context: parent, admissionTry: make(chan struct{})}
}

func (c *admissionObservedContext) Done() <-chan struct{} {
	if c.probeDone.Load() && !c.inView.Load() {
		c.waitOnce.Do(func() { close(c.admissionTry) })
	}
	return c.Context.Done()
}

// admissionGateDB is installed while the fixture constructs its BlockChain.
// It is armed only after fixture setup, so no live ChainDB replacement is
// needed to control commit scheduling.
type admissionGateDB struct {
	kv.RwDB
	armed atomic.Bool

	canonicalKey  []byte
	canonicalHash types.Hash
	receiptKey    []byte

	initialLimit   int64
	canonicalReads atomic.Int64
	initialFalse   atomic.Int64
	initialEntered chan struct{}
	initialRelease chan struct{}
	initialOnce    sync.Once

	holdFreshProbe atomic.Bool
	freshEntered   chan struct{}
	freshRelease   chan struct{}
	freshOnce      sync.Once

	holdWriter    atomic.Bool
	writerEntered chan struct{}
	writerRelease chan struct{}
	writerEnter   sync.Once
	writerClose   sync.Once

	readerEntered chan struct{}
	readerRelease chan struct{}
	readerOnce    sync.Once
	physicalReads atomic.Int64
}

func newAdmissionGateDB(db kv.RwDB) *admissionGateDB {
	return &admissionGateDB{
		RwDB:           db,
		initialEntered: make(chan struct{}, 16),
		initialRelease: make(chan struct{}),
		freshEntered:   make(chan struct{}),
		freshRelease:   make(chan struct{}),
		writerEntered:  make(chan struct{}),
		writerRelease:  make(chan struct{}),
		readerEntered:  make(chan struct{}, 16),
		readerRelease:  make(chan struct{}),
	}
}

func newAdmissionFixture(t *testing.T, commit bool) (strictIngressFixture, *block.Block, CertifiedCommitRequest, *admissionGateDB) {
	t.Helper()
	var db *admissionGateDB
	f, b, request := certifiedCanonicalFixtureWithDB(t, commit, func(base kv.RwDB) kv.RwDB {
		db = newAdmissionGateDB(base)
		return db
	})
	db.canonicalKey = modules.EncodeBlockNumber(request.Number)
	db.canonicalHash = request.BlockHash
	db.receiptKey = modules.EncodeBlockNumber(request.Number)
	db.armed.Store(true)
	t.Cleanup(func() {
		db.releaseInitial()
		db.releaseFresh()
		db.releaseWriter()
		db.releaseReaders()
	})
	return f, b, request, db
}

func (d *admissionGateDB) releaseInitial() { d.initialOnce.Do(func() { close(d.initialRelease) }) }
func (d *admissionGateDB) releaseFresh()   { d.freshOnce.Do(func() { close(d.freshRelease) }) }
func (d *admissionGateDB) releaseWriter()  { d.writerClose.Do(func() { close(d.writerRelease) }) }
func (d *admissionGateDB) releaseReaders() { d.readerOnce.Do(func() { close(d.readerRelease) }) }

func (d *admissionGateDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	observed, _ := ctx.(*admissionObservedContext)
	if observed != nil {
		observed.inView.Store(true)
		defer observed.inView.Store(false)
	}
	return d.RwDB.View(ctx, func(tx kv.Tx) error {
		return fn(&admissionGateTx{Tx: tx, db: d, observed: observed})
	})
}

func (d *admissionGateDB) Update(ctx context.Context, fn func(kv.RwTx) error) error {
	if d.armed.Load() && d.holdWriter.Load() {
		d.writerEnter.Do(func() {
			close(d.writerEntered)
			<-d.writerRelease
		})
	}
	return d.RwDB.Update(ctx, fn)
}

func (d *admissionGateDB) UpdateNosync(ctx context.Context, fn func(kv.RwTx) error) error {
	if d.armed.Load() && d.holdWriter.Load() {
		d.writerEnter.Do(func() {
			close(d.writerEntered)
			<-d.writerRelease
		})
	}
	return d.RwDB.UpdateNosync(ctx, fn)
}

type admissionGateTx struct {
	kv.Tx
	db       *admissionGateDB
	observed *admissionObservedContext
}

func (tx *admissionGateTx) GetOne(table string, key []byte) ([]byte, error) {
	value, err := tx.Tx.GetOne(table, key)
	if err != nil || !tx.db.armed.Load() {
		return value, err
	}
	if table == modules.HeaderCanonical && bytes.Equal(key, tx.db.canonicalKey) {
		index := tx.db.canonicalReads.Add(1)
		if tx.observed != nil {
			tx.observed.probeDone.Store(true)
		}
		if index <= tx.db.initialLimit && tx.db.initialLimit > 0 {
			if !bytes.Equal(value, tx.db.canonicalHash[:]) {
				tx.db.initialFalse.Add(1)
			}
			tx.db.initialEntered <- struct{}{}
			<-tx.db.initialRelease
		} else if index == tx.db.initialLimit+1 && tx.db.holdFreshProbe.Load() {
			close(tx.db.freshEntered)
			<-tx.db.freshRelease
		}
	}
	if table == modules.Receipts && bytes.Equal(key, tx.db.receiptKey) && rawdb.ReadHeadBlockHash(tx.Tx) == tx.db.canonicalHash {
		tx.db.physicalReads.Add(1)
		tx.db.readerEntered <- struct{}{}
		<-tx.db.readerRelease
	}
	return value, err
}

func awaitAdmissionEvent(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for " + name)
	}
}

func TestCertifiedWriterAdmissionReprobesAndCoalescesFalseHintFollowers(t *testing.T) {
	f, b, request, db := newAdmissionFixture(t, false)
	before, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	db.initialLimit = 7
	db.holdWriter.Store(true)

	type outcome struct {
		result CommitResult
		err    error
	}
	contexts := make([]*admissionObservedContext, 7)
	results := make(chan outcome, len(contexts))
	start := make(chan struct{})
	for i := range contexts {
		contexts[i] = newAdmissionObservedContext(context.Background())
		go func(ctx *admissionObservedContext) {
			<-start
			result, err := f.server.CommitCertified(ctx, request)
			results <- outcome{result: result, err: err}
		}(contexts[i])
	}
	close(start)
	for range contexts {
		awaitAdmissionEvent(t, db.initialEntered, "initial false canonical probe")
	}
	if got := db.initialFalse.Load(); got != int64(len(contexts)) {
		t.Fatalf("initial hints were not all false: %d/%d", got, len(contexts))
	}
	db.releaseInitial()
	awaitAdmissionEvent(t, db.writerEntered, "first exclusive writer")
	// The first writer is stopped in its database mutation. Each caller has
	// already completed its initial false hint and entered writer admission.
	for _, ctx := range contexts {
		awaitAdmissionEvent(t, ctx.admissionTry, "writer admission select")
	}
	db.releaseReaders()
	db.releaseWriter()
	awaitFreshWriteJoins(t, f.server, 6)
	if got := db.physicalReads.Load(); got != 0 {
		t.Fatalf("same-QC fresh commit followers entered duplicate reads: %d", got)
	}
	db.releaseReaders()
	for range contexts {
		result := <-results
		if result.err != nil || !result.result.Committed || result.result.HeadHash != b.Hash() || result.result.HeadNumber != 1 {
			t.Fatalf("concurrent commit result: %+v, %v", result.result, result.err)
		}
	}
	flights, joins := f.server.CertifiedFreshCommitCounts()
	if flights != 1 || joins != 6 {
		t.Fatalf("actual fresh commit flight counts = %d creators / %d joins, want 1 / 6", flights, joins)
	}
	after, err := f.server.StrictIngressStatus()
	if err != nil || after.CanonicalGeneration != before.CanonicalGeneration+1 || after.Reconciling {
		t.Fatalf("canonical writer did not reconcile exactly once: %+v, %v", after, err)
	}

	forged := request
	forged.Certificate = request.Certificate.Clone()
	forged.Certificate.AggregateSignature[0] ^= 1
	if _, err := f.server.CommitCertified(context.Background(), forged); err == nil {
		t.Fatal("canonical read-flight accepted a forged aggregate signature")
	}
}

func TestCertifiedWriterAdmissionDoesNotMergeDifferentQCs(t *testing.T) {
	f, b, request, db := newAdmissionFixture(t, true)
	db.initialLimit = 0
	first := certifiedRequestAtView(f, b, request, hotstuff.ViewNumber(6))
	second := certifiedRequestAtView(f, b, request, hotstuff.ViewNumber(7))
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, candidate := range []CertifiedCommitRequest{first, second} {
		go func(candidate CertifiedCommitRequest) {
			<-start
			_, err := f.server.CommitCertified(context.Background(), candidate)
			results <- err
		}(candidate)
	}
	close(start)
	awaitAdmissionEvent(t, db.readerEntered, "first QC physical read")
	awaitAdmissionEvent(t, db.readerEntered, "second QC physical read")
	if got := db.physicalReads.Load(); got != 2 {
		t.Fatalf("distinct QCs were incorrectly coalesced: %d physical reads", got)
	}
	flights, joins := f.server.CertifiedDuplicateReadCounts()
	if flights != 2 || joins != 0 {
		t.Fatalf("distinct-QC counters = %d flights / %d joins, want 2 / 0", flights, joins)
	}
	db.releaseReaders()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestCertifiedWriterAdmissionWaitHonorsRequesterAndServerCancellation(t *testing.T) {
	for _, mode := range []string{"requester", "server-close"} {
		t.Run(mode, func(t *testing.T) {
			f, b, request, db := newAdmissionFixture(t, false)
			db.initialLimit = 1
			db.holdFreshProbe.Store(true)
			first := make(chan error, 1)
			go func() {
				_, err := f.server.CommitCertified(context.Background(), request)
				first <- err
			}()
			awaitAdmissionEvent(t, db.initialEntered, "holder's initial false hint")
			db.releaseInitial()
			awaitAdmissionEvent(t, db.freshEntered, "holder's fresh canonical probe")

			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			waiter := newAdmissionObservedContext(parent)
			waiterRequest := certifiedRequestAtView(f, b, request, hotstuff.ViewNumber(6))
			second := make(chan error, 1)
			go func() {
				_, err := f.server.CommitCertified(waiter, waiterRequest)
				second <- err
			}()
			awaitAdmissionEvent(t, waiter.admissionTry, "different-QC writer admission select")
			var closeDone <-chan struct{}
			if mode == "requester" {
				cancel()
			} else {
				closed := make(chan struct{})
				closeDone = closed
				go func() { f.server.Close(); close(closed) }()
				awaitAdmissionEvent(t, f.server.ctx.Done(), "server close cancellation")
			}
			select {
			case err := <-second:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("admission waiter cancellation = %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("admission waiter did not leave after cancellation")
			}
			db.releaseFresh()
			select {
			case err := <-first:
				if mode == "requester" && err != nil {
					t.Fatalf("writer failed after unrelated waiter cancellation: %v", err)
				}
				if mode == "server-close" && err == nil {
					t.Fatal("active writer ignored server Close cancellation")
				}
			case <-time.After(10 * time.Second):
				t.Fatal("admission holder did not finish")
			}
			if got := len(f.server.certifiedWriteGate); got != 0 {
				t.Fatalf("writer-admission token leaked after cancellation: %d", got)
			}
			if closeDone != nil {
				awaitAdmissionEvent(t, closeDone, "server Close completion")
			}
		})
	}
}
