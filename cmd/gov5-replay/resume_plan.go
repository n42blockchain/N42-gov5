package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/internal/sharedvalidator"
)

// These pins identify historical audit documents. They do not grant an owner
// capability, attest the current database, or authorize transaction dispatch.
type resumePinnedInput struct {
	Path   string     `json:"path"`
	SHA256 types.Hash `json:"sha256"`
}

type resumePlanInputs struct {
	SourceRoot string            `json:"source_root"`
	Manifest   resumePinnedInput `json:"manifest"`
	Runtime    resumePinnedInput `json:"runtime"`
	Outer      resumePinnedInput `json:"recovery_outer"`
	Recovery   resumePinnedInput `json:"recovery"`
	Original   resumePinnedInput `json:"original_restore"`
}

type resumeReportAnchor struct {
	Number uint64     `json:"number"`
	Hash   types.Hash `json:"hash"`
	Root   types.Hash `json:"root"`
}

type resumeOuterReport struct {
	Kind         string             `json:"kind"`
	Passed       bool               `json:"passed"`
	Error        string             `json:"error"`
	OriginalHash string             `json:"original_proof_sha256"`
	RecoveryPath string             `json:"recovery_result"`
	NonceSum     uint64             `json:"canonical_nonce_sum"`
	VectorSHA256 types.Hash         `json:"account_vector_sha256"`
	Canonical    resumeReportAnchor `json:"canonical"`
}

type resumeOriginalReport struct {
	Kind   string `json:"kind"`
	Passed bool   `json:"passed"`
	Error  string `json:"error"`
	Audit  struct {
		Kind          string             `json:"kind"`
		Passed        bool               `json:"passed"`
		GenesisSHA256 string             `json:"genesis_sha256"`
		Genesis       resumeReportAnchor `json:"genesis"`
		Canonical     resumeReportAnchor `json:"canonical"`
		Applied       resumeReportAnchor `json:"applied"`
	} `json:"physical_reopen_audit"`
}

// No self hash, PlanID, session, path or timestamp is included in this semantic
// document. Its actual newline-terminated bytes are bound by the later plan.
type resumeSourceProof struct {
	Kind          string                   `json:"kind"`
	Identity      sharedexec.Identity      `json:"identity"`
	Manifest      types.Hash               `json:"manifest_sha256"`
	CorpusID      types.Hash               `json:"corpus_id"`
	Genesis       types.Hash               `json:"genesis_file_sha256"`
	Shards        []sharedexec.CorpusShard `json:"shards"`
	Witnesses     []resumeSourceWitness    `json:"witnesses"`
	MappingSHA256 types.Hash               `json:"mapping_sha256"`
}

type resumePlanDocument struct {
	Kind   string                `json:"kind"`
	Plan   sharedexec.ResumePlan `json:"plan"`
	PlanID types.Hash            `json:"plan_id"`
}

type resumePlanResult struct {
	Kind                     string                 `json:"kind"`
	Passed                   bool                   `json:"passed"`
	Error                    string                 `json:"error,omitempty"`
	Inputs                   resumePlanInputs       `json:"inputs"`
	SourceProofSHA256        types.Hash             `json:"source_proof_sha256"`
	PlanFileSHA256           types.Hash             `json:"plan_file_sha256"`
	PlanID                   types.Hash             `json:"plan_id"`
	FilesBefore              []resumeSourceMetadata `json:"source_files_before"`
	FilesAfter               []resumeSourceMetadata `json:"source_files_after"`
	FullSourceBytes          uint64                 `json:"full_source_bytes"`
	FullSourceShards         uint16                 `json:"full_source_shards"`
	MappingSignatureChecks   uint64                 `json:"mapping_signature_checks"`
	SelectedSignatureChecks  uint64                 `json:"selected_frame_signature_checks"`
	CanonicalSkipped         uint64                 `json:"selected_canonical_skipped"`
	NewlyAdmitted            uint64                 `json:"planned_new_transactions"`
	OwnerSessionEnabled      bool                   `json:"owner_session_enabled"`
	CurrentSnapshotRechecked bool                   `json:"current_canonical_snapshot_rechecked"`
	Dispatched               uint64                 `json:"dispatched_transactions"`
	TemporaryGenesisMDBXUsed bool                   `json:"temporary_genesis_mdbx_used"`
}

func resumeHashText(text string) (types.Hash, error) {
	raw, err := hex.DecodeString(strings.TrimPrefix(text, "0x"))
	if err != nil || len(raw) != 32 || bytes.Equal(raw, make([]byte, 32)) {
		return types.Hash{}, errors.New("explicit nonzero SHA256 required")
	}
	return types.BytesToHash(raw), nil
}

// Reject duplicate keys at every depth before decoding projections of pinned
// historical reports. Projections intentionally do not reinterpret their full
// earlier proof; that proof's exact reviewed bytes remain a required input pin.
func resumeDecodeJSON(raw []byte, out any, strict bool) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("resume JSON nesting exceeds limit")
		}
		t, err := d.Token()
		if err != nil {
			return err
		}
		delim, nested := t.(json.Delim)
		if !nested {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]bool)
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || strings.IndexFunc(name, func(r rune) bool { return r > 127 }) >= 0 {
					return errors.New("resume JSON requires ASCII object keys")
				}
				// encoding/json matches struct fields case-insensitively.
				// Reject aliases as well as exact duplicate spellings.
				folded := strings.ToLower(name)
				if seen[folded] {
					return errors.New("resume JSON duplicate/invalid object key")
				}
				seen[folded] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid resume JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("one resume JSON value required")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	if strict {
		d.DisallowUnknownFields()
	}
	return d.Decode(out)
}

func readResumePin(input resumePinnedInput, limit int64) ([]byte, error) {
	if input.SHA256 == (types.Hash{}) {
		return nil, errors.New("resume input lacks exact SHA256 pin")
	}
	raw, err := strictMetadata(input.Path, limit)
	if err != nil {
		return nil, err
	}
	if types.Hash(sha256.Sum256(raw)) != input.SHA256 {
		return nil, fmt.Errorf("resume pinned input changed: %s", input.Path)
	}
	return raw, nil
}

func resumeHistoricalBaseline(inputs resumePlanInputs, m supplyManifest, runtime strictRuntime, outerRaw, recoveryRaw, originalRaw []byte) (strictRecoveryResult, resumeReportAnchor, error) {
	var outer resumeOuterReport
	var recovery strictRecoveryResult
	var original resumeOriginalReport
	if err := resumeDecodeJSON(outerRaw, &outer, false); err != nil {
		return recovery, outer.Canonical, err
	}
	if err := resumeDecodeJSON(recoveryRaw, &recovery, true); err != nil {
		return recovery, outer.Canonical, err
	}
	if err := resumeDecodeJSON(originalRaw, &original, false); err != nil {
		return recovery, outer.Canonical, err
	}
	originalHash, err := resumeHashText(outer.OriginalHash)
	if err != nil || originalHash != inputs.Original.SHA256 || outer.Kind != "same-data-recovery-q2-canonical-nonce-audit-not-tps" || !outer.Passed || outer.Error != "" || outer.RecoveryPath != inputs.Recovery.Path || outer.Canonical.Number == 0 || outer.Canonical.Hash == (types.Hash{}) || outer.Canonical.Root == (types.Hash{}) {
		return recovery, outer.Canonical, errors.New("resume historical outer proof differs")
	}
	a := original.Audit
	if original.Kind != "gov5-qmdb-original-two-leaf-restore-v1" || !original.Passed || original.Error != "" || !a.Passed || a.Kind != "gov5-qmdb-missing-genesis-leaves-readonly-audit-v2" || a.Canonical != outer.Canonical || a.Applied != outer.Canonical || a.Genesis.Number != 0 || a.Genesis.Hash != m.Descriptor.Identity.GenesisHash || a.GenesisSHA256 != strings.TrimPrefix(m.Descriptor.GenesisFileHash.Hex(), "0x") {
		return recovery, outer.Canonical, errors.New("resume original restore anchor differs")
	}
	if recovery.Kind != "strict-owner-queue-recovery-no-dispatch-not-tps" || !recovery.Passed || recovery.Error != "" || recovery.Identity != m.Descriptor.Identity || recovery.ManifestHash != m.Digest || recovery.RuntimeManifest != inputs.Runtime.Path || recovery.Dispatched != 0 || recovery.NonceSum != outer.NonceSum || recovery.VectorSHA256 != outer.VectorSHA256 || len(recovery.Initial) != 7 || len(recovery.Final) != 7 || len(recovery.Accounts) != int(m.Descriptor.SenderCount) {
		return recovery, outer.Canonical, errors.New("resume historical recovery/vector binding differs")
	}
	vector, err := json.Marshal(recovery.Accounts)
	if err != nil || types.Hash(sha256.Sum256(vector)) != recovery.VectorSHA256 {
		return recovery, outer.Canonical, errors.New("resume historical account vector hash differs")
	}
	epoch := recovery.OwnerFinal.Epoch
	if epoch == ([16]byte{}) || recovery.OwnerInitial.Epoch != epoch {
		return recovery, outer.Canonical, errors.New("resume historical owner epoch differs")
	}
	for _, owner := range []sharedexec.StrictIngressStatus{recovery.OwnerInitial, recovery.OwnerFinal} {
		if err := recoverySession(owner, epoch, m); err != nil || !strictQueueEmpty(owner) || owner.CanonicalNumber != outer.Canonical.Number || owner.CanonicalHash != outer.Canonical.Hash {
			return recovery, outer.Canonical, errors.New("resume historical owner is not an empty matching canonical session")
		}
	}
	if runtime.Identity != recovery.Identity {
		return recovery, outer.Canonical, errors.New("resume historical runtime identity differs")
	}
	for _, observations := range [][]sharedvalidator.StatusResult{recovery.Initial, recovery.Final} {
		for _, status := range observations {
			if err := strictValidatorBinding(status, epoch, m); err != nil || status.ProducerRunning || status.QueuedTransactions != 0 || status.Number != outer.Canonical.Number || status.BlockHash != outer.Canonical.Hash || status.CommittedExecutedTransactions != outer.NonceSum {
				return recovery, outer.Canonical, errors.New("resume historical seven-validator canonical evidence differs")
			}
		}
	}
	return recovery, outer.Canonical, nil
}

func resumeJSONBytes(value any) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func writeResumeArtifact(path string, value any) (types.Hash, error) {
	expected, err := resumeJSONBytes(value)
	if err != nil {
		return types.Hash{}, err
	}
	if err := exclusiveJSON(path, value); err != nil {
		return types.Hash{}, err
	}
	raw, err := strictMetadata(path, 16<<20)
	if err != nil || !bytes.Equal(raw, expected) {
		return types.Hash{}, errors.New("resume artifact bytes differ after fsync")
	}
	return types.Hash(sha256.Sum256(raw)), nil
}

func syncResumeDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(dir.Sync(), dir.Close())
}

// Publication callbacks are private test seams. The production entrypoint fixes
// both implementations; neither can be selected by an input document or flag.
type resumePlanPublication struct {
	write func(string, any) (types.Hash, error)
	sync  func(string) error
}

// runResumePlanOnly never opens the retained execution database or a socket.
// Existing GenesisBlock.ToBlock uses a temporary MDBX to compute genesis. All
// historical input reports still require a fresh owner-side verification later.
func runResumePlanOnly(ctx context.Context, inputs resumePlanInputs, output, runtimeBase, outputBase string) (returnErr error) {
	return runResumePlanOnlyPublication(ctx, inputs, output, runtimeBase, outputBase, resumePlanPublication{write: writeResumeArtifact, sync: syncResumeDirectory})
}

func runResumePlanOnlyPublication(ctx context.Context, inputs resumePlanInputs, output, runtimeBase, outputBase string, publication resumePlanPublication) (returnErr error) {
	if ctx == nil || ctx.Err() != nil {
		return errors.New("active resume plan context required")
	}
	if inputs.Manifest.Path != filepath.Join(inputs.SourceRoot, "manifest.json") || !filepath.IsAbs(output) || filepath.Clean(output) != output || !stringsHasPrefixPath(filepath.Dir(output), outputBase) || filepath.Base(output) != "plan-output" {
		return errors.New("explicit source manifest and fresh validation plan-output required")
	}
	if err := strictPrivatePath(filepath.Dir(output), true, false); err != nil {
		return err
	}
	manifestRaw, err := readResumePin(inputs.Manifest, sharedexec.CorpusMaxManifestBytes)
	if err != nil {
		return err
	}
	var manifest sharedexec.CorpusManifest
	if err := resumeDecodeJSON(manifestRaw, &manifest, true); err != nil {
		return err
	}
	m, err := parseSupplyManifest(manifestRaw)
	if err != nil {
		return err
	}
	runtimeRaw, err := readResumePin(inputs.Runtime, 2<<20)
	if err != nil {
		return err
	}
	var runtimeDocument strictRuntime
	if err := resumeDecodeJSON(runtimeRaw, &runtimeDocument, true); err != nil {
		return err
	}
	runtime, err := loadStrictRuntime(inputs.Runtime.Path, inputs.SourceRoot, runtimeBase, m)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(runtime, runtimeDocument) {
		return errors.New("resume runtime validation read differs from pinned document")
	}
	outerRaw, err := readResumePin(inputs.Outer, 16<<20)
	if err != nil {
		return err
	}
	recoveryRaw, err := readResumePin(inputs.Recovery, 16<<20)
	if err != nil {
		return err
	}
	originalRaw, err := readResumePin(inputs.Original, 16<<20)
	if err != nil {
		return err
	}
	baseline, anchor, err := resumeHistoricalBaseline(inputs, m, runtime, outerRaw, recoveryRaw, originalRaw)
	if err != nil {
		return err
	}
	genesisRaw, err := strictMetadata(filepath.Join(inputs.SourceRoot, "genesis.json"), 16<<20)
	if err != nil {
		return err
	}
	runtimeGenesis, err := strictMetadata(runtime.Genesis, 16<<20)
	if err != nil || !bytes.Equal(genesisRaw, runtimeGenesis) {
		return errors.New("resume source/runtime exact genesis bytes differ")
	}
	if err := os.Mkdir(output, 0700); err != nil {
		return err
	}
	result := resumePlanResult{Kind: "gov5-resume-plan-only-candidate-no-dispatch-v1", Inputs: inputs, TemporaryGenesisMDBXUsed: true}
	defer func() {
		if returnErr != nil {
			result.Passed = false
			result.Error = returnErr.Error()
			returnErr = errors.Join(returnErr, exclusiveJSON(filepath.Join(output, "failure.json"), result))
		}
	}()
	if err := publication.sync(filepath.Dir(output)); err != nil {
		return err
	}
	scan, err := scanResumeSources(ctx, inputs.SourceRoot, m, genesisRaw, baseline.Accounts)
	if err != nil {
		return err
	}
	proof := resumeSourceProof{Kind: "gov5-resume-source-mapping-v1", Identity: m.Descriptor.Identity, Manifest: m.Digest, CorpusID: m.CorpusID, Genesis: m.Descriptor.GenesisFileHash, Shards: m.Shards, Witnesses: scan.Witnesses, MappingSHA256: scan.MappingSHA256}
	proofBytes, err := resumeJSONBytes(proof)
	if err != nil {
		return err
	}
	p := sharedexec.ResumePlan{Version: 1, Identity: m.Descriptor.Identity, ManifestSHA256: m.Digest, CorpusID: m.CorpusID, GenesisFileSHA256: m.Descriptor.GenesisFileHash, SourceProofSHA256: types.Hash(sha256.Sum256(proofBytes)), MappingSHA256: scan.MappingSHA256, RecoveryOuterSHA256: inputs.Outer.SHA256, RecoverySHA256: inputs.Recovery.SHA256, AccountVectorSHA256: baseline.VectorSHA256, OriginalRestoreSHA256: inputs.Original.SHA256, Canonical: sharedexec.ResumeCanonicalAnchor{Number: anchor.Number, Hash: anchor.Hash, Root: anchor.Root}, CanonicalNonceSum: baseline.NonceSum, Senders: scan.Senders, Frames: scan.Frames}
	planID, err := sharedexec.ResumePlanID(p, manifestRaw, baseline.Accounts)
	if err != nil {
		return err
	}
	for _, input := range []resumePinnedInput{inputs.Manifest, inputs.Runtime, inputs.Outer, inputs.Recovery, inputs.Original} {
		if _, err := readResumePin(input, 16<<20); err != nil {
			return err
		}
	}
	for _, path := range []string{filepath.Join(inputs.SourceRoot, "genesis.json"), runtime.Genesis} {
		raw, err := strictMetadata(path, 16<<20)
		if err != nil || !bytes.Equal(raw, genesisRaw) {
			return errors.New("resume genesis bytes changed during scan")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	result.SourceProofSHA256, err = publication.write(filepath.Join(output, "source-proof.json"), proof)
	if err != nil || result.SourceProofSHA256 != p.SourceProofSHA256 {
		return errors.New("resume source proof publication differs")
	}
	result.PlanFileSHA256, err = publication.write(filepath.Join(output, "plan.json"), resumePlanDocument{Kind: "gov5-resume-four-frame-plan-v1", Plan: p, PlanID: planID})
	if err != nil {
		return err
	}
	result.PlanID = planID
	result.FilesBefore, result.FilesAfter = scan.FilesBefore, scan.FilesAfter
	result.FullSourceBytes, result.FullSourceShards = scan.FullSourceBytes, scan.FullSourceShards
	result.MappingSignatureChecks, result.SelectedSignatureChecks = scan.MappingSignatureChecks, scan.SelectedFrameSignatureChecks
	for _, frame := range scan.Frames {
		result.CanonicalSkipped += uint64(frame.CanonicalSkipped)
		result.NewlyAdmitted += uint64(frame.NewlyAdmitted)
	}
	if err := publication.sync(output); err != nil {
		return err
	}
	result.Passed = true
	if _, err := publication.write(filepath.Join(output, "result-candidate.json"), result); err != nil {
		return err
	}
	// A candidate alone is never a promoted result: an external controller must
	// also require our rc=0, no failure record, exact artifact hashes, and normal
	// claim-tail completion before publishing its independent success evidence.
	if err := publication.sync(output); err != nil {
		return err
	}
	return ctx.Err()
}
