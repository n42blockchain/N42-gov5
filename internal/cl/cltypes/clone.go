// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Clone unit for the cltypes package.
// Exports helpers such as Clone, Clone, Clone, and Clone.
// Beacon chain SSZ data structures used across phases.

//go:build n42el

package cltypes

import (
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	"github.com/n42blockchain/N42/internal/cl/depshim/clonable"
)

func (s *SignedBeaconBlock) Clone() clonable.Clonable {
	other := NewSignedBeaconBlock(s.Block.Body.beaconCfg, s.Version())
	other.Block.Body.Version = s.Block.Body.Version
	return other
}

func (i *IndexedAttestation) Clone() clonable.Clonable {
	/*
	   var attestingIndices *solid.RawUint64List

	   	if i.AttestingIndices != nil {
	   		attestingIndices = solid.NewRawUint64List(i.AttestingIndices.Cap(), []uint64{})
	   	}

	*/
	return &IndexedAttestation{
		//AttestingIndices: attestingIndices,
		Data: &solid.AttestationData{},
	}
}

func (b *BeaconBody) Clone() clonable.Clonable {
	other := NewBeaconBody(b.beaconCfg, b.Version)
	return other
}

func (e *Eth1Block) Clone() clonable.Clonable {
	return NewEth1Block(e.version, e.beaconCfg)
}

func (*Eth1Data) Clone() clonable.Clonable {
	return &Eth1Data{}
}

func (*SignedBLSToExecutionChange) Clone() clonable.Clonable {
	return &SignedBLSToExecutionChange{}
}

func (*HistoricalSummary) Clone() clonable.Clonable {
	return &HistoricalSummary{}
}

func (*DepositData) Clone() clonable.Clonable {
	return &DepositData{}
}

func (*Status) Clone() clonable.Clonable {
	return &Status{}
}

func (*SignedAggregateAndProof) Clone() clonable.Clonable {
	return &SignedAggregateAndProof{}
}

func (a *SyncAggregate) Clone() clonable.Clonable {
	return NewSyncAggregateWithSize(len(a.SyncCommiteeBits))
}

func (*SignedVoluntaryExit) Clone() clonable.Clonable {
	return &SignedVoluntaryExit{}
}

func (*ProposerSlashing) Clone() clonable.Clonable {
	return &ProposerSlashing{}
}

func (*AttesterSlashing) Clone() clonable.Clonable {
	return &AttesterSlashing{}
}

func (*Metadata) Clone() clonable.Clonable {
	return &Metadata{}
}

func (*Ping) Clone() clonable.Clonable {
	return &Ping{}
}

func (*Deposit) Clone() clonable.Clonable {
	return &Deposit{}
}

func (b *BeaconBlock) Clone() clonable.Clonable {
	other := NewBeaconBlock(b.Body.beaconCfg, b.Version())
	other.Body.Version = b.Body.Version
	return other
}

func (*AggregateAndProof) Clone() clonable.Clonable {
	return &AggregateAndProof{}
}

func (*BeaconBlockHeader) Clone() clonable.Clonable {
	return &BeaconBlockHeader{}
}

func (*BLSToExecutionChange) Clone() clonable.Clonable {
	return &BLSToExecutionChange{}
}

func (*SignedBeaconBlockHeader) Clone() clonable.Clonable {
	return &SignedBeaconBlockHeader{}
}

func (*Fork) Clone() clonable.Clonable {
	return &Fork{}
}

func (*KZGCommitment) Clone() clonable.Clonable {
	return &KZGCommitment{}
}

func (*Eth1Header) Clone() clonable.Clonable {
	return &Eth1Header{}
}

func (*Withdrawal) Clone() clonable.Clonable {
	return &Withdrawal{}
}

func (s *SignedContributionAndProof) Clone() clonable.Clonable {
	return &SignedContributionAndProof{}
}

func (s *ContributionAndProof) Clone() clonable.Clonable {
	return &ContributionAndProof{}
}

func (s *Contribution) Clone() clonable.Clonable {
	return &Contribution{}
}

func (*Root) Clone() clonable.Clonable {
	return &Root{}
}

func (*LightClientUpdatesByRangeRequest) Clone() clonable.Clonable {
	return &LightClientUpdatesByRangeRequest{}
}
