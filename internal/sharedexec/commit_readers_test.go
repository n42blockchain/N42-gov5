package sharedexec

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

// The gates stop actual database reads/writes; no production callback is used.
type certifiedReaderGateDB struct {
	kv.RwDB
	hash              types.Hash
	owner             *Server
	receiptKey        []byte
	writerEntered     chan struct{}
	writerRelease     chan struct{}
	readerEntered     chan struct{}
	readerRelease     chan struct{}
	viewDone          chan struct{}
	writerOnce        sync.Once
	writerOpen        sync.Once
	readerOpen        sync.Once
	holdWriter        bool
	holdInitialProbes bool
	probeEntered      chan struct{}
	probeRelease      chan struct{}
	probeOpen         sync.Once
	initialProbes     atomic.Int64
	active            atomic.Int64
	maximum           atomic.Int64
	reads             atomic.Int64
	writes            atomic.Int64
}

type certifiedReaderGateTx struct {
	kv.Tx
	db            *certifiedReaderGateDB
	readCanonical bool
}

func (d *certifiedReaderGateDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	err := d.RwDB.View(ctx, func(tx kv.Tx) error {
		wrapped := &certifiedReaderGateTx{Tx: tx, db: d}
		err := fn(wrapped)
		q := d.owner.ingress
		q.mu.Lock()
		classified := wrapped.readCanonical && !q.reconciling && q.stamp.hash == d.hash
		q.mu.Unlock()
		if classified {
			select {
			case d.viewDone <- struct{}{}:
			default:
			}
		}
		return err
	})
	return err
}

func (t *certifiedReaderGateTx) GetOne(table string, key []byte) ([]byte, error) {
	if table == modules.HeaderCanonical && bytes.Equal(key, t.db.receiptKey) {
		t.readCanonical = true
		if t.db.holdInitialProbes {
			data, err := t.Tx.GetOne(table, key)
			if len(data) == 0 && t.db.initialProbes.Add(1) <= 7 {
				t.db.probeEntered <- struct{}{}
				<-t.db.probeRelease
			}
			return data, err
		}
	}
	if table == modules.Receipts && bytes.Equal(key, t.db.receiptKey) && rawdb.ReadHeadBlockHash(t.Tx) == t.db.hash {
		d := t.db
		active := d.active.Add(1)
		defer d.active.Add(-1)
		for maximum := d.maximum.Load(); active > maximum; maximum = d.maximum.Load() {
			if d.maximum.CompareAndSwap(maximum, active) {
				break
			}
		}
		d.reads.Add(1)
		d.readerEntered <- struct{}{}
		<-d.readerRelease
	}
	return t.Tx.GetOne(table, key)
}

func (d *certifiedReaderGateDB) Update(ctx context.Context, fn func(kv.RwTx) error) error {
	d.writes.Add(1)
	if d.holdWriter {
		d.writerOnce.Do(func() {
			close(d.writerEntered)
			<-d.writerRelease
		})
	}
	return d.RwDB.Update(ctx, fn)
}

func readerGate(t *testing.T, f strictIngressFixture, b *block.Block, holdWriter bool) *certifiedReaderGateDB {
	t.Helper()
	original := f.chain.ChainDB
	d := &certifiedReaderGateDB{RwDB: original, hash: b.Hash(), owner: f.server, receiptKey: modules.EncodeBlockNumber(b.Number64().Uint64()), holdWriter: holdWriter,
		writerEntered: make(chan struct{}), writerRelease: make(chan struct{}), probeEntered: make(chan struct{}, 7), probeRelease: make(chan struct{}), readerEntered: make(chan struct{}, 32), readerRelease: make(chan struct{}), viewDone: make(chan struct{}, 128)}
	f.chain.ChainDB = d
	t.Cleanup(func() { d.releaseProbes(); d.releaseWriter(); d.releaseReaders(); f.chain.ChainDB = original })
	return d
}
func (d *certifiedReaderGateDB) releaseProbes()  { d.probeOpen.Do(func() { close(d.probeRelease) }) }
func (d *certifiedReaderGateDB) releaseWriter()  { d.writerOpen.Do(func() { close(d.writerRelease) }) }
func (d *certifiedReaderGateDB) releaseReaders() { d.readerOpen.Do(func() { close(d.readerRelease) }) }
func awaitCertifiedEvent(t *testing.T, channel <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(10 * time.Second):
		t.Fatal("missing " + name)
	}
}

func TestCertifiedDuplicateReadersQueuedAfterAdvanceOverlapAndStayBounded(t *testing.T) {
	f, b, request := certifiedCanonicalFixture(t, false)
	d := readerGate(t, f, b, true)
	d.holdInitialProbes = true
	var wg sync.WaitGroup
	t.Cleanup(func() { d.releaseProbes(); d.releaseWriter(); d.releaseReaders(); f.server.Close(); wg.Wait() })
	// Queue all sibling routing/classification before opening two physical readers.
	f.server.certifiedReadSlots <- struct{}{}
	f.server.certifiedReadSlots <- struct{}{}
	results := make(chan error, 7)
	commit := func() {
		defer wg.Done()
		result, err := f.server.CommitCertified(context.Background(), request)
		if err == nil && (!result.Committed || result.HeadHash != b.Hash() || result.HeadNumber != 1) {
			err = errors.New("certified result differs")
		}
		results <- err
	}
	for range 7 {
		wg.Add(1)
		go commit()
	}
	for range 7 {
		awaitCertifiedEvent(t, d.probeEntered, "initial noncanonical routing probe")
	}
	d.releaseProbes()
	awaitCertifiedEvent(t, d.writerEntered, "first canonical writer")
	d.releaseWriter()
	for range 6 {
		awaitCertifiedEvent(t, d.viewDone, "post-advance duplicate routing/classification")
	}
	<-f.server.certifiedReadSlots
	<-f.server.certifiedReadSlots
	awaitCertifiedEvent(t, d.readerEntered, "first duplicate physical read")
	awaitCertifiedEvent(t, d.readerEntered, "overlapping duplicate physical read")
	if d.active.Load() != 2 || d.maximum.Load() != 2 || cap(f.server.certifiedReadSlots) != 2 {
		t.Fatal("duplicate physical checks did not overlap within the fixed bound")
	}
	d.releaseReaders()
	wg.Wait()
	for range 7 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	status, _ := f.server.StrictIngressStatus()
	if d.maximum.Load() != 2 || d.reads.Load() != 6 || status.CanonicalGeneration != 1 || status.Reconciling || len(f.server.certifiedReadSlots) != 0 {
		t.Fatalf("duplicate bound/count/generation changed: max=%d reads=%d status=%+v", d.maximum.Load(), d.reads.Load(), status)
	}
}

func TestCertifiedDuplicateReadSlotsCancellationAndCloseDrainWaiters(t *testing.T) {
	f, b, request := certifiedCanonicalFixture(t, true)
	d := readerGate(t, f, b, false)
	before, _ := f.server.StrictIngressStatus()
	// Occupy both physical-decode reservations, so public requests route
	// under shared ownership and then wait without retaining either lock.
	f.server.certifiedReadSlots <- struct{}{}
	f.server.certifiedReadSlots <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := make(chan error, 3)
	var wg sync.WaitGroup
	t.Cleanup(func() { cancel(); d.releaseReaders(); f.server.Close(); wg.Wait() })
	for i := range 3 {
		wg.Add(1)
		requestCtx := context.Background()
		if i == 0 {
			requestCtx = ctx
		}
		go func() { defer wg.Done(); _, err := f.server.CommitCertified(requestCtx, request); results <- err }()
	}
	for range 3 {
		awaitCertifiedEvent(t, d.viewDone, "public request routing/classification")
	}
	unlocked := make(chan struct{})
	go func() { f.server.execute.Lock(); f.server.execute.Unlock(); close(unlocked) }()
	awaitCertifiedEvent(t, unlocked, "exclusive lock available while slots are saturated")
	cancel()
	select {
	case err := <-results:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request cancellation lost: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("slot wait ignored requester cancellation")
	}
	after, _ := f.server.StrictIngressStatus()
	if !reflect.DeepEqual(before, after) || d.reads.Load() != 0 || d.writes.Load() != 0 {
		t.Fatal("canceled reservation changed queue or touched physical execution")
	}
	closed := make(chan struct{})
	go func() { f.server.Close(); close(closed) }()
	awaitCertifiedEvent(t, closed, "Close cancels and joins slot waiters")
	wg.Wait()
	for range 2 {
		if err := <-results; !errors.Is(err, context.Canceled) {
			t.Fatalf("owner cancellation lost: %v", err)
		}
	}
	if len(f.server.certifiedReadSlots) != 2 {
		t.Fatal("waiters consumed another owner's synthetic slots")
	}
	<-f.server.certifiedReadSlots
	<-f.server.certifiedReadSlots
	if d.active.Load() != 0 {
		t.Fatal("read worker outlived Close")
	}
}

// Hold reservations until both public requests have completed their routing and
// classification, then allow their fresh physical read views to overlap.
func holdTwoCertifiedReaders(t *testing.T, f strictIngressFixture, d *certifiedReaderGateDB, request CertifiedCommitRequest) (<-chan error, *sync.WaitGroup) {
	t.Helper()
	f.server.certifiedReadSlots <- struct{}{}
	f.server.certifiedReadSlots <- struct{}{}
	results := make(chan error, 2)
	wg := new(sync.WaitGroup)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.server.CommitCertified(context.Background(), request)
			if err == nil && !result.Committed {
				err = errors.New("read duplicate not committed")
			}
			results <- err
		}()
	}
	t.Cleanup(func() { d.releaseReaders(); f.server.Close(); wg.Wait() })
	for range 2 {
		awaitCertifiedEvent(t, d.viewDone, "duplicate routing/classification")
	}
	<-f.server.certifiedReadSlots
	<-f.server.certifiedReadSlots
	for range 2 {
		awaitCertifiedEvent(t, d.readerEntered, "held duplicate physical reader")
	}
	return results, wg
}

func TestCertifiedDuplicateReadersExcludeCanonicalWriter(t *testing.T) {
	f, first, request := certifiedCanonicalFixture(t, true)
	status, _ := f.server.StrictIngressStatus()
	if _, err := f.server.AdmitStrictFrame(context.Background(), f.server.Identity(), status.Epoch, f.frames[1]); err != nil {
		t.Fatal(err)
	}
	client := NewClient(startExecutionSocket(t, f.server), f.server.Identity())
	t.Cleanup(client.Close)
	leader, header := queueLeader(t, f, first, 2)
	built, err := client.BuildFromQueue(context.Background(), queueRequest(t, f, header, 6))
	if err != nil {
		t.Fatal(err)
	}
	second := queueSealImport(t, f, client, leader, built)
	next := CertifiedCommitRequest{CommitRequest: CommitRequest{Identity: f.server.Identity(), BlockHash: second.Hash(), ParentHash: first.Hash(), Number: 2}, Certificate: *queueQC(f.keys, second.Hash(), 2, 5)}
	d := readerGate(t, f, first, false)
	results, wg := holdTwoCertifiedReaders(t, f, d, request)
	writerStarted := make(chan struct{})
	writerFinished := make(chan error, 1)
	var writerWG sync.WaitGroup
	writerWG.Add(1)
	t.Cleanup(func() { d.releaseReaders(); f.server.Close(); writerWG.Wait() })
	go func() {
		defer writerWG.Done()
		close(writerStarted)
		_, err := f.server.CommitCertified(context.Background(), next)
		writerFinished <- err
	}()
	awaitCertifiedEvent(t, writerStarted, "next canonical writer call")
	if f.server.execute.TryLock() {
		f.server.execute.Unlock()
		t.Fatal("exclusive owner writer overlapped certified read views")
	}
	if d.writes.Load() != 0 {
		t.Fatal("canonical mutation began while readers were held")
	}
	d.releaseReaders()
	wg.Wait()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	select {
	case err := <-writerFinished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("writer did not continue after read views released")
	}
	status, _ = f.server.StrictIngressStatus()
	if d.writes.Load() == 0 || status.CanonicalHash != second.Hash() || status.CanonicalGeneration != 2 || status.PendingTransactions != 0 || status.Reconciling {
		t.Fatalf("canonical writer failed to reconcile: %+v", status)
	}
}

func TestCertifiedDuplicateReadersCloseWaitsForPhysicalViews(t *testing.T) {
	f, b, request := certifiedCanonicalFixture(t, true)
	d := readerGate(t, f, b, false)
	results, wg := holdTwoCertifiedReaders(t, f, d, request)
	closed := make(chan struct{})
	go func() { f.server.Close(); close(closed) }()
	awaitCertifiedEvent(t, f.server.ctx.Done(), "owner lifetime cancellation")
	select {
	case <-closed:
		t.Fatal("Close returned before actual physical readers completed")
	default:
	}
	q := f.server.ingress
	q.mu.Lock()
	premature := q.closed
	q.mu.Unlock()
	if premature || d.active.Load() != 2 {
		t.Fatal("active reader state was cleaned prematurely")
	}
	d.releaseReaders()
	awaitCertifiedEvent(t, closed, "Close joins active physical read views")
	wg.Wait()
	for range 2 {
		if err := <-results; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if d.active.Load() != 0 || len(f.server.certifiedReadSlots) != 0 {
		t.Fatal("physical reader or reservation outlived Close")
	}
}

func TestCertifiedDuplicateReadersIndependentlyVerifyPublicQC(t *testing.T) {
	f, b, request := certifiedCanonicalFixture(t, true)
	before, _ := f.server.StrictIngressStatus()
	db := countedCertifiedReads(t, f, b)
	four := request
	four.Certificate = *commitProof(f.keys, b.Hash(), 4, false)
	prepare := request
	prepare.Certificate = *commitProof(f.keys, b.Hash(), 5, true)
	view := request
	view.Certificate.View++
	parent := request
	parent.ParentHash[0] ^= 1
	hash := request
	hash.BlockHash[0] ^= 1
	cases := []struct {
		request CertifiedCommitRequest
		valid   bool
	}{{request, true}, {four, false}, {prepare, false}, {view, false}, {parent, false}, {hash, false}, {request, true}}
	var wg sync.WaitGroup
	results := make(chan error, len(cases))
	for _, item := range cases {
		wg.Add(1)
		go func(item struct {
			request CertifiedCommitRequest
			valid   bool
		}) {
			defer wg.Done()
			result, err := f.server.CommitCertified(context.Background(), item.request)
			if (err == nil) != item.valid || item.valid && !result.Committed {
				results <- errors.New("parallel public QC authorization differed")
			} else {
				results <- nil
			}
		}(item)
	}
	wg.Wait()
	for range cases {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	// Two valid requests and the valid QC with a wrong request parent each
	// undergo fresh physical verification; the latter fails scalar binding.
	after, _ := f.server.StrictIngressStatus()
	if !reflect.DeepEqual(before, after) || db.writes.Load() != 0 || db.bodies.Load() != 3 || db.receipts.Load() != 3 || db.senders.Load() != 3 || db.lookups.Load() != 3*int64(len(b.Transactions())) || len(f.server.certifiedReadSlots) != 0 {
		t.Fatal("public requests shared authorization/evidence or changed queue state")
	}
}

func TestCertifiedDuplicateReaderBoundRespectsServerCapacity(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	for _, capacity := range []int{1, 2, 8, 256} {
		s, err := New(f.chain, capacity)
		if err != nil {
			t.Fatal(err)
		}
		if cap(s.certifiedReadSlots) != min(capacity, 2) {
			t.Fatal("physical-read reservation bound differs from server capacity")
		}
		s.Close()
	}
}

func TestCertifiedDuplicateReadersStaggeredRequestsJoinActiveView(t *testing.T) {
	f, b, request := certifiedCanonicalFixture(t, true)
	before, _ := f.server.StrictIngressStatus()
	d := readerGate(t, f, b, false)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	t.Cleanup(func() { d.releaseReaders(); f.server.Close(); wg.Wait() })
	start := func() {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := f.server.CommitCertified(context.Background(), request)
			if err == nil && !result.Committed {
				err = errors.New("duplicate not committed")
			}
			results <- err
		}()
	}
	start()
	awaitCertifiedEvent(t, d.readerEntered, "already active first physical read")
	// Launch only after the first reader holds both shared locks. An initial
	// exclusive classification would prevent this second read from arriving.
	start()
	awaitCertifiedEvent(t, d.readerEntered, "staggered second physical read")
	if d.active.Load() != 2 || d.maximum.Load() != 2 || d.writes.Load() != 0 {
		t.Fatal("staggered duplicate could not join the active read view")
	}
	for len(d.viewDone) > 0 {
		<-d.viewDone
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	third := make(chan error, 1)
	wg.Add(1)
	go func() { defer wg.Done(); _, err := f.server.CommitCertified(ctx, request); third <- err }()
	awaitCertifiedEvent(t, d.viewDone, "third request routed while two physical views are held")
	cancel()
	select {
	case err := <-third:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("third reservation ignored cancellation: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("third request retained an exclusive lock or ignored its full read-slot bound")
	}
	if d.reads.Load() != 2 || d.active.Load() != 2 || len(f.server.certifiedReadSlots) != 2 {
		t.Fatal("third request entered physical decoding past the two-reader bound")
	}
	d.releaseReaders()
	wg.Wait()
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	after, _ := f.server.StrictIngressStatus()
	if !reflect.DeepEqual(before, after) || d.reads.Load() != 2 || len(f.server.certifiedReadSlots) != 0 {
		t.Fatal("concurrent certified reads changed admission state or retained a slot")
	}
}

type certifiedFallbackFaultDB struct {
	*certifiedReadOnlyDB
	receipts atomic.Int64
	lookups  atomic.Int64
	failure  error
}
type certifiedFallbackFaultTx struct {
	kv.Tx
	db *certifiedFallbackFaultDB
}

func (d *certifiedFallbackFaultDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	return d.RwDB.View(ctx, func(tx kv.Tx) error { return fn(&certifiedFallbackFaultTx{Tx: tx, db: d}) })
}
func (t *certifiedFallbackFaultTx) GetOne(table string, key []byte) ([]byte, error) {
	if table == modules.Receipts && t.db.receipts.Add(1) == 2 {
		return nil, t.db.failure
	}
	if table == modules.TxLookup && t.db.lookups.Add(1) == 1 {
		return nil, nil // first full shared proof requires legacy repair
	}
	return t.Tx.GetOne(table, key)
}

func TestCertifiedDuplicateReadersFallbackFreshlyRevalidatesBeforeRepair(t *testing.T) {
	f, _, request := certifiedCanonicalFixture(t, true)
	before, _ := f.server.StrictIngressStatus()
	original := f.chain.ChainDB
	sentinel := errors.New("exclusive fallback fresh receipt read failed")
	d := &certifiedFallbackFaultDB{certifiedReadOnlyDB: &certifiedReadOnlyDB{RwDB: original}, failure: sentinel}
	f.chain.ChainDB = d
	t.Cleanup(func() { f.chain.ChainDB = original })
	result, err := f.server.CommitCertified(context.Background(), request)
	if !errors.Is(err, sentinel) || result.Committed || d.receipts.Load() != 2 || d.lookups.Load() != 1 {
		t.Fatalf("ineligible shared proof survived into exclusive repair: %+v %v receipts=%d lookup=%d", result, err, d.receipts.Load(), d.lookups.Load())
	}
	after, _ := f.server.StrictIngressStatus()
	if !reflect.DeepEqual(before, after) || d.writes.Load() != 0 || len(f.server.certifiedReadSlots) != 0 {
		t.Fatal("failed exclusive revalidation wrote or changed queue/reservations")
	}
}
