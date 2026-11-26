// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// Stream handlers of the QMDB state-sync servers (N42 sync standard §5.3):
// qmdb_status/1, qmdb_manifest/1, qmdb_twigs/1, code_by_hash/1. The data
// comes from v11.Server, built lazily on a profile-1 QMDB chain.

package sync

import (
	"context"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/internal/sync/syncwire"
	"github.com/n42blockchain/N42/internal/sync/v11"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// stateSyncServer returns the node's state-sync server (nil off profile 1
// or off QMDB).
func (s *Service) stateSyncServer() *v11.Server {
	s.stateSyncOnce.Do(func() {
		chain := s.cfg.chain
		if chain == nil || chain.DB() == nil {
			return
		}
		s.stateSync = v11.NewServer(chain.DB(), chain.Config(), chain.GenesisBlock().Hash())
	})
	return s.stateSync
}

func (s *Service) qmdbStatusHandler(ctx context.Context, pid peer.ID, raw []byte, stream network.Stream) {
	if len(raw) != 0 {
		writeV11(stream, syncwire.AppendError(nil, syncwire.CodeInvalidRequest, "qmdb_status takes an empty request"))
		return
	}
	src := chainRangeSource{s}
	st := syncwire.QMDBStatus{EarliestBody: src.EarliestBody()}
	if srv := s.stateSyncServer(); srv != nil {
		srv.MaybeRefresh(s.ctx, src.CommittedHead())
		st = srv.Status(src.EarliestBody())
	}
	writeV11(stream, syncwire.AppendChunk(nil, syncwire.CodeSuccess, nil, st.Encode()))
	log.Info("qmdb_status: served", "peer", shortPeer(pid), "pivot", st.PivotHi, "flags", st.Flags)
}

func (s *Service) qmdbManifestHandler(_ context.Context, pid peer.ID, raw []byte, stream network.Stream) {
	req, err := syncwire.DecodeManifestRequest(raw)
	if err != nil {
		writeV11(stream, syncwire.AppendError(nil, syncwire.CodeInvalidRequest, err.Error()))
		return
	}
	var snap *v11.Snapshot
	if srv := s.stateSyncServer(); srv != nil {
		snap = srv.Lookup(req.PivotHash)
	}
	payload, code, msg := v11.ServeManifest(snap, req)
	if code != syncwire.CodeSuccess {
		writeV11(stream, syncwire.AppendError(nil, code, msg))
		return
	}
	writeV11(stream, syncwire.AppendChunk(nil, syncwire.CodeSuccess, nil, payload))
	log.Info("qmdb_manifest: served", "peer", shortPeer(pid), "first", req.FirstTwig, "count", req.Count)
}

func (s *Service) qmdbTwigsHandler(_ context.Context, pid peer.ID, raw []byte, stream network.Stream) {
	req, err := syncwire.DecodeTwigsRequest(raw)
	if err != nil {
		writeV11(stream, syncwire.AppendError(nil, syncwire.CodeInvalidRequest, err.Error()))
		return
	}
	var snap *v11.Snapshot
	if srv := s.stateSyncServer(); srv != nil {
		snap = srv.Lookup(req.PivotHash)
	}
	v11.ServeTwigs(snap, req, func(b []byte) bool { return writeV11(stream, b) })
	log.Info("qmdb_twigs: served", "peer", shortPeer(pid), "first", req.FirstTwig, "count", req.TwigCount)
}

func (s *Service) codeByHashHandler(ctx context.Context, pid peer.ID, raw []byte, stream network.Stream) {
	hashes, err := syncwire.DecodeCodeRequest(raw)
	if err != nil {
		writeV11(stream, syncwire.AppendError(nil, syncwire.CodeInvalidRequest, err.Error()))
		return
	}
	db := s.cfg.chain.DB()
	get := func(h syncwire.Hash) []byte {
		var code []byte
		_ = db.View(ctx, func(tx kv.Tx) error {
			v, err := tx.GetOne(modules.Code, h[:])
			if err == nil {
				code = append([]byte(nil), v...)
			}
			return nil
		})
		return code
	}
	v11.ServeCode(get, hashes, func(b []byte) bool { return writeV11(stream, b) })
	log.Info("code_by_hash: served", "peer", shortPeer(pid), "hashes", len(hashes))
}
