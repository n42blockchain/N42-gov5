// n42-reth-state-dump exports a gov5 QMDB state snapshot in reth init-state
// JSONL format and writes the exact applied-head header as canonical RLP.
//
// The source node must be stopped (or the MDBX directory must be a filesystem
// snapshot). A single read transaction binds the header, accounts, storage and
// bytecode to one applied QMDB head.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/snapshotjoin"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

type manifest struct {
	Version       uint64 `json:"version"`
	Format        string `json:"format"`
	ChainID       uint64 `json:"chain_id"`
	BlockNumber   uint64 `json:"block_number"`
	BlockHash     string `json:"block_hash"`
	StateRoot     string `json:"state_root"`
	StateRootFrom uint64 `json:"state_root_header"` // the header whose Root is state_root: block_number, or block_number+1 under deferred execution
	HeaderRLP     string `json:"header_rlp"`
	StateJSONL    string `json:"state_jsonl"`
	StateSHA256   string `json:"state_sha256"`
	Accounts      uint64 `json:"accounts"`
	StorageSlots  uint64 `json:"storage_slots"`
	CompleteState bool   `json:"complete_state"`
}

func main() {
	dbPath := flag.String("datadir", "", "stopped gov5 chaindata MDBX directory")
	outPath := flag.String("out", "", "reth init-state JSONL output")
	headerPath := flag.String("header-out", "", "raw RLP anchor header output (default <out>.header.rlp)")
	manifestPath := flag.String("manifest-out", "", "snapshot manifest output (default <out>.manifest.json)")
	chainID := flag.Uint64("chain-id", 94, "chain ID recorded in the manifest")
	limit := flag.Uint64("limit", 0, "test-only account limit; output is not a complete state when non-zero")
	bundleDir := flag.String("bundle", "", "write the profile-1 snapshot bundle (JSONL, header(P), portable QMDB 0x03, header(P+1), commit QC, block P, bundle.json) into this empty directory instead of --out")
	anchorWait := flag.Duration("anchor-wait", 2*time.Minute, "with --bundle: how long to wait for header(P+1) and a commit QC reaching it (a running source node)")
	flag.Parse()

	if *bundleDir != "" {
		if *dbPath == "" || *outPath != "" {
			fmt.Fprintln(os.Stderr, "usage: n42-reth-state-dump --datadir <chaindata> --bundle <dir> [--anchor-wait 2m]")
			os.Exit(2)
		}
		if err := runBundle(*dbPath, *bundleDir, *chainID, *anchorWait); err != nil {
			fmt.Fprintln(os.Stderr, "n42-reth-state-dump:", err)
			os.Exit(1)
		}
		return
	}
	if *dbPath == "" || *outPath == "" {
		fmt.Fprintln(os.Stderr, "usage: n42-reth-state-dump --datadir <chaindata> --out <state.jsonl>")
		os.Exit(2)
	}
	if *headerPath == "" {
		*headerPath = *outPath + ".header.rlp"
	}
	if *manifestPath == "" {
		*manifestPath = *outPath + ".manifest.json"
	}

	if err := run(*dbPath, *outPath, *headerPath, *manifestPath, *chainID, *limit); err != nil {
		fmt.Fprintln(os.Stderr, "n42-reth-state-dump:", err)
		os.Exit(1)
	}
}

// openSource opens a gov5 chaindata directory read-only.
func openSource(dbPath string, logger log.Logger) (kv.RwDB, error) {
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	return mdbx.NewMDBX(logger).
		Path(dbPath).
		Label(kv.ChainDB).
		PageSize(4096).
		MapSize(4 * datasize.TB).
		Readonly().
		Accede().
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return kv.ChaindataTablesCfg }).
		Open(context.Background())
}

// runBundle writes the profile-1 snapshot bundle of the source's applied
// head. The source may be a running node: the state is read in one
// transaction, the anchor appears once the pivot's successor is committed.
func runBundle(dbPath, dir string, chainID uint64, wait time.Duration) error {
	if err := validateExportPaths(dbPath, dir); err != nil {
		return err
	}
	logger := log.New()
	db, err := openSource(dbPath, logger)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer db.Close()
	m, err := snapshotjoin.ExportBundle(context.Background(), db, dir, snapshotjoin.ExportOptions{ChainID: chainID, Wait: wait, Logger: logger})
	if err != nil {
		return err
	}
	fmt.Printf("bundle %s: chain %d pivot %d %s post-state root %s (= header(%d).Root), anchor QC view %d on block %d, %d accounts, %d slots, %d live leaves\n",
		dir, m.ChainID, m.PivotNumber, m.PivotHash, m.PostStateRoot, m.PivotNumber+1, m.AnchorQCView, m.AnchorNumber, m.Accounts, m.StorageSlots, m.LiveLeaves)
	return nil
}

func run(dbPath, outPath, headerPath, manifestPath string, chainID, limit uint64) error {
	if err := validateExportPaths(dbPath, outPath, headerPath, manifestPath); err != nil {
		return err
	}
	logger := log.New()
	db, err := openSource(dbPath, logger)
	if err != nil {
		return fmt.Errorf("open source (stop the gov5 node before exporting): %w", err)
	}
	defer db.Close()

	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return fmt.Errorf("begin read snapshot: %w", err)
	}
	defer tx.Rollback()

	blockNumber, appliedHash, err := exportHead(tx)
	if err != nil {
		return err
	}

	header := rawdb.ReadHeader(tx, types.Hash(appliedHash), blockNumber)
	if header == nil {
		return fmt.Errorf("applied header #%d %x is absent", blockNumber, appliedHash)
	}
	if got := header.Hash(); got != types.Hash(appliedHash) {
		return fmt.Errorf("applied header hash mismatch: marker=%x header=%x", appliedHash, got)
	}
	if header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != blockNumber {
		return fmt.Errorf("applied header number does not match export height %d", blockNumber)
	}
	genesis, err := rawdb.ReadCanonicalHash(tx, 0)
	if err != nil {
		return err
	}
	cfg, err := rawdb.ReadChainConfig(tx, genesis)
	if err != nil {
		return err
	}
	if cfg == nil || cfg.ChainID == nil || !cfg.ChainID.IsUint64() || cfg.ChainID.Uint64() != chainID {
		return fmt.Errorf("manifest chain ID %d does not match source chain config", chainID)
	}
	// Replay v2 generations may predate QMDBApplied. Authenticate the fallback
	// against the actual forest in the same read transaction before publishing files.
	computer := commitment.NewQMDBRootComputer()
	if err := computer.LoadFrom(tx); err != nil {
		return fmt.Errorf("load QMDB forest: %w", err)
	}
	// Under deferred execution (profile 1, k = 1) header(N) carries the
	// state after N-1: the applied state of N is certified by header(N+1).
	post, err := snapshotjoin.PostStateHeader(tx, cfg, header)
	if err != nil {
		return fmt.Errorf("%w (on a deferred chain the state of the applied head is certified by the next header; export a bundle with --bundle from a running node)", err)
	}
	if err := verifyExportRoot(computer.Tree().Root(), post.Root); err != nil {
		return err
	}
	if err := computer.Tree().VerifyPersistedValues(tx); err != nil {
		return fmt.Errorf("authenticate QMDB live entries: %w", err)
	}
	headerRLP, err := rlp.EncodeToBytes(header)
	if err != nil {
		return fmt.Errorf("encode anchor header RLP: %w", err)
	}

	if err := mkdirParents(outPath, headerPath, manifestPath); err != nil {
		return err
	}

	started := time.Now()
	out, err := os.CreateTemp(filepath.Dir(outPath), "."+filepath.Base(outPath)+".partial-*")
	if err != nil {
		return err
	}
	partial := out.Name()
	if err := out.Chmod(0o644); err != nil {
		_ = out.Close()
		_ = os.Remove(partial)
		return err
	}
	defer func() {
		_ = out.Close()
		_ = os.Remove(partial)
	}()

	digest := sha256.New()
	buffered := bufio.NewWriterSize(io.MultiWriter(out, digest), 4<<20)
	encoder := json.NewEncoder(buffered)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(map[string]string{"root": post.Root.Hex()}); err != nil {
		return err
	}

	accounts, slots, err := exportAccounts(tx, computer.Tree(), encoder, limit, logger, started)
	if err != nil {
		return err
	}
	if err := buffered.Flush(); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := writeAtomic(headerPath, headerRLP, 0o644); err != nil {
		return fmt.Errorf("write anchor header: %w", err)
	}
	if err := os.Rename(partial, outPath); err != nil {
		return err
	}

	m := manifest{
		Version:       1,
		Format:        "reth-init-state-jsonl+gov5-qmdb-anchor",
		ChainID:       chainID,
		BlockNumber:   blockNumber,
		BlockHash:     types.Hash(appliedHash).Hex(),
		StateRoot:     post.Root.Hex(),
		StateRootFrom: post.Number.Uint64(),
		HeaderRLP:     filepath.Base(headerPath),
		StateJSONL:    filepath.Base(outPath),
		StateSHA256:   hex.EncodeToString(digest.Sum(nil)),
		Accounts:      accounts,
		StorageSlots:  slots,
		CompleteState: limit == 0,
	}
	manifestBytes, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	manifestBytes = append(manifestBytes, '\n')
	if err := writeAtomic(manifestPath, manifestBytes, 0o644); err != nil {
		return err
	}

	logger.Info("reth state snapshot exported",
		"block", blockNumber,
		"hash", types.Hash(appliedHash),
		"root", post.Root,
		"accounts", accounts,
		"storageSlots", slots,
		"elapsed", time.Since(started).Round(time.Second),
		"sha256", m.StateSHA256)
	return nil
}

func exportAccounts(tx kv.Tx, tree *qmdb.Tree, encoder *json.Encoder, limit uint64, logger log.Logger, started time.Time) (uint64, uint64, error) {
	return snapshotjoin.ExportAccounts(tx, tree, encoder, limit, logger, started)
}

func quantity(value []byte) string { return snapshotjoin.Quantity(value) }

type dumpAccount = snapshotjoin.DumpAccount

func mkdirParents(paths ...string) error {
	for _, path := range paths {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".partial-*")
	if err != nil {
		return err
	}
	partial := f.Name()
	defer os.Remove(partial)
	if err := f.Chmod(mode); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(partial, path)
}

// exportHead prefers the explicit applied marker. Older replay v2 databases
// lack it; their canonical head is usable only with the forest root check above.
func exportHead(tx kv.Getter) (uint64, [32]byte, error) {
	number, hash, ok, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		return 0, [32]byte{}, fmt.Errorf("read QMDB applied head: %w", err)
	}
	if !ok {
		hash = rawdb.ReadHeadBlockHash(tx)
		n := rawdb.ReadHeaderNumber(tx, types.Hash(hash))
		if hash == ([32]byte{}) || n == nil {
			return 0, [32]byte{}, fmt.Errorf("database has neither a QMDB applied marker nor a readable head")
		}
		number = *n
	}
	canonical, err := rawdb.ReadCanonicalHash(tx, number)
	if err != nil {
		return 0, [32]byte{}, err
	}
	if canonical != types.Hash(hash) {
		return 0, [32]byte{}, fmt.Errorf("export head %d/%x is not canonical", number, hash)
	}
	return number, hash, nil
}

func verifyExportRoot(root qmdb.Hash, expected types.Hash) error {
	if root != qmdb.Hash(expected) {
		return fmt.Errorf("QMDB root %x does not match export header root %x", root, expected)
	}
	return nil
}
