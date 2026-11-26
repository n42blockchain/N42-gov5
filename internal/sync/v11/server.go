// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Package v11 holds the protocol cores of the v1.1 sync protocols of the
// N42 sync standard (§5): the servers and readers of bodies_by_range/2 and
// commit_qc/1, the QMDB state-sync servers (qmdb_status/1, qmdb_manifest/1,
// qmdb_twigs/1, code_by_hash/1) and the state-sync client that joins a
// fresh profile-1 node from peers' state at a committed, QC-anchored pivot
// (the peer-to-peer equivalent of init-snapshot --bundle). It reads the
// database directly and has no libp2p dependency: internal/sync wraps the
// cores in stream handlers, the client takes a Peer.
//
// A state-sync server serves from an in-memory snapshot of one pivot P: the persisted
// QMDB forest at its applied head, read in one transaction exactly as the
// offline bundle exporter does (snapshotjoin.ExportBundle), paged by twig in
// the portable 0x03 record forms, with the plain-state preimage of every
// live key (LIVE_P, owner decision Q-S3). It is published only once header
// P+1 is committed, its Root equals the forest root (deferred depth 1, §5.3.1)
// and a commit QC reaching P+1 is stored. The previous snapshot stays
// servable so a client mid-transfer is not cut off by a refresh.
package v11

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/snapshotjoin"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// Snapshot is the state of one pivot in wire form.
type Snapshot struct {
	Pivot     uint64
	PivotHash types.Hash
	Root      types.Hash
	NextSlot  uint64
	Twigs     []syncwire.ServedTwig
	roots     []syncwire.Hash
	Built     time.Time
}

// NewSnapshot wraps twigs of pivot (number, hash) with root and nextSlot.
func NewSnapshot(number uint64, hash, root types.Hash, nextSlot uint64, twigs []syncwire.ServedTwig) *Snapshot {
	snap := &Snapshot{Pivot: number, PivotHash: hash, Root: root, NextSlot: nextSlot, Twigs: twigs, Built: time.Now()}
	snap.roots = make([]syncwire.Hash, len(twigs))
	for i := range twigs {
		snap.roots[i] = syncwire.TwigRoot(twigs[i].Roots)
	}
	return snap
}

// TwigCount is ceil(NextSlot/2048).
func (s *Snapshot) TwigCount() uint64 { return uint64(len(s.Twigs)) }

// BuildSnapshot reads db's applied QMDB head P in one read transaction and
// returns its twigs with preimages. It does not wait for P to be committed
// (Publishable checks that).
func BuildSnapshot(ctx context.Context, db kv.RoDB, chainID uint64, genesis types.Hash) (*Snapshot, error) {
	var snap *Snapshot
	err := db.View(ctx, func(tx kv.Tx) error {
		number, hash, ok, err := rawdb.ReadQMDBApplied(tx)
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("v11: no QMDB applied marker")
		}
		computer := commitment.NewQMDBRootComputer()
		computer.SetCold(tx)
		if err := computer.LoadFrom(tx); err != nil {
			return fmt.Errorf("v11: load QMDB forest: %w", err)
		}
		tree := computer.Tree()
		if err := tree.VerifyPersistedValues(tx); err != nil {
			return fmt.Errorf("v11: authenticate QMDB live entries: %w", err)
		}
		md := qmdb.PortableSnapshotMetadata{
			ChainID: chainID, GenesisHash: qmdb.Hash(genesis), BlockNumber: number, BlockHash: qmdb.Hash(hash),
			Root: tree.Root(), NextSlot: tree.NextSlot(),
		}
		var buf bytes.Buffer
		if _, err := qmdb.WritePortableSnapshotTombstone(&buf, md, tree, tx); err != nil {
			return fmt.Errorf("v11: portable snapshot: %w", err)
		}
		_, twigs, err := syncwire.TwigsFromPortable(buf.Bytes())
		if err != nil {
			return err
		}
		pre, err := preimages(tx)
		if err != nil {
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
		snap = NewSnapshot(number, hash, types.Hash(md.Root), md.NextSlot, twigs)
		return nil
	})
	return snap, err
}

// preimages maps the key hash of every plain-state account and storage key
// to its preimage (20 or 52 bytes).
func preimages(tx kv.Tx) (map[syncwire.Hash][]byte, error) {
	out := make(map[syncwire.Hash][]byte)
	if err := tx.ForEach(modules.Account, nil, func(k, _ []byte) error {
		if len(k) == types.AddressLength {
			out[syncwire.KeyHash(k)] = append([]byte(nil), k...)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("v11: scan Account: %w", err)
	}
	if err := tx.ForEach(modules.Storage, nil, func(k, _ []byte) error {
		if len(k) == types.AddressLength+types.HashLength {
			out[syncwire.KeyHash(k)] = append([]byte(nil), k...)
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("v11: scan Storage: %w", err)
	}
	return out, nil
}

// Publishable reports whether snap's pivot is committed with header P+1
// committed above it, header(P+1).Root equal to the snapshot root, and a
// commit QC reaching P+1 stored.
func Publishable(ctx context.Context, db kv.RoDB, cfg *params.ChainConfig, genesis types.Hash, snap *Snapshot) (bool, error) {
	id, err := snapshotjoin.IdentityFor(cfg, genesis)
	if err != nil {
		return false, err
	}
	ok := false
	err = db.View(ctx, func(tx kv.Tx) error {
		pivot := rawdb.ReadHeader(tx, snap.PivotHash, snap.Pivot)
		if pivot == nil {
			return fmt.Errorf("v11: pivot header %d absent", snap.Pivot)
		}
		post, err := snapshotjoin.PostStateHeader(tx, cfg, pivot)
		if errors.Is(err, snapshotjoin.ErrPostStateHeaderMissing) {
			return nil
		}
		if err != nil {
			return err
		}
		if post.Number.Uint64() != snap.Pivot+1 || post.Root != snap.Root {
			return fmt.Errorf("v11: header(%d).Root %x is not the forest root %x", snap.Pivot+1, post.Root, snap.Root)
		}
		a, err := snapshotjoin.FindAnchor(tx, id, snap.Pivot, snap.PivotHash, 1)
		if err != nil {
			return err
		}
		ok = a != nil
		return nil
	})
	return ok, err
}

// Server holds the servable snapshots of one node.
type Server struct {
	db      kv.RoDB
	cfg     *params.ChainConfig
	genesis types.Hash

	// RefreshBlocks: rebuild once the committed head is this far above the
	// current pivot. PublishWait bounds the wait for P+1 and its anchor.
	RefreshBlocks uint64
	PublishWait   time.Duration

	mu       sync.Mutex
	cur      *Snapshot
	prev     *Snapshot
	building bool
	failedAt time.Time
}

// NewServer returns a server for a profile-1 QMDB chain, or nil when the
// chain cannot serve state sync.
func NewServer(db kv.RoDB, cfg *params.ChainConfig, genesis types.Hash) *Server {
	if db == nil || cfg == nil || !cfg.IsProfile1() || cfg.StateScheme != string(params.StateCommitmentPresetQMDB) {
		return nil
	}
	return &Server{db: db, cfg: cfg, genesis: genesis, RefreshBlocks: 512, PublishWait: 2 * time.Minute}
}

// Current returns the newest published snapshot (nil while none is).
func (s *Server) Current() *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Lookup returns a published snapshot with pivot hash h.
func (s *Server) Lookup(h syncwire.Hash) *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sn := range []*Snapshot{s.cur, s.prev} {
		if sn != nil && sn.PivotHash == types.Hash(h) {
			return sn
		}
	}
	return nil
}

// Publish installs snap as the current snapshot (tests and builders).
func (s *Server) Publish(snap *Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur != nil && snap != nil && s.cur.PivotHash == snap.PivotHash {
		return
	}
	s.prev, s.cur = s.cur, snap
}

// MaybeRefresh starts a background build when there is no snapshot or the
// committed head moved RefreshBlocks past it.
func (s *Server) MaybeRefresh(ctx context.Context, head uint64) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.building || (s.cur != nil && head < s.cur.Pivot+s.RefreshBlocks) || time.Since(s.failedAt) < 30*time.Second {
		s.mu.Unlock()
		return
	}
	s.building = true
	s.mu.Unlock()
	go func() {
		err := s.build(ctx)
		s.mu.Lock()
		s.building = false
		if err != nil {
			s.failedAt = time.Now()
		}
		s.mu.Unlock()
		if err != nil {
			log.Warn("state sync: snapshot build failed", "err", err)
		}
	}()
}

func (s *Server) build(ctx context.Context) error {
	started := time.Now()
	snap, err := BuildSnapshot(ctx, s.db, s.cfg.ChainID.Uint64(), s.genesis)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(s.PublishWait)
	for {
		ok, err := Publishable(ctx, s.db, s.cfg, s.genesis, snap)
		if err != nil {
			return err
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("v11: pivot %d not anchored within %s", snap.Pivot, s.PublishWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	s.Publish(snap)
	log.Info("state sync: serving snapshot", "pivot", snap.Pivot, "hash", snap.PivotHash, "root", snap.Root,
		"twigs", snap.TwigCount(), "nextSlot", snap.NextSlot, "elapsed", time.Since(started).Round(time.Millisecond))
	return nil
}

// Status is the qmdb_status/1 answer.
func (s *Server) Status(earliestBody uint64) syncwire.QMDBStatus {
	st := syncwire.QMDBStatus{EarliestBody: earliestBody, Flags: syncwire.StatusFlagCode}
	if cur := s.Current(); cur != nil {
		st.PivotLo, st.PivotHi, st.PivotHiHash = cur.Pivot, cur.Pivot, cur.PivotHash
		st.Flags |= syncwire.StatusFlagTwigs
	}
	return st
}

// ServeManifest answers one qmdb_manifest/1 request: a SUCCESS payload or
// an error code and message.
func ServeManifest(snap *Snapshot, req syncwire.ManifestRequest) ([]byte, byte, string) {
	if snap == nil {
		return nil, syncwire.CodeResourceUnavailable, "pivot not servable"
	}
	page := syncwire.ManifestPage{
		PivotNumber: snap.Pivot, PivotHash: snap.PivotHash, Root: snap.Root, NextSlot: snap.NextSlot,
		TwigCount: snap.TwigCount(), FirstTwig: req.FirstTwig,
	}
	total := snap.TwigCount()
	if total == 0 && req.FirstTwig == 0 {
		return page.Encode(), syncwire.CodeSuccess, ""
	}
	if req.FirstTwig >= total {
		return nil, syncwire.CodeInvalidRequest, fmt.Sprintf("first_twig %d >= twig_count %d", req.FirstTwig, total)
	}
	n := uint64(req.Count)
	if n > total-req.FirstTwig {
		n = total - req.FirstTwig
	}
	for i := req.FirstTwig; i < req.FirstTwig+n; i++ {
		page.Twigs = append(page.Twigs, snap.Twigs[i].Roots)
	}
	proof, err := syncwire.RangeProof(snap.roots, req.FirstTwig, n)
	if err != nil {
		return nil, syncwire.CodeServerError, err.Error()
	}
	page.Proof = proof
	return page.Encode(), syncwire.CodeSuccess, ""
}

// ServeTwigs writes the qmdb_twigs/1 reply through write (false = stream
// gone): one chunk per twig, then END; or a single error chunk.
func ServeTwigs(snap *Snapshot, req syncwire.TwigsRequest, write func([]byte) bool) {
	if snap == nil {
		write(syncwire.AppendError(nil, syncwire.CodeResourceUnavailable, "pivot not servable"))
		return
	}
	total := snap.TwigCount()
	if req.FirstTwig >= total {
		write(syncwire.AppendError(nil, syncwire.CodeInvalidRequest, fmt.Sprintf("first_twig %d >= twig_count %d", req.FirstTwig, total)))
		return
	}
	budget := req.Budget()
	var served uint64
	end := syncwire.TwigsEnd{NextTwig: req.FirstTwig, Reason: syncwire.ReasonComplete}
	last := req.FirstTwig + uint64(req.TwigCount)
	for id := req.FirstTwig; id < last; id++ {
		if id >= total {
			end.Reason = syncwire.ReasonNotAvailable
			break
		}
		tw := &snap.Twigs[id]
		c := syncwire.TwigChunk{TwigID: id, Bits: tw.Bits}
		if !req.BitsOnly() {
			c.Records = twigRecordsFrom(tw, snap.NextSlot, req.FromSlot)
		}
		payload, err := c.Encode()
		if err != nil {
			write(syncwire.AppendError(nil, syncwire.CodeServerError, err.Error()))
			return
		}
		if id > req.FirstTwig && served+uint64(len(payload)) > budget {
			end.Reason = syncwire.ReasonByteBudget
			break
		}
		if !write(syncwire.AppendChunk(nil, syncwire.CodeSuccess, nil, payload)) {
			return
		}
		served += uint64(len(payload))
		end.NextTwig = id + 1
	}
	end.NextSlot = end.NextTwig * syncwire.TwigSize
	if end.NextSlot > snap.NextSlot {
		end.NextSlot = snap.NextSlot
	}
	if end.NextSlot < req.FromSlot {
		end.NextSlot = req.FromSlot
	}
	write(syncwire.AppendChunk(nil, syncwire.CodeEnd, nil, end.Encode()))
}

// twigRecordsFrom returns the records of tw from slot max(base, fromSlot).
// A hollow twig is served whole (its leaves are gone) when the request starts
// at its first slot, and without records otherwise.
func twigRecordsFrom(tw *syncwire.ServedTwig, nextSlot, fromSlot uint64) []syncwire.Record {
	start, end := syncwire.TwigSlots(tw.ID, nextSlot, fromSlot)
	if end <= start {
		return nil
	}
	base := tw.ID * syncwire.TwigSize
	if len(tw.Records) == 1 && tw.Records[0].Kind == syncwire.KindHollow {
		if start == base {
			return tw.Records
		}
		return nil
	}
	return tw.Records[start-base : end-base]
}

// ServeCode writes one chunk per requested hash (code or 0x03 "missing").
func ServeCode(get func(h syncwire.Hash) []byte, hashes []syncwire.Hash, write func([]byte) bool) {
	for _, h := range hashes {
		code := get(h)
		var b []byte
		if len(code) == 0 || len(code) > syncwire.MaxCodeBytes {
			b = syncwire.AppendError(nil, syncwire.CodeResourceUnavailable, "missing")
		} else {
			b = syncwire.AppendChunk(nil, syncwire.CodeSuccess, nil, code)
		}
		if !write(b) {
			return
		}
	}
}
