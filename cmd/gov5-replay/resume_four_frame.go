package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
)

type resumeFourFrameReceipt struct {
	Frame sharedexec.ResumeFrame      `json:"frame"`
	Ack   sharedexec.ResumeIngressAck `json:"ack"`
}

type resumeFourFrameResult struct {
	Kind                string                         `json:"kind"`
	Passed              bool                           `json:"passed"`
	Error               string                         `json:"error,omitempty"`
	Identity            sharedexec.Identity            `json:"identity"`
	RuntimeManifest     string                         `json:"runtime_manifest"`
	RuntimeSHA256       types.Hash                     `json:"runtime_sha256"`
	PlanSHA256          types.Hash                     `json:"plan_sha256"`
	PlanID              types.Hash                     `json:"plan_id"`
	OwnerInitial        sharedexec.StrictIngressStatus `json:"owner_initial"`
	OwnerFinal          sharedexec.StrictIngressStatus `json:"owner_final"`
	Acknowledgments     []resumeFourFrameReceipt       `json:"acknowledgments"`
	CanonicalSkipped    uint64                         `json:"canonical_skipped"`
	NewlyAdmitted       uint64                         `json:"newly_admitted"`
	KnownZeroRetries    uint64                         `json:"known_zero_retries"`
	ProductionStarted   bool                           `json:"production_started"`
	CommitProofIncluded bool                           `json:"commit_proof_included"`
}

// Four frames are admitted before the external launcher starts any validator.
// This result is admission evidence only; canonical execution and physical
// restart acceptance are independent mandatory steps in that launcher.
func runResumeFourFrame(parent context.Context, root, runtimePath, attempt string, planPin resumePinnedInput, expectedPlan types.Hash) (returnErr error) {
	if !stringsHasPrefixPath(root, supplyRoot) || expectedPlan == (types.Hash{}) {
		return errors.New("explicit original source and expected plan ID required")
	}
	ctx, cancel := context.WithTimeout(parent, 45*time.Second)
	defer cancel()
	m, err := loadSupplyManifest(root)
	if err != nil {
		return err
	}
	runtimeRaw, err := strictMetadata(runtimePath, 2<<20)
	if err != nil {
		return err
	}
	var pinnedRuntime strictRuntime
	if err := resumeDecodeJSON(runtimeRaw, &pinnedRuntime, true); err != nil {
		return err
	}
	runtime, err := loadStrictRuntime(runtimePath, root, strictRuntimeBase, m)
	if err != nil || !reflect.DeepEqual(runtime, pinnedRuntime) {
		return errors.New("four-frame runtime metadata changed or invalid")
	}
	planRaw, err := readResumePin(planPin, sharedexec.ResumeMaxPlanDocumentBytes)
	if err != nil {
		return err
	}
	document, err := sharedexec.DecodeResumePlanDocument(planRaw)
	if err != nil {
		return err
	}
	p := document.Plan
	if document.PlanID != expectedPlan || p.Identity != runtime.Identity || p.CorpusID != m.CorpusID || p.ManifestSHA256 != m.Digest || p.GenesisFileSHA256 != m.Descriptor.GenesisFileHash {
		return errors.New("four-frame plan/source/runtime binding differs")
	}
	frames, checkSources, closeSources, err := readResumeFourFrames(ctx, root, m, p.Frames)
	if err != nil {
		return err
	}
	defer closeSources()
	for _, path := range []string{runtime.OwnerSocket, filepath.Join(runtime.OwnerDir, "execution-rpc", "ingress.sock")} {
		if err := strictPrivatePath(path, false, true); err != nil {
			return err
		}
	}
	owner := sharedexec.NewClient(runtime.OwnerSocket, runtime.Identity)
	defer owner.Close()
	initial, err := owner.StrictIngressStatus(ctx)
	if err != nil {
		return err
	}
	if initial.Identity != p.Identity || initial.ManifestHash != m.Digest || initial.Epoch == ([16]byte{}) || initial.CanonicalHash != p.Canonical.Hash || initial.CanonicalNumber != p.Canonical.Number || !strictQueueEmpty(initial) || initial.NewlyAdmitted != 0 || initial.CryptoVerified != 0 {
		return errors.New("four-frame owner must be newly armed at the exact canonical baseline")
	}
	dir, err := strictAttempt(runtime.Root, attempt)
	if err != nil {
		return err
	}
	if err := syncResumeDirectory(filepath.Dir(dir)); err != nil {
		return err
	}
	result := resumeFourFrameResult{Kind: "gov5-resume-four-frame-admission-candidate-v1", Identity: runtime.Identity, RuntimeManifest: runtimePath, RuntimeSHA256: types.Hash(sha256.Sum256(runtimeRaw)), PlanSHA256: planPin.SHA256, PlanID: expectedPlan, OwnerInitial: initial}
	defer func() {
		if returnErr != nil {
			result.Passed, result.Error = false, returnErr.Error()
		}
		returnErr = errors.Join(returnErr, exclusiveJSON(filepath.Join(dir, "result-candidate.json"), result), syncResumeDirectory(dir), ctx.Err())
	}()
	intent := struct {
		Kind   string                   `json:"kind"`
		PlanID types.Hash               `json:"plan_id"`
		Epoch  [16]byte                 `json:"epoch"`
		Frames []sharedexec.ResumeFrame `json:"frames"`
	}{"gov5-resume-four-frame-dispatch-intent-v1", expectedPlan, initial.Epoch, p.Frames}
	if err := exclusiveJSON(filepath.Join(dir, "dispatch-intent.json"), intent); err != nil {
		return err
	}
	if err := syncResumeDirectory(dir); err != nil {
		return err
	}
	for i, selected := range p.Frames {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := checkSources(); err != nil {
			return err
		}
		client, err := sharedexec.DialResumeIngress(ctx, filepath.Join(runtime.OwnerDir, "execution-rpc", "ingress.sock"), runtime.Identity, m.Digest, expectedPlan, initial.Epoch, selected.ID.Shard)
		if err != nil {
			return err
		}
		ack, retries, err := admitResumeFourFrame(ctx, frames[i], selected, client.Admit)
		_ = client.Close()
		result.KnownZeroRetries += retries
		if err != nil {
			return err // An ambiguous outcome is never retried or reconnected.
		}
		if ack.PlanID != expectedPlan || ack.Status.Epoch != initial.Epoch || ack.Duplicate || ack.NewlyAdmitted != int(selected.NewlyAdmitted) || ack.CanonicalSkipped != int(selected.CanonicalSkipped) || ack.PriorAdmission != int(selected.NewlyAdmitted) {
			return errors.New("four-frame acknowledgment differs from sealed plan")
		}
		receipt := resumeFourFrameReceipt{Frame: selected, Ack: ack}
		result.Acknowledgments = append(result.Acknowledgments, receipt)
		result.NewlyAdmitted += uint64(ack.NewlyAdmitted)
		result.CanonicalSkipped += uint64(ack.CanonicalSkipped)
		if err := exclusiveJSON(filepath.Join(dir, fmt.Sprintf("ack-%d.json", i)), receipt); err != nil {
			return err
		}
		if err := syncResumeDirectory(dir); err != nil {
			return err
		}
	}
	result.OwnerFinal, err = owner.StrictIngressStatus(ctx)
	if err != nil {
		return err
	}
	final := result.OwnerFinal
	var retained int
	for _, frame := range p.Frames {
		if frame.NewlyAdmitted != 0 {
			retained += int(frame.PayloadBytes)
		}
	}
	if result.NewlyAdmitted != 1417 || result.CanonicalSkipped != 583 || final.Identity != initial.Identity || final.Epoch != initial.Epoch || final.ManifestHash != initial.ManifestHash || final.CanonicalHash != initial.CanonicalHash || final.CanonicalNumber != initial.CanonicalNumber || final.Reconciling || final.NewlyAdmitted != 1417 || final.PendingTransactions != 1417 || final.ReservedTransactions != 0 || final.ReservedEncodedBytes != 0 || final.RetainedEncodedBytes != retained || final.CryptoVerified < 2000 {
		return errors.New("four-frame owner admission totals/credits/baseline differ")
	}
	if err := checkSources(); err != nil {
		return err
	}
	if _, err := readResumePin(planPin, sharedexec.ResumeMaxPlanDocumentBytes); err != nil {
		return err
	}
	current, err := strictMetadata(runtimePath, 2<<20)
	if err != nil || types.Hash(sha256.Sum256(current)) != result.RuntimeSHA256 {
		return errors.New("four-frame runtime metadata changed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result.Passed = true
	return nil
}

// Only typed, explicit zero-publication outcomes allow another synchronous call
// on this same connection. Every I/O call has a fresh five-second bound.
func admitResumeFourFrame(ctx context.Context, frame sharedexec.CorpusFrame, selected sharedexec.ResumeFrame, admit func(context.Context, sharedexec.CorpusFrame, sharedexec.ResumeFrame) (sharedexec.ResumeIngressAck, error)) (sharedexec.ResumeIngressAck, uint64, error) {
	stop := time.Now().Add(5 * time.Second)
	var retries uint64
	for {
		if err := ctx.Err(); err != nil {
			return sharedexec.ResumeIngressAck{}, retries, err
		}
		call, cancel := context.WithTimeout(ctx, 5*time.Second)
		ack, err := admit(call, frame, selected)
		cancel()
		if err == nil {
			return ack, retries, nil
		}
		if !errors.Is(err, sharedexec.ErrIngressCreditsExhausted) && !errors.Is(err, sharedexec.ErrIngressSnapshotChanged) {
			return ack, retries, err
		}
		if ack.NewlyAdmitted != 0 || ack.PriorAdmission != 0 || ack.CanonicalSkipped != 0 || ack.Duplicate || time.Now().After(stop) {
			return ack, retries, errors.New("four-frame known-zero retry exhausted or malformed")
		}
		retries++
		select {
		case <-ctx.Done():
			return sharedexec.ResumeIngressAck{}, retries, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// The owner independently authenticated every complete shard before arming.
// This dispatch path rereads only the four exact pinned original frames, keeps
// the descriptors open, and checks their metadata until all final ACKs arrive.
func readResumeFourFrames(ctx context.Context, root string, m supplyManifest, selected []sharedexec.ResumeFrame) (frames []sharedexec.CorpusFrame, check func() error, closeFiles func(), err error) {
	if len(selected) != 4 {
		return nil, nil, nil, errors.New("exactly four sealed original frames required")
	}
	var files []strictSource
	var paths []string
	closeFiles = func() { closeStrictSources(files) }
	defer func() {
		if err != nil {
			closeFiles()
			frames, check, closeFiles = nil, nil, nil
		}
	}()
	for _, target := range selected {
		if int(target.ID.Shard) >= len(m.Shards) {
			return nil, nil, closeFiles, errors.New("four-frame shard invalid")
		}
		sender, firstNonce, count, rangeErr := m.Descriptor.FrameRange(target.ID.Shard, target.ID.Sequence)
		if rangeErr != nil || target.ID.CorpusID != m.CorpusID || sender != target.SenderIndex || firstNonce != 0 || count != 500 || uint32(target.CanonicalSkipped)+uint32(target.NewlyAdmitted) != 500 {
			return nil, nil, closeFiles, errors.New("four-frame original range differs from bounded plan")
		}
		path := filepath.Join(root, m.Shards[target.ID.Shard].File)
		if err = strictPrivatePath(path, false, false); err != nil {
			return nil, nil, closeFiles, err
		}
		var before os.FileInfo
		before, err = os.Lstat(path)
		if err != nil {
			return nil, nil, closeFiles, err
		}
		stat, ok := before.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 || before.Size() != int64(m.Shards[target.ID.Shard].Bytes) {
			return nil, nil, closeFiles, errors.New("four-frame source size/link count differs")
		}
		var file *os.File
		file, err = os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, nil, closeFiles, err
		}
		files, paths = append(files, strictSource{file: file, info: before}), append(paths, path)
		reader := bufio.NewReaderSize(file, 128<<10)
		var frame sharedexec.CorpusFrame
		var scratch []byte
		for sequence := uint64(0); sequence <= target.ID.Sequence; sequence++ {
			if err = ctx.Err(); err != nil {
				return nil, nil, closeFiles, err
			}
			frame, scratch, err = sharedexec.ReadCorpusFrame(reader, scratch)
			if err != nil {
				return nil, nil, closeFiles, err
			}
			if frame.CorpusID != m.CorpusID || frame.Shard != target.ID.Shard || frame.Sequence != sequence {
				return nil, nil, closeFiles, errors.New("four-frame source position differs")
			}
		}
		if err = frame.Validate(m.Descriptor); err != nil {
			return nil, nil, closeFiles, err
		}
		if frame.CorpusFrameID != target.ID || frame.PayloadHash != target.PayloadHash || len(frame.Payload) != int(target.PayloadBytes) || frame.Transactions != 500 {
			return nil, nil, closeFiles, errors.New("four-frame original payload differs from sealed plan")
		}
		frames = append(frames, frame)
	}
	check = func() error {
		for i, source := range files {
			info, err := source.file.Stat()
			if err != nil || !sameResumeSourceMetadata(source.info, info) {
				return errors.New("four-frame source descriptor changed")
			}
			current, err := os.Lstat(paths[i])
			if err != nil || !sameResumeSourceMetadata(source.info, current) {
				return errors.New("four-frame source path changed")
			}
		}
		return nil
	}
	if err = check(); err != nil {
		return nil, nil, closeFiles, err
	}
	return frames, check, closeFiles, nil
}

func sameResumeSourceMetadata(before, after os.FileInfo) bool {
	if before == nil || after == nil || !os.SameFile(before, after) || before.Mode() != after.Mode() || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return false
	}
	a, aok := before.Sys().(*syscall.Stat_t)
	b, bok := after.Sys().(*syscall.Stat_t)
	return aok && bok && a.Uid == b.Uid && a.Nlink == b.Nlink && a.Ctim == b.Ctim
}
