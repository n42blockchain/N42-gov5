package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

type boundedCanonicalObservation struct {
	RequestStartNS int64                        `json:"request_start_monotonic_ns"`
	ResponseEndNS  int64                        `json:"response_end_monotonic_ns"`
	Canonical      sharedvalidator.StatusResult `json:"single_validator_canonical"`
}

func boundedWindowRate(start, end boundedCanonicalObservation) (float64, error) {
	if start.RequestStartNS < 0 || start.ResponseEndNS < start.RequestStartNS || end.RequestStartNS < start.ResponseEndNS || end.ResponseEndNS < end.RequestStartNS || end.RequestStartNS-start.ResponseEndNS < int64(20*time.Second) || !recoveryNonregressing(end.Canonical, start.Canonical) {
		return 0, errors.New("bounded canonical window is shorter than 20 seconds or counter/head regressed")
	}
	return float64(end.Canonical.CommittedExecutedTransactions-start.Canonical.CommittedExecutedTransactions) / (float64(end.ResponseEndNS-start.RequestStartNS) / 1e9), nil
}

type boundedWindowResult struct {
	Kind                         string                         `json:"kind"`
	Passed                       bool                           `json:"passed"`
	Error                        string                         `json:"error,omitempty"`
	Identity                     sharedexec.Identity            `json:"identity"`
	RuntimeManifest              string                         `json:"runtime_manifest"`
	RuntimeSHA256                types.Hash                     `json:"runtime_sha256"`
	PlanSHA256                   types.Hash                     `json:"plan_sha256"`
	PlanID                       types.Hash                     `json:"plan_id"`
	OfferedTPS                   uint64                         `json:"offered_tps"`
	WarmupSeconds                float64                        `json:"target_warmup_seconds"`
	ActualWarmupSeconds          float64                        `json:"actual_warmup_seconds"`
	OwnerInitial                 sharedexec.StrictIngressStatus `json:"owner_initial"`
	OwnerFinal                   sharedexec.StrictIngressStatus `json:"owner_final"`
	ReferenceFinal               sharedvalidator.StatusResult   `json:"reference_final"`
	Initial                      []sharedvalidator.StatusResult `json:"initial"`
	WindowStart                  *boundedCanonicalObservation   `json:"window_start,omitempty"`
	WindowEnd                    *boundedCanonicalObservation   `json:"window_end,omitempty"`
	Samples                      []boundedCanonicalObservation  `json:"samples"`
	CanonicalWindowDelta         uint64                         `json:"canonical_window_delta"`
	ActualWindowSeconds          float64                        `json:"actual_window_seconds"`
	CanonicalTPS                 *float64                       `json:"canonical_committed_executed_tps,omitempty"`
	CorpusAvailableThroughWindow bool                           `json:"corpus_available_through_window"`
	DrainedCanonicalCount        uint64                         `json:"drained_canonical_count"`
	Stream                       boundedStreamSample            `json:"stream_final"`
	Budget                       boundedSenderBudgetSample      `json:"sender_lifetime_budget"`
	Prefix                       []strictShardProgress          `json:"consumed_prefixes"`
	SenderNew                    []uint64                       `json:"sender_new_transactions"`
	FilesBefore                  []resumeSourceMetadata         `json:"source_files_before"`
	FilesAfter                   []resumeSourceMetadata         `json:"source_files_after"`
	FullSourceIntegrityVerified  bool                           `json:"full_source_integrity_verified"`
	PhysicalReopenIncluded       bool                           `json:"physical_reopen_included"`
	Capacity5MProven             bool                           `json:"capacity_5m_proven"`
}

// Snapshot fd and path identity including ctime: restoring an old mtime cannot
// hide an in-place write after whole-source hashing. Scanner metadata is local
// audit data, never a transferable owner activation capability.
func boundedSourceMetadata(path string, info os.FileInfo) (resumeSourceMetadata, error) {
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || s.Uid != uint32(os.Geteuid()) || s.Nlink != 1 {
		return resumeSourceMetadata{}, errors.New("bounded original source ownership changed")
	}
	return resumeSourceMetadata{Path: path, Device: uint64(s.Dev), Inode: s.Ino, Bytes: uint64(info.Size()), Links: s.Nlink, UID: s.Uid, Mode: uint32(info.Mode()), ModifiedNS: info.ModTime().UnixNano(), ChangedNS: s.Ctim.Sec*1e9 + s.Ctim.Nsec}, nil
}
func checkBoundedSources(files []strictSource, before []resumeSourceMetadata) ([]resumeSourceMetadata, error) {
	if len(files) != len(before) {
		return nil, errors.New("bounded source descriptor set differs")
	}
	after := make([]resumeSourceMetadata, len(files))
	for i, f := range files {
		fd, err := f.file.Stat()
		if err != nil {
			return nil, err
		}
		path, err := os.Lstat(before[i].Path)
		if err != nil {
			return nil, err
		}
		a, err := boundedSourceMetadata(before[i].Path, fd)
		if err != nil {
			return nil, err
		}
		b, err := boundedSourceMetadata(before[i].Path, path)
		if err != nil || a != before[i] || b != before[i] {
			return nil, errors.New("bounded source fd/path metadata changed")
		}
		after[i] = b
	}
	return after, nil
}

// The caller holds runtime BOX ownership and independently checks process
// liveness. This command measures and drains; it does not claim physical-reopen
// success or 5M capacity. The external runner stops producers before that audit.
func runBoundedWindow(parent context.Context, root, runtimePath, attempt string, planPin, baselinePin resumePinnedInput, expectedPlan types.Hash, offered uint64) (returnErr error) {
	if !stringsHasPrefixPath(root, supplyRoot) || expectedPlan == (types.Hash{}) || offered == 0 || offered > 20_000_000 {
		return errors.New("explicit bounded source/plan and offered rate 1..20M required")
	}
	ctx, cancel := context.WithTimeout(parent, 180*time.Second)
	defer cancel()
	input, err := loadBoundedSenderInput(ctx, root, runtimePath, planPin, baselinePin, expectedPlan)
	if err != nil {
		return err
	}
	defer input.close()
	m, runtime, runtimeRaw := input.manifest, input.runtime, input.runtimeRaw
	p, scan, sources, owner, initialOwner := input.document.Plan, input.scan, input.sources, input.owner, input.initialOwner
	epoch := initialOwner.Epoch
	validators := make([]*sharedvalidator.APIClient, 7)
	defer func() {
		for _, c := range validators {
			if c != nil {
				c.Close()
			}
		}
	}()
	for i, v := range runtime.Validators {
		validators[i] = sharedvalidator.NewAPIClient(v.APISocket, runtime.Identity)
	}
	dir, err := strictAttempt(runtime.Root, attempt)
	if err != nil {
		return err
	}
	result := boundedWindowResult{Kind: "gov5-bounded-resume-20s-window-and-drain-candidate-not-capacity-v2", Identity: p.Identity, RuntimeManifest: runtimePath, RuntimeSHA256: types.Hash(sha256.Sum256(runtimeRaw)), PlanSHA256: planPin.SHA256, PlanID: expectedPlan, OfferedTPS: offered, WarmupSeconds: 4, OwnerInitial: initialOwner, FilesBefore: scan.Source.FilesBefore, FullSourceIntegrityVerified: true}
	defer func() {
		if returnErr != nil {
			result.Passed = false
			result.Error = returnErr.Error()
		}
		returnErr = errors.Join(returnErr, exclusiveJSON(filepath.Join(dir, "result-candidate.json"), result), syncResumeDirectory(dir))
	}()
	if err := exclusiveJSON(filepath.Join(dir, "dispatch-intent.json"), struct {
		PlanID                                     types.Hash
		Epoch                                      [16]byte
		PlanSHA256                                 types.Hash
		MaxFrames, MaxOriginal, MaxNew, MaxEncoded uint64
	}{expectedPlan, epoch, planPin.SHA256, p.MaxSourceFrames, p.MaxSourceTransactions, p.MaxNewTransactions, p.SourceEncodedBytes}); err != nil {
		return err
	}
	if err := syncResumeDirectory(dir); err != nil {
		return err
	}
	admitters := make([]boundedAdmitter, len(m.Shards))
	journals := make([]*os.File, 0, len(m.Shards))
	defer func() {
		for _, c := range admitters {
			if c != nil {
				_ = c.Close()
			}
		}
		for _, f := range journals {
			returnErr = errors.Join(returnErr, f.Close())
		}
	}()
	byShard := make([][]sharedexec.BoundedResumeFrame, len(m.Shards))
	for _, frame := range scan.Frames {
		byShard[frame.ID.Shard] = append(byShard[frame.ID.Shard], frame)
	}
	for i := range m.Shards {
		journal, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("ack-shard-%02d.jsonl", i)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		journals = append(journals, journal)
	}
	// Source verification and the initial empty-owner baseline precede
	// production. Connections are opened only after validators are ready, so
	// their bounded first-header deadlines cannot expire during node startup.
	readyEvidence := struct {
		Kind           string                         `json:"kind"`
		PlanID         types.Hash                     `json:"plan_id"`
		Epoch          [16]byte                       `json:"epoch"`
		Owner          sharedexec.StrictIngressStatus `json:"owner"`
		SourceVerified bool                           `json:"full_source_integrity_verified"`
		Dispatched     uint64                         `json:"dispatched_transactions"`
	}{"gov5-bounded-source-ready-before-production-v2", expectedPlan, epoch, initialOwner, true, 0}
	if err := publishBoundedReady(filepath.Join(dir, "source-ready.json"), readyEvidence, resumePlanPublication{write: writeResumeArtifact, sync: syncResumeDirectory}); err != nil {
		return err
	}
	productionDeadline := time.Now().Add(30 * time.Second)
	for {
		path := filepath.Join(dir, "production-ready.json")
		_, err := os.Lstat(path + ".complete")
		if err == nil {
			if err := checkBoundedReadyComplete(path + ".complete"); err != nil {
				return err
			}
			raw, err := strictMetadata(path, 4096)
			if err != nil {
				return err
			}
			var marker struct {
				PlanID          types.Hash `json:"plan_id"`
				Epoch           [16]byte   `json:"epoch"`
				ValidatorsReady bool       `json:"validators_ready"`
			}
			if err := resumeDecodeJSON(raw, &marker, true); err != nil {
				return err
			}
			if marker.PlanID != expectedPlan || marker.Epoch != epoch || !marker.ValidatorsReady {
				return errors.New("bounded production marker differs from source-ready session")
			}
			break
		}
		if !os.IsNotExist(err) {
			return err
		}
		if !time.Now().Before(productionDeadline) {
			return errors.New("bounded validators were not ready within startup deadline")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	initial, err := strictStatuses(ctx, validators, m, epoch)
	if err != nil {
		return err
	}
	for _, v := range initial {
		if !v.ProducerRunning || v.CommittedExecutedTransactions != p.CanonicalNonceSum || v.Number < p.Canonical.Number || (v.Number == p.Canonical.Number && v.BlockHash != p.Canonical.Hash) {
			return errors.New("bounded validator initial canonical baseline differs")
		}
	}
	result.Initial = append([]sharedvalidator.StatusResult(nil), initial...)
	dialCtx, cancelDial := context.WithTimeout(ctx, 2*time.Second)
	dialErrors := make([]error, len(m.Shards))
	var dialWG sync.WaitGroup
	for i := range m.Shards {
		dialWG.Add(1)
		go func(i int) {
			defer dialWG.Done()
			admitters[i], dialErrors[i] = sharedexec.DialBoundedResumeIngress(dialCtx, filepath.Join(runtime.OwnerDir, "execution-rpc", "ingress.sock"), p, expectedPlan, epoch, uint16(i))
		}(i)
	}
	dialWG.Wait()
	cancelDial()
	for _, err := range dialErrors {
		if err != nil {
			return err
		}
	}
	dispatch, stop := context.WithCancel(ctx)
	defer stop()
	var mu sync.Mutex
	var firstError error
	record := func(err error) {
		if err != nil {
			mu.Lock()
			if firstError == nil {
				firstError = err
			}
			mu.Unlock()
			stop()
		}
	}
	failure := func() error { mu.Lock(); defer mu.Unlock(); return firstError }
	counters := new(boundedStreamCounters)
	budget := &boundedSenderBudget{plan: p}
	pacer := &supplyPacer{rate: offered}
	ready := make(chan error, len(m.Shards))
	start := make(chan struct{})
	progress := make([]strictShardProgress, len(m.Shards))
	senderNew := make([]uint64, len(p.Senders))
	var wg sync.WaitGroup
	for i := range m.Shards {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			record(boundedPumpShard(ctx, dispatch, sources[i], m.Descriptor, byShard[i], admitters[i], expectedPlan, epoch, budget, pacer, counters, ready, start, journals[i], senderNew, &progress[i]))
		}(i)
	}
	// Every first-frame path sends ready exactly once, even on cancellation.
	for range m.Shards {
		record(<-ready)
	}
	pacer.start = time.Now()
	close(start)
	last := initial[0]
	observe := func(readContext context.Context) (boundedCanonicalObservation, error) {
		out := boundedCanonicalObservation{RequestStartNS: time.Since(pacer.start).Nanoseconds()}
		probe, cancel := context.WithTimeout(readContext, 2*time.Second)
		defer cancel()
		status, readErr := validators[0].Status(probe)
		out.Canonical = status
		out.ResponseEndNS = time.Since(pacer.start).Nanoseconds()
		if readErr != nil {
			return out, readErr
		}
		if err := strictValidatorBinding(out.Canonical, epoch, m); err != nil {
			return out, err
		}
		if !out.Canonical.ProducerRunning {
			return out, errors.New("bounded reference producer stopped during window/drain")
		}
		if !recoveryNonregressing(out.Canonical, last) {
			return out, errors.New("bounded reference canonical count/head regressed")
		}
		last = out.Canonical
		return out, nil
	}
	waitUntil := func(deadline time.Time) error {
		for time.Now().Before(deadline) {
			timer := time.NewTimer(min(time.Until(deadline), time.Second))
			select {
			case <-dispatch.Done():
				timer.Stop()
				if err := failure(); err != nil {
					return err
				}
				return dispatch.Err()
			case <-timer.C:
			}
			if !time.Now().Before(deadline) {
				break
			}
			sample, err := observe(ctx)
			if err != nil {
				return err
			}
			result.Samples = append(result.Samples, sample)
			// Moving heads may differ; only binding/liveness and each node's
			// nonregression are required until producers have stopped.
			peers, err := strictStatuses(ctx, validators, m, epoch)
			if err != nil {
				return err
			}
			for i, v := range peers {
				if !v.ProducerRunning || !recoveryNonregressing(v, initial[i]) {
					return errors.New("bounded peer canonical progress regressed")
				}
				initial[i] = v
			}
			if !recoveryNonregressing(peers[0], last) {
				return errors.New("bounded later reference peer observation regressed")
			}
			last = peers[0]
		}
		return nil
	}
	if err := failure(); err != nil {
		record(err)
	} else if err := waitUntil(pacer.start.Add(4 * time.Second)); err != nil {
		record(err)
	} else {
		begin, err := observe(ctx)
		if err != nil {
			record(err)
		} else {
			result.WindowStart = &begin
			result.ActualWarmupSeconds = float64(begin.RequestStartNS) / 1e9
			if err := waitUntil(pacer.start.Add(time.Duration(begin.ResponseEndNS) + 20*time.Second)); err != nil {
				record(err)
			} else {
				end, err := observe(ctx) // Supply continues through the end response.
				if err != nil {
					record(err)
				} else {
					result.WindowEnd = &end
				}
			}
		}
	}
	stop()
	wg.Wait()
	result.Stream = counters.boundedSample()
	result.Budget = budget.sample()
	result.Prefix = progress
	result.SenderNew = senderNew
	record(syncBoundedJournals(journals))
	result.FilesAfter, err = checkBoundedSources(sources, scan.Source.FilesBefore)
	record(err)
	if result.WindowEnd != nil {
		begin, end := *result.WindowStart, *result.WindowEnd
		rate, err := boundedWindowRate(begin, end)
		if err != nil {
			record(err)
		} else {
			result.CanonicalTPS = &rate
			result.CanonicalWindowDelta = end.Canonical.CommittedExecutedTransactions - begin.Canonical.CommittedExecutedTransactions
			result.ActualWindowSeconds = float64(end.ResponseEndNS-begin.RequestStartNS) / 1e9
		}
		first := counters.firstExhaustedNanos.Load()
		result.CorpusAvailableThroughWindow = first == 0 || first > end.ResponseEndNS
		if !result.CorpusAvailableThroughWindow {
			record(errors.New("bounded prefix exhausted before complete 20s window; supply insufficient"))
		}
	}
	if err := failure(); err != nil {
		return err
	}
	if result.WindowEnd == nil {
		return errors.New("bounded 20s canonical window incomplete")
	}
	// Drain cannot alter the saved end-point count or window denominator.
	drain, cancelDrain := context.WithTimeout(ctx, 45*time.Second)
	defer cancelDrain()
	expected := p.CanonicalNonceSum + result.Stream.Admitted
	for {
		probe, cancel := context.WithTimeout(drain, 2*time.Second)
		current, err := owner.StrictIngressStatus(probe)
		cancel()
		if err != nil {
			return err
		}
		if current.Identity != p.Identity || current.ManifestHash != p.ManifestSHA256 || current.Epoch != epoch || current.NewlyAdmitted != result.Stream.Admitted {
			return errors.New("bounded owner changed during drain")
		}
		sample, err := observe(drain)
		if err != nil {
			return err
		}
		if err := drain.Err(); err != nil {
			return errors.New("bounded drain deadline exceeded during final observation")
		}
		result.OwnerFinal = current
		result.ReferenceFinal = sample.Canonical
		if sample.Canonical.CommittedExecutedTransactions > expected {
			return errors.New("bounded canonical count exceeded acknowledged prefix")
		}
		if strictQueueEmpty(current) && sample.Canonical.CommittedExecutedTransactions == expected {
			result.DrainedCanonicalCount = expected
			break
		}
		select {
		case <-drain.Done():
			return errors.New("bounded acknowledged prefix did not drain before deadline")
		case <-time.After(100 * time.Millisecond):
		}
	}
	if _, err := readResumePin(planPin, sharedexec.ResumeMaxPlanDocumentBytes); err != nil {
		return err
	}
	currentRuntime, err := strictMetadata(runtimePath, 2<<20)
	if err != nil || !bytes.Equal(currentRuntime, runtimeRaw) {
		return errors.New("bounded runtime bytes changed")
	}
	result.Passed = true
	return ctx.Err()
}

// A reader waits for the completion marker, never for a partially written JSON
// path. A failure after marker publication is terminal and is never retried.
func publishBoundedReady(path string, value any, publication resumePlanPublication) error {
	if _, err := publication.write(path, value); err != nil {
		return err
	}
	if err := publication.sync(filepath.Dir(path)); err != nil {
		return err
	}
	complete, err := os.OpenFile(path+".complete", os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	if err := errors.Join(complete.Sync(), complete.Close()); err != nil {
		return err
	}
	return publication.sync(filepath.Dir(path))
}
func checkBoundedReadyComplete(path string) error {
	if err := strictPrivatePath(path, false, false); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Size() != 0 || stat.Nlink != 1 {
		return errors.New("bounded ready completion marker invalid")
	}
	return nil
}
