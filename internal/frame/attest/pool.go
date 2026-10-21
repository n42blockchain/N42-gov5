// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package attest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/frame/store"
	"github.com/n42blockchain/N42/log"
)

// Validators is the validator view the pool works against: the committed
// set of the current epoch.
type Validators struct {
	// Sorted holds the validator addresses ascending (SortAddresses).
	Sorted []types.Address
	// F is the fault tolerance: f+1 matching attestations are accepted.
	F int
	// Verify checks attester's signature over msg (a 32-byte SigningHash).
	Verify func(attester types.Address, msg, sig []byte) bool
}

// SortAddresses returns addrs sorted ascending (a copy).
func SortAddresses(addrs []types.Address) []types.Address {
	out := append([]types.Address(nil), addrs...)
	sort.Slice(out, func(i, j int) bool { return bytes.Compare(out[i][:], out[j][:]) < 0 })
	return out
}

func (v *Validators) index(a types.Address) int {
	i := sort.Search(len(v.Sorted), func(i int) bool { return bytes.Compare(v.Sorted[i][:], a[:]) >= 0 })
	if i < len(v.Sorted) && v.Sorted[i] == a {
		return i
	}
	return -1
}

// Config wires a pool into a node.
type Config struct {
	// Validators returns the current set (nil: attestation is inactive and
	// every frame is recovered locally).
	Validators func() *Validators
	// Self is this node's validator address (zero: not a validator; it
	// only consumes attestations).
	Self func() types.Address
	// Sign signs a SigningHash with this node's validator key (nil, or an
	// error: nothing is published).
	Sign func(msg []byte) ([]byte, error)
	// Signer is the signer accepted senders are cached under (the one
	// admission recovery uses).
	Signer func() transaction.Signer
	// Store receives accepted senders (nil: Senders only).
	Store *store.Store
	// CommitteeSize is the committee size; it is raised to f+1.
	CommitteeSize int
	// TestCommittee (TEST ONLY, N42_ATTEST_COMMITTEE; 0 = off) replaces
	// CommitteeSize with k and accepts min(k, f+1) matching attestations
	// instead of f+1. Below f+1 the honest-member guarantee is gone.
	TestCommittee int
	// Send broadcasts one EncodeWire message to the other validators.
	Send func(msg []byte)
	// FlushEvery is the publishing batch interval (<= 0: 20 ms).
	FlushEvery time.Duration
	// Cap bounds the frames remembered (<= 0: 1<<16).
	Cap int
}

type digestVotes struct {
	by   []types.Address
	list []types.Address
}

type frameVotes struct {
	digests  map[types.Hash]*digestVotes
	accepted []types.Address
}

// Pool collects attestations, accepts a frame's senders at f+1 matching
// ones, and publishes this node's own attestations. It implements
// frame.SenderSource. Safe for concurrent use.
type Pool struct {
	cfg Config

	mu     sync.Mutex
	frames map[types.Hash]*frameVotes
	order  []types.Hash
	head   int

	outMu sync.Mutex
	out   []Entry
	kick  chan struct{}

	batches, entries, accepted, published, publishedBatches atomic.Uint64
	badSig, notMember, badDigest, badBatch, fallback        atomic.Uint64
	lastDigestWarn                                          atomic.Int64
	reducedWarned                                           atomic.Bool

	startOnce sync.Once
	cancel    context.CancelFunc
	wg        sync.WaitGroup
}

// NewPool returns a pool; Start begins publishing.
func NewPool(cfg Config) *Pool {
	if cfg.FlushEvery <= 0 {
		cfg.FlushEvery = 20 * time.Millisecond
	}
	if cfg.Cap <= 0 {
		cfg.Cap = 1 << 16
	}
	if cfg.CommitteeSize <= 0 {
		cfg.CommitteeSize = 3
	}
	return &Pool{cfg: cfg, frames: make(map[types.Hash]*frameVotes), kick: make(chan struct{}, 1)}
}

// committee returns frame h's members and the attestations needed.
func (p *Pool) committee(v *Validators, h types.Hash) ([]types.Address, int) {
	if v == nil || len(v.Sorted) == 0 {
		return nil, 0
	}
	need := v.F + 1
	size := p.cfg.CommitteeSize
	if k := p.cfg.TestCommittee; k > 0 {
		size = k
		if k < need {
			if !p.reducedWarned.Swap(true) {
				log.Warn("!!! FRAME ATTESTATION QUORUM BELOW f+1 (TEST ONLY) !!! "+
					"k colluding committee members can forge frame senders; never run this outside a private benchmark fleet",
					"k", k, "f", v.F, "f+1", v.F+1, "validators", len(v.Sorted))
			}
			need = k
		}
	}
	if size < need {
		size = need
	}
	idx := Committee(h, len(v.Sorted), size)
	if len(idx) < need {
		return nil, 0
	}
	out := make([]types.Address, len(idx))
	for i, k := range idx {
		out[i] = v.Sorted[k]
	}
	return out, need
}

func (p *Pool) validators() *Validators {
	if p.cfg.Validators == nil {
		return nil
	}
	return p.cfg.Validators()
}

func (p *Pool) self() types.Address {
	if p.cfg.Self == nil {
		return types.Address{}
	}
	return p.cfg.Self()
}

// InCommittee reports whether this node is on frame h's committee.
func (p *Pool) InCommittee(h types.Hash) bool {
	self := p.self()
	if self == (types.Address{}) {
		return false
	}
	members, _ := p.committee(p.validators(), h)
	for _, m := range members {
		if m == self {
			return true
		}
	}
	return false
}

// Senders returns frame h's accepted senders (n < 0: any count), or nil.
func (p *Pool) Senders(h types.Hash, n int) []types.Address {
	p.mu.Lock()
	fv := p.frames[h]
	var out []types.Address
	if fv != nil {
		out = fv.accepted
	}
	p.mu.Unlock()
	if out == nil || (n >= 0 && len(out) != n) {
		return nil
	}
	return out
}

// Pending reports whether frame h's senders are expected from the
// committee: attestation is active and this node is not on h's committee.
func (p *Pool) Pending(h types.Hash) bool {
	v := p.validators()
	if v == nil || len(v.Sorted) == 0 {
		return false
	}
	return !p.InCommittee(h)
}

// Recovered is told every frame this node recovered: a committee member
// queues its attestation; any other node's recovery is a fallback (its
// attestations did not arrive in time).
func (p *Pool) Recovered(h types.Hash, ss *store.Senders) {
	if ss == nil {
		return
	}
	if v := p.validators(); v == nil || len(v.Sorted) == 0 {
		return
	}
	if !p.InCommittee(h) {
		p.fallback.Add(1)
		return
	}
	e := Entry{Frame: h, Digest: Digest(ss.Addrs), Senders: ss.Addrs}
	// The list is known now: the other members' attestations of the same
	// digest count without hashing their copies of it again (hasList).
	p.know(h, e.Digest, ss.Addrs)
	if p.cfg.Sign == nil || p.cfg.Send == nil {
		return
	}
	p.outMu.Lock()
	p.out = append(p.out, e)
	full := len(p.out) >= MaxEntries
	p.outMu.Unlock()
	if full {
		select {
		case p.kick <- struct{}{}:
		default:
		}
	}
}

// Errors of Add.
var (
	ErrBadSignature = errors.New("attest: signature does not verify")
	ErrNotValidator = errors.New("attest: attester is not a validator")
)

// HandleWire takes one batch body off the ingest listener.
func (p *Pool) HandleWire(body []byte) {
	b, err := DecodeBody(body)
	if err != nil {
		p.badBatch.Add(1)
		return
	}
	if err := p.Add(b); err != nil {
		log.Debug("frame attestation refused", "attester", b.Attester, "err", err)
	}
}

// Add verifies a batch and records its entries. An entry whose attester
// is not on the frame's committee, or whose senders do not hash to its
// digest, is dropped (and counted); the rest of the batch stands.
func (p *Pool) Add(b *Batch) error {
	v := p.validators()
	if v == nil || v.index(b.Attester) < 0 {
		p.badBatch.Add(1)
		return ErrNotValidator
	}
	msg := SigningHash(b.Attester, b.Entries)
	if v.Verify == nil || !v.Verify(b.Attester, msg[:], b.Sig) {
		p.badSig.Add(1)
		return ErrBadSignature
	}
	p.batches.Add(1)
	type acc struct {
		h     types.Hash
		addrs []types.Address
	}
	var done []acc
	for _, e := range b.Entries {
		p.entries.Add(1)
		members, need := p.committee(v, e.Frame)
		member := false
		for _, m := range members {
			if m == b.Attester {
				member = true
				break
			}
		}
		if !member {
			p.notMember.Add(1)
			continue
		}
		if e.Senders != nil && p.hasList(e.Frame, e.Digest) {
			// The list is known (or the frame accepted): the digest alone
			// counts, and the list is not hashed again.
			e.Senders = nil
		}
		if e.Senders != nil && Digest(e.Senders) != e.Digest {
			p.badDigest.Add(1)
			if now := time.Now().Unix(); p.lastDigestWarn.Swap(now) != now {
				log.Warn("frame attestation: senders do not match the signed digest, dropped",
					"attester", b.Attester, "frame", e.Frame, "digest", e.Digest)
			}
			continue
		}
		if e.Senders != nil {
			// DecodeBody's lists alias the wire buffer: the pool keeps its
			// own copy of a list it records.
			e.Senders = append([]types.Address(nil), e.Senders...)
		}
		if addrs := p.record(e, b.Attester, need); addrs != nil {
			done = append(done, acc{e.Frame, addrs})
		}
	}
	for _, a := range done {
		p.accepted.Add(1)
		if p.cfg.Store != nil && p.cfg.Signer != nil {
			if signer := p.cfg.Signer(); signer != nil {
				p.cfg.Store.SetSenders(a.h, &store.Senders{Signer: signer, Addrs: a.addrs})
			}
		}
	}
	return nil
}

// hasList reports whether frame h is accepted or digest d already has a
// verified list.
func (p *Pool) hasList(h, d types.Hash) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	fv := p.frames[h]
	if fv == nil {
		return false
	}
	if fv.accepted != nil {
		return true
	}
	dv := fv.digests[d]
	return dv != nil && dv.list != nil
}

// know records addrs as the verified list of frame h's digest d (this
// node recovered it and computed d) without counting a vote.
func (p *Pool) know(h, d types.Hash, addrs []types.Address) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fv := p.frameLocked(h)
	if fv.accepted != nil {
		return
	}
	dv := fv.digests[d]
	if dv == nil {
		dv = &digestVotes{}
		fv.digests[d] = dv
	}
	if dv.list == nil {
		dv.list = addrs
	}
}

// frameLocked returns frame h's votes, creating them (and evicting the
// oldest frames past Cap) when absent.
func (p *Pool) frameLocked(h types.Hash) *frameVotes {
	fv := p.frames[h]
	if fv != nil {
		return fv
	}
	fv = &frameVotes{digests: make(map[types.Hash]*digestVotes, 1)}
	p.frames[h] = fv
	p.order = append(p.order, h)
	for len(p.frames) > p.cfg.Cap && p.head < len(p.order) {
		delete(p.frames, p.order[p.head])
		p.head++
	}
	if p.head > len(p.order)/2 && p.head > 1024 {
		p.order = append([]types.Hash(nil), p.order[p.head:]...)
		p.head = 0
	}
	return fv
}

// record adds one attestation and returns the senders when it completes
// f+1 matching attestations with a known list.
func (p *Pool) record(e Entry, by types.Address, need int) []types.Address {
	p.mu.Lock()
	defer p.mu.Unlock()
	fv := p.frameLocked(e.Frame)
	if fv.accepted != nil {
		return nil
	}
	dv := fv.digests[e.Digest]
	if dv == nil {
		dv = &digestVotes{}
		fv.digests[e.Digest] = dv
	}
	for _, a := range dv.by {
		if a == by {
			return nil
		}
	}
	dv.by = append(dv.by, by)
	if dv.list == nil && e.Senders != nil {
		dv.list = e.Senders
	}
	if len(dv.by) >= need && dv.list != nil {
		fv.accepted = dv.list
		fv.digests = nil
		return dv.list
	}
	return nil
}

// Stats is a snapshot of the pool's counters.
type Stats struct {
	Batches, Entries, Accepted, Published, PublishedBatches uint64
	BadSig, NotMember, BadDigest, BadBatch, Fallback        uint64
}

// Stats returns the counters.
func (p *Pool) Stats() Stats {
	return Stats{Batches: p.batches.Load(), Entries: p.entries.Load(), Accepted: p.accepted.Load(),
		Published: p.published.Load(), PublishedBatches: p.publishedBatches.Load(),
		BadSig: p.badSig.Load(), NotMember: p.notMember.Load(), BadDigest: p.badDigest.Load(),
		BadBatch: p.badBatch.Load(), Fallback: p.fallback.Load()}
}

func (s Stats) String() string {
	return fmt.Sprintf("batches=%d entries=%d accepted=%d published=%d/%d badSig=%d notMember=%d badDigest=%d badBatch=%d fallback=%d",
		s.Batches, s.Entries, s.Accepted, s.Published, s.PublishedBatches, s.BadSig, s.NotMember, s.BadDigest, s.BadBatch, s.Fallback)
}

// Start begins publishing this node's attestations every FlushEvery and
// logging the counters every 10 s.
func (p *Pool) Start(ctx context.Context) {
	p.startOnce.Do(func() {
		ctx, p.cancel = context.WithCancel(ctx)
		p.wg.Add(1)
		go p.publishLoop(ctx)
	})
}

// Stop stops publishing (pending attestations are flushed first).
func (p *Pool) Stop() {
	if p.cancel != nil {
		p.cancel()
	}
	p.wg.Wait()
}

func (p *Pool) publishLoop(ctx context.Context) {
	defer p.wg.Done()
	t := time.NewTicker(p.cfg.FlushEvery)
	defer t.Stop()
	logT := time.NewTicker(10 * time.Second)
	defer logT.Stop()
	var last Stats
	for {
		select {
		case <-ctx.Done():
			p.Flush()
			return
		case <-t.C:
			p.Flush()
		case <-p.kick:
			p.Flush()
		case <-logT.C:
			if s := p.Stats(); s != last {
				log.Info("frame attestations", "stats", s.String())
				last = s
			}
		}
	}
}

// Flush signs and sends the queued attestations now.
func (p *Pool) Flush() {
	p.outMu.Lock()
	out := p.out
	p.out = nil
	p.outMu.Unlock()
	self := p.self()
	if len(out) == 0 || self == (types.Address{}) || p.cfg.Sign == nil || p.cfg.Send == nil {
		return
	}
	for len(out) > 0 {
		n := len(out)
		if n > MaxEntries {
			n = MaxEntries
		}
		b := &Batch{Attester: self, Entries: out[:n]}
		msg := SigningHash(self, b.Entries)
		sig, err := p.cfg.Sign(msg[:])
		if err != nil {
			log.Debug("frame attestation: cannot sign", "err", err)
			return
		}
		b.Sig = sig
		p.cfg.Send(EncodeWire(b))
		p.published.Add(uint64(n))
		p.publishedBatches.Add(1)
		out = out[n:]
	}
}
