// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// NoopPeerDasState is a zero-custody PeerDasStateReader for the B+ block-only
// fork choice driver (#34). PeerDAS (Fusaka) column custody is not used by
// block-only fork choice; fork choice and the sentinel only read these values
// for metadata/status, so safe zero defaults are sufficient. Pair it with
// das.NewNoopPeerDas to satisfy forkChoice.InitPeerDas.

//go:build n42el

package peerdasstate

import "github.com/n42blockchain/N42/internal/cl/cltypes"

type NoopPeerDasState struct{}

var _ PeerDasStateReader = NoopPeerDasState{}

func (NoopPeerDasState) GetEarliestAvailableSlot() uint64 { return 0 }
func (NoopPeerDasState) GetRealCgc() uint64               { return 0 }
func (NoopPeerDasState) GetAdvertisedCgc() uint64         { return 0 }
func (NoopPeerDasState) GetMyCustodyColumns() (map[cltypes.CustodyIndex]bool, error) {
	return map[cltypes.CustodyIndex]bool{}, nil
}
func (NoopPeerDasState) IsSupernode() bool { return false }
