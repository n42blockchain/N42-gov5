package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
)

type originalRestoreResult struct {
	Kind                 string             `json:"kind"`
	Passed               bool               `json:"passed"`
	ProofSHA256          string             `json:"promoted_audit_sha256"`
	CandidateProofSHA256 string             `json:"promoted_candidate_sha256"`
	Before               fileObservation    `json:"original_before"`
	After                fileObservation    `json:"original_after"`
	ProtectedBefore      []fileObservation  `json:"protected_before"`
	ProtectedAfter       []fileObservation  `json:"protected_after"`
	Audit                auditReport        `json:"physical_reopen_audit"`
	BeforeTables         []tableFingerprint `json:"before_tables"`
	AfterTables          []tableFingerprint `json:"after_tables"`
	OriginalOpened       bool               `json:"original_database_opened"`
	BackupOpened         bool               `json:"backup_database_opened"`
	AuditCopyOpened      bool               `json:"audit_copy_database_opened"`
	CandidateOpened      bool               `json:"candidate_database_opened"`
}

func readCandidateRestoreProof(path, expected string) (candidateRestoreResult, error) {
	var out candidateRestoreResult
	if filepath.Base(path) != "restore-result.json" {
		return out, errors.New("promoted candidate restore-result.json required")
	}
	if err := privateDirectory(filepath.Dir(path), validationRoot); err != nil {
		return out, err
	}
	want, err := hex.DecodeString(expected)
	if err != nil || len(want) != 32 || hex.EncodeToString(want) != expected {
		return out, errors.New("explicit lowercase candidate report SHA256 required")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return out, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return out, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64<<10 || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) {
		return out, errors.New("bounded private candidate proof required")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != expected {
		return out, errors.New("candidate report SHA256 differs")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&out); err != nil {
		return out, err
	}
	if d.Decode(new(any)) != io.EOF {
		return out, errors.New("trailing candidate report data")
	}
	return out, nil
}

func validateOriginalProof(p candidateRestoreResult, audit auditReport, auditSHA string) error {
	if !p.Passed || p.Kind != "gov5-qmdb-candidate-two-leaf-restore-v1" || p.ProofSHA256 != auditSHA || p.OriginalDatabaseOpened || p.BackupDatabaseOpened {
		return errors.New("successful candidate proof must bind the original promoted audit")
	}
	if len(p.SourcesBefore) != 3 || !reflect.DeepEqual(p.SourcesBefore, p.SourcesAfter) || p.SourcesBefore[0].Mode != 0600 || p.SourcesBefore[1].Mode != 0400 || p.SourcesBefore[2].Mode != 0400 {
		return errors.New("candidate proof source identities are incomplete or changed")
	}
	if p.Before.Path != p.After.Path || p.Before.Device != p.After.Device || p.Before.Inode != p.After.Inode || p.Before.Bytes != p.After.Bytes || p.Before.Mode != 0600 || p.After.Mode != 0400 || p.Before.SHA256 != audit.DatabaseSHA256 || p.After.SHA256 == p.Before.SHA256 {
		return errors.New("candidate physical restoration identity differs")
	}
	for _, source := range p.SourcesBefore {
		if source.SHA256 != audit.DatabaseSHA256 {
			return errors.New("candidate source snapshot differs from original audit")
		}
	}
	if !sameOtherRows(p.BeforeTables, p.AfterTables, true) || len(p.BeforeTables) == 0 || !p.Audit.Passed || p.Audit.DatabaseSHA256 != p.After.SHA256 || len(p.Audit.Forest.MissingGenesis) != 0 || !sameAuditedSnapshot(p.Audit, audit, true) {
		return errors.New("candidate proof does not establish exactly two rows and unchanged anchors")
	}
	return nil
}

func originalDataFile(dir string, expected fileObservation) (string, error) {
	if filepath.Base(dir) != "chaindata" || filepath.Base(filepath.Dir(dir)) != "e" || filepath.Join(dir, "mdbx.dat") != expected.Path {
		return "", errors.New("original path must equal the proved stopped runtime/e/chaindata")
	}
	if err := privateDirectory(dir, "/data/blockchain/gov5-performance-5m/runtime"); err != nil {
		return "", err
	}
	return expected.Path, nil
}

// restoreOriginalFile is reached only after the original-specific CLI has
// checked both promoted proofs, the immutable copies and the held node LOCK.
// There is no rename, file replacement, mode override or retry path.
func restoreOriginalFile(ctx context.Context, dir string, before fileObservation, proof auditReport, candidate candidateRestoreResult, g *auditedGenesis, auditSHA, candidateSHA, intent string, protected []fileObservation) (originalRestoreResult, error) {
	return restoreOriginalWithOpen(ctx, dir, before, proof, candidate, g, auditSHA, candidateSHA, intent, protected, openCandidateDB)
}

// The opener parameter permits a physical-reopen failure test after a real
// committed transaction. The production wrapper always supplies Accede MDBX;
// this is not an input or override exposed by either command.
func restoreOriginalWithOpen(ctx context.Context, dir string, before fileObservation, proof auditReport, candidate candidateRestoreResult, g *auditedGenesis, auditSHA, candidateSHA, intent string, protected []fileObservation, openDB func(context.Context, string, bool) (kv.RwDB, error)) (originalRestoreResult, error) {
	out := originalRestoreResult{Kind: "gov5-qmdb-original-two-leaf-restore-v1", ProofSHA256: auditSHA, CandidateProofSHA256: candidateSHA, Before: before, ProtectedBefore: protected}
	if err := validateOriginalProof(candidate, proof, auditSHA); err != nil {
		return out, err
	}
	if before != candidate.SourcesBefore[0] || before.Path != filepath.Join(dir, "mdbx.dat") || before.Mode != 0600 {
		return out, errors.New("original inode/times/mode/SHA differ from promoted candidate proof")
	}
	blobs, err := verifiedGenesisBlobs(g, proof)
	if err != nil {
		return out, err
	}
	// The exact proved original baseline is checked before any writable open.
	out.OriginalOpened = true
	err = func() error {
		db, err := openDB(ctx, dir, true)
		if err != nil {
			return err
		}
		defer db.Close()
		return db.View(ctx, func(tx kv.Tx) error {
			snapshot, err := auditBoundSnapshot(tx, g)
			if err != nil {
				return err
			}
			if !sameAuditedSnapshot(snapshot, proof, false) {
				return errors.New("original baseline differs from promoted audit")
			}
			out.BeforeTables, err = logicalFingerprints(ctx, tx)
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(out.BeforeTables, candidate.BeforeTables) || !sameOtherRows(out.BeforeTables, out.BeforeTables, false) {
				return errors.New("original baseline tables differ from proved candidate preimage")
			}
			return nil
		})
	}()
	if err != nil {
		return out, err
	}
	current, err := observeData(before.Path, 0600)
	if err != nil {
		return out, err
	}
	if current != before {
		return out, errors.New("original changed during readonly baseline")
	}
	if err := writeExclusiveJSON(intent, struct {
		Kind         string             `json:"kind"`
		AuditSHA     string             `json:"promoted_audit_sha256"`
		CandidateSHA string             `json:"promoted_candidate_sha256"`
		Original     fileObservation    `json:"original_preimage"`
		Protected    []fileObservation  `json:"protected_copies"`
		Targets      []leafCandidate    `json:"absent_targets_to_insert"`
		BeforeTables []tableFingerprint `json:"before_tables"`
	}{"gov5-qmdb-original-two-leaf-intent-v1", auditSHA, candidateSHA, before, protected, proof.Forest.MissingGenesis, out.BeforeTables}); err != nil {
		return out, err
	}
	err = func() error {
		db, err := openDB(ctx, dir, false)
		if err != nil {
			return err
		}
		defer db.Close()
		tx, err := db.BeginRw(ctx)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if err := restoreMissingGenesisRows(ctx, tx, g, proof, out.BeforeTables, blobs); err != nil {
			return err
		}
		return tx.Commit()
	}()
	if err != nil {
		return out, err
	}
	// Every RW transaction/environment is closed. Keep the original's 0600
	// mode and inode for the later separately gated node recovery.
	f, err := os.OpenFile(before.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return out, err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return out, err
	}
	after, err := observeData(before.Path, 0600)
	if err != nil {
		return out, err
	}
	if after.Device != before.Device || after.Inode != before.Inode || after.Bytes != before.Bytes || after.SHA256 == before.SHA256 {
		return out, errors.New("original physical identity/hash differs after write")
	}
	err = func() error {
		db, err := openDB(ctx, dir, true)
		if err != nil {
			return err
		}
		defer db.Close()
		return db.View(ctx, func(tx kv.Tx) error {
			for id, blob := range blobs {
				got, err := tx.GetOne(qmdb.LeavesTable, position(uint64(id)))
				if err != nil {
					return err
				}
				if !bytes.Equal(got, blob) {
					return errors.New("original reopen leaf bytes differ")
				}
			}
			out.Audit, err = auditBoundSnapshot(tx, g)
			if err != nil {
				return err
			}
			if len(out.Audit.Forest.MissingGenesis) != 0 || !sameAuditedSnapshot(out.Audit, proof, true) {
				return errors.New("original reopen does not reproduce snapshot without overlay")
			}
			out.AfterTables, err = logicalFingerprints(ctx, tx)
			if err != nil {
				return err
			}
			if !sameOtherRows(out.BeforeTables, out.AfterTables, true) || !reflect.DeepEqual(out.AfterTables, candidate.AfterTables) {
				return errors.New("original reopen differs from proved candidate logical rows/sequences")
			}
			return nil
		})
	}()
	if err != nil {
		return out, err
	}
	unchanged, err := observeData(before.Path, 0600)
	if err != nil {
		return out, err
	}
	if unchanged != after {
		return out, errors.New("original changed during physical readonly reopen")
	}
	out.After = after
	out.Audit.DatabaseSHA256, out.Audit.Passed = after.SHA256, true
	return out, nil // Outer protected-copy checks must complete before Passed.
}

func runRestoreOriginal(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("restore-original-two-leaves", flag.ContinueOnError)
	original := flags.String("original-db", "", "Exact stopped runtime/e/chaindata from the candidate proof")
	backup := flags.String("backup-data", "", "Immutable untouched-backup/mdbx.dat")
	auditDir := flags.String("audit-db", "", "Immutable preserved audit-copy/chaindata")
	candidateDir := flags.String("candidate-db", "", "Immutable proved restore-candidate/chaindata")
	proofPath := flags.String("proof", "", "Promoted v2 audit-result.json")
	proofSHA := flags.String("proof-sha256", "", "Exact promoted v2 audit SHA256")
	candidatePath := flags.String("candidate-proof", "", "Promoted candidate restore-result.json")
	candidateSHA := flags.String("candidate-proof-sha256", "", "Exact promoted candidate report SHA256")
	genesis := flags.String("genesis", "", "Original exact genesis bytes")
	output := flags.String("output", "", "New validation artifact original-observation.json")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || filepath.Base(*output) != "original-observation.json" {
		return errors.New("explicit original-observation.json and no positional arguments required")
	}
	artifact := filepath.Dir(*output)
	if err := privateDirectory(artifact, validationRoot); err != nil {
		return err
	}
	for _, name := range []string{"original-intent.json", "original-source-postcheck.json", "original-observation.json", "original-result.json"} {
		if _, err := os.Lstat(filepath.Join(artifact, name)); err == nil || !os.IsNotExist(err) {
			return errors.New("original restore attempt already started; never overwrite or retry")
		}
	}
	proof, err := readRestoreProof(*proofPath, *proofSHA)
	if err != nil {
		return err
	}
	candidate, err := readCandidateRestoreProof(*candidatePath, *candidateSHA)
	if err != nil {
		return err
	}
	if err := validateOriginalProof(candidate, proof, *proofSHA); err != nil {
		return err
	}
	originalPath, err := originalDataFile(*original, candidate.SourcesBefore[0])
	if err != nil {
		return err
	}
	auditPath, _, err := copyDataFile(*auditDir)
	if err != nil {
		return err
	}
	if *backup != candidate.SourcesBefore[1].Path || auditPath != candidate.SourcesBefore[2].Path || *backup != filepath.Join(filepath.Dir(filepath.Dir(*auditDir)), "untouched-backup", "mdbx.dat") {
		return errors.New("backup/audit paths differ from proved immutable sources")
	}
	if err := privateDirectory(filepath.Dir(*backup), validationRoot); err != nil {
		return err
	}
	candidateData, err := candidateDataFile(*candidateDir)
	if err != nil {
		return err
	}
	if candidateData != candidate.After.Path || *candidatePath != filepath.Join(filepath.Dir(filepath.Dir(*candidateDir)), "restore-result.json") || artifact == filepath.Dir(*candidatePath) || artifact == filepath.Dir(*proofPath) {
		return errors.New("candidate proof/data or new artifact path differs")
	}
	if *genesis != filepath.Join(filepath.Dir(filepath.Dir(*original)), "genesis.json") {
		return errors.New("genesis must belong to the proved original runtime")
	}
	g, err := prepareAuditGenesis(ctx, *genesis, proof.GenesisSHA256)
	if err != nil {
		return err
	}
	defer g.close()
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(*original), "LOCK"), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) {
		return errors.New("existing owned regular original LOCK required")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("original runtime is not exclusively stopped: %w", err)
	}
	before, err := observeData(originalPath, 0600)
	if err != nil {
		return err
	}
	if before != candidate.SourcesBefore[0] {
		return errors.New("original complete identity/SHA differs from candidate proof")
	}
	wantProtected := []fileObservation{candidate.SourcesBefore[1], candidate.SourcesBefore[2], candidate.After}
	var protected []fileObservation
	all := []fileObservation{before}
	for _, want := range wantProtected {
		got, err := observeData(want.Path, 0400)
		if err != nil {
			return err
		}
		if got != want {
			return errors.New("immutable backup/audit/candidate identity or SHA changed")
		}
		for _, other := range all {
			if got.Device == other.Device && got.Inode == other.Inode {
				return errors.New("original and protected copies must have independent inodes")
			}
		}
		all, protected = append(all, got), append(protected, got)
	}
	result, actionErr := restoreOriginalFile(ctx, *original, before, proof, candidate, g, *proofSHA, *candidateSHA, filepath.Join(artifact, "original-intent.json"), protected)
	var after []fileObservation
	var protectedErr error
	for i, expected := range protected {
		got, err := observeData(expected.Path, 0400)
		if err != nil {
			protectedErr = errors.Join(protectedErr, err)
			continue
		}
		after = append(after, got)
		if got != protected[i] {
			protectedErr = errors.Join(protectedErr, errors.New("protected copy changed during original restoration"))
		}
	}
	originalAfter, originalErr := observeData(originalPath, 0600)
	postErr := writeExclusiveJSON(filepath.Join(artifact, "original-source-postcheck.json"), struct {
		OriginalBefore fileObservation   `json:"original_before"`
		OriginalAfter  fileObservation   `json:"original_after"`
		Before         []fileObservation `json:"protected_before"`
		After          []fileObservation `json:"protected_after"`
		Unchanged      bool              `json:"protected_unchanged"`
		ActionError    string            `json:"action_error,omitempty"`
	}{before, originalAfter, protected, after, protectedErr == nil, errorText(errors.Join(actionErr, originalErr))})
	if err := errors.Join(actionErr, protectedErr, originalErr, postErr); err != nil {
		return err
	}
	if originalAfter != result.After {
		return errors.New("original changed after physical reopen audit")
	}
	result.ProtectedAfter, result.Passed = after, true
	return writeExclusiveJSON(*output, result)
}
