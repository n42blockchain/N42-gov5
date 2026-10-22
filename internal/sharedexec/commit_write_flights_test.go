package sharedexec

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

func awaitFreshWriteJoins(t *testing.T, s *Server, want uint64) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		_, got := s.CertifiedFreshCommitCounts()
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	created, got := s.CertifiedFreshCommitCounts()
	t.Fatalf("fresh commit flight counts = %d created / %d joined, want %d joins", created, got, want)
}

func freshCommitHeadMatches(db kv.RwDB, want types.Hash) bool {
	var head types.Hash
	err := db.View(context.Background(), func(tx kv.Tx) error {
		head = rawdb.ReadHeadBlockHash(tx)
		return nil
	})
	return err == nil && head == want
}

func activeFreshCommitFlight(t *testing.T, s *Server, request CertifiedCommitRequest) (certifiedReadKey, *certifiedWriteJob) {
	t.Helper()
	key, ok := certifiedDuplicateKey(request.CommitRequest, &request.Certificate)
	if !ok {
		t.Fatal("request did not produce a fresh-commit flight key")
	}
	s.mu.Lock()
	job := s.certifiedWrites[key]
	s.mu.Unlock()
	if job == nil {
		t.Fatal("expected active fresh-commit flight")
	}
	return key, job
}

func assertFreshCommitFailedAndRetired(t *testing.T, s *Server, key certifiedReadKey, job *certifiedWriteJob) {
	t.Helper()
	select {
	case <-job.done:
	default:
		t.Fatal("fresh-commit flight was not completed")
	}
	if job.success {
		t.Fatal("failed or canceled creator published a reusable success")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.certifiedWrites[key] != nil {
		t.Fatal("completed fresh-commit flight remained in the map")
	}
}

func TestCertifiedFreshCommitSharesOnlyIdenticalInFlightQC(t *testing.T) {
	f, b, request, db := newAdmissionFixture(t, false)
	db.holdWriter.Store(true)

	type outcome struct {
		request CertifiedCommitRequest
		result  CommitResult
		err     error
	}
	results := make(chan outcome, 8)
	start := func(candidate CertifiedCommitRequest) {
		go func() {
			result, err := f.server.CommitCertified(context.Background(), candidate)
			results <- outcome{request: candidate, result: result, err: err}
		}()
	}

	start(request)
	awaitAdmissionEvent(t, db.writerEntered, "fresh commit database write")
	for range 6 {
		start(request)
	}
	differentQC := certifiedRequestAtView(f, b, request, hotstuff.ViewNumber(2))
	start(differentQC)
	awaitFreshWriteJoins(t, f.server, 6)
	created, joined := f.server.CertifiedFreshCommitCounts()
	if created != 1 || joined != 6 {
		t.Fatalf("identical-QC flight counts = %d created / %d joined, want 1 / 6", created, joined)
	}
	select {
	case result := <-results:
		t.Fatalf("caller returned before creator publication: %+v %v", result.result, result.err)
	default:
	}

	db.releaseReaders()
	db.releaseWriter()
	for range 8 {
		result := <-results
		if result.err != nil || !result.result.Committed || result.result.HeadHash != b.Hash() || result.result.HeadNumber != 1 {
			t.Fatalf("fresh commit result for view %d: %+v %v", result.request.Certificate.View, result.result, result.err)
		}
	}
	created, joined = f.server.CertifiedFreshCommitCounts()
	if created != 1 || joined != 6 {
		t.Fatalf("completed fresh commit counts changed: %d created / %d joined", created, joined)
	}
	if got := db.physicalReads.Load(); got != 1 {
		t.Fatalf("different QC did not get an independent physical canonical proof: %d receipt reads", got)
	}
	readFlights, readJoins := f.server.CertifiedDuplicateReadCounts()
	if readFlights != 1 || readJoins != 0 {
		t.Fatalf("different-QC read counts = %d flights / %d joins, want 1 / 0", readFlights, readJoins)
	}
}

func TestCertifiedFreshCommitFlightIsDeletedAndLaterRequestRechecksStorage(t *testing.T) {
	f, b, request, db := newAdmissionFixture(t, false)
	result, err := f.server.CommitCertified(context.Background(), request)
	if err != nil || !result.Committed || result.HeadHash != b.Hash() {
		t.Fatalf("initial fresh commit: %+v %v", result, err)
	}
	if created, joined := f.server.CertifiedFreshCommitCounts(); created != 1 || joined != 0 {
		t.Fatalf("initial flight counts = %d created / %d joined", created, joined)
	}

	if err := f.chain.DB().Update(context.Background(), func(tx kv.RwTx) error {
		return tx.Delete(modules.Receipts, modules.EncodeBlockNumber(request.Number))
	}); err != nil {
		t.Fatal(err)
	}
	db.releaseReaders()
	if _, err := f.server.CommitCertified(context.Background(), request); err == nil {
		t.Fatal("later request reused a completed fresh-commit observation after receipt deletion")
	}
	if got := db.physicalReads.Load(); got != 1 {
		t.Fatalf("later request skipped a fresh physical receipt check: %d reads", got)
	}
	if created, joined := f.server.CertifiedFreshCommitCounts(); created != 1 || joined != 0 {
		t.Fatalf("completed fresh-commit observation was retained: %d created / %d joined", created, joined)
	}
}

func TestCertifiedFreshCommitCapacityMissUsesUnsharedExclusivePath(t *testing.T) {
	f, b, request := certifiedCanonicalFixture(t, false)
	f.server.mu.Lock()
	f.server.capacity = 0
	f.server.mu.Unlock()
	result, err := f.server.CommitCertified(context.Background(), request)
	if err != nil || !result.Committed || result.HeadHash != b.Hash() || result.HeadNumber != 1 {
		t.Fatalf("capacity-miss exclusive commit: %+v %v", result, err)
	}
	if created, joined := f.server.CertifiedFreshCommitCounts(); created != 0 || joined != 0 {
		t.Fatalf("capacity miss created/reused fresh flight: %d created / %d joined", created, joined)
	}
}

type freshCommitFaultDB struct {
	kv.RwDB
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	first   atomic.Bool
	err     error
}

func newFreshCommitFaultDB(db kv.RwDB, err error) *freshCommitFaultDB {
	return &freshCommitFaultDB{RwDB: db, entered: make(chan struct{}), release: make(chan struct{}), err: err}
}

func (d *freshCommitFaultDB) failFirstUpdate() error {
	if !d.armed.Load() || !d.first.CompareAndSwap(false, true) {
		return nil
	}
	close(d.entered)
	<-d.release
	return d.err
}

func (d *freshCommitFaultDB) Update(ctx context.Context, fn func(kv.RwTx) error) error {
	if err := d.failFirstUpdate(); err != nil {
		return err
	}
	return d.RwDB.Update(ctx, fn)
}

func (d *freshCommitFaultDB) UpdateNosync(ctx context.Context, fn func(kv.RwTx) error) error {
	if err := d.failFirstUpdate(); err != nil {
		return err
	}
	return d.RwDB.UpdateNosync(ctx, fn)
}

func TestCertifiedFreshCommitFailureRetiresFlightAndWaiterRunsExclusiveFallback(t *testing.T) {
	sentinel := errors.New("first canonical commit write failed")
	var db *freshCommitFaultDB
	f, b, request := certifiedCanonicalFixtureWithDB(t, false, func(base kv.RwDB) kv.RwDB {
		db = newFreshCommitFaultDB(base, sentinel)
		return db
	})
	db.armed.Store(true)
	defer func() {
		select {
		case <-db.release:
		default:
			close(db.release)
		}
	}()

	creator := make(chan error, 1)
	go func() {
		_, err := f.server.CommitCertified(context.Background(), request)
		creator <- err
	}()
	awaitAdmissionEvent(t, db.entered, "fresh commit failing database write")
	waiter := make(chan error, 1)
	go func() {
		result, err := f.server.CommitCertified(context.Background(), request)
		if err == nil && (!result.Committed || result.HeadHash != b.Hash() || result.HeadNumber != 1) {
			err = errors.New("exclusive fallback returned the wrong canonical head")
		}
		waiter <- err
	}()
	awaitFreshWriteJoins(t, f.server, 1)
	db.release <- struct{}{}

	if err := <-creator; !errors.Is(err, sentinel) {
		t.Fatalf("creator error = %v, want injected database error", err)
	}
	if err := <-waiter; err != nil {
		t.Fatalf("waiter did not perform the original exclusive fresh commit fallback: %v", err)
	}
	if created, joined := f.server.CertifiedFreshCommitCounts(); created != 1 || joined != 1 {
		t.Fatalf("failed cohort counts = %d created / %d joined", created, joined)
	}
	status, err := f.server.StrictIngressStatus()
	if err != nil || status.CanonicalGeneration < 1 || status.Reconciling {
		t.Fatalf("fallback failed to reconcile canonical ingress: %+v %v", status, err)
	}
}

func TestCertifiedFreshCommitCanceledWaiterDoesNotCancelCreator(t *testing.T) {
	f, _, request, db := newAdmissionFixture(t, false)
	db.holdWriter.Store(true)
	creator := make(chan error, 1)
	go func() {
		_, err := f.server.CommitCertified(context.Background(), request)
		creator <- err
	}()
	awaitAdmissionEvent(t, db.writerEntered, "fresh commit database write")

	waiterCtx, cancelWaiter := context.WithCancel(context.Background())
	defer cancelWaiter()
	waiter := make(chan error, 1)
	go func() {
		_, err := f.server.CommitCertified(waiterCtx, request)
		waiter <- err
	}()
	awaitFreshWriteJoins(t, f.server, 1)
	cancelWaiter()
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled waiter returned %v", err)
	}
	db.releaseWriter()
	if err := <-creator; err != nil {
		t.Fatalf("waiter cancellation canceled the creator: %v", err)
	}
	if created, joined := f.server.CertifiedFreshCommitCounts(); created != 1 || joined != 1 {
		t.Fatalf("canceled waiter cohort counts = %d created / %d joined", created, joined)
	}
}

func TestCertifiedFreshCommitCloseCancelsCreatorAndJoinedWaiter(t *testing.T) {
	f, _, request, db := newAdmissionFixture(t, false)
	db.holdWriter.Store(true)
	creator := make(chan error, 1)
	go func() {
		_, err := f.server.CommitCertified(context.Background(), request)
		creator <- err
	}()
	awaitAdmissionEvent(t, db.writerEntered, "fresh commit database write")
	waiter := make(chan error, 1)
	go func() {
		_, err := f.server.CommitCertified(context.Background(), request)
		waiter <- err
	}()
	awaitFreshWriteJoins(t, f.server, 1)
	closed := make(chan struct{})
	go func() {
		f.server.Close()
		close(closed)
	}()
	awaitAdmissionEvent(t, f.server.ctx.Done(), "server close cancellation")
	db.releaseWriter()
	awaitAdmissionEvent(t, closed, "server close joining active creator")
	if err := <-creator; !errors.Is(err, context.Canceled) {
		t.Fatalf("creator returned %v after Close, want cancellation", err)
	}
	if err := <-waiter; !errors.Is(err, context.Canceled) {
		t.Fatalf("joined waiter returned %v after Close, want cancellation", err)
	}
	if created, joined := f.server.CertifiedFreshCommitCounts(); created != 1 || joined != 1 {
		t.Fatalf("closed cohort counts = %d created / %d joined", created, joined)
	}
}

type freshCommitDurabilityGateDB struct {
	kv.RwDB
	server  *Server
	target  types.Hash
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	gated   atomic.Bool
}

type countedDBViewContext struct {
	context.Context
	views atomic.Int64
}

func (d *freshCommitDurabilityGateDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	if observed, ok := ctx.(*countedDBViewContext); ok {
		observed.views.Add(1)
	}
	if d.armed.Load() && d.server != nil && d.server.ingress != nil {
		q := d.server.ingress
		q.mu.Lock()
		reconciling := q.reconciling
		q.mu.Unlock()
		if reconciling && freshCommitHeadMatches(d.RwDB, d.target) && d.gated.CompareAndSwap(false, true) {
			close(d.entered)
			<-d.release
		}
	}
	return d.RwDB.View(ctx, fn)
}

func TestCertifiedFreshCommitCanceledAfterDurableMutationFallsBack(t *testing.T) {
	var db *freshCommitDurabilityGateDB
	f, b, request := certifiedCanonicalFixtureWithDB(t, false, func(base kv.RwDB) kv.RwDB {
		db = &freshCommitDurabilityGateDB{RwDB: base, entered: make(chan struct{}), release: make(chan struct{})}
		return db
	})
	db.server = f.server
	db.target = request.BlockHash
	db.armed.Store(true)
	defer func() {
		select {
		case <-db.release:
		default:
			close(db.release)
		}
	}()

	creatorCtx, cancelCreator := context.WithCancel(context.Background())
	defer cancelCreator()
	creator := make(chan error, 1)
	go func() {
		_, err := f.server.CommitCertified(creatorCtx, request)
		creator <- err
	}()
	awaitAdmissionEvent(t, db.entered, "durability gate after canonical mutation")
	if err := db.RwDB.View(context.Background(), func(tx kv.Tx) error {
		got, err := rawdb.ReadCanonicalHash(tx, request.Number)
		if err != nil {
			return err
		}
		if got != request.BlockHash {
			return errors.New("canonical mutation was not durable before cancellation")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	key, job := activeFreshCommitFlight(t, f.server, request)
	waiterCtx := &countedDBViewContext{Context: context.Background()}
	waiter := make(chan error, 1)
	go func() {
		result, err := f.server.CommitCertified(waiterCtx, request)
		if err == nil && (!result.Committed || result.HeadHash != b.Hash() || result.HeadNumber != 1) {
			err = errors.New("post-cancellation fallback returned the wrong canonical head")
		}
		waiter <- err
	}()
	awaitFreshWriteJoins(t, f.server, 1)
	cancelCreator()
	db.release <- struct{}{}
	if err := <-creator; err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled creator returned unexpected error %v", err)
	}
	if err := <-waiter; err != nil {
		t.Fatalf("waiter failed to reclassify durable commit after creator cancellation: %v", err)
	}
	if waiterCtx.views.Load() == 0 {
		t.Fatal("waiter returned without executing the exclusive classification fallback")
	}
	assertFreshCommitFailedAndRetired(t, f.server, key, job)
	if flights, joins := f.server.CertifiedDuplicateReadCounts(); flights != 0 || joins != 0 {
		t.Fatalf("canceled creator entered shared duplicate reads: %d flights / %d joins", flights, joins)
	}
	if created, joined := f.server.CertifiedFreshCommitCounts(); created != 1 || joined != 1 {
		t.Fatalf("canceled creator cohort counts = %d created / %d joined", created, joined)
	}
}

type freshCommitReconcileFaultDB struct {
	kv.RwDB
	server       *Server
	target       types.Hash
	armed        atomic.Bool
	failureSent  atomic.Bool
	entered      chan struct{}
	release      chan struct{}
	reconcileErr error
}

func (d *freshCommitReconcileFaultDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	if d.armed.Load() && d.server != nil && d.server.ingress != nil {
		q := d.server.ingress
		q.mu.Lock()
		reconciling := q.reconciling
		q.mu.Unlock()
		if reconciling && ctx == d.server.ctx && freshCommitHeadMatches(d.RwDB, d.target) && d.failureSent.CompareAndSwap(false, true) {
			close(d.entered)
			<-d.release
			return d.reconcileErr
		}
	}
	return d.RwDB.View(ctx, fn)
}

func TestCertifiedFreshCommitReconcileFailureRetiresFlightForExclusiveFallback(t *testing.T) {
	sentinel := errors.New("first ingress reconciliation read failed")
	var db *freshCommitReconcileFaultDB
	f, b, request := certifiedCanonicalFixtureWithDB(t, false, func(base kv.RwDB) kv.RwDB {
		db = &freshCommitReconcileFaultDB{RwDB: base, reconcileErr: sentinel, entered: make(chan struct{}), release: make(chan struct{})}
		return db
	})
	db.server = f.server
	db.target = request.BlockHash
	db.armed.Store(true)
	creator := make(chan error, 1)
	go func() {
		_, err := f.server.CommitCertified(context.Background(), request)
		creator <- err
	}()
	defer func() {
		select {
		case <-db.release:
		default:
			close(db.release)
		}
	}()
	awaitAdmissionEvent(t, db.entered, "creator reconciliation fault")
	key, job := activeFreshCommitFlight(t, f.server, request)
	waiter := make(chan error, 1)
	go func() {
		result, err := f.server.CommitCertified(context.Background(), request)
		if err == nil && (!result.Committed || result.HeadHash != b.Hash() || result.HeadNumber != 1) {
			err = errors.New("reconciliation fallback returned the wrong canonical head")
		}
		waiter <- err
	}()
	awaitFreshWriteJoins(t, f.server, 1)
	db.release <- struct{}{}
	if err := <-creator; !errors.Is(err, sentinel) {
		t.Fatalf("creator error = %v, want reconciliation failure", err)
	}
	if err := <-waiter; err != nil {
		t.Fatalf("waiter did not perform exclusive fallback after failed reconciliation: %v", err)
	}
	assertFreshCommitFailedAndRetired(t, f.server, key, job)
	if created, joined := f.server.CertifiedFreshCommitCounts(); created != 1 || joined != 1 {
		t.Fatalf("reconciliation failure cohort counts = %d created / %d joined", created, joined)
	}
	status, err := f.server.StrictIngressStatus()
	if err != nil || status.Reconciling {
		t.Fatalf("exclusive retry did not clear failed reconciliation: %+v %v", status, err)
	}
}

type freshCommitPanicDB struct {
	kv.RwDB
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
	first   atomic.Bool
}

func newFreshCommitPanicDB(db kv.RwDB) *freshCommitPanicDB {
	return &freshCommitPanicDB{RwDB: db, entered: make(chan struct{}), release: make(chan struct{})}
}

func (d *freshCommitPanicDB) panicFirstUpdate() {
	if !d.armed.Load() || !d.first.CompareAndSwap(false, true) {
		return
	}
	close(d.entered)
	<-d.release
	panic("injected canonical write panic")
}

func (d *freshCommitPanicDB) Update(ctx context.Context, fn func(kv.RwTx) error) error {
	d.panicFirstUpdate()
	return d.RwDB.Update(ctx, fn)
}

func (d *freshCommitPanicDB) UpdateNosync(ctx context.Context, fn func(kv.RwTx) error) error {
	d.panicFirstUpdate()
	return d.RwDB.UpdateNosync(ctx, fn)
}

func TestCertifiedFreshCommitPanicRetiresFlightForExclusiveFallback(t *testing.T) {
	var db *freshCommitPanicDB
	f, b, request := certifiedCanonicalFixtureWithDB(t, false, func(base kv.RwDB) kv.RwDB {
		db = newFreshCommitPanicDB(base)
		return db
	})
	db.armed.Store(true)
	defer func() {
		select {
		case <-db.release:
		default:
			close(db.release)
		}
	}()

	creator := make(chan bool, 1)
	go func() {
		panicked := false
		defer func() {
			if recover() != nil {
				panicked = true
			}
			creator <- panicked
		}()
		_, _ = f.server.CommitCertified(context.Background(), request)
	}()
	awaitAdmissionEvent(t, db.entered, "panicking fresh commit database write")
	waiter := make(chan error, 1)
	go func() {
		result, err := f.server.CommitCertified(context.Background(), request)
		if err == nil && (!result.Committed || result.HeadHash != b.Hash() || result.HeadNumber != 1) {
			err = errors.New("panic fallback returned the wrong canonical head")
		}
		waiter <- err
	}()
	awaitFreshWriteJoins(t, f.server, 1)
	db.release <- struct{}{}
	if panicked := <-creator; !panicked {
		t.Fatal("injected creator panic did not reach the caller")
	}
	if err := <-waiter; err != nil {
		t.Fatalf("waiter did not run exclusive fallback after creator panic: %v", err)
	}
	if created, joined := f.server.CertifiedFreshCommitCounts(); created != 1 || joined != 1 {
		t.Fatalf("panic cohort counts = %d created / %d joined", created, joined)
	}
}
