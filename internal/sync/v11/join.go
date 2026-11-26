// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package v11

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/snapshotjoin"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules/state/commitment"
	"github.com/n42blockchain/N42/params"
)

// JoinOptions configures FetchBundle.
type JoinOptions struct {
	Config  *params.ChainConfig
	Genesis types.Hash
	// Dir receives the bundle; it must be empty or absent.
	Dir string
	// ManifestPage and TwigBatch size the requests (defaults 4096 and 64);
	// TwigBytes is the per-reply budget (0 = server default).
	ManifestPage uint32
	TwigBatch    uint32
	TwigBytes    uint32
	// StatusWait is how long to keep asking for qmdb_status while no peer
	// has a published snapshot yet (a server builds one on first request).
	StatusWait time.Duration
	// RequestTimeout bounds one request (default 60 s).
	RequestTimeout time.Duration
	Logger         log.Logger
}

// ErrJoinRefused marks data that failed verification; the peer that sent it
// is dropped and the next source is tried.
var ErrJoinRefused = errors.New("state sync: refused")

func refused(format string, a ...interface{}) error {
	return fmt.Errorf("%w: %s", ErrJoinRefused, fmt.Sprintf(format, a...))
}

// sources rotates through the peers serving one pivot, dropping a peer that
// failed.
type sources struct {
	peers []*Peer
	log   log.Logger
}

func (s *sources) try(what string, f func(p *Peer) error) error {
	var last error
	for len(s.peers) > 0 {
		p := s.peers[0]
		err := f(p)
		if err == nil {
			return nil
		}
		last = err
		s.log.Warn("state sync: source failed, dropping it", "peer", p, "step", what, "err", err)
		s.peers = s.peers[1:]
	}
	if last == nil {
		last = errors.New("no peers")
	}
	return fmt.Errorf("state sync: %s: no source left: %w", what, last)
}

// FetchBundle downloads the state of a committed pivot from peers and writes
// it to opts.Dir as a profile-1 snapshot bundle (snapshotjoin layout), which
// snapshotjoin.Import then verifies and installs. Every piece is verified as
// it arrives: the commit QC against the genesis validator set, the header
// chain from the QC's block down to P-255, each manifest page's range proof
// and each twig against header(P+1).Root, the plain-state rows against the
// twig records (LIVE_P preimages) and each bytecode against its hash.
func FetchBundle(ctx context.Context, peers []*Peer, opts JoinOptions) (*snapshotjoin.Manifest, error) {
	lg := opts.Logger
	if lg == nil {
		lg = log.New()
	}
	if opts.ManifestPage == 0 {
		opts.ManifestPage = 4096
	}
	if opts.TwigBatch == 0 || opts.TwigBatch > syncwire.MaxTwigsPerRequest {
		opts.TwigBatch = 64
	}
	if opts.RequestTimeout == 0 {
		opts.RequestTimeout = time.Minute
	}
	id, err := snapshotjoin.IdentityFor(opts.Config, opts.Genesis)
	if err != nil {
		return nil, err
	}
	if err := emptyDir(opts.Dir); err != nil {
		return nil, err
	}
	call := func(f func(ctx context.Context) error) error {
		c, cancel := context.WithTimeout(ctx, opts.RequestTimeout)
		defer cancel()
		return f(c)
	}
	started := time.Now()

	// 1. The pivot: the highest advertised; the peers advertising it serve.
	type adv struct {
		p  *Peer
		st syncwire.QMDBStatus
	}
	var advs []adv
	deadline := time.Now().Add(opts.StatusWait)
	for {
		for _, p := range peers {
			var st syncwire.QMDBStatus
			err := call(func(c context.Context) (err error) { st, err = p.Status(c); return })
			if err != nil {
				lg.Warn("state sync: status failed", "peer", p, "err", err)
				continue
			}
			if st.Flags&syncwire.StatusFlagTwigs == 0 || st.PivotHi == 0 {
				lg.Info("state sync: peer has no servable pivot yet", "peer", p)
				continue
			}
			advs = append(advs, adv{p, st})
		}
		if len(advs) > 0 || !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	if len(advs) == 0 {
		return nil, errors.New("state sync: no peer serves a pivot")
	}
	sort.SliceStable(advs, func(i, j int) bool { return advs[i].st.PivotHi > advs[j].st.PivotHi })
	pivot, pivotHash := advs[0].st.PivotHi, types.Hash(advs[0].st.PivotHiHash)
	src := &sources{log: lg}
	for _, a := range advs {
		if a.st.PivotHi == pivot && types.Hash(a.st.PivotHiHash) == pivotHash {
			src.peers = append(src.peers, a.p)
		}
	}
	lg.Info("state sync: pivot chosen", "pivot", pivot, "hash", pivotHash, "sources", len(src.peers))

	// 2. The anchor: a commit QC reaching P+1 with its headers, then the
	// headers P..max(P-255, 1) over bodies_by_range/2.
	var anchor *snapshotjoin.Anchor
	var pivotHdr, postHdr *block.Header
	if err := src.try("anchor", func(p *Peer) error {
		var rep *CommitQCReply
		if err := call(func(c context.Context) (err error) {
			rep, err = p.CommitQC(c, syncwire.CommitQCRequest{Number: pivot + 1})
			return
		}); err != nil {
			return err
		}
		if len(rep.Headers) == 0 {
			return refused("the commit QC of block %d is more than %d headers above it", pivot+1, syncwire.MaxCommitQCHeaders)
		}
		if err := id.VerifyQC(rep.QC); err != nil {
			return refused("commit QC (view %d): %v", rep.QC.View, err)
		}
		if low := rep.Headers[len(rep.Headers)-1]; low.ParentHash != pivotHash {
			return refused("block %d does not extend the advertised pivot %x", pivot+1, pivotHash)
		}
		lowest := uint64(1)
		if pivot > snapshotjoin.AncestorHeaders {
			lowest = pivot - snapshotjoin.AncestorHeaders + 1
		}
		var below []*block.Header
		for next := lowest; next <= pivot; {
			var res *RangeV2Result
			req := syncwire.RangeRequest{Start: next, Count: pivot - next + 1, Flags: syncwire.FlagHeadersOnly}
			if err := call(func(c context.Context) (err error) { res, err = p.Range(c, req); return }); err != nil {
				return err
			}
			if len(res.Headers) == 0 {
				return refused("no headers from %d", next)
			}
			if len(below) > 0 && res.Headers[0].ParentHash != below[len(below)-1].Hash() {
				return refused("header %d does not link", next)
			}
			below = append(below, res.Headers...)
			next += uint64(len(res.Headers))
		}
		a := &snapshotjoin.Anchor{QC: rep.QC, Headers: append([]*block.Header(nil), rep.Headers...)}
		for i := len(below) - 1; i >= 0; i-- {
			a.Headers = append(a.Headers, below[i])
		}
		ph, nh, err := id.VerifyAnchor(a, pivot, 1)
		if err != nil {
			return refused("%v", err)
		}
		if ph.Hash() != pivotHash {
			return refused("the certified block %d is %x, not the advertised %x", pivot, ph.Hash(), pivotHash)
		}
		if !opts.Config.IsDeferredExecution(nh.Time) {
			return refused("block %d is not under deferred execution", pivot+1)
		}
		anchor, pivotHdr, postHdr = a, ph, nh
		return nil
	}); err != nil {
		return nil, err
	}
	root := postHdr.Root
	lg.Info("state sync: pivot anchored", "pivot", pivot, "postStateRoot", root, "qcView", anchor.QC.View,
		"qcBlock", anchor.Headers[0].Number.Uint64(), "headers", len(anchor.Headers))

	// 3. Block P.
	var blk *block.Block
	if err := src.try("pivot block", func(p *Peer) error {
		var res *RangeV2Result
		if err := call(func(c context.Context) (err error) {
			res, err = p.Range(c, syncwire.RangeRequest{Start: pivot, Count: 1})
			return
		}); err != nil {
			return err
		}
		if len(res.Blocks) != 1 || res.Blocks[0].Hash() != pivotHash {
			return refused("block %d is not the pivot", pivot)
		}
		b := res.Blocks[0]
		if got := hash.DeriveShaErigon(transaction.EthTransactions(b.Transactions())); got != pivotHdr.TxHash {
			return refused("block %d transactions root %x, header %x", pivot, got, pivotHdr.TxHash)
		}
		blk = b
		return nil
	}); err != nil {
		return nil, err
	}

	// 4. The manifest: every twig root, proven against header(P+1).Root.
	var (
		nextSlot, twigCount uint64
		roots               []syncwire.TwigRoots
	)
	for first := uint64(0); first == 0 || first < twigCount; {
		if err := src.try("manifest", func(p *Peer) error {
			var page syncwire.ManifestPage
			req := syncwire.ManifestRequest{PivotHash: pivotHash, FirstTwig: first, Count: opts.ManifestPage}
			if err := call(func(c context.Context) (err error) { page, err = p.Manifest(c, req); return }); err != nil {
				return err
			}
			if page.PivotNumber != pivot || types.Hash(page.PivotHash) != pivotHash || page.FirstTwig != first {
				return refused("manifest page for %d %x from %d", page.PivotNumber, page.PivotHash, page.FirstTwig)
			}
			if err := syncwire.VerifyManifestPage(page, root); err != nil {
				return refused("manifest page from twig %d: %v", first, err)
			}
			if first == 0 {
				nextSlot, twigCount = page.NextSlot, page.TwigCount
			} else if page.NextSlot != nextSlot || page.TwigCount != twigCount {
				return refused("manifest pages disagree on next_slot")
			}
			if len(page.Twigs) == 0 && twigCount > 0 {
				return refused("empty manifest page")
			}
			roots = append(roots, page.Twigs...)
			return nil
		}); err != nil {
			return nil, err
		}
		first = uint64(len(roots))
		if twigCount == 0 {
			break
		}
	}

	// 5. The twigs, each verified against its proven roots.
	twigs := make([]syncwire.ServedTwig, 0, twigCount)
	for uint64(len(twigs)) < twigCount {
		next := uint64(len(twigs))
		count := uint64(opts.TwigBatch)
		if count > twigCount-next {
			count = twigCount - next
		}
		if err := src.try("twigs", func(p *Peer) error {
			var chunks []syncwire.TwigChunk
			req := syncwire.TwigsRequest{PivotHash: pivotHash, FirstTwig: next, TwigCount: uint32(count), FromSlot: next * syncwire.TwigSize, MaxBytes: opts.TwigBytes}
			if err := call(func(c context.Context) (err error) { chunks, _, err = p.Twigs(c, req); return }); err != nil {
				return err
			}
			if len(chunks) == 0 {
				return refused("no twig from %d", next)
			}
			for _, c := range chunks {
				if err := syncwire.VerifyTwig(c, roots[c.TwigID], nextSlot); err != nil {
					return refused("twig %d: %v", c.TwigID, err)
				}
				twigs = append(twigs, syncwire.ServedTwig{ID: c.TwigID, Bits: c.Bits, Records: c.Records, Roots: roots[c.TwigID]})
			}
			return nil
		}); err != nil {
			return nil, err
		}
		if len(twigs)%4096 < int(opts.TwigBatch) {
			lg.Info("state sync: twigs", "fetched", len(twigs), "of", twigCount)
		}
	}
	qmdbBin, err := syncwire.AssemblePortable(syncwire.PortableHeader{
		ChainID: id.ChainID, GenesisHash: opts.Genesis, BlockNumber: pivot, BlockHash: pivotHash, Root: root, NextSlot: nextSlot,
	}, twigs)
	if err != nil {
		return nil, err
	}

	// 6. Plain state from the LIVE_P records, code by hash.
	rows, slots, live, err := plainRows(twigs)
	if err != nil {
		return nil, err
	}
	need := map[types.Hash]bool{}
	for _, r := range rows {
		if !r.acct.IsEmptyCodeHash() {
			need[r.acct.CodeHash] = true
		}
	}
	codes := map[types.Hash][]byte{}
	var want []syncwire.Hash
	for h := range need {
		want = append(want, syncwire.Hash(h))
	}
	sort.Slice(want, func(i, j int) bool { return bytes.Compare(want[i][:], want[j][:]) < 0 })
	for len(want) > 0 {
		n := len(want)
		if n > syncwire.MaxCodeHashes {
			n = syncwire.MaxCodeHashes
		}
		batch := want[:n]
		if err := src.try("code", func(p *Peer) error {
			var got [][]byte
			if err := call(func(c context.Context) (err error) { got, err = p.Code(c, batch); return }); err != nil {
				return err
			}
			for i, h := range batch {
				if got[i] == nil {
					return fmt.Errorf("code %x missing", h)
				}
				if crypto.Keccak256Hash(got[i]) != types.Hash(h) {
					return refused("code %x does not hash to its key", h)
				}
				codes[types.Hash(h)] = got[i]
			}
			return nil
		}); err != nil {
			return nil, err
		}
		want = want[n:]
	}

	// 7. The bundle.
	m, err := writeBundle(opts, id.ChainID, anchor, pivotHdr, postHdr, blk, qmdbBin, rows, codes, root)
	if err != nil {
		return nil, err
	}
	m.StorageSlots, m.LiveLeaves, m.NextSlot = slots, live, nextSlot
	if err := writeManifest(opts.Dir, m); err != nil {
		return nil, err
	}
	lg.Info("state sync: bundle fetched", "pivot", pivot, "root", root, "twigs", twigCount, "accounts", len(rows),
		"storageSlots", slots, "codes", len(codes), "elapsed", time.Since(started).Round(time.Millisecond))
	return m, nil
}

type plainRow struct {
	addr    types.Address
	acct    account.StateAccount
	storage map[types.Hash]types.Hash
}

// plainRows turns the live records into accounts with storage. A LIVE
// record without a preimage is only allowed for a retained empty account
// (value 0x00), which the plain state does not list.
func plainRows(twigs []syncwire.ServedTwig) ([]*plainRow, uint64, uint64, error) {
	byAddr := map[types.Address]*plainRow{}
	type slot struct {
		addr       types.Address
		key, value types.Hash
	}
	var storage []slot
	var live uint64
	for i := range twigs {
		for _, r := range twigs[i].Records {
			switch r.Kind {
			case syncwire.KindLive:
				live++
				if !bytes.Equal(r.Value, []byte{0}) {
					return nil, 0, 0, refused("twig %d: a live leaf without preimage that is not an empty account", twigs[i].ID)
				}
			case syncwire.KindLiveP:
				live++
				switch len(r.Preimage) {
				case types.AddressLength:
					addr := types.BytesToAddress(r.Preimage)
					row := &plainRow{addr: addr, storage: map[types.Hash]types.Hash{}}
					if err := commitment.DecodeAccountValue(r.Value, &row.acct); err != nil {
						return nil, 0, 0, refused("account %s: %v", addr, err)
					}
					if !bytes.Equal(commitment.EncodeAccountValue(&row.acct), r.Value) {
						return nil, 0, 0, refused("account %s: non-canonical value", addr)
					}
					if _, dup := byAddr[addr]; dup {
						return nil, 0, 0, refused("account %s twice", addr)
					}
					byAddr[addr] = row
				case types.AddressLength + types.HashLength:
					if len(r.Value) != types.HashLength {
						return nil, 0, 0, refused("storage value of %d bytes", len(r.Value))
					}
					storage = append(storage, slot{types.BytesToAddress(r.Preimage[:20]), types.BytesToHash(r.Preimage[20:]), types.BytesToHash(r.Value)})
				}
			}
		}
	}
	for _, s := range storage {
		row, ok := byAddr[s.addr]
		if !ok {
			return nil, 0, 0, refused("storage of %s, which has no account", s.addr)
		}
		row.storage[s.key] = s.value
	}
	rows := make([]*plainRow, 0, len(byAddr))
	for _, r := range byAddr {
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return bytes.Compare(rows[i].addr[:], rows[j].addr[:]) < 0 })
	return rows, uint64(len(storage)), live, nil
}

func emptyDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return os.MkdirAll(dir, 0o755)
	}
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("state sync: %s is not empty", dir)
	}
	return nil
}

func writeBundle(opts JoinOptions, chainID uint64, anchor *snapshotjoin.Anchor, pivotHdr, postHdr *block.Header, blk *block.Block,
	qmdbBin []byte, rows []*plainRow, codes map[types.Hash][]byte, root types.Hash) (*snapshotjoin.Manifest, error) {
	m := &snapshotjoin.Manifest{
		Format: snapshotjoin.ManifestFormat, ChainID: chainID, GenesisHash: opts.Genesis.Hex(),
		PivotNumber: pivotHdr.Number.Uint64(), PivotHash: pivotHdr.Hash().Hex(), PostStateRoot: root.Hex(), DeferredDepth: 1,
		RootLabel:    "post_state_root is the state after executing pivot_number; it equals header(pivot_number+1).Root (deferred depth 1)",
		AnchorQCView: uint64(anchor.QC.View), AnchorNumber: anchor.Headers[0].Number.Uint64(), AnchorHash: anchor.Headers[0].Hash().Hex(),
		AnchorDomain: "H2-v4 commit, fetched over commit_qc/1", AnchorLow: anchor.Headers[len(anchor.Headers)-1].Number.Uint64(),
		Accounts: uint64(len(rows)), Files: map[string]string{},
	}
	put := func(name string, data []byte) error {
		if err := os.WriteFile(filepath.Join(opts.Dir, name), data, 0o644); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		m.Files[name] = hex.EncodeToString(sum[:])
		return nil
	}
	enc := func(v interface{}) []byte { b, _ := rlp.EncodeToBytes(v); return b }
	anchorBin, err := snapshotjoin.EncodeAnchor(anchor)
	if err != nil {
		return nil, err
	}
	var state bytes.Buffer
	je := json.NewEncoder(&state)
	if err := je.Encode(map[string]string{"root": root.Hex()}); err != nil {
		return nil, err
	}
	for _, r := range rows {
		item := snapshotjoin.DumpAccount{
			Address: "0x" + hex.EncodeToString(r.addr[:]), Balance: snapshotjoin.Quantity(r.acct.Balance.Bytes()), Nonce: fmt.Sprintf("0x%x", r.acct.Nonce),
		}
		if !r.acct.IsEmptyCodeHash() {
			item.Code = "0x" + hex.EncodeToString(codes[r.acct.CodeHash])
		}
		if len(r.storage) > 0 {
			item.Storage = map[string]string{}
			for k, v := range r.storage {
				item.Storage[k.Hex()] = v.Hex()
			}
		}
		if err := je.Encode(&item); err != nil {
			return nil, err
		}
	}
	for _, f := range []struct {
		name string
		data []byte
	}{
		{snapshotjoin.FileHeader, enc(pivotHdr)}, {snapshotjoin.FileNextHeader, enc(postHdr)}, {snapshotjoin.FileBlock, enc(blk)},
		{snapshotjoin.FileAnchor, anchorBin}, {snapshotjoin.FileQMDB, qmdbBin}, {snapshotjoin.FileState, state.Bytes()},
	} {
		if err := put(f.name, f.data); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func writeManifest(dir string, m *snapshotjoin.Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, snapshotjoin.FileManifest), data, 0o644)
}
