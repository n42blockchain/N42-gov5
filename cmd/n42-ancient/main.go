// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// n42-ancient operates on a qs-chain era store (ancient-era directory):
//
//	n42-ancient ls     --dir E:/x/ancient-era
//	n42-ancient verify --dir E:/x/ancient-era [--deep --source <chaindata> --sample 64]
//	n42-ancient fetch  --dir E:/x/ancient-era --from E:/peer/ancient-era
//	n42-ancient prune  --dir E:/x/ancient-era --class aux --before-era 10
//
// verify: full payload scrub (blake3 + per-frame xxh64) of every sealed
// file; with --deep it additionally cross-reads sampled blocks against a
// source chaindata (byte compare of every table). fetch: restore
// missing/damaged files from a peer store, hash-verified against the
// local manifest. prune: delete optional-class files and record the
// pruning in the manifest (the chain class is refused).
package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb/ancientera"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	dir := fs.String("dir", "", "era store directory")
	deep := fs.Bool("deep", false, "verify: cross-read sampled blocks against --source")
	source := fs.String("source", "", "verify --deep: source chaindata to compare against")
	sample := fs.Int("sample", 64, "verify --deep: blocks sampled per era")
	from := fs.String("from", "", "fetch: peer era store directory")
	classFlag := fs.String("class", "", "prune: exec or aux")
	beforeEra := fs.Uint64("before-era", 0, "prune: prune eras < this number")
	fs.Parse(os.Args[2:])
	if *dir == "" {
		usage()
	}

	var err error
	switch cmd {
	case "ls":
		err = runLs(*dir)
	case "verify":
		err = runVerify(*dir, *deep, *source, *sample)
	case "fetch":
		err = runFetch(*dir, *from)
	case "prune":
		err = runPrune(*dir, *classFlag, *beforeEra)
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: n42-ancient <ls|verify|fetch|prune> --dir <era-dir> [options]")
	os.Exit(2)
}

func runLs(dir string) error {
	m, err := ancientera.LoadManifest(dir)
	if err != nil {
		return err
	}
	fmt.Printf("era store %s — span=%d chainId=%d genesis=%s\n",
		dir, m.Span, m.ChainID, short(m.GenesisHash))
	fmt.Printf("%-6s %-6s %-10s %-44s %10s %-7s\n", "era", "class", "blocks", "file", "size", "status")
	for _, e := range m.Entries {
		fmt.Printf("%-6d %-6s [%d,%d) %-40s %10s %-7s\n",
			e.Era, e.Class, e.Era*m.Span, (e.Era+1)*m.Span, e.File,
			datasize.ByteSize(e.Size).HR(), e.Status)
	}
	return nil
}

func runVerify(dir string, deep bool, source string, sample int) error {
	m, err := ancientera.LoadManifest(dir)
	if err != nil {
		return err
	}
	bad := 0
	for _, e := range m.Entries {
		if e.Status == ancientera.StatusPruned {
			continue
		}
		t0 := time.Now()
		r, err := ancientera.OpenReader(filepath.Join(dir, e.File))
		if err != nil {
			fmt.Printf("FAIL %-40s open: %v\n", e.File, err)
			bad++
			continue
		}
		if r.Meta.PayloadBlake3 != e.PayloadBlake3 {
			fmt.Printf("FAIL %-40s payload hash disagrees with manifest\n", e.File)
			bad++
			r.Close()
			continue
		}
		if err := r.VerifyPayload(); err != nil {
			fmt.Printf("FAIL %-40s %v\n", e.File, err)
			bad++
		} else {
			fmt.Printf("OK   %-40s %s\n", e.File, time.Since(t0).Truncate(time.Millisecond))
		}
		r.Close()
	}
	if bad > 0 {
		return fmt.Errorf("%d file(s) failed verification", bad)
	}
	if deep {
		if source == "" {
			return fmt.Errorf("--deep requires --source")
		}
		return deepVerify(dir, source, sample)
	}
	return nil
}

// deepVerify cross-reads sampled blocks from the era store and the
// source chaindata and compares every stored byte.
func deepVerify(dir, source string, sample int) error {
	store, health, err := ancientera.OpenStore(dir)
	if err != nil {
		return err
	}
	defer store.Close()
	if len(health.ChainGaps) > 0 || len(health.Degraded) > 0 {
		return fmt.Errorf("store unhealthy: gaps=%v degraded=%v", health.ChainGaps, health.Degraded)
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbx.NewMDBX(log.New()).Path(source).Label(kv.ChainDB).
		MapSize(4 * datasize.TB).Accede().Readonly().Open(context.Background())
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		return err
	}
	defer tx.Rollback()

	span := store.Span()
	checked := 0
	for start := uint64(0); start < store.SealedEnd(); start += span {
		era := start / span
		step := span / uint64(sample)
		if step == 0 {
			step = 1
		}
		for i := uint64(0); i < uint64(sample); i++ {
			n := start + i*step
			if n >= start+span {
				break
			}
			if err := compareBlock(tx, store, n); err != nil {
				return fmt.Errorf("era %d block %d: %w", era, n, err)
			}
			checked++
		}
		fmt.Printf("era %d deep-verified (%d samples)\n", era, sample)
	}
	fmt.Printf("deep verify OK: %d blocks compared\n", checked)
	return nil
}

func compareBlock(tx kv.Tx, store *ancientera.Store, n uint64) error {
	numKey := modules.EncodeBlockNumber(n)
	ch, err := tx.GetOne(modules.HeaderCanonical, numKey)
	if err != nil || len(ch) != 32 {
		return fmt.Errorf("source canonical hash: %v", err)
	}
	hash := types.BytesToHash(ch)

	// Chain class.
	eh, hdr, ev, err := store.Chain(n)
	if err != nil {
		return fmt.Errorf("era chain: %w", err)
	}
	if types.Hash(eh) != hash {
		return fmt.Errorf("canonical hash mismatch")
	}
	srcHdr, _ := tx.GetOne(modules.Headers, modules.HeaderKey(n, hash))
	if !bytes.Equal(hdr, srcHdr) {
		return fmt.Errorf("header bytes mismatch")
	}
	srcEv, _ := tx.GetOne(modules.ConsensusEvidence, numKey)
	if !bytes.Equal(ev, srcEv) {
		return fmt.Errorf("evidence bytes mismatch")
	}

	// Exec class.
	rec, err := store.Exec(n)
	if err != nil {
		return fmt.Errorf("era exec: %w", err)
	}
	srcBody, _ := tx.GetOne(modules.BlockBody, modules.BlockBodyKey(n, hash))
	if !bytes.Equal(rec.BodyRaw, srcBody) {
		return fmt.Errorf("body bytes mismatch")
	}
	if len(srcBody) == 12 {
		base := binary.BigEndian.Uint64(srcBody[:8])
		amount := binary.BigEndian.Uint32(srcBody[8:])
		if amount >= 2 {
			want := int(amount - 2)
			if len(rec.Txs) != want {
				return fmt.Errorf("tx count %d != %d", len(rec.Txs), want)
			}
			for i := 0; i < want; i++ {
				srcTx, _ := tx.GetOne(modules.BlockTx, modules.EncodeBlockNumber(base+1+uint64(i)))
				if !bytes.Equal(rec.Txs[i], srcTx) {
					return fmt.Errorf("tx %d bytes mismatch", i)
				}
			}
		}
	}
	srcRc, _ := tx.GetOne(modules.Receipts, numKey)
	if !bytes.Equal(rec.ReceiptsRaw, srcRc) {
		return fmt.Errorf("receipts bytes mismatch")
	}

	// Logs: every source row under the block prefix must match, in order.
	logCur, err := tx.Cursor(modules.Log)
	if err != nil {
		return err
	}
	defer logCur.Close()
	li := 0
	for k, v, cerr := logCur.Seek(numKey); k != nil; k, v, cerr = logCur.Next() {
		if cerr != nil {
			return cerr
		}
		if binary.BigEndian.Uint64(k[:8]) != n {
			break
		}
		if li >= len(rec.Logs) {
			return fmt.Errorf("era logs short: source has more than %d", li)
		}
		if rec.Logs[li].TxID != binary.BigEndian.Uint32(k[8:12]) || !bytes.Equal(rec.Logs[li].Data, v) {
			return fmt.Errorf("log %d mismatch", li)
		}
		li++
	}
	if li != len(rec.Logs) {
		return fmt.Errorf("era logs long: %d vs source %d", len(rec.Logs), li)
	}

	// Aux class.
	aux, err := store.Aux(n)
	if err != nil {
		return fmt.Errorf("era aux: %w", err)
	}
	srcWit, _ := tx.GetOne(modules.BlockWitness, numKey)
	if !bytes.Equal(aux.Witness, srcWit) {
		return fmt.Errorf("witness bytes mismatch")
	}
	// Account changeset dup values, in order.
	accCur, err := tx.CursorDupSort(modules.AccountChangeSet)
	if err != nil {
		return err
	}
	defer accCur.Close()
	ai := 0
	for k, v, cerr := accCur.Seek(numKey); k != nil; k, v, cerr = accCur.NextDup() {
		if cerr != nil {
			return cerr
		}
		if binary.BigEndian.Uint64(k[:8]) != n {
			break
		}
		if ai >= len(aux.AcctCS) || !bytes.Equal(aux.AcctCS[ai], v) {
			return fmt.Errorf("acctcs %d mismatch", ai)
		}
		ai++
	}
	if ai != len(aux.AcctCS) {
		return fmt.Errorf("era acctcs count %d vs source %d", len(aux.AcctCS), ai)
	}
	// Storage changeset (key suffix + value), in order.
	stoCur, err := tx.Cursor(modules.StorageChangeSet)
	if err != nil {
		return err
	}
	defer stoCur.Close()
	si := 0
	for k, v, cerr := stoCur.Seek(numKey); k != nil; k, v, cerr = stoCur.Next() {
		if cerr != nil {
			return cerr
		}
		if binary.BigEndian.Uint64(k[:8]) != n {
			break
		}
		if si >= len(aux.StorCS) || !bytes.Equal(aux.StorCS[si].Suffix, k[8:]) || !bytes.Equal(aux.StorCS[si].Value, v) {
			return fmt.Errorf("storcs %d mismatch", si)
		}
		si++
	}
	if si != len(aux.StorCS) {
		return fmt.Errorf("era storcs count %d vs source %d", len(aux.StorCS), si)
	}
	return nil
}

func runFetch(dir, from string) error {
	if from == "" {
		return fmt.Errorf("--from required")
	}
	m, err := ancientera.LoadManifest(dir)
	if err != nil {
		return err
	}
	restored := 0
	for i := range m.Entries {
		e := &m.Entries[i]
		if e.Status != ancientera.StatusSealed {
			continue
		}
		local := filepath.Join(dir, e.File)
		needs := false
		if st, err := os.Stat(local); err != nil || st.Size() != e.Size {
			needs = true
		} else if r, err := ancientera.OpenReader(local); err != nil {
			needs = true
		} else {
			if r.Meta.PayloadBlake3 != e.PayloadBlake3 || r.VerifyPayload() != nil {
				needs = true
			}
			r.Close()
		}
		if !needs {
			continue
		}
		src := filepath.Join(from, e.File)
		if err := copyFile(src, local+".tmp"); err != nil {
			return fmt.Errorf("%s: copy: %w", e.File, err)
		}
		r, err := ancientera.OpenReader(local + ".tmp")
		if err != nil {
			os.Remove(local + ".tmp")
			return fmt.Errorf("%s: fetched file invalid: %w", e.File, err)
		}
		ok := r.Meta.PayloadBlake3 == e.PayloadBlake3 && r.VerifyPayload() == nil
		r.Close()
		if !ok {
			os.Remove(local + ".tmp")
			return fmt.Errorf("%s: fetched file fails hash check", e.File)
		}
		os.Remove(local)
		if err := os.Rename(local+".tmp", local); err != nil {
			return err
		}
		fmt.Printf("restored %s\n", e.File)
		restored++
	}
	fmt.Printf("fetch done: %d file(s) restored\n", restored)
	return nil
}

func runPrune(dir, classStr string, beforeEra uint64) error {
	cl, err := ancientera.ParseClass(classStr)
	if err != nil {
		return err
	}
	if cl == ancientera.ClassChain {
		return fmt.Errorf("the chain class is the consensus-safety record and cannot be pruned")
	}
	m, err := ancientera.LoadManifest(dir)
	if err != nil {
		return err
	}
	pruned := 0
	for i := range m.Entries {
		e := &m.Entries[i]
		if e.Class != cl.String() || e.Era >= beforeEra || e.Status == ancientera.StatusPruned {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.File)); err != nil && !os.IsNotExist(err) {
			return err
		}
		e.Status = ancientera.StatusPruned
		e.PrunedAt = time.Now().UTC().Format(time.RFC3339)
		fmt.Printf("pruned %s\n", e.File)
		pruned++
	}
	if pruned > 0 {
		if _, err := m.Save(dir); err != nil {
			return err
		}
	}
	fmt.Printf("prune done: %d file(s)\n", pruned)
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12] + "…"
	}
	return s
}
