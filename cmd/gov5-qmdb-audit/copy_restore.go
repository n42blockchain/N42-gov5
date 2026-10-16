package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
)

const validationRoot = "/data/blockchain/gov5-work/validation"

type fileObservation struct {
	Path     string `json:"path"`
	Device   uint64 `json:"device"`
	Inode    uint64 `json:"inode"`
	Bytes    int64  `json:"bytes"`
	Modified int64  `json:"modified_nanos"`
	Changed  int64  `json:"changed_nanos"`
	Mode     uint32 `json:"mode"`
	SHA256   string `json:"sha256"`
}

func observeData(path string, mode os.FileMode) (fileObservation, error) {
	var out fileObservation
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path || !filepath.IsAbs(path) {
		return out, errors.New("data path must be absolute and have no aliases")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return out, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return out, err
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || !before.Mode().IsRegular() || before.Mode().Perm() != mode || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) || before.Size() <= 0 || before.Size() > 8<<30 {
		return out, errors.New("data file ownership/mode/link/size differs")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return out, err
	}
	after, err := f.Stat()
	if err != nil {
		return out, err
	}
	atPath, err := os.Lstat(path)
	if err != nil {
		return out, err
	}
	ast := after.Sys().(*syscall.Stat_t)
	if !os.SameFile(before, after) || !os.SameFile(before, atPath) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || st.Ctim != ast.Ctim {
		return out, errors.New("data file changed during hash")
	}
	return fileObservation{path, uint64(st.Dev), st.Ino, before.Size(), before.ModTime().UnixNano(), st.Ctim.Sec*1e9 + st.Ctim.Nsec, uint32(before.Mode().Perm()), hex.EncodeToString(h.Sum(nil))}, nil
}

func privateDirectory(path, root string) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path || !filepath.IsAbs(path) || !strings.HasPrefix(path, root+"/") {
		return errors.New("private directory must be inside its explicit root without aliases")
	}
	for p := path; p != root; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || info.Mode().Perm() != 0700 || st.Uid != uint32(os.Getuid()) {
			return errors.New("directory must be private and owned")
		}
	}
	return nil
}

func candidateDataFile(dir string) (string, error) {
	if filepath.Base(dir) != "chaindata" || filepath.Base(filepath.Dir(dir)) != "restore-candidate" {
		return "", errors.New("only validation/.../restore-candidate/chaindata is writable")
	}
	if err := privateDirectory(dir, validationRoot); err != nil {
		return "", err
	}
	return filepath.Join(dir, "mdbx.dat"), nil
}

func readRestoreProof(path, expected string) (auditReport, error) {
	var proof auditReport
	if filepath.Base(path) != "audit-result.json" {
		return proof, errors.New("a promoted audit-result.json is required")
	}
	if err := privateDirectory(filepath.Dir(path), validationRoot); err != nil {
		return proof, err
	}
	decoded, err := hex.DecodeString(expected)
	if err != nil || len(decoded) != 32 || expected != strings.ToLower(expected) {
		return proof, errors.New("explicit lowercase promoted report SHA256 required")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return proof, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return proof, err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 64<<10 || st.Nlink != 1 || st.Uid != uint32(os.Getuid()) {
		return proof, errors.New("bounded private promoted proof required")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil {
		return proof, err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != expected {
		return proof, errors.New("promoted report SHA256 differs")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&proof); err != nil {
		return proof, err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return proof, errors.New("trailing proof data")
	}
	if !proof.Passed || proof.Kind != "gov5-qmdb-missing-genesis-leaves-readonly-audit-v2" || len(proof.Forest.MissingGenesis) != 2 || proof.Forest.MissingGenesis[0].TwigID != 0 || proof.Forest.MissingGenesis[1].TwigID != 1 || proof.Forest.GenesisSlots < 2*qmdb.TwigSize {
		return proof, errors.New("proof must cover exactly two absent sealed genesis leaves")
	}
	return proof, nil
}

type tableFingerprint struct {
	Table        string `json:"table"`
	Rows         uint64 `json:"other_rows"`
	RestoredRows uint64 `json:"target_rows"`
	Sequence     uint64 `json:"sequence"`
	SHA256       string `json:"other_rows_sha256"`
}

func hashField(h hash.Hash, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	h.Write(length[:])
	h.Write(value)
}

// Only the two target positions are excluded; every other bucket, duplicate,
// key, value and table sequence is included. Unknown/unreadable buckets fail.
func logicalFingerprints(ctx context.Context, tx kv.Tx) ([]tableFingerprint, error) {
	names, err := tx.ListBuckets()
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	var out []tableFingerprint
	for i, name := range names {
		if i > 0 && names[i-1] == name {
			return nil, errors.New("duplicate bucket name")
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seqRaw, err := tx.GetOne(kv.Sequence, []byte(name))
		if err != nil {
			return nil, err
		}
		if len(seqRaw) != 0 && len(seqRaw) != 8 {
			return nil, errors.New("malformed bucket sequence")
		}
		sequence, err := tx.ReadSequence(name)
		if err != nil {
			return nil, err
		}
		row := tableFingerprint{Table: name, Sequence: sequence}
		h := sha256.New()
		hashField(h, []byte(name))
		cursor, err := tx.Cursor(name)
		if err != nil {
			return nil, err
		}
		err = func() error {
			defer cursor.Close()
			key, value, err := cursor.First()
			for key != nil && err == nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				if name == qmdb.LeavesTable && (bytes.Equal(key, position(0)) || bytes.Equal(key, position(1))) {
					row.RestoredRows++
				} else {
					row.Rows++
					hashField(h, key)
					hashField(h, value)
				}
				key, value, err = cursor.Next()
			}
			return err
		}()
		if err != nil {
			return nil, fmt.Errorf("fingerprint %s: %w", name, err)
		}
		row.SHA256 = hex.EncodeToString(h.Sum(nil))
		out = append(out, row)
	}
	return out, nil
}

func sameOtherRows(before, after []tableFingerprint, restored bool) bool {
	if len(before) != len(after) {
		return false
	}
	for i, a := range before {
		b := after[i]
		if a.Table != b.Table || a.Rows != b.Rows || a.Sequence != b.Sequence || a.SHA256 != b.SHA256 || a.RestoredRows != 0 {
			return false
		}
		expected := uint64(0)
		if restored && a.Table == qmdb.LeavesTable {
			expected = 2
		}
		if b.RestoredRows != expected {
			return false
		}
	}
	return true
}

func sameAuditedSnapshot(got, want auditReport, restored bool) bool {
	got.Passed, want.Passed = false, false
	got.DatabaseSHA256, want.DatabaseSHA256 = "", ""
	if restored {
		want.Forest.MissingGenesis = nil
	}
	return reflect.DeepEqual(got, want)
}

func writeExclusiveJSON(path string, value any) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	err = json.NewEncoder(f).Encode(value)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func openCandidateDB(ctx context.Context, dir string, readonly bool) (kv.RwDB, error) {
	opts := mdbx.NewMDBX(log.New()).Path(dir).Label(kv.ChainDB).WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).MapSize(64 * datasize.GB).Accede()
	if readonly {
		opts = opts.Readonly()
	}
	return opts.Open(ctx)
}

type candidateRestoreResult struct {
	Kind                   string             `json:"kind"`
	Passed                 bool               `json:"passed"`
	ProofSHA256            string             `json:"promoted_proof_sha256"`
	Before                 fileObservation    `json:"candidate_before"`
	After                  fileObservation    `json:"candidate_after"`
	SourcesBefore          []fileObservation  `json:"sources_before"`
	SourcesAfter           []fileObservation  `json:"sources_after"`
	Audit                  auditReport        `json:"physical_reopen_audit"`
	BeforeTables           []tableFingerprint `json:"before_tables"`
	AfterTables            []tableFingerprint `json:"after_tables"`
	OriginalDatabaseOpened bool               `json:"original_database_opened"`
	BackupDatabaseOpened   bool               `json:"backup_database_opened"`
}

func verifiedGenesisBlobs(g *auditedGenesis, proof auditReport) ([][]byte, error) {
	if proof.Genesis != g.Anchor || proof.GenesisSHA256 != g.SHA256 || len(proof.Forest.MissingGenesis) != 2 {
		return nil, errors.New("restore genesis/proof binding differs")
	}
	blobs := make([][]byte, 2)
	for id := range uint64(2) {
		expected := proof.Forest.MissingGenesis[id]
		if expected.TwigID != id || id >= proof.Forest.GenesisSlots/qmdb.TwigSize {
			return nil, errors.New("restore target is not a sealed genesis twig")
		}
		raw, err := g.Tx.GetOne(qmdb.LeavesTable, position(id))
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(raw)
		if len(raw) != expected.Bytes || hex.EncodeToString(digest[:]) != expected.SHA256 {
			return nil, errors.New("recreated genesis blob differs from promoted proof")
		}
		if _, _, err := blobRoot(raw); err != nil {
			return nil, err
		}
		blobs[id] = append([]byte(nil), raw...)
	}
	return blobs, nil
}

func restoreCandidate(ctx context.Context, dir string, before fileObservation, proof auditReport, g *auditedGenesis, proofSHA, intent string, sources []fileObservation) (candidateRestoreResult, error) {
	out := candidateRestoreResult{Kind: "gov5-qmdb-candidate-two-leaf-restore-v1", ProofSHA256: proofSHA, Before: before, SourcesBefore: sources}
	if !proof.Passed || proof.Kind != "gov5-qmdb-missing-genesis-leaves-readonly-audit-v2" || before.SHA256 != proof.DatabaseSHA256 || before.Path != filepath.Join(dir, "mdbx.dat") || before.Mode != 0600 {
		return out, errors.New("candidate requires its exact promoted snapshot and writable-copy identity")
	}
	blobs, err := verifiedGenesisBlobs(g, proof)
	if err != nil {
		return out, err
	}
	// Read-only baseline precedes even the RW environment open, so unexpected
	// bucket creation or sequence changes during open cannot be hidden.
	err = func() error {
		db, err := openCandidateDB(ctx, dir, true)
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
				return errors.New("candidate snapshot differs from promoted proof")
			}
			out.BeforeTables, err = logicalFingerprints(ctx, tx)
			if err != nil {
				return err
			}
			if !sameOtherRows(out.BeforeTables, out.BeforeTables, false) {
				return errors.New("target leaf row already exists")
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
		return out, errors.New("candidate changed during readonly baseline")
	}
	if err := writeExclusiveJSON(intent, struct {
		Kind        string             `json:"kind"`
		ProofSHA256 string             `json:"proof_sha256"`
		Candidate   fileObservation    `json:"candidate"`
		Sources     []fileObservation  `json:"sources"`
		Targets     []leafCandidate    `json:"absent_targets_to_insert"`
		Tables      []tableFingerprint `json:"before_tables"`
	}{"gov5-qmdb-candidate-restore-intent-v1", proofSHA, before, sources, proof.Forest.MissingGenesis, out.BeforeTables}); err != nil {
		return out, err
	}
	// This is the only writable environment. The only application writes are
	// two Put calls after Has=false in the same exclusive transaction.
	err = func() error {
		db, err := openCandidateDB(ctx, dir, false)
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
	// The writable environment and every transaction are physically closed.
	if err := os.Chmod(before.Path, 0400); err != nil {
		return out, err
	}
	file, err := os.OpenFile(before.Path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return out, err
	}
	err = file.Sync()
	file.Close()
	if err != nil {
		return out, err
	}
	after, err := observeData(before.Path, 0400)
	if err != nil {
		return out, err
	}
	if after.Device != before.Device || after.Inode != before.Inode || after.SHA256 == before.SHA256 {
		return out, errors.New("candidate physical write identity/hash differs")
	}
	err = func() error {
		db, err := openCandidateDB(ctx, dir, true)
		if err != nil {
			return err
		}
		defer db.Close()
		return db.View(ctx, func(tx kv.Tx) error {
			for id, raw := range blobs {
				got, err := tx.GetOne(qmdb.LeavesTable, position(uint64(id)))
				if err != nil {
					return err
				}
				if !bytes.Equal(got, raw) {
					return errors.New("physical reopen leaf bytes differ")
				}
			}
			out.Audit, err = auditBoundSnapshot(tx, g)
			if err != nil {
				return err
			}
			if len(out.Audit.Forest.MissingGenesis) != 0 || !sameAuditedSnapshot(out.Audit, proof, true) {
				return errors.New("physical reopen does not reproduce snapshot without overlay")
			}
			out.AfterTables, err = logicalFingerprints(ctx, tx)
			if err != nil {
				return err
			}
			if !sameOtherRows(out.BeforeTables, out.AfterTables, true) {
				return errors.New("physical reopen logical rows/sequences differ")
			}
			return nil
		})
	}()
	if err != nil {
		return out, err
	}
	unchanged, err := observeData(before.Path, 0400)
	if err != nil {
		return out, err
	}
	if unchanged != after {
		return out, errors.New("candidate changed during readonly physical reopen")
	}
	out.After = after
	out.Audit.DatabaseSHA256 = after.SHA256
	out.Audit.Passed = true
	// Passed remains false until all original/backup/audit observations are
	// rechecked outside this helper, after both environments have closed.
	return out, nil
}

// restoreMissingGenesisRows has no path or database-open authority. Both
// explicit restore commands retain their own file, lock, intent and lifecycle
// checks. The caller owns this transaction and decides whether to commit.
func restoreMissingGenesisRows(ctx context.Context, tx kv.RwTx, g *auditedGenesis, proof auditReport, before []tableFingerprint, blobs [][]byte) error {
	snapshot, err := auditBoundSnapshot(tx, g)
	if err != nil {
		return err
	}
	if !sameAuditedSnapshot(snapshot, proof, false) {
		return errors.New("snapshot changed before write")
	}
	tables, err := logicalFingerprints(ctx, tx)
	if err != nil {
		return err
	}
	if !sameOtherRows(before, tables, false) {
		return errors.New("logical database changed before write")
	}
	if err := insertAbsentLeaves(tx, blobs); err != nil {
		return err
	}
	snapshot, err = auditBoundSnapshot(tx, g)
	if err != nil {
		return err
	}
	if len(snapshot.Forest.MissingGenesis) != 0 || !sameAuditedSnapshot(snapshot, proof, true) {
		return errors.New("restored transaction does not reproduce exact snapshot without overlay")
	}
	tables, err = logicalFingerprints(ctx, tx)
	if err != nil {
		return err
	}
	if !sameOtherRows(before, tables, true) {
		return errors.New("restore changed logical keys/sequences other than the two leaves")
	}
	return ctx.Err()
}

func insertAbsentLeaves(tx kv.RwTx, blobs [][]byte) error {
	if len(blobs) != 2 {
		return errors.New("restore inserts exactly two leaf rows")
	}
	for id, raw := range blobs {
		exists, err := tx.Has(qmdb.LeavesTable, position(uint64(id)))
		if err != nil {
			return err
		}
		if exists {
			return errors.New("restore never overwrites an existing leaf row")
		}
		if err := tx.Put(qmdb.LeavesTable, position(uint64(id)), raw); err != nil {
			return err
		}
	}
	return nil
}

func runRestoreCandidate(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("restore-candidate-copy", flag.ContinueOnError)
	candidate := flags.String("candidate-db", "", "New private validation/.../restore-candidate/chaindata (0600 data)")
	original := flags.String("original-db", "", "Stopped isolated performance runtime/e/chaindata (hash only)")
	backup := flags.String("backup-data", "", "Unopened private untouched-backup/mdbx.dat (hash only)")
	audit := flags.String("audit-db", "", "Unchanged original audit-copy/chaindata (hash only)")
	proofPath := flags.String("proof", "", "Promoted v2 audit-result.json")
	proofSHA := flags.String("proof-sha256", "", "Exact promoted report SHA256")
	genesis := flags.String("genesis", "", "Original explicit genesis bytes")
	output := flags.String("output", "", "New restore-observation.json in candidate artifact")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("no positional arguments allowed")
	}
	path, err := candidateDataFile(*candidate)
	if err != nil {
		return err
	}
	artifact := filepath.Dir(filepath.Dir(*candidate))
	if *output != filepath.Join(artifact, "restore-observation.json") {
		return errors.New("output must be the new candidate artifact's restore-observation.json")
	}
	for _, name := range []string{"restore-intent.json", "restore-source-postcheck.json", "restore-observation.json"} {
		_, err := os.Lstat(filepath.Join(artifact, name))
		if err == nil || !os.IsNotExist(err) {
			return errors.New("restore attempt has already started; never overwrite or automatically retry")
		}
	}
	root := "/data/blockchain/gov5-performance-5m/runtime"
	if filepath.Base(*original) != "chaindata" || filepath.Base(filepath.Dir(*original)) != "e" {
		return errors.New("original must be an isolated runtime/e/chaindata")
	}
	if err := privateDirectory(*original, root); err != nil {
		return err
	}
	runtimeRoot := filepath.Dir(filepath.Dir(*original))
	if *genesis != filepath.Join(runtimeRoot, "genesis.json") {
		return errors.New("genesis must belong to the bound stopped runtime")
	}
	auditPath, _, err := copyDataFile(*audit)
	if err != nil {
		return err
	}
	if *backup != filepath.Join(filepath.Dir(filepath.Dir(*audit)), "untouched-backup", "mdbx.dat") {
		return errors.New("backup must belong to the same original audit artifact")
	}
	if err := privateDirectory(filepath.Dir(*backup), validationRoot); err != nil {
		return err
	}
	proof, err := readRestoreProof(*proofPath, *proofSHA)
	if err != nil {
		return err
	}
	g, err := prepareAuditGenesis(ctx, *genesis, proof.GenesisSHA256)
	if err != nil {
		return err
	}
	defer g.close()
	if g.Anchor != proof.Genesis {
		return errors.New("promoted proof/genesis anchor differs")
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(*original), "LOCK"), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	defer lock.Close()
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() {
		return errors.New("original node lock must be an existing regular file")
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("original runtime is not exclusively stopped: %w", err)
	}
	sourcePaths := []string{filepath.Join(*original, "mdbx.dat"), *backup, auditPath}
	var before []fileObservation
	for i, p := range sourcePaths {
		mode := os.FileMode(0400)
		if i == 0 {
			mode = 0600
		}
		observed, err := observeData(p, mode)
		if err != nil {
			return err
		}
		if observed.SHA256 != proof.DatabaseSHA256 {
			return errors.New("original/backup/audit SHA differs from promoted snapshot")
		}
		before = append(before, observed)
	}
	candidateBefore, err := observeData(path, 0600)
	if err != nil {
		return err
	}
	if candidateBefore.SHA256 != proof.DatabaseSHA256 {
		return errors.New("new candidate SHA differs from promoted snapshot")
	}
	all := append(append([]fileObservation(nil), before...), candidateBefore)
	for i, a := range all {
		for _, b := range all[:i] {
			if a.Device == b.Device && a.Inode == b.Inode {
				return errors.New("candidate/source/backup/audit must use distinct inodes")
			}
		}
	}
	result, actionErr := restoreCandidate(ctx, *candidate, candidateBefore, proof, g, *proofSHA, filepath.Join(artifact, "restore-intent.json"), before)
	var after []fileObservation
	var sourceErr error
	for i, p := range sourcePaths {
		mode := os.FileMode(0400)
		if i == 0 {
			mode = 0600
		}
		observed, err := observeData(p, mode)
		if err != nil {
			sourceErr = errors.Join(sourceErr, err)
			continue
		}
		after = append(after, observed)
		if observed != before[i] {
			sourceErr = errors.Join(sourceErr, errors.New("original/backup/audit changed during candidate restoration"))
		}
	}
	postErr := writeExclusiveJSON(filepath.Join(artifact, "restore-source-postcheck.json"), struct {
		Before      []fileObservation `json:"before"`
		After       []fileObservation `json:"after"`
		Unchanged   bool              `json:"unchanged"`
		ActionError string            `json:"action_error,omitempty"`
	}{before, after, sourceErr == nil, errorText(actionErr)})
	if err := errors.Join(actionErr, sourceErr, postErr); err != nil {
		return err
	}
	result.SourcesAfter = after
	result.Passed = true
	return writeExclusiveJSON(*output, result)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
