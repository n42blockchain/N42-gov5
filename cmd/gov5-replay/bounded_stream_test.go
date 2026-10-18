package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

type boundedTestAdmitter struct {
	epoch [16]byte
	id    types.Hash
	calls int
	admit func(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error)
}

func (c *boundedTestAdmitter) Epoch() [16]byte    { return c.epoch }
func (c *boundedTestAdmitter) PlanID() types.Hash { return c.id }
func (c *boundedTestAdmitter) Close() error       { return nil }
func (c *boundedTestAdmitter) AdmitBounded(ctx context.Context, f sharedexec.CorpusFrame, p sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
	c.calls++
	return c.admit(ctx, f, p)
}
func boundedTestAck(c *boundedTestAdmitter, p sharedexec.BoundedResumeFrame) sharedexec.ResumeIngressAck {
	return sharedexec.ResumeIngressAck{PlanID: c.id, Status: sharedexec.StrictIngressStatus{Epoch: c.epoch}, CanonicalSkipped: int(p.CanonicalSkipped), NewlyAdmitted: int(p.NewlyAdmitted), PriorAdmission: int(p.NewlyAdmitted)}
}

func TestBoundedStreamKnownZeroGetsFreshDeadlineAndPreservesOriginalBytes(t *testing.T) {
	frame := strictTestFrame(t)
	selected := sharedexec.BoundedResumeFrame{ID: frame.CorpusFrameID, PayloadHash: frame.PayloadHash, CanonicalSkipped: 41, NewlyAdmitted: 459}
	client := &boundedTestAdmitter{epoch: [16]byte{1}, id: types.Hash{2}}
	var old context.Context
	var first time.Time
	client.admit = func(ctx context.Context, f sharedexec.CorpusFrame, p sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
		if f.CorpusFrameID != frame.CorpusFrameID || !bytes.Equal(f.Payload, frame.Payload) || p != selected {
			t.Fatal("known-zero retry changed fixed original frame")
		}
		deadline, ok := ctx.Deadline()
		if !ok || ctx.Err() != nil {
			t.Fatal("request lacks live bounded deadline")
		}
		if client.calls == 1 {
			old, first = ctx, deadline
			return sharedexec.ResumeIngressAck{}, sharedexec.ErrIngressCreditsExhausted
		}
		if old.Err() != context.Canceled || !deadline.After(first) || time.Until(deadline) < 4*time.Second {
			t.Fatal("retry reused old IO allowance")
		}
		if client.calls == 2 {
			return sharedexec.ResumeIngressAck{}, sharedexec.ErrIngressSnapshotChanged
		}
		return boundedTestAck(client, p), nil
	}
	counters := new(boundedStreamCounters)
	ack, ok, err := boundedAdmitFrame(context.Background(), context.Background(), client, frame, selected, client.id, client.epoch, counters)
	if err != nil || !ok || ack.NewlyAdmitted != 459 || client.calls != 3 || counters.busy.Load() != 1 || counters.snapshot.Load() != 1 {
		t.Fatal("known zero retry differs", err)
	}
}

func TestBoundedStreamUnknownAndMalformedReceiptStopWithoutReplay(t *testing.T) {
	for _, kind := range []string{"unknown", "validation", "plan", "epoch", "duplicate", "count", "prior-count", "nonzero-retry"} {
		t.Run(kind, func(t *testing.T) {
			frame := strictTestFrame(t)
			selected := sharedexec.BoundedResumeFrame{CanonicalSkipped: 42, NewlyAdmitted: 458}
			client := &boundedTestAdmitter{epoch: [16]byte{1}, id: types.Hash{2}}
			client.admit = func(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
				ack := boundedTestAck(client, selected)
				switch kind {
				case "unknown":
					return ack, sharedexec.ErrIngressUnknownOutcome
				case "validation":
					return ack, errors.New("bad nonce")
				case "plan":
					ack.PlanID = types.Hash{9}
				case "epoch":
					ack.Status.Epoch = [16]byte{9}
				case "duplicate":
					ack.Duplicate = true
				case "count":
					ack.NewlyAdmitted++
				case "prior-count":
					ack.PriorAdmission++
				case "nonzero-retry":
					return ack, sharedexec.ErrIngressSnapshotChanged
				}
				return ack, nil
			}
			_, accepted, err := boundedAdmitFrame(context.Background(), context.Background(), client, frame, selected, client.id, client.epoch, new(boundedStreamCounters))
			if err == nil || accepted || client.calls != 1 {
				t.Fatal("fatal outcome replayed", err)
			}
			if kind != "validation" && !errors.Is(err, sharedexec.ErrIngressUnknownOutcome) {
				t.Fatal("unknown outcome classification lost", err)
			}
		})
	}
}

func TestBoundedStopAllowsInflightAckButKnownZeroNeverStartsNextIO(t *testing.T) {
	for _, zero := range []bool{false, true} {
		dispatch, stop := context.WithCancel(context.Background())
		client := &boundedTestAdmitter{epoch: [16]byte{1}, id: types.Hash{2}}
		selected := sharedexec.BoundedResumeFrame{NewlyAdmitted: 500}
		client.admit = func(ctx context.Context, _ sharedexec.CorpusFrame, _ sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
			stop()
			if ctx.Err() != nil {
				t.Fatal("stop canceled granted work")
			}
			if zero {
				return sharedexec.ResumeIngressAck{}, sharedexec.ErrIngressCreditsExhausted
			}
			return boundedTestAck(client, selected), nil
		}
		_, accepted, err := boundedAdmitFrame(context.Background(), dispatch, client, strictTestFrame(t), selected, client.id, client.epoch, new(boundedStreamCounters))
		if err != nil || accepted == zero || client.calls != 1 {
			t.Fatal("dispatch boundary differs", err)
		}
		stop()
	}
}

func TestBoundedSenderLifetimeCannotBeRefundedByQueueDrain(t *testing.T) {
	p := sharedexec.BoundedResumePlan{MaxSourceFrames: 2, MaxSourceTransactions: 1000, SourceEncodedBytes: 2168, MaxNewTransactions: 459}
	b := &boundedSenderBudget{plan: p}
	a := sharedexec.BoundedResumeFrame{PayloadBytes: 1000, CanonicalSkipped: 41, NewlyAdmitted: 459}
	skip := sharedexec.BoundedResumeFrame{PayloadBytes: 1000, CanonicalSkipped: 500}
	if err := b.reserve(a); err != nil {
		t.Fatal(err)
	}
	// Owner queue release has no method that can refund this sender budget.
	if err := b.reserve(skip); err != nil {
		t.Fatal(err)
	}
	if got := b.sample(); got.Frames != 2 || got.Transactions != 1000 || got.Bytes != 2168 || got.New != 459 {
		t.Fatal("sender lifetime accounting differs", got)
	}
	if err := b.reserve(skip); err == nil {
		t.Fatal("source cap exceeded after simulated drain")
	}
	b.releaseKnownZero(skip)
	if err := b.reserve(skip); err != nil {
		t.Fatal("confirmed zero reservation not reusable", err)
	}
	if b.sample().New != 459 {
		t.Fatal("all-skipped source charged new transactions")
	}
}

func TestBoundedPumpJournalsKnownPrefixAndRecordsExactExhaustion(t *testing.T) {
	source := makeResumeSourceFixture(t)
	file, err := os.Open(filepath.Join(source.root, source.manifest.Shards[0].File))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	frame, _, err := sharedexec.ReadCorpusFrame(file, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	selected := sharedexec.BoundedResumeFrame{ID: frame.CorpusFrameID, SenderIndex: 0, PayloadHash: frame.PayloadHash, PayloadBytes: uint32(len(frame.Payload)), Transactions: 500, NewlyAdmitted: 500}
	p := sharedexec.BoundedResumePlan{MaxSourceFrames: 1, MaxSourceTransactions: 500, SourceEncodedBytes: uint64(84 + len(frame.Payload)), MaxNewTransactions: 500}
	budget := &boundedSenderBudget{plan: p}
	client := &boundedTestAdmitter{epoch: [16]byte{1}, id: types.Hash{2}}
	client.admit = func(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
		return boundedTestAck(client, selected), nil
	}
	start := make(chan struct{})
	close(start)
	ready := make(chan error, 1)
	pacer := &supplyPacer{start: time.Now().Add(-time.Second), rate: 1_000_000}
	counters := new(boundedStreamCounters)
	progress := new(strictShardProgress)
	senderNew := make([]uint64, 4)
	var journal bytes.Buffer
	if err := boundedPumpShard(context.Background(), context.Background(), strictSource{file: file}, source.manifest.Descriptor, []sharedexec.BoundedResumeFrame{selected}, client, client.id, client.epoch, budget, pacer, counters, ready, start, &journal, senderNew, progress); err != nil {
		t.Fatal(err)
	}
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	if !progress.Exhausted || progress.Sequence != 1 || counters.exhaustedShards.Load() != 1 || counters.firstExhaustedNanos.Load() <= 0 || counters.admitted.Load() != 500 || senderNew[0] != 500 || journal.Len() == 0 || budget.sample().New != 500 {
		t.Fatal("known ACK/exhaustion evidence differs")
	}
}

func TestBoundedWindowConservativeReadBracketsExcludeDrain(t *testing.T) {
	before := boundedCanonicalObservation{RequestStartNS: int64(time.Second), ResponseEndNS: int64(2 * time.Second), Canonical: sharedvalidator.StatusResult{CanonicalProgress: sharedvalidator.CanonicalProgress{Number: 10, BlockHash: types.Hash{1}, CommittedExecutedTransactions: 100}}}
	after := boundedCanonicalObservation{RequestStartNS: int64(22 * time.Second), ResponseEndNS: int64(23 * time.Second), Canonical: sharedvalidator.StatusResult{CanonicalProgress: sharedvalidator.CanonicalProgress{Number: 11, BlockHash: types.Hash{2}, CommittedExecutedTransactions: 320}}}
	rate, err := boundedWindowRate(before, after)
	if err != nil || rate != 10 {
		t.Fatalf("conservative bracket: %f %v", rate, err)
	}
	// A later drain count cannot enter this pure start/end computation.
	later := after
	later.Canonical.CommittedExecutedTransactions = 100000
	if again, err := boundedWindowRate(before, after); err != nil || again != rate || later.Canonical.CommittedExecutedTransactions == after.Canonical.CommittedExecutedTransactions {
		t.Fatal("drain changed frozen endpoint")
	}
	for _, change := range []func(*boundedCanonicalObservation){func(o *boundedCanonicalObservation) { o.RequestStartNS-- }, func(o *boundedCanonicalObservation) { o.Canonical.CommittedExecutedTransactions = 99 }, func(o *boundedCanonicalObservation) { o.Canonical.Number = 10 }} {
		bad := after
		change(&bad)
		if _, err := boundedWindowRate(before, bad); err == nil {
			t.Fatal("short window/regression/fork accepted")
		}
	}
}

func TestBoundedReadyPublicationRequiresCompletedDurableJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ready.json")
	publication := resumePlanPublication{write: writeResumeArtifact, sync: syncResumeDirectory}
	if err := publishBoundedReady(path, struct{ Ready bool }{true}, publication); err != nil {
		t.Fatal(err)
	}
	if err := checkBoundedReadyComplete(path + ".complete"); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{"write", "sync"} {
		p := filepath.Join(t.TempDir(), "failure.json")
		failure := errors.New("injected ready publication failure")
		pub := publication
		if phase == "write" {
			pub.write = func(path string, value any) (types.Hash, error) {
				_, _ = writeResumeArtifact(path, value)
				return types.Hash{}, failure
			}
		} else {
			pub.sync = func(string) error { return failure }
		}
		if err := publishBoundedReady(p, struct{ Ready bool }{true}, pub); !errors.Is(err, failure) {
			t.Fatal("late ready failure lost", err)
		}
		if _, err := os.Lstat(p + ".complete"); !os.IsNotExist(err) {
			t.Fatal("failed JSON publication exposed complete marker")
		}
	}
}
