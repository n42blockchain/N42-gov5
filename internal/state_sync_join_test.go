// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package internal

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/internal/sync/syncwire"
	v11 "github.com/n42blockchain/N42/internal/sync/v11"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// stateSyncServerNode joins a node from the fixture's bundle (pivot 6),
// runs it to an applied head of 8, snapshots it, then imports blocks 9..11
// and commits 7..10 so that P+1 = 9 is committed and the canonical block 10
// carries its commit QC. It returns a Responder serving pivot 8.
func stateSyncServerNode(t *testing.T, f *p1Fixture) (*v11.Responder, *p1Node) {
	t.Helper()
	db := openP1DB(t, filepath.Join(t.TempDir(), "server"))
	if _, err := f.importInto(t, db, f.bundle, nil); err != nil {
		t.Fatal(err)
	}
	n := startP1Node(t, db, f.genesis, f.cfg, f.b.hs)
	commit := func(i int) {
		if err := n.bc.CommitToCanonical(f.blocks[i].Hash()); err != nil {
			t.Fatalf("commit %d: %v", i+1, err)
		}
	}
	n.insert(t, f.blocks[6])
	n.insert(t, f.blocks[7])
	commit(6)
	snap, err := v11.BuildSnapshot(context.Background(), db, f.cfg.ChainID.Uint64(), f.genesis.Hash())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Pivot != 8 {
		t.Fatalf("snapshot pivot %d", snap.Pivot)
	}
	srv := v11.NewServer(db, f.cfg, f.genesis.Hash())
	if srv == nil {
		t.Fatal("no state-sync server on a profile-1 QMDB chain")
	}
	if ok, err := v11.Publishable(context.Background(), db, f.cfg, f.genesis.Hash(), snap); ok || err != nil {
		t.Fatalf("publishable before P+1 is committed: %v %v", ok, err)
	}
	n.insert(t, f.blocks[8])
	n.insert(t, f.blocks[9])
	n.insert(t, f.blocks[10])
	commit(7)
	commit(8)
	commit(9)
	if ok, err := v11.Publishable(context.Background(), db, f.cfg, f.genesis.Hash(), snap); !ok || err != nil {
		t.Fatalf("not publishable: %v %v", ok, err)
	}
	srv.Publish(snap)
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tx.Rollback)
	rs := v11.TxRangeSource{Tx: tx, Genesis: f.genesis.Hash()}
	return &v11.Responder{
		Range:    rs,
		CommitQC: func() v11.CommitQCSource { return v11.TxCommitQCSource{Tx: tx, Head: rs.CommittedHead()} },
		State:    srv,
		CodeOf: func(h syncwire.Hash) []byte {
			v, _ := tx.GetOne(modules.Code, h[:])
			return v
		},
	}, n
}

// rewriteChunk applies edit to the payload of the index-th chunk of a reply
// without context bytes.
func rewriteChunk(t *testing.T, reply []byte, index int, edit func([]byte) []byte) []byte {
	t.Helper()
	r := bytes.NewReader(reply)
	var out []byte
	for i := 0; r.Len() > 0; i++ {
		c, err := syncwire.ReadChunk(r, false, 1<<30)
		if err != nil {
			t.Fatal(err)
		}
		p := c.Payload
		if i == index {
			p = edit(append([]byte(nil), p...))
		}
		out = syncwire.AppendChunk(out, c.Code, nil, p)
	}
	return out
}

func TestStateSyncJoinFromPeers(t *testing.T) {
	f := buildSnapshotJoinFixture(t)
	resp, _ := stateSyncServerNode(t, f)

	fetch := func(peers ...*v11.Peer) (string, error) {
		dir := filepath.Join(t.TempDir(), "fetched")
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, err := v11.FetchBundle(ctx, peers, v11.JoinOptions{Config: f.cfg, Genesis: f.genesis.Hash(), Dir: dir, TwigBatch: 1, ManifestPage: 1})
		return dir, err
	}
	honest := v11.LoopbackPeer("honest", resp, nil)
	dir, err := fetch(honest)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}

	// The fetched bundle installs and the joiner continues from block 9.
	db := openP1DB(t, filepath.Join(t.TempDir(), "joiner"))
	res, err := f.importInto(t, db, dir, nil)
	if err != nil {
		t.Fatalf("import of the fetched bundle: %v", err)
	}
	if res.Pivot != 8 || res.Root != f.results[7].Root {
		t.Fatalf("import result %+v", res)
	}
	node := startP1Node(t, db, f.genesis, f.cfg, f.b.hs)
	for i := 8; i < len(f.blocks); i++ {
		if got := node.insert(t, f.blocks[i]); got != f.results[i] {
			t.Fatalf("block %d executed to %+v on the joiner, %+v on the source", i+1, got, f.results[i])
		}
	}
	if err := f.src.db.View(context.Background(), func(stx kv.Tx) error {
		return db.View(context.Background(), func(jtx kv.Tx) error {
			for _, table := range []string{modules.Account, modules.Storage, modules.Code} {
				if a, b := tableDigest(t, stx, table), tableDigest(t, jtx, table); a != b {
					t.Fatalf("table %s differs between the source and the joiner", table)
				}
			}
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}

	// Adversarial sources: each lie is refused; with an honest peer behind
	// it the fetch completes from that peer.
	flip := func(off int) func([]byte) []byte {
		return func(p []byte) []byte {
			if off < 0 {
				off += len(p)
			}
			p[off] ^= 0x01
			return p
		}
	}
	lies := map[string]func(proto string, req, reply []byte) []byte{
		"bad QC signature": func(proto string, _, reply []byte) []byte {
			if proto != syncwire.ProtocolCommitQC {
				return reply
			}
			// qc_len u32 ‖ QC: the aggregate signature sits inside the QC.
			return rewriteChunk(t, reply, 0, flip(60))
		},
		"bad manifest proof": func(proto string, _, reply []byte) []byte {
			if proto != syncwire.ProtocolQMDBManifest {
				return reply
			}
			return rewriteChunk(t, reply, 0, flip(-1))
		},
		"wrong twig value": func(proto string, req, reply []byte) []byte {
			if proto != syncwire.ProtocolQMDBTwigs {
				return reply
			}
			return rewriteChunk(t, reply, 0, flip(-1))
		},
		"wrong pivot block": func(proto string, req, reply []byte) []byte {
			if proto != syncwire.ProtocolBodiesByRangeV2 {
				return reply
			}
			r, _ := syncwire.DecodeRangeRequest(req)
			var digest [4]byte
			copy(digest[:], f.genesis.Hash().Bytes())
			if r.HeadersOnly() {
				return reply
			}
			return syncwire.AppendChunk(syncwire.AppendChunk(nil, 0, &digest, mustEncode(t, f.blocks[6])),
				syncwire.CodeEnd, nil, syncwire.RangeEnd{Next: r.Start + 1, ServedBytes: uint64(len(mustEncode(t, f.blocks[6]))), Reason: 0}.Encode())
		},
		"poisoned code": func(proto string, _, reply []byte) []byte {
			if proto != syncwire.ProtocolCodeByHash {
				return reply
			}
			return rewriteChunk(t, reply, 0, flip(0))
		},
	}
	for name, lie := range lies {
		liar := v11.LoopbackPeer("liar", resp, lie)
		if _, err := fetch(liar); err == nil {
			t.Fatalf("%s: fetch succeeded from a lying peer", name)
		} else if !errors.Is(err, v11.ErrJoinRefused) && !strings.Contains(err.Error(), "fault") {
			t.Logf("%s: refused with %v", name, err)
		}
		if _, err := fetch(liar, v11.LoopbackPeer("honest", resp, nil)); err != nil {
			t.Fatalf("%s: fetch with an honest fallback failed: %v", name, err)
		}
	}
}

func mustEncode(t *testing.T, b *block.Block) []byte {
	t.Helper()
	enc, err := v11.EncodeBlockPayload(b)
	if err != nil {
		t.Fatal(err)
	}
	return enc
}
