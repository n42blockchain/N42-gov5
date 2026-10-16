// gov5-qmdb-audit validates an explicitly copied performance database. Missing
// sealed genesis leaves are tested in memory; this command has no repair mode.
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
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/conf"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/node"
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
	if err := run(ctx, os.Args[1:]); err != nil {
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
	gf, err := os.OpenFile(*genesisFile, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return err
	}
	info, err := gf.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 16<<20 {
		gf.Close()
		return errors.New("bounded regular genesis required")
	}
	raw, err := io.ReadAll(io.LimitReader(gf, (16<<20)+1))
	gf.Close()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != *expectedGenesis {
		return errors.New("genesis bytes differ")
	}
	var genesis conf.Genesis
	if err := json.Unmarshal(raw, &genesis); err != nil {
		return err
	}
	if genesis.Config == nil || genesis.Config.StateScheme != "qmdb" || len(genesis.Alloc) == 0 || len(genesis.Alloc) > 12500 || genesis.StateRoot != (types.Hash{}) {
		return errors.New("bounded computed QMDB performance genesis required")
	}
	cfg := &conf.Config{NodeCfg: conf.NodeConfig{Profile: "n42", Chain: "private", PerformanceGenesis: *genesisFile}, ChainCfg: genesis.Config}
	if err := node.ValidatePerformanceGenesis(cfg, &genesis); err != nil {
		return err
	}
	modules.N42Init()
	scratchDir, err := os.MkdirTemp("", "gov5-genesis-audit-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratchDir)
	scratch, err := mdbx.NewMDBX(log.New()).Path(scratchDir).Label(kv.ChainDB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).MapSize(256 * datasize.MB).GrowthStep(4 * datasize.MB).Open(ctx)
	if err != nil {
		return err
	}
	defer scratch.Close()
	var genesisAnchor chainAnchor
	if err := scratch.Update(ctx, func(tx kv.RwTx) error {
		b, _, err := (&internal.GenesisBlock{GenesisConfig: &genesis}).WriteGenesisState(tx)
		if err == nil {
			genesisAnchor = chainAnchor{0, b.Hash(), b.StateRoot()}
		}
		return err
	}); err != nil {
		return err
	}
	gtx, err := scratch.BeginRo(ctx)
	if err != nil {
		return err
	}
	defer gtx.Rollback()
	// Independently reject a genesis whose plain storage was not represented
	// by its commitment, rather than silently inheriting a genesis reader gap.
	if _, err := auditForest(gtx, gtx, genesisAnchor.Root); err != nil {
		return fmt.Errorf("scratch genesis: %w", err)
	}
	db, err := mdbx.NewMDBX(log.New()).Path(*dir).Label(kv.ChainDB).
		WithTableCfg(func(kv.TableCfg) kv.TableCfg { return modules.N42TableCfg }).MapSize(64 * datasize.GB).Accede().Readonly().Open(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	report := auditReport{Kind: "gov5-qmdb-missing-genesis-leaves-readonly-audit-v2", DatabaseSHA256: dbSHA, GenesisSHA256: *expectedGenesis, Genesis: genesisAnchor}
	if err := db.View(ctx, func(tx kv.Tx) error {
		stored, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		if stored != genesisAnchor.Hash {
			return errors.New("copy genesis hash differs")
		}
		storedCfg, err := rawdb.ReadChainConfig(tx, stored)
		if err != nil {
			return err
		}
		a, err := json.Marshal(storedCfg)
		if err != nil {
			return err
		}
		b, err := json.Marshal(genesis.Config)
		if err != nil {
			return err
		}
		if !bytes.Equal(a, b) {
			return errors.New("stored chain configuration differs")
		}
		n, h, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("missing applied marker")
		}
		applied, err := checkedHeader(tx, types.Hash(h), n)
		if err != nil {
			return err
		}
		ch := rawdb.ReadHeadBlockHash(tx)
		cn := rawdb.ReadHeaderNumber(tx, ch)
		if cn == nil || *cn > n || n-*cn > 256 {
			return errors.New("canonical/applied ancestry outside bounded audit")
		}
		canonical, err := rawdb.ReadCanonicalHash(tx, *cn)
		if err != nil {
			return err
		}
		if canonical != ch {
			return errors.New("head is not canonical")
		}
		header, err := checkedHeader(tx, ch, *cn)
		if err != nil {
			return err
		}
		ancestor := applied
		for height := n; height > *cn; height-- {
			ancestor, err = checkedHeader(tx, ancestor.ParentHash, height-1)
			if err != nil {
				return err
			}
		}
		if ancestor.Hash() != ch {
			return errors.New("applied is not a canonical extension")
		}
		report.Canonical = chainAnchor{*cn, ch, header.Root}
		report.Applied = chainAnchor{n, types.Hash(h), applied.Root}
		if !genesis.Config.IsSpuriousDragon(0) || genesis.Config.HotStuff == nil {
			return errors.New("empty-beneficiary policy requires EIP-161 from genesis")
		}
		validators := make(map[types.Address]bool)
		for _, validator := range genesis.Config.HotStuff.Validators {
			validators[types.HexToAddress(validator.Address)] = true
		}
		beneficiaries, err := usedBeneficiaries(tx, applied, genesisAnchor.Hash, validators)
		if err != nil {
			return err
		}
		report.Forest, err = auditForestWithBeneficiaries(tx, gtx, applied.Root, beneficiaries)
		return err
	}); err != nil {
		return err
	}
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
