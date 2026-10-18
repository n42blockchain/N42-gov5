package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

type boundedFinitePreloadResult struct {
	Kind                        string                         `json:"kind"`
	Passed                      bool                           `json:"passed"`
	Error                       string                         `json:"error,omitempty"`
	Identity                    sharedexec.Identity            `json:"identity"`
	RuntimeManifest             string                         `json:"runtime_manifest"`
	RuntimeSHA256               types.Hash                     `json:"runtime_sha256"`
	PlanSHA256                  types.Hash                     `json:"plan_sha256"`
	PlanID                      types.Hash                     `json:"plan_id"`
	Schedule                    boundedFiniteSchedule          `json:"schedule"`
	OwnerInitial                sharedexec.StrictIngressStatus `json:"owner_initial"`
	OwnerFinal                  sharedexec.StrictIngressStatus `json:"owner_final"`
	Stream                      boundedStreamSample            `json:"stream_final"`
	Budget                      boundedSenderBudgetSample      `json:"sender_lifetime_budget"`
	Prefix                      []strictShardProgress          `json:"consumed_prefixes"`
	SenderNew                   []uint64                       `json:"sender_new_transactions"`
	FilesBefore                 []resumeSourceMetadata         `json:"source_files_before"`
	FilesAfter                  []resumeSourceMetadata         `json:"source_files_after"`
	FullSourceIntegrityVerified bool                           `json:"full_source_integrity_verified"`
	ProductionStarted           bool                           `json:"production_started"`
	CommitProofIncluded         bool                           `json:"commit_proof_included"`
	TPSMeasured                 bool                           `json:"tps_measured"`
	Capacity5MProven            bool                           `json:"capacity_5m_proven"`
}

// This command only preloads a finite original prefix. The independent launcher
// proves no validator has started yet and starts production only after rc=0,
// exact receipt/journal verification and an unchanged canonical baseline.
func runBoundedFinitePreload(parent context.Context, root, runtimePath, attempt string, planPin, baselinePin resumePinnedInput, expectedPlan types.Hash) (returnErr error) {
	if parent == nil || !stringsHasPrefixPath(root, supplyRoot) || expectedPlan == (types.Hash{}) {
		return errors.New("finite diagnostic requires explicit source/plan/runtime")
	}
	ctx, cancel := context.WithTimeout(parent, 180*time.Second)
	defer cancel()
	input, err := loadBoundedSenderInput(ctx, root, runtimePath, planPin, baselinePin, expectedPlan)
	if err != nil {
		return err
	}
	defer input.close()
	p, initial := input.document.Plan, input.initialOwner
	if initial.Epoch == input.baseline.OwnerFinal.Epoch {
		return errors.New("finite diagnostic cannot reuse the historical owner epoch")
	}
	schedule, err := selectBoundedFiniteSchedule(input.manifest.Descriptor, p, expectedPlan, input.scan.Frames, finitePhaseTargetNew)
	if err != nil {
		return err
	}
	if len(schedule.Frames) > 1810 || schedule.NewlyAdmitted < 200000 || schedule.NewlyAdmitted > 200499 {
		return errors.New("finite production diagnostic frame/new bound exceeded")
	}
	dir, err := strictAttempt(input.runtime.Root, attempt)
	if err != nil {
		return err
	}
	result := boundedFinitePreloadResult{Kind: "gov5-bounded-finite-preload-candidate-no-tps-v1", Identity: p.Identity, RuntimeManifest: runtimePath, RuntimeSHA256: types.Hash(sha256.Sum256(input.runtimeRaw)), PlanSHA256: planPin.SHA256, PlanID: expectedPlan, Schedule: schedule, OwnerInitial: initial, FilesBefore: input.scan.Source.FilesBefore, FullSourceIntegrityVerified: true, Prefix: make([]strictShardProgress, len(input.sources)), SenderNew: make([]uint64, len(p.Senders))}
	defer func() {
		if returnErr != nil {
			result.Passed, result.Error = false, returnErr.Error()
		}
		returnErr = errors.Join(returnErr, exclusiveJSON(filepath.Join(dir, "result-candidate.json"), result), syncResumeDirectory(dir))
	}()
	if err := exclusiveJSON(filepath.Join(dir, "dispatch-intent.json"), struct {
		Kind     string                `json:"kind"`
		Epoch    [16]byte              `json:"epoch"`
		Schedule boundedFiniteSchedule `json:"schedule"`
	}{"gov5-bounded-finite-preload-intent-v1", initial.Epoch, schedule}); err != nil {
		return err
	}
	if err := syncResumeDirectory(dir); err != nil {
		return err
	}
	admitters := make([]boundedAdmitter, len(input.sources))
	journals := make([]*os.File, 0, len(input.sources))
	defer func() {
		for _, client := range admitters {
			if client != nil {
				_ = client.Close()
			}
		}
		for _, file := range journals {
			returnErr = errors.Join(returnErr, file.Close())
		}
	}()
	for shard := range input.sources {
		file, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("ack-shard-%02d.jsonl", shard)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		journals = append(journals, file)
	}
	// Full verification precedes all welcomes. Parallel, bounded dialing avoids
	// leaving the earliest welcome idle while a later shard's dial stalls.
	dial, stopDial := context.WithTimeout(ctx, 2*time.Second)
	dialErrors := make([]error, len(admitters))
	var wg sync.WaitGroup
	for shard := range admitters {
		wg.Add(1)
		go func(shard int) {
			defer wg.Done()
			client, err := sharedexec.DialBoundedResumeIngress(dial, filepath.Join(input.runtime.OwnerDir, "execution-rpc", "ingress.sock"), p, expectedPlan, initial.Epoch, uint16(shard))
			dialErrors[shard] = err
			if err == nil {
				admitters[shard] = client
			}
		}(shard)
	}
	wg.Wait()
	stopDial()
	for _, err := range dialErrors {
		if err != nil {
			return err
		}
	}
	readers := make([]*bufio.Reader, len(input.sources))
	scratch := make([][]byte, len(input.sources))
	for shard, source := range input.sources {
		readers[shard] = bufio.NewReaderSize(source.file, 64<<10)
	}
	counters := new(boundedStreamCounters)
	budget := &boundedSenderBudget{plan: p}
	defer func() { result.Stream, result.Budget = counters.boundedSample(), budget.sample() }()
	for _, planned := range schedule.Frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		shard := int(planned.ID.Shard)
		before := time.Now()
		frame, next, err := sharedexec.ReadCorpusFrame(readers[shard], scratch[shard])
		scratch[shard] = next
		counters.readNanos.Add(uint64(time.Since(before)))
		if err != nil {
			return err
		}
		if err := frame.Validate(input.manifest.Descriptor); err != nil {
			return err
		}
		if frame.CorpusFrameID != planned.ID || frame.PayloadHash != planned.PayloadHash || len(frame.Payload) != int(planned.PayloadBytes) || frame.Transactions != 500 || result.Prefix[shard].Sequence != planned.ID.Sequence {
			return errors.New("finite selected original frame differs or skips a shard sequence")
		}
		counters.read.Add(500)
		counters.encodedBytes.Add(uint64(sharedexec.CorpusWireHeaderSize + len(frame.Payload)))
		ack, err := admitBoundedFiniteFrame(ctx, admitters[shard], frame, planned, expectedPlan, initial, counters, budget, journals[shard])
		if err != nil {
			return err
		}
		result.SenderNew[planned.SenderIndex] += uint64(ack.NewlyAdmitted)
		result.Prefix[shard].Sequence++
		result.Prefix[shard].Transactions += uint64(ack.NewlyAdmitted)
	}
	if err := syncBoundedJournals(journals); err != nil {
		return err
	}
	probe, stopProbe := context.WithTimeout(ctx, 2*time.Second)
	final, err := input.owner.StrictIngressStatus(probe)
	stopProbe()
	if err != nil {
		return err
	}
	result.OwnerFinal = final
	minimumCrypto := uint64(len(schedule.Frames)) * 500
	attempts := counters.busy.Load() + counters.snapshot.Load()
	if final.Identity != initial.Identity || final.Epoch != initial.Epoch || final.ManifestHash != initial.ManifestHash || final.CanonicalHash != initial.CanonicalHash || final.CanonicalNumber != initial.CanonicalNumber || final.Reconciling || final.NewlyAdmitted != schedule.NewlyAdmitted || final.PendingTransactions != int(schedule.NewlyAdmitted) || final.ReservedTransactions != 0 || final.ReservedEncodedBytes != 0 || final.RetainedEncodedBytes != int(schedule.RetainedBytes) || final.CryptoVerified < minimumCrypto || final.CryptoVerified > minimumCrypto+500*attempts || counters.admitted.Load() != schedule.NewlyAdmitted || counters.skipped.Load() != schedule.CanonicalSkipped || counters.frames.Load() != uint64(len(schedule.Frames)) {
		return errors.New("finite fully acknowledged preload totals/credits/baseline differ")
	}
	result.FilesAfter, err = checkBoundedSources(input.sources, input.scan.Source.FilesBefore)
	if err != nil {
		return err
	}
	if _, err := readResumePin(planPin, sharedexec.ResumeMaxPlanDocumentBytes); err != nil {
		return err
	}
	if _, err := readResumePin(baselinePin, 16<<20); err != nil {
		return err
	}
	current, err := strictMetadata(runtimePath, 2<<20)
	if err != nil || !bytes.Equal(current, input.runtimeRaw) {
		return errors.New("finite runtime metadata changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result.Passed = true
	return nil
}

// A failed publication or durable journal write terminates this finite dispatch.
// Budget stays charged unless the protocol explicitly confirmed zero admission.
func admitBoundedFiniteFrame(ctx context.Context, client boundedAdmitter, frame sharedexec.CorpusFrame, planned sharedexec.BoundedResumeFrame, planID types.Hash, initial sharedexec.StrictIngressStatus, counters *boundedStreamCounters, budget *boundedSenderBudget, journal io.Writer) (sharedexec.ResumeIngressAck, error) {
	if err := budget.reserve(planned); err != nil {
		return sharedexec.ResumeIngressAck{}, err
	}
	ack, accepted, err := boundedAdmitFrame(ctx, ctx, client, frame, planned, planID, initial.Epoch, counters)
	if err != nil {
		return sharedexec.ResumeIngressAck{}, err
	}
	if !accepted {
		budget.releaseKnownZero(planned)
		return sharedexec.ResumeIngressAck{}, errors.New("finite dispatch stopped before known publication")
	}
	expected := counters.admitted.Load() + uint64(ack.NewlyAdmitted)
	status := ack.Status
	if status.Identity != initial.Identity || status.ManifestHash != initial.ManifestHash || status.CanonicalHash != initial.CanonicalHash || status.CanonicalNumber != initial.CanonicalNumber || status.Reconciling || status.NewlyAdmitted != expected || status.PendingTransactions != int(expected) || status.ReservedTransactions != 0 || status.ReservedEncodedBytes != 0 {
		return sharedexec.ResumeIngressAck{}, errors.New("finite preload owner advanced or admission state differs before production")
	}
	if err := json.NewEncoder(journal).Encode(boundedReceipt{planned, ack}); err != nil {
		return sharedexec.ResumeIngressAck{}, err
	}
	counters.admitted.Add(uint64(ack.NewlyAdmitted))
	counters.skipped.Add(uint64(ack.CanonicalSkipped))
	counters.frames.Add(1)
	return ack, nil
}
