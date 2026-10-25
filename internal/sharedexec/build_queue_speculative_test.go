package sharedexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

type speculativeOverlapGateDB struct {
	*admissionGateDB
	markerArmed   atomic.Bool
	failWriter    atomic.Bool
	markerHit     atomic.Bool
	markerEntered chan struct{}
	markerRelease chan struct{}
}

func (d *speculativeOverlapGateDB) View(ctx context.Context, fn func(kv.Tx) error) error {
	return d.admissionGateDB.View(ctx, func(tx kv.Tx) error {
		return fn(&speculativeOverlapGateTx{Tx: tx, db: d})
	})
}

func (d *speculativeOverlapGateDB) BeginRo(ctx context.Context) (kv.Tx, error) {
	tx, err := d.admissionGateDB.BeginRo(ctx)
	if err != nil {
		return nil, err
	}
	return &speculativeOverlapGateTx{Tx: tx, db: d}, nil
}

func (d *speculativeOverlapGateDB) Update(ctx context.Context, fn func(kv.RwTx) error) error {
	if d.failWriter.Load() {
		return errors.New("injected speculative parent writer failure")
	}
	return d.admissionGateDB.Update(ctx, fn)
}

func (d *speculativeOverlapGateDB) UpdateNosync(ctx context.Context, fn func(kv.RwTx) error) error {
	if d.failWriter.Load() {
		return errors.New("injected speculative parent writer failure")
	}
	return d.admissionGateDB.UpdateNosync(ctx, fn)
}

type speculativeOverlapGateTx struct {
	kv.Tx
	db *speculativeOverlapGateDB
}

func (tx *speculativeOverlapGateTx) GetOne(table string, key []byte) ([]byte, error) {
	if tx.db.markerArmed.Load() && table == modules.DatabaseInfo && bytes.Equal(key, []byte("qmdbAppliedHead")) {
		if tx.db.markerHit.CompareAndSwap(false, true) {
			close(tx.db.markerEntered)
			<-tx.db.markerRelease
		}
	}
	return tx.Tx.GetOne(table, key)
}

func TestSpeculativeQueueChildEVMOverlapsBlockedCertifiedWriter(t *testing.T) {
	ctx := context.Background()
	var db *speculativeOverlapGateDB
	f := newStrictIngressFixtureWithDB(t, 10, 1<<20, func(base kv.RwDB) kv.RwDB {
		db = &speculativeOverlapGateDB{admissionGateDB: newAdmissionGateDB(base), markerEntered: make(chan struct{}), markerRelease: make(chan struct{})}
		return db
	})
	f.server.speculativeEnabled = true
	t.Cleanup(func() {
		select {
		case <-db.markerRelease:
		default:
			close(db.markerRelease)
		}
		db.releaseWriter()
	})
	status, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
	}
	parent := strictExecute(t, f, [][]byte{strictFrameRaws(t, f.frames[0])[0]})
	before := f.chain.PerformanceExecutionAttempts()
	_, header := queueLeader(t, f, parent, 2)
	request := queueRequest(t, f, header, 6)
	db.markerArmed.Store(true)
	if err := f.server.prepareSpeculativeQueueBuild(request); err != nil {
		t.Fatal(err)
	}
	awaitAdmissionEvent(t, db.markerEntered, "advisory applied-state read")
	db.armed.Store(true)
	db.holdWriter.Store(true)
	commitResult := make(chan error, 1)
	go func() { commitResult <- strictCommit(t, f, parent, ctx) }()
	awaitAdmissionEvent(t, db.writerEntered, "certified canonical writer")
	close(db.markerRelease)
	f.server.specMu.Lock()
	slot := f.server.specSlot
	f.server.specMu.Unlock()
	if slot == nil {
		t.Fatal("certified writer lost exact advisory slot")
	}
	select {
	case <-slot.done:
	case <-time.After(10 * time.Second):
		t.Fatal("child EVM did not finish while certified writer was blocked")
	}
	slot.mu.Lock()
	child, executionErr := slot.child, slot.err
	slot.mu.Unlock()
	if child == nil || executionErr != nil || f.chain.PerformanceExecutionAttempts() != before+1 {
		t.Fatalf("child EVM did not finish during writer hold: child=%v err=%v attempts=%d", child != nil, executionErr, f.chain.PerformanceExecutionAttempts())
	}
	select {
	case err := <-commitResult:
		t.Fatalf("certified commit returned before writer release: %v", err)
	default:
	}
	db.releaseWriter()
	select {
	case err := <-commitResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("certified commit did not finish after writer release")
	}
}

func TestSpeculativeQueueConcurrentRecoveryDrainsJoinFullCleanup(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	f.server.speculativeEnabled = true
	status, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slot := &speculativeQueueSlot{done: make(chan struct{}), cleanup: make(chan struct{}), cancel: cancel, epoch: status.Epoch}
	f.server.specMu.Lock()
	f.server.specSlot = slot
	f.server.specMu.Unlock()
	raw, err := json.Marshal(DrainSpeculativeQueueRequest{Identity: f.server.Identity(), Epoch: status.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	responses := make(chan int, 2)
	for range 2 {
		go func() {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/drain-prepare-queue", bytes.NewReader(raw))
			f.server.serveDrainSpeculativeQueue(recorder, request)
			responses <- recorder.Code
		}()
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("recovery drains did not cancel advisory worker")
	}
	select {
	case code := <-responses:
		t.Fatalf("recovery drain acknowledged before worker exit: %d", code)
	default:
	}
	f.server.specMu.Lock()
	tombstoned := f.server.specRecoveryEpoch == status.Epoch
	f.server.specMu.Unlock()
	if !tombstoned {
		t.Fatal("recovery drain did not seal its owner epoch")
	}
	close(slot.done)
	for range 2 {
		select {
		case code := <-responses:
			if code != http.StatusNoContent {
				t.Fatalf("concurrent bound drain returned %d", code)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent bound drain did not join cleanup")
		}
	}
	if err := f.server.prepareSpeculativeQueueBuild(queueRequest(t, f, f.template.Header().(*block.Header), 6)); err == nil || !strings.Contains(err.Error(), "slot or executor busy") {
		t.Fatalf("late advisory escaped recovery epoch tombstone: %v", err)
	}
}

func TestSpeculativeQueueFailedCertifiedWriterRevokesSlot(t *testing.T) {
	ctx := context.Background()
	var db *speculativeOverlapGateDB
	f := newStrictIngressFixtureWithDB(t, 10, 1<<20, func(base kv.RwDB) kv.RwDB {
		db = &speculativeOverlapGateDB{admissionGateDB: newAdmissionGateDB(base), markerEntered: make(chan struct{}), markerRelease: make(chan struct{})}
		return db
	})
	f.server.speculativeEnabled = true
	status, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
	}
	parent := strictExecute(t, f, [][]byte{strictFrameRaws(t, f.frames[0])[0]})
	_, header := queueLeader(t, f, parent, 2)
	if err := f.server.prepareSpeculativeQueueBuild(queueRequest(t, f, header, 6)); err != nil {
		t.Fatal(err)
	}
	f.server.specMu.Lock()
	slot := f.server.specSlot
	f.server.specMu.Unlock()
	if slot == nil {
		t.Fatal("advisory slot not installed")
	}
	select {
	case <-slot.done:
	case <-time.After(10 * time.Second):
		t.Fatal("advisory worker did not finish before injected failure")
	}
	db.failWriter.Store(true)
	if err := strictCommit(t, f, parent, ctx); err == nil || !strings.Contains(err.Error(), "injected speculative parent writer failure") {
		t.Fatalf("certified writer failure was not observed: %v", err)
	}
	f.server.specMu.Lock()
	retained := f.server.specSlot != nil || f.server.specDraining != nil
	f.server.specMu.Unlock()
	if retained || f.server.specTransition != nil {
		t.Fatal("failed certified writer retained an advisory lease or success transition")
	}
}

func TestSpeculativeQueueExecutesBeforeParentCommitAndReusesAfterQC(t *testing.T) {
	ctx := context.Background()
	f := newStrictIngressFixture(t, 10, 1<<20)
	f.server.speculativeEnabled = true
	status, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
	}
	first := strictFrameRaws(t, f.frames[0])[0]
	parent := strictExecute(t, f, [][]byte{first})
	before := f.chain.PerformanceExecutionAttempts()
	leader, advisoryHeader := queueLeader(t, f, parent, 2)
	advisory := queueRequest(t, f, advisoryHeader, 6)
	if err := f.server.prepareSpeculativeQueueBuild(advisory); err != nil {
		t.Fatal(err)
	}
	f.server.specMu.Lock()
	slot := f.server.specSlot
	f.server.specMu.Unlock()
	if slot == nil {
		t.Fatal("advisory slot not installed")
	}
	select {
	case <-slot.done:
	case <-time.After(10 * time.Second):
		t.Fatal("applied-child EVM did not finish before canonical parent commit")
	}
	slot.mu.Lock()
	child, executionErr := slot.child, slot.err
	slot.mu.Unlock()
	if executionErr != nil || child == nil || f.chain.PerformanceExecutionAttempts() != before+1 {
		t.Fatalf("advisory did not execute one real child before CommitQC: child=%v err=%v attempts=%d", child != nil, executionErr, f.chain.PerformanceExecutionAttempts())
	}
	if err := strictCommit(t, f, parent, ctx); err != nil {
		t.Fatal(err)
	}
	if f.server.specTransition == nil || f.server.specTransition.to.hash != parent.Hash() {
		t.Fatal("certified canonical writer did not publish exact successful transition")
	}
	// Re-prepare the same view with the now-committed QC. Only Extra may
	// change; the actual native request remains fully public-validated.
	qc := queueQC(f.keys, parent.Hash(), 1, 5)
	leader.Engine().RestoreState(2, hotstuff.GenesisQC(), *qc, 0)
	actualHeader := block.CopyHeader(advisoryHeader)
	if err := leader.Prepare(f.chain, actualHeader); err != nil {
		t.Fatal(err)
	}
	actual := queueRequest(t, f, actualHeader, 6)
	if actual.PreparedHash == advisory.PreparedHash {
		t.Fatal("actual request did not change QC Extra")
	}
	built, err := f.server.BuildFromQueue(ctx, actual)
	if err != nil || len(built.BlockRLP) == 0 {
		t.Fatalf("actual queue build failed after certified parent: %v", err)
	}
	if got := f.chain.PerformanceExecutionAttempts(); got != before+1 {
		t.Fatalf("actual queue build reran EVM instead of consuming child: %d -> %d", before+1, got)
	}
	var unsigned block.Block
	if err := unsigned.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	if unsigned.ParentHash() != parent.Hash() || len(unsigned.Transactions()) != 5 {
		t.Fatal("reused child did not bind actual selected queue body")
	}
	for _, tx := range unsigned.Transactions() {
		if tx == nil || tx.Type() != transaction.LegacyTxType {
			t.Fatal("reused child changed transaction type")
		}
	}
}

func TestSpeculativeQueueSnapshotRetainsTwoFullBlockEntryBudget(t *testing.T) {
	const blockTransactions = 163_000
	if maxSpeculativeQueueSnapshotEntries < 2*blockTransactions {
		t.Fatal("one uncommitted parent plus one child cannot fit advisory snapshot")
	}
	address := types.Address{1}
	slab := &ingressSlab{payload: []byte{7}}
	entry := &ingressEntry{slab: slab, start: 0, end: 1}
	lane := &ingressLane{entries: make([]*ingressEntry, 2*blockTransactions)}
	for i := range lane.entries {
		lane.entries[i] = entry
	}
	q := &strictIngressQueue{lanes: map[types.Address]*ingressLane{address: lane}}
	copyQ, addresses, err := snapshotSpeculativeLanes(q)
	if err != nil || copyQ == nil || len(addresses) != 1 {
		t.Fatalf("two-block retained queue rejected: err=%v", err)
	}
	if len(copyQ.lanes[address].entries) != 2*blockTransactions {
		t.Fatalf("two-block retained queue copied %d entries", len(copyQ.lanes[address].entries))
	}
	slab.payload[0] = 9
	if copyQ.lanes[address].entries[blockTransactions].slab.payload[0] != 7 {
		t.Fatal("advisory snapshot retained a mutable ingress slab")
	}
}

func TestSpeculativeQueueTransitionRequiresExactSuccessfulCanonicalAdvance(t *testing.T) {
	parent := types.Hash{1}
	previous := types.Hash{2}
	epoch := [16]byte{3}
	from := ingressStamp{generation: 7, number: 11, hash: previous}
	to := ingressStamp{generation: 8, number: 12, hash: parent}
	makeServer := func(stamp ingressStamp, transition *speculativeCommitTransition) *Server {
		_, cancel := context.WithCancel(context.Background())
		slot := &speculativeQueueSlot{done: make(chan struct{}), cancel: cancel, epoch: epoch, stamp: from, parent: parent, number: 13, view: 4, maxTx: 2, maxBytes: 1024, binding: types.Hash{4}}
		close(slot.done)
		return &Server{ingress: &strictIngressQueue{epoch: epoch, stamp: stamp}, specSlot: slot, specTransition: transition}
	}
	request := QueueBuildRequest{Epoch: epoch, MaxTransactions: 2, MaxEncodedBytes: 1024}
	header := &block.Header{ParentHash: parent}
	header.Number = uint256.NewInt(13)
	for _, tc := range []struct {
		name       string
		stamp      ingressStamp
		transition *speculativeCommitTransition
		view       hotstuff.ViewNumber
		wantTake   bool
	}{
		{name: "exact certified advance", stamp: to, transition: &speculativeCommitTransition{from: from, to: to, epoch: epoch}, view: 4, wantTake: true},
		{name: "failed writer still incremented generation", stamp: to},
		{name: "different view", stamp: to, transition: &speculativeCommitTransition{from: from, to: to, epoch: epoch}, view: 5},
		{name: "wrong canonical hash", stamp: ingressStamp{generation: 8, number: 12, hash: types.Hash{5}}, transition: &speculativeCommitTransition{from: from, to: to, epoch: epoch}},
		{name: "different transition origin", stamp: to, transition: &speculativeCommitTransition{from: ingressStamp{generation: 6, number: 11, hash: previous}, to: to, epoch: epoch}},
		{name: "same settled canonical generation", stamp: ingressStamp{generation: 7, number: 12, hash: parent}, wantTake: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := makeServer(tc.stamp, tc.transition)
			_, _, cancel := s.takeSpeculativeQueueChild(request, header, tc.view)
			if (cancel != nil) != tc.wantTake {
				t.Fatalf("take=%t, want %t", cancel != nil, tc.wantTake)
			}
			if cancel != nil {
				cancel()
			} else {
				s.discardSpeculativeQueueSlot()
			}
		})
	}
}

func TestSpeculativeQueueNonmatchingExecutionDrainsWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slot := &speculativeQueueSlot{done: make(chan struct{}), cancel: cancel, parent: types.Hash{1}, number: 13}
	s := &Server{speculativeEnabled: true, specSlot: slot}
	finished := make(chan struct{})
	go func() {
		s.stopSpeculativeQueueUnlessCertifiedParent(nil, CommitRequest{BlockHash: slot.parent, Number: 12})
		close(finished)
	}()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("worker cancellation was not requested")
	}
	select {
	case <-finished:
		t.Fatal("nonmatching execution did not join advisory worker")
	default:
	}
	close(slot.done)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("nonmatching execution did not finish after worker exit")
	}
	if s.specSlot != nil {
		t.Fatal("advisory slot retained after drain")
	}
}

func TestSpeculativeQueueOnlyExactCertifiedParentMayOverlap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slot := &speculativeQueueSlot{done: make(chan struct{}), cancel: cancel, parent: types.Hash{1}, number: 13}
	s := &Server{speculativeEnabled: true, specSlot: slot}
	s.stopSpeculativeQueueUnlessCertifiedParent(&hotstuff.QuorumCertificate{}, CommitRequest{BlockHash: slot.parent, Number: 12})
	if s.specSlot != slot {
		t.Fatal("exact certified parent writer drained advisory child")
	}
	select {
	case <-ctx.Done():
		t.Fatal("exact certified parent writer canceled advisory child")
	default:
	}
	close(slot.done)
	s.discardSpeculativeQueueSlot()
}

func TestSpeculativeQueueTimerDrainsBeforeOrdinaryExecutionOrNewHint(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	f.server.speculativeEnabled = true
	workerCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	slot := &speculativeQueueSlot{done: make(chan struct{}), cancel: cancel, parent: types.Hash{1}, number: 2}
	f.server.specMu.Lock()
	f.server.specSlot = slot
	f.server.specMu.Unlock()
	expired := make(chan struct{})
	go func() {
		f.server.expireSpeculativeQueueSlot(slot)
		close(expired)
	}()
	select {
	case <-workerCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("timer did not cancel advisory worker")
	}
	f.server.specMu.Lock()
	draining, blocked := f.server.specDraining == slot, f.server.specExecutionBlocks != 0
	f.server.specMu.Unlock()
	if !draining || !blocked {
		t.Fatal("timed-out worker published idle before its EVM exited")
	}
	ordinary := make(chan struct{})
	go func() {
		f.server.execute.Lock()
		close(ordinary)
		f.server.execute.Unlock()
	}()
	select {
	case <-ordinary:
		t.Fatal("ordinary executor entered before timed-out worker exited")
	default:
	}
	if err := f.server.prepareSpeculativeQueueBuild(queueRequest(t, f, f.template.Header().(*block.Header), 6)); err == nil || !strings.Contains(err.Error(), "slot or executor busy") {
		t.Fatalf("new advisory escaped draining gate: %v", err)
	}
	close(slot.done)
	select {
	case <-expired:
	case <-time.After(time.Second):
		t.Fatal("timer did not join advisory worker")
	}
	select {
	case <-ordinary:
	case <-time.After(time.Second):
		t.Fatal("ordinary executor did not resume after advisory exit")
	}
}
