package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

func boundedSuffixFixture(t *testing.T) (resumeSourceFixture, []sharedexec.BoundedResumeFrame) {
	t.Helper()
	f := makeResumeSourceFixture(t)
	raw, err := os.ReadFile(filepath.Join(f.root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	scan, err := sharedexec.ScanBoundedResumeSources(context.Background(), f.root, raw, f.genesis, f.baseline, 1000)
	if err != nil {
		t.Fatal(err)
	}
	return f, scan.Frames
}

func boundedSuffixCanonicalFirstFixture(t *testing.T) (resumeSourceFixture, []sharedexec.BoundedResumeFrame) {
	t.Helper()
	f := makeResumeSourceFixture(t)
	raw, err := os.ReadFile(filepath.Join(f.root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	scan, err := sharedexec.ScanBoundedResumeSources(context.Background(), f.root, raw, f.genesis, f.baseline, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var first sharedexec.BoundedResumeFrame
	for _, frame := range scan.Frames {
		if frame.ID.Shard == 1 && frame.ID.Sequence == 0 {
			first = frame
			break
		}
	}
	baseline := append([]sharedexec.CanonicalAccount(nil), f.baseline...)
	found := false
	for i := range baseline {
		if baseline[i].Address == first.Address {
			baseline[i].Nonce = 500
			found = true
			break
		}
	}
	if !found {
		t.Fatal("first-frame sender missing from baseline")
	}
	canonical, err := sharedexec.ScanBoundedResumeSources(context.Background(), f.root, raw, f.genesis, baseline, 1000)
	if err != nil {
		t.Fatal(err)
	}
	f.baseline = baseline
	return f, canonical.Frames
}

func boundedSuffixShard(frames []sharedexec.BoundedResumeFrame, shard uint16) []sharedexec.BoundedResumeFrame {
	var selected []sharedexec.BoundedResumeFrame
	for _, frame := range frames {
		if frame.ID.Shard == shard {
			selected = append(selected, frame)
		}
	}
	return selected
}

func TestBoundedSuffixOmitsOnlyWholeFullyCanonicalFrames(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame sharedexec.BoundedResumeFrame
		want  bool
		bad   bool
	}{
		{"fully canonical", sharedexec.BoundedResumeFrame{Transactions: 500, CanonicalSkipped: 500}, true, false},
		{"partial prefix", sharedexec.BoundedResumeFrame{Transactions: 500, CanonicalSkipped: 499, NewlyAdmitted: 1}, false, false},
		{"partial falsely omitted", sharedexec.BoundedResumeFrame{Transactions: 500, CanonicalSkipped: 499}, false, true},
		{"truncated original", sharedexec.BoundedResumeFrame{Transactions: 499, CanonicalSkipped: 499}, false, true},
		{"classification gap", sharedexec.BoundedResumeFrame{Transactions: 500, CanonicalSkipped: 400, NewlyAdmitted: 99}, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := boundedSuffixOmitted(tc.frame)
			if tc.bad {
				if err == nil {
					t.Fatal("invalid or partially omitted original frame accepted")
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("omission classification = %t, %v; want %t", got, err, tc.want)
			}
		})
	}
}

func TestNewBoundedSuffixDigestBindsPlanEpochAndOriginalFrame(t *testing.T) {
	_, frames := boundedSuffixFixture(t)
	plan := sharedexec.BoundedResumePlan{CanonicalNonceSum: 1583}
	planID, epoch := types.Hash{1}, [16]byte{2}
	makeResult := func(p sharedexec.BoundedResumePlan, id types.Hash, e [16]byte, fs []sharedexec.BoundedResumeFrame) types.Hash {
		t.Helper()
		result, err := newBoundedSuffix(t.TempDir(), p, fs, id, e, 2)
		if err != nil {
			t.Fatal(err)
		}
		return result.ScheduleSHA256
	}
	base := makeResult(plan, planID, epoch, frames)
	if base == (types.Hash{}) {
		t.Fatal("empty suffix schedule digest")
	}
	changedPlan := plan
	changedPlan.CanonicalNonceSum++
	changedEpoch := epoch
	changedEpoch[0]++
	changedID := planID
	changedID[0]++
	changedFrame := append([]sharedexec.BoundedResumeFrame(nil), frames...)
	changedFrame[0].ID.Sequence++
	for name, digest := range map[string]types.Hash{
		"plan":           makeResult(changedPlan, planID, epoch, frames),
		"epoch":          makeResult(plan, planID, changedEpoch, frames),
		"plan id":        makeResult(plan, changedID, epoch, frames),
		"original frame": makeResult(plan, planID, epoch, changedFrame),
	} {
		if digest == base {
			t.Fatalf("schedule digest omitted %s", name)
		}
	}
}

func TestBoundedSuffixReadsOriginalFramesButOnlyDispatchesNonemptySuffix(t *testing.T) {
	f, frames := boundedSuffixFixture(t)
	selected := boundedSuffixShard(frames, 1)
	if len(selected) != 4 || selected[1].CanonicalSkipped != 500 || selected[1].NewlyAdmitted != 0 || selected[0].CanonicalSkipped != 41 || selected[0].NewlyAdmitted != 459 {
		t.Fatal("fixture lacks a fully canonical frame and a partial frame")
	}
	file, err := os.Open(filepath.Join(f.root, f.manifest.Shards[1].File))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	planID, epoch, schedule := types.Hash{9}, [16]byte{3}, types.Hash{4}
	client := &boundedTestAdmitter{id: planID, epoch: epoch}
	var dispatched []sharedexec.CorpusFrameID
	client.admit = func(_ context.Context, frame sharedexec.CorpusFrame, planned sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
		if frame.CorpusFrameID != planned.ID || frame.PayloadHash != planned.PayloadHash || len(frame.Payload) != int(planned.PayloadBytes) {
			t.Fatal("partial suffix did not dispatch the exact original frame bytes")
		}
		dispatched = append(dispatched, frame.CorpusFrameID)
		return boundedTestAck(client, planned), nil
	}
	budget := &boundedSenderBudget{plan: sharedexec.BoundedResumePlan{MaxSourceFrames: 4, MaxSourceTransactions: 2000, SourceEncodedBytes: 2_000_000, MaxNewTransactions: 2000}}
	counters := new(boundedStreamCounters)
	start := make(chan struct{})
	close(start)
	ready := make(chan error, 1)
	pacer := &supplyPacer{start: time.Now().Add(-time.Second), rate: 1_000_000}
	progress := new(boundedSuffixProgress)
	senderNew := make([]uint64, 4)
	var journal bytes.Buffer
	if err := boundedSuffixPumpShard(context.Background(), context.Background(), strictSource{file: file}, f.manifest.Descriptor, selected, client, planID, epoch, schedule, budget, pacer, counters, ready, start, &journal, senderNew, progress); err != nil {
		t.Fatal(err)
	}
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	wantDispatched := []sharedexec.CorpusFrameID{selected[0].ID, selected[2].ID, selected[3].ID}
	if client.calls != 3 || !reflect.DeepEqual(dispatched, wantDispatched) {
		t.Fatalf("dispatched frames = %v; want original nonempty frames %v", dispatched, wantDispatched)
	}
	if !progress.Exhausted || progress.ScanNextSequence != 4 || progress.OmittedFrames != 1 || progress.OmittedTransactions != 500 || progress.OmittedBytes != uint64(sharedexec.CorpusWireHeaderSize)+uint64(selected[1].PayloadBytes) || progress.AcknowledgedFrames != 3 || progress.Admitted != 1459 || progress.Pending != nil {
		t.Fatalf("suffix progress differs: %+v", progress)
	}
	if counters.read.Load() != 2000 || counters.admitted.Load() != 1459 || counters.skipped.Load() != 41 || budget.sample().Frames != 3 || budget.sample().Transactions != 1500 || budget.sample().New != 1459 || senderNew[1] != 959 || senderNew[3] != 500 {
		t.Fatal("omission was charged/dispatched or partial bytes were not admitted", counters.boundedSample(), budget.sample(), senderNew)
	}
	var receipts []boundedSuffixReceipt
	dec := json.NewDecoder(&journal)
	for {
		var receipt boundedSuffixReceipt
		if err := dec.Decode(&receipt); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		receipts = append(receipts, receipt)
	}
	if len(receipts) != 3 {
		t.Fatalf("journal receipts = %d, want 3", len(receipts))
	}
	for i, receipt := range receipts {
		if receipt.Kind != boundedSuffixJournalKind || receipt.ScheduleSHA256 != schedule || receipt.PlanID != planID || receipt.Epoch != epoch || receipt.Frame != selected[[]int{0, 2, 3}[i]] || receipt.Ack.NewlyAdmitted != int(receipt.Frame.NewlyAdmitted) {
			t.Fatalf("journal row %d lost exact original frame/schedule binding: %+v", i, receipt)
		}
	}
}

func TestBoundedSuffixCancelAfterPreloadingSelectedFrameRecordsNoChargeOrAck(t *testing.T) {
	f, frames := boundedSuffixFixture(t)
	selected := boundedSuffixShard(frames, 1)
	file, err := os.Open(filepath.Join(f.root, f.manifest.Shards[1].File))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	dispatch, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &boundedTestAdmitter{id: types.Hash{1}, epoch: [16]byte{2}}
	client.admit = func(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
		t.Fatal("pre-start selected frame dispatched")
		return sharedexec.ResumeIngressAck{}, nil
	}
	budget := &boundedSenderBudget{plan: sharedexec.BoundedResumePlan{MaxSourceFrames: 4, MaxSourceTransactions: 2000, SourceEncodedBytes: 2_000_000, MaxNewTransactions: 2000}}
	counters := new(boundedStreamCounters)
	progress := new(boundedSuffixProgress)
	ready, start := make(chan error, 1), make(chan struct{})
	var journal bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- boundedSuffixPumpShard(context.Background(), dispatch, strictSource{file: file}, f.manifest.Descriptor, selected, client, client.id, client.epoch, types.Hash{3}, budget, &supplyPacer{start: time.Now(), rate: 1_000_000}, counters, ready, start, &journal, make([]uint64, 4), progress)
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if progress.ScanNextSequence != 1 || progress.Pending == nil || progress.Pending.Stage != "read_unsubmitted" || progress.OmittedFrames != 0 || progress.Exhausted || counters.read.Load() != 500 || counters.encodedBytes.Load() != uint64(sharedexec.CorpusWireHeaderSize)+uint64(selected[0].PayloadBytes) || client.calls != 0 || budget.sample() != (boundedSenderBudgetSample{}) || journal.Len() != 0 {
		t.Fatal("preloaded selected frame was not recorded without charge/ACK", progress, counters.boundedSample(), budget.sample())
	}
}

func TestBoundedSuffixCancelAfterPreloadingOmittedFrameRecordsOneReadNoChargeOrAck(t *testing.T) {
	f, frames := boundedSuffixCanonicalFirstFixture(t)
	selected := boundedSuffixShard(frames, 1)
	if len(selected) != 4 || selected[0].CanonicalSkipped != 500 || selected[0].NewlyAdmitted != 0 {
		t.Fatal("fixture first frame is not fully canonical")
	}
	file, err := os.Open(filepath.Join(f.root, f.manifest.Shards[1].File))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	dispatch, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &boundedTestAdmitter{id: types.Hash{1}, epoch: [16]byte{2}}
	client.admit = func(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
		t.Fatal("pre-start omitted frame dispatched")
		return sharedexec.ResumeIngressAck{}, nil
	}
	budget := &boundedSenderBudget{plan: sharedexec.BoundedResumePlan{MaxSourceFrames: 4, MaxSourceTransactions: 2000, SourceEncodedBytes: 2_000_000, MaxNewTransactions: 2000}}
	counters := new(boundedStreamCounters)
	progress := new(boundedSuffixProgress)
	ready, start := make(chan error, 1), make(chan struct{})
	var journal bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- boundedSuffixPumpShard(context.Background(), dispatch, strictSource{file: file}, f.manifest.Descriptor, selected, client, client.id, client.epoch, types.Hash{3}, budget, &supplyPacer{start: time.Now(), rate: 1_000_000}, counters, ready, start, &journal, make([]uint64, 4), progress)
	}()
	if err := <-ready; err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	wire := uint64(sharedexec.CorpusWireHeaderSize) + uint64(selected[0].PayloadBytes)
	if progress.ScanNextSequence != 1 || progress.Pending != nil || progress.OmittedFrames != 1 || progress.OmittedTransactions != 500 || progress.OmittedBytes != wire || progress.Exhausted || counters.read.Load() != 500 || counters.encodedBytes.Load() != wire || client.calls != 0 || budget.sample() != (boundedSenderBudgetSample{}) || journal.Len() != 0 {
		t.Fatal("preloaded omitted frame was not accounted without charge/ACK", progress, counters.boundedSample(), budget.sample())
	}
}

func TestBoundedSuffixCancelBeforeFirstReadLeavesCursorZero(t *testing.T) {
	f, frames := boundedSuffixFixture(t)
	selected := boundedSuffixShard(frames, 1)
	file, err := os.Open(filepath.Join(f.root, f.manifest.Shards[1].File))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	dispatch, cancel := context.WithCancel(context.Background())
	cancel()
	client := &boundedTestAdmitter{id: types.Hash{1}, epoch: [16]byte{2}}
	client.admit = func(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
		t.Fatal("pre-read canceled frame dispatched")
		return sharedexec.ResumeIngressAck{}, nil
	}
	budget := &boundedSenderBudget{plan: sharedexec.BoundedResumePlan{MaxSourceFrames: 4, MaxSourceTransactions: 2000, SourceEncodedBytes: 2_000_000, MaxNewTransactions: 2000}}
	counters := new(boundedStreamCounters)
	progress := new(boundedSuffixProgress)
	ready, start := make(chan error, 1), make(chan struct{})
	var journal bytes.Buffer
	err = boundedSuffixPumpShard(context.Background(), dispatch, strictSource{file: file}, f.manifest.Descriptor, selected, client, client.id, client.epoch, types.Hash{3}, budget, &supplyPacer{start: time.Now(), rate: 1_000_000}, counters, ready, start, &journal, make([]uint64, 4), progress)
	if err != nil || !errors.Is(<-ready, context.Canceled) {
		t.Fatalf("pre-read canceled pump: %v", err)
	}
	if progress.ScanNextSequence != 0 || progress.Pending != nil || progress.OmittedFrames != 0 || progress.Exhausted || counters.read.Load() != 0 || counters.encodedBytes.Load() != 0 || client.calls != 0 || budget.sample() != (boundedSenderBudgetSample{}) || journal.Len() != 0 {
		t.Fatal("pre-read cancellation advanced or charged source", progress, counters.boundedSample(), budget.sample())
	}
}

func TestBoundedSuffixKnownZeroCancellationReleasesChargeAndStopsTail(t *testing.T) {
	f, frames := boundedSuffixFixture(t)
	selected := boundedSuffixShard(frames, 1)
	file, err := os.Open(filepath.Join(f.root, f.manifest.Shards[1].File))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	dispatch, stop := context.WithCancel(context.Background())
	defer stop()
	client := &boundedTestAdmitter{id: types.Hash{1}, epoch: [16]byte{2}}
	client.admit = func(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
		stop()
		return sharedexec.ResumeIngressAck{}, sharedexec.ErrIngressCreditsExhausted
	}
	budget := &boundedSenderBudget{plan: sharedexec.BoundedResumePlan{MaxSourceFrames: 4, MaxSourceTransactions: 2000, SourceEncodedBytes: 2_000_000, MaxNewTransactions: 2000}}
	progress := new(boundedSuffixProgress)
	ready, start := make(chan error, 1), make(chan struct{})
	close(start)
	var journal bytes.Buffer
	err = boundedSuffixPumpShard(context.Background(), dispatch, strictSource{file: file}, f.manifest.Descriptor, selected, client, client.id, client.epoch, types.Hash{3}, budget, &supplyPacer{start: time.Now().Add(-time.Second), rate: 1_000_000}, new(boundedStreamCounters), ready, start, &journal, make([]uint64, 4), progress)
	if err != nil || <-ready != nil {
		t.Fatalf("known-zero canceled dispatch: %v", err)
	}
	if client.calls != 1 || progress.ScanNextSequence != 1 || progress.Pending == nil || progress.Pending.Stage != "known_zero" || budget.sample() != (boundedSenderBudgetSample{}) || journal.Len() != 0 {
		t.Fatal("known-zero stop retained a charge or scanned tail", progress, budget.sample())
	}
}

func TestBoundedSuffixUnknownCanceledRequestRetainsChargeAndStopsTail(t *testing.T) {
	f, frames := boundedSuffixFixture(t)
	selected := boundedSuffixShard(frames, 1)
	file, err := os.Open(filepath.Join(f.root, f.manifest.Shards[1].File))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &boundedTestAdmitter{id: types.Hash{1}, epoch: [16]byte{2}}
	client.admit = func(context.Context, sharedexec.CorpusFrame, sharedexec.BoundedResumeFrame) (sharedexec.ResumeIngressAck, error) {
		cancel()
		return sharedexec.ResumeIngressAck{}, sharedexec.ErrIngressUnknownOutcome
	}
	budget := &boundedSenderBudget{plan: sharedexec.BoundedResumePlan{MaxSourceFrames: 4, MaxSourceTransactions: 2000, SourceEncodedBytes: 2_000_000, MaxNewTransactions: 2000}}
	progress := new(boundedSuffixProgress)
	ready, start := make(chan error, 1), make(chan struct{})
	close(start)
	var journal bytes.Buffer
	err = boundedSuffixPumpShard(ctx, context.Background(), strictSource{file: file}, f.manifest.Descriptor, selected, client, client.id, client.epoch, types.Hash{3}, budget, &supplyPacer{start: time.Now().Add(-time.Second), rate: 1_000_000}, new(boundedStreamCounters), ready, start, &journal, make([]uint64, 4), progress)
	if !errors.Is(err, sharedexec.ErrIngressUnknownOutcome) || <-ready != nil {
		t.Fatalf("unknown canceled request error: %v", err)
	}
	charged := budget.sample()
	if client.calls != 1 || progress.ScanNextSequence != 1 || progress.Pending == nil || progress.Pending.Stage != "charged_unacknowledged" || charged.Frames != 1 || charged.Transactions != 500 || charged.New != uint64(selected[0].NewlyAdmitted) || journal.Len() != 0 {
		t.Fatal("unknown outcome refunded charge, wrote a receipt or scanned tail", progress, charged)
	}
}
