// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// n42-statesync-measure sizes the v1.1 QMDB state-sync payload of a stopped
// QMDB datadir (or a copy of one). It builds the served snapshot the way
// internal/sync/v11.BuildSnapshot does (one read transaction, portable
// tombstone form, LIVE_P preimages from the plain state) and then encodes
// every reply a client fetching the whole pivot would receive: qmdb_status/1,
// every qmdb_manifest/1 page and every qmdb_twigs/1 chunk, framed exactly as
// the server writes them, plus the code_by_hash/1 payload for every bytecode.
// It reports build time and byte counts; run it under /usr/bin/time -v for
// the peak RSS. It does not need an applied marker or a commit QC, so it
// also sizes chains that are not profile 1 (e.g. the chain-94 replay seed).
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/c2h5oh/datasize"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/internal/sync/v11"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func main() {
	dbPath := flag.String("db", "", "stopped QMDB chaindata directory (work on a copy)")
	mapGB := flag.Int("map.gb", 512, "MDBX map size in GiB")
	manifestPage := flag.Uint("manifest-page", 4096, "twigs per manifest page (client default)")
	twigBatch := flag.Uint("twig-batch", 64, "twigs per qmdb_twigs/1 request (client default)")
	flag.Parse()
	if *dbPath == "" {
		fatalf("usage: n42-statesync-measure --db <chaindata>")
	}
	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(*dbPath).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().Open(context.Background())
	if err != nil {
		fatalf("open: %v", err)
	}
	defer db.Close()

	started := time.Now()
	var (
		snap      *v11.Snapshot
		codeBytes uint64
		codes     uint64
		accounts  uint64
		slots     uint64
		codeSizes []int
	)
	phase := func(name string, t time.Time) { fmt.Printf("phase %-22s %8.1f s\n", name, time.Since(t).Seconds()) }
	err = db.View(context.Background(), func(tx kv.Tx) error {
		blockHash := rawdb.ReadHeadBlockHash(tx)
		number, hash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !ok {
			n := rawdb.ReadHeaderNumber(tx, blockHash)
			if n == nil {
				return fmt.Errorf("no applied marker and no head")
			}
			number, hash = *n, blockHash
		}
		genesis, err := rawdb.ReadCanonicalHash(tx, 0)
		if err != nil {
			return err
		}
		cfg, err := rawdb.ReadChainConfig(tx, genesis)
		if err != nil || cfg == nil {
			return fmt.Errorf("chain config: %v", err)
		}
		t := time.Now()
		computer := commitment.NewQMDBRootComputer()
		computer.SetCold(tx)
		if err := computer.LoadFrom(tx); err != nil {
			return err
		}
		tree := computer.Tree()
		phase("load forest", t)
		t = time.Now()
		if err := tree.VerifyPersistedValues(tx); err != nil {
			return fmt.Errorf("authenticate live entries: %w", err)
		}
		phase("verify live values", t)
		t = time.Now()
		md := qmdb.PortableSnapshotMetadata{ChainID: cfg.ChainID.Uint64(), GenesisHash: qmdb.Hash(genesis), BlockNumber: number,
			BlockHash: qmdb.Hash(hash), Root: tree.Root(), NextSlot: tree.NextSlot()}
		var buf bytes.Buffer
		if _, err := qmdb.WritePortableSnapshotTombstone(&buf, md, tree, tx); err != nil {
			return err
		}
		phase("portable tombstone", t)
		fmt.Printf("portable tombstone bytes %d\n", buf.Len())
		t = time.Now()
		_, twigs, err := syncwire.TwigsFromPortable(buf.Bytes())
		if err != nil {
			return err
		}
		buf = bytes.Buffer{}
		phase("decode twigs", t)
		t = time.Now()
		pre := map[syncwire.Hash][]byte{}
		if err := tx.ForEach(modules.Account, nil, func(k, _ []byte) error {
			if len(k) == types.AddressLength {
				accounts++
				pre[syncwire.KeyHash(k)] = append([]byte(nil), k...)
			}
			return nil
		}); err != nil {
			return err
		}
		if err := tx.ForEach(modules.Storage, nil, func(k, _ []byte) error {
			if len(k) == types.AddressLength+types.HashLength {
				slots++
				pre[syncwire.KeyHash(k)] = append([]byte(nil), k...)
			}
			return nil
		}); err != nil {
			return err
		}
		for i := range twigs {
			for j := range twigs[i].Records {
				r := &twigs[i].Records[j]
				if r.Kind != syncwire.KindLive {
					continue
				}
				if p, ok := pre[r.KeyHash]; ok {
					r.Kind, r.Preimage = syncwire.KindLiveP, p
				}
			}
		}
		pre = nil
		phase("preimages", t)
		if err := tx.ForEach(modules.Code, nil, func(_, v []byte) error {
			codes++
			codeBytes += uint64(len(v))
			codeSizes = append(codeSizes, len(v))
			return nil
		}); err != nil {
			return err
		}
		snap = v11.NewSnapshot(number, types.Hash(hash), types.Hash(md.Root), md.NextSlot, twigs)
		fmt.Printf("pivot %d hash %x root %x nextSlot %d twigs %d live %d\n", number, hash, md.Root, md.NextSlot, snap.TwigCount(), tree.LiveCount())
		return nil
	})
	if err != nil {
		fatalf("%v", err)
	}
	fmt.Printf("phase %-22s %8.1f s\n", "BUILD TOTAL", time.Since(started).Seconds())

	// Twig kinds.
	var hollow, partial, fullLive, live, liveP, dead uint64
	for i := range snap.Twigs {
		rs := snap.Twigs[i].Records
		if len(rs) == 1 && rs[0].Kind == syncwire.KindHollow {
			hollow++
			continue
		}
		any := false
		for _, r := range rs {
			switch r.Kind {
			case syncwire.KindLive:
				live++
				any = true
			case syncwire.KindLiveP:
				liveP++
				any = true
			case syncwire.KindDead:
				dead++
			}
		}
		if any {
			fullLive++
		} else {
			partial++
		}
	}
	fmt.Printf("twigs hollow %d withLive %d noLiveNotHollow %d | records LIVE_P %d LIVE %d DEAD %d\n", hollow, fullLive, partial, liveP, live, dead)
	fmt.Printf("accounts %d storageSlots %d codes %d codeBytes %d\n", accounts, slots, codes, codeBytes)

	// Wire bytes as a whole-pivot client receives them (framed chunks).
	status := uint64(len(syncwire.AppendChunk(nil, syncwire.CodeSuccess, nil, make([]byte, syncwire.QMDBStatusLen))))
	var manifestBytes, manifestPages uint64
	for first := uint64(0); first < snap.TwigCount(); first += uint64(*manifestPage) {
		p, code, msg := v11.ServeManifest(snap, syncwire.ManifestRequest{PivotHash: syncwire.Hash(snap.PivotHash), FirstTwig: first, Count: uint32(*manifestPage)})
		if code != syncwire.CodeSuccess {
			fatalf("manifest: %s", msg)
		}
		manifestBytes += uint64(len(syncwire.AppendChunk(nil, syncwire.CodeSuccess, nil, p)))
		manifestPages++
	}
	t := time.Now()
	var twigBytes, twigReqs, maxChunk uint64
	for next := uint64(0); next < snap.TwigCount(); {
		req := syncwire.TwigsRequest{PivotHash: syncwire.Hash(snap.PivotHash), FirstTwig: next, TwigCount: uint32(*twigBatch), FromSlot: next * syncwire.TwigSize}
		if rest := snap.TwigCount() - next; rest < uint64(*twigBatch) {
			req.TwigCount = uint32(rest)
		}
		got := uint64(0)
		v11.ServeTwigs(snap, req, func(b []byte) bool {
			twigBytes += uint64(len(b))
			if uint64(len(b)) > maxChunk {
				maxChunk = uint64(len(b))
			}
			if b[0] == syncwire.CodeSuccess {
				got++
			}
			return true
		})
		twigReqs++
		if got == 0 {
			fatalf("no twig served from %d", next)
		}
		next += got
	}
	fmt.Printf("phase %-22s %8.1f s\n", "encode all twigs", time.Since(t).Seconds())
	var codeWire uint64
	for _, n := range codeSizes {
		codeWire += uint64(len(syncwire.AppendChunk(nil, syncwire.CodeSuccess, nil, make([]byte, n))))
	}
	total := status + manifestBytes + twigBytes + codeWire
	fmt.Printf("wire status %d manifest %d (%d pages) twigs %d (%d requests, max chunk %d) code %d TOTAL %d\n",
		status, manifestBytes, manifestPages, twigBytes, twigReqs, maxChunk, codeWire, total)
	for _, mbit := range []float64{100, 1000} {
		fmt.Printf("download at %4.0f Mbit/s: %.1f min (payload only)\n", mbit, float64(total)*8/(mbit*1e6)/60)
	}
}

func fatalf(format string, a ...interface{}) {
	fmt.Fprintf(os.Stderr, "n42-statesync-measure: "+format+"\n", a...)
	os.Exit(1)
}
