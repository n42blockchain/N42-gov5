// gov5-qmdb-audit validates an explicitly copied performance database. Missing
// sealed genesis leaves are tested in memory. Two explicit restoration commands
// have separate candidate-copy and proved stopped-original path contracts.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
)

type chainAnchor struct {
	Number uint64     `json:"number"`
	Hash   types.Hash `json:"hash"`
	Root   types.Hash `json:"root"`
}

type auditReport struct {
	Kind           string       `json:"kind"`
	Passed         bool         `json:"passed"`
	DatabaseSHA256 string       `json:"database_sha256"`
	GenesisSHA256  string       `json:"genesis_sha256"`
	Genesis        chainAnchor  `json:"genesis"`
	Canonical      chainAnchor  `json:"canonical"`
	Applied        chainAnchor  `json:"applied"`
	Forest         forestReport `json:"forest"`
}

func checkedHeader(tx kv.Tx, hash types.Hash, number uint64) (*block.Header, error) {
	header := rawdb.ReadHeader(tx, hash, number)
	if header == nil || header.Number == nil || !header.Number.IsUint64() || header.Number.Uint64() != number || header.Hash() != hash {
		return nil, errors.New("header hash/number differs from requested anchor")
	}
	return header, nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	args := os.Args[1:]
	var err error
	if len(args) > 0 && args[0] == "restore-candidate-copy" {
		err = runRestoreCandidate(ctx, args[1:])
	} else if len(args) > 0 && args[0] == "restore-original-two-leaves" {
		err = runRestoreOriginal(ctx, args[1:])
	} else {
		err = run(ctx, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func shaFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Restrict this incident tool to a private, non-aliased validation copy. Never
// accept the live runtime path, hardlinked data, writable data or a FIFO.
func copyDataFile(dir string) (string, os.FileInfo, error) {
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil || resolved != dir || !strings.HasPrefix(dir, "/data/blockchain/gov5-work/validation/") || filepath.Base(dir) != "chaindata" || filepath.Base(filepath.Dir(dir)) != "audit-copy" {
		return "", nil, errors.New("requires absolute non-aliased validation/.../audit-copy/chaindata")
	}
	for p := dir; p != "/data/blockchain/gov5-work/validation"; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return "", nil, errors.New("copy directories must be private")
		}
	}
	path := filepath.Join(dir, "mdbx.dat")
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0400 || stat.Nlink != 1 || stat.Uid != uint32(os.Getuid()) {
		return "", nil, errors.New("copied mdbx.dat must be owned, regular, 0400 and not hardlinked")
	}
	return path, info, nil
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("gov5-qmdb-audit", flag.ContinueOnError)
	dir := flags.String("copy-db", "", "Private validation/.../audit-copy/chaindata copy only")
	genesisFile := flags.String("genesis", "", "Original explicit performance genesis (read only)")
	expectedDB := flags.String("expected-db-sha256", "", "SHA256 of untouched copied mdbx.dat")
	expectedGenesis := flags.String("expected-genesis-sha256", "", "SHA256 of exact original genesis bytes")
	output := flags.String("output", "", "New report path, created exclusively")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*genesisFile) || !filepath.IsAbs(*output) {
		return errors.New("absolute genesis/output and no positional arguments required")
	}
	for _, digest := range []string{*expectedDB, *expectedGenesis} {
		decoded, err := hex.DecodeString(digest)
		if err != nil || len(decoded) != 32 || digest != strings.ToLower(digest) {
			return errors.New("exact lowercase SHA256 inputs required")
		}
	}
	path, before, err := copyDataFile(*dir)
	if err != nil {
		return err
	}
	dbSHA, err := shaFile(path)
	if err != nil {
		return err
	}
	if dbSHA != *expectedDB {
		return errors.New("copy database SHA256 differs")
	}
	g, err := prepareAuditGenesis(ctx, *genesisFile, *expectedGenesis)
	if err != nil {
		return err
	}
	defer g.close()
	db, err := mdbx.NewMDBX(log.New()).Path(*dir).Label(kv.ChainDB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).MapSize(64 * datasize.GB).Accede().Readonly().Open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	var report auditReport
	if err := db.View(ctx, func(tx kv.Tx) error {
		var err error
		report, err = auditBoundSnapshot(tx, g)
		return err
	}); err != nil {
		return err
	}
	report.DatabaseSHA256 = dbSHA
	afterSHA, err := shaFile(path)
	if err != nil {
		return err
	}
	after, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) || dbSHA != afterSHA {
		return errors.New("copied data changed during readonly audit")
	}
	report.Passed = true
	f, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}
