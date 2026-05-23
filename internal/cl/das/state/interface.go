// Copyright 2024 The Erigon Authors / 2022-2026 The N42 Authors
// This file is part of the N42 library — cherry-picked verbatim from
// ../erigon/cl/das/state/interface.go (12 lines, leaf node).
//
// Package peerdasstate exposes the PeerDasStateReader interface that
// the fork-choice store reads to learn about local DAS custody.
// Phase 7.2.0 — first cherry-pick from cl/das, kept as a stub-friendly
// shim until the full PeerDAS implementation lands (Fusaka).

//go:build n42el

package peerdasstate

import "github.com/n42blockchain/N42/internal/cl/cltypes"

//go:generate mockgen -typed=true -destination=mock_services/peer_das_state_reader_mock.go -package=mock_services . PeerDasStateReader
type PeerDasStateReader interface {
	GetEarliestAvailableSlot() uint64
	GetRealCgc() uint64
	GetAdvertisedCgc() uint64
	GetMyCustodyColumns() (map[cltypes.CustodyIndex]bool, error)
	IsSupernode() bool
}
