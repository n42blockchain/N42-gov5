// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package snapshotjoin

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// ExportOptions tunes ExportBundle.
type ExportOptions struct {
	// ChainID, when non-zero, must equal the source chain's ID.
	ChainID uint64
	// Wait bounds how long the exporter polls for header(P+1) and a commit
	// QC reaching it. On a running node the applied pivot is usually not
	// committed yet when the state is read; the anchor appears a few blocks
	// later. Zero: one look only (a stopped node).
	Wait time.Duration
	// Poll is the interval between looks (default 500 ms).
	Poll time.Duration
	// Logger receives progress (default log.New()).
	Logger log.Logger
	// OnStateRead, when set, is called once the state of the pivot has been
	// read and its read transaction closed, before the anchor is looked for.
	OnStateRead func(pivot uint64, hash types.Hash)
}

// ExportBundle writes the bundle of db's applied QMDB head P into dir. One
// read transaction binds the JSONL, the portable snapshot and block P to
// the same tree; later read transactions find header(P+1) and the commit QC
// anchoring it. Nothing is published unless the tree's root equals
// header(P+1).Root and the anchor verifies against the genesis set.
func ExportBundle(ctx context.Context, db kv.RoDB, dir string, opts ExportOptions) (*Manifest, error) {
	if opts.Logger == nil {
		opts.Logger = log.New()
	}
	if opts.Poll <= 0 {
		opts.Poll = 500 * time.Millisecond
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	for _, name := range []string{FileState, FileHeader, FileQMDB, FileNextHeader, FileAnchor, FileBlock, FileManifest} {
		if _, err := os.Lstat(filepath.Join(dir, name)); err == nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, name), errBundleExists)
		}
	}
	started := time.Now()
	var tmps []string
	defer func() {
		for _, p := range tmps {
			_ = os.Remove(p)
		}
	}()
	newTemp := func(name string) (*os.File, error) {
		f, err := os.CreateTemp(dir, "."+name+".partial-*")
		if err != nil {
			return nil, err
		}
		tmps = append(tmps, f.Name())
		return f, nil
	}

	// Pass 1: the state at the applied head, in one read transaction.
	var (
		cfg         *params.ChainConfig
		genesisHash types.Hash
		pivot       *block.Header
		pivotBlock  []byte
		root        qmdb.Hash
		nextSlot    uint64
		live        uint64
		accounts    uint64
		slots       uint64
		stateTmp    string
		qmdbTmp     string
	)
	if err := db.View(ctx, func(tx kv.Tx) error {
		number, hash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return fmt.Errorf("read QMDB applied head: %w", err)
		}
		if !ok {
			return errors.New("the database has no QMDB applied marker")
		}
		if genesisHash, err = rawdb.ReadCanonicalHash(tx, 0); err != nil {
			return err
		}
		if cfg, err = rawdb.ReadChainConfig(tx, genesisHash); err != nil {
			return err
		}
		if cfg == nil || cfg.ChainID == nil || !cfg.ChainID.IsUint64() {
			return errors.New("the database has no chain config with a uint64 chain ID")
		}
		if opts.ChainID != 0 && cfg.ChainID.Uint64() != opts.ChainID {
			return fmt.Errorf("chain ID %d does not match the source chain config %d", opts.ChainID, cfg.ChainID.Uint64())
		}
		if !cfg.IsProfile1() {
			return errors.New("the bundle is defined for profile-1 chains only")
		}
		blk := rawdb.ReadBlock(tx, hash, number)
		if blk == nil {
			return fmt.Errorf("applied block %d %x is absent", number, hash)
		}
		if blk.Hash() != hash {
			return fmt.Errorf("applied block hash mismatch: marker %x, block %x", hash, blk.Hash())
		}
		if pivotBlock, err = rlp.EncodeToBytes(blk); err != nil {
			return fmt.Errorf("encode block %d: %w", number, err)
		}
		pivot = blk.Header().(*block.Header)

		computer := commitment.NewQMDBRootComputer()
		computer.SetCold(tx)
		if err := computer.LoadFrom(tx); err != nil {
			return fmt.Errorf("load QMDB forest: %w", err)
		}
		tree := computer.Tree()
		if err := tree.VerifyPersistedValues(tx); err != nil {
			return fmt.Errorf("authenticate QMDB live entries: %w", err)
		}
		root, nextSlot, live = tree.Root(), tree.NextSlot(), uint64(tree.LiveCount())

		// The JSONL: every row a leaf of the tree, every live leaf a row.
		out, err := newTemp(FileState)
		if err != nil {
			return err
		}
		stateTmp = out.Name()
		buffered := bufio.NewWriterSize(out, 4<<20)
		encoder := json.NewEncoder(buffered)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(map[string]string{"root": types.Hash(root).Hex()}); err != nil {
			_ = out.Close()
			return err
		}
		if accounts, slots, err = ExportAccounts(tx, tree, encoder, 0, opts.Logger, started); err != nil {
			_ = out.Close()
			return err
		}
		if err := closeSynced(out, buffered); err != nil {
			return err
		}

		// The portable snapshot of the same tree, labelled with the pivot.
		qf, err := newTemp(FileQMDB)
		if err != nil {
			return err
		}
		qmdbTmp = qf.Name()
		qbuf := bufio.NewWriterSize(qf, 1<<20)
		md := qmdb.PortableSnapshotMetadata{
			ChainID:     cfg.ChainID.Uint64(),
			GenesisHash: qmdb.Hash(genesisHash),
			BlockNumber: number,
			BlockHash:   qmdb.Hash(hash),
			Root:        root,
			NextSlot:    nextSlot,
		}
		if _, err := qmdb.WritePortableSnapshotTombstone(qbuf, md, tree, tx); err != nil {
			_ = qf.Close()
			return fmt.Errorf("write portable snapshot: %w", err)
		}
		return closeSynced(qf, qbuf)
	}); err != nil {
		return nil, err
	}
	pivotNumber, pivotHash := pivot.Number.Uint64(), pivot.Hash()
	opts.Logger.Info("snapshot bundle: state read", "pivot", pivotNumber, "hash", pivotHash, "root", types.Hash(root),
		"accounts", accounts, "storageSlots", slots, "liveLeaves", live, "elapsed", time.Since(started).Round(time.Millisecond))
	if opts.OnStateRead != nil {
		opts.OnStateRead(pivotNumber, pivotHash)
	}

	// Pass 2: header(P+1) and a commit QC reaching it.
	id, err := IdentityFor(cfg, genesisHash)
	if err != nil {
		return nil, err
	}
	var (
		post   *block.Header
		anchor *Anchor
	)
	deadline := time.Now().Add(opts.Wait)
	for {
		err := db.View(ctx, func(tx kv.Tx) error {
			var err error
			if post, err = PostStateHeader(tx, cfg, pivot); err != nil {
				return err
			}
			anchor, err = FindAnchor(tx, id, pivotNumber, pivotHash, 1)
			return err
		})
		switch {
		case err == nil && anchor != nil:
		case err == nil || errors.Is(err, ErrPostStateHeaderMissing):
			if time.Now().After(deadline) {
				if err == nil {
					err = errors.New("no commit QC reaching the pivot's next block is stored yet")
				}
				return nil, fmt.Errorf("pivot %d: %w (waited %s; export from a running node with a longer wait)", pivotNumber, err, opts.Wait)
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(opts.Poll):
			}
			continue
		default:
			return nil, err
		}
		break
	}
	if post.Number.Uint64() != pivotNumber+1 {
		return nil, fmt.Errorf("profile-1 chain without deferred execution at block %d", pivotNumber+1)
	}
	if qmdb.Hash(post.Root) != root {
		return nil, fmt.Errorf("QMDB root %x at pivot %d is not header(%d).Root %x", root, pivotNumber, pivotNumber+1, post.Root)
	}
	if _, _, err := id.VerifyAnchor(anchor, pivotNumber, 1); err != nil {
		return nil, err
	}

	// Publish: the small files, then the two big ones, then the manifest.
	headerRLP, err := rlp.EncodeToBytes(pivot)
	if err != nil {
		return nil, err
	}
	nextRLP, err := rlp.EncodeToBytes(post)
	if err != nil {
		return nil, err
	}
	anchorBytes, err := EncodeAnchor(anchor)
	if err != nil {
		return nil, err
	}
	for _, f := range []struct {
		name string
		data []byte
	}{{FileHeader, headerRLP}, {FileNextHeader, nextRLP}, {FileAnchor, anchorBytes}, {FileBlock, pivotBlock}} {
		if err := writeAtomic(filepath.Join(dir, f.name), f.data); err != nil {
			return nil, err
		}
	}
	if err := os.Rename(stateTmp, filepath.Join(dir, FileState)); err != nil {
		return nil, err
	}
	if err := os.Rename(qmdbTmp, filepath.Join(dir, FileQMDB)); err != nil {
		return nil, err
	}
	m := &Manifest{
		Format:        ManifestFormat,
		ChainID:       cfg.ChainID.Uint64(),
		GenesisHash:   genesisHash.Hex(),
		PivotNumber:   pivotNumber,
		PivotHash:     pivotHash.Hex(),
		PostStateRoot: types.Hash(root).Hex(),
		DeferredDepth: 1,
		RootLabel:     rootLabelText,
		AnchorQCView:  anchor.QC.View,
		AnchorNumber:  anchor.Headers[0].Number.Uint64(),
		AnchorHash:    anchor.QC.BlockHash.Hex(),
		AnchorDomain:  anchorDomainText,
		AnchorLow:     anchor.Headers[len(anchor.Headers)-1].Number.Uint64(),
		Accounts:      accounts,
		StorageSlots:  slots,
		LiveLeaves:    live,
		NextSlot:      nextSlot,
		Files:         map[string]string{},
	}
	for _, name := range []string{FileState, FileHeader, FileQMDB, FileNextHeader, FileAnchor, FileBlock} {
		d, err := fileDigest(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		m.Files[name] = d
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeAtomic(filepath.Join(dir, FileManifest), append(raw, '\n')); err != nil {
		return nil, err
	}
	opts.Logger.Info("snapshot bundle written", "dir", dir, "pivot", pivotNumber, "postStateRoot", types.Hash(root),
		"anchorView", anchor.QC.View, "anchorBlock", m.AnchorNumber, "headers", len(anchor.Headers),
		"elapsed", time.Since(started).Round(time.Millisecond))
	return m, nil
}

func closeSynced(f *os.File, w *bufio.Writer) error {
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func writeAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".partial-*")
	if err != nil {
		return err
	}
	partial := f.Name()
	defer os.Remove(partial)
	if err := f.Chmod(0o644); err != nil {
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

// sha256Reader hashes what is read through it.
type sha256Reader struct {
	r io.Reader
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
}

func newSHA256Reader(r io.Reader) *sha256Reader { return &sha256Reader{r: r, h: sha256.New()} }

func (s *sha256Reader) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	_, _ = s.h.Write(p[:n])
	return n, err
}

func (s *sha256Reader) hex() string { return hex.EncodeToString(s.h.Sum(nil)) }
