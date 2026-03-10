// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package sync_pb

// HotStuff-2 consensus wire-format message types for SSZ encoding.

// HotStuffQC is the wire format for a QuorumCertificate.
type HotStuffQC struct {
	View               uint64 `ssz-size:"8"`
	BlockHash          []byte `ssz-size:"32"`
	AggregateSignature []byte `ssz-max:"256"`
	SignersBitmap      []byte `ssz-max:"1024"` // packed bool bitmap
}

// HotStuffTC is the wire format for a TimeoutCertificate.
type HotStuffTC struct {
	View               uint64 `ssz-size:"8"`
	AggregateSignature []byte `ssz-max:"256"`
	SignersBitmap      []byte `ssz-max:"1024"`
	HighQC             []byte `ssz-max:"2048"` // encoded HotStuffQC
}

// HotStuffProposal is the wire format for a Proposal message.
type HotStuffProposal struct {
	View       uint64 `ssz-size:"8"`
	BlockHash  []byte `ssz-size:"32"`
	JustifyQC  []byte `ssz-max:"2048"` // encoded HotStuffQC
	Proposer   uint32 `ssz-size:"4"`
	Signature  []byte `ssz-max:"256"`
	PrepareQC  []byte `ssz-max:"2048"` // optional piggybacked QC (0 length = absent)
}

// HotStuffVote is the wire format for a Round 1 (Prepare) vote.
type HotStuffVote struct {
	View      uint64 `ssz-size:"8"`
	BlockHash []byte `ssz-size:"32"`
	Voter     uint32 `ssz-size:"4"`
	Signature []byte `ssz-max:"256"`
}

// HotStuffCommitVote is the wire format for a Round 2 (Commit) vote.
type HotStuffCommitVote struct {
	View      uint64 `ssz-size:"8"`
	BlockHash []byte `ssz-size:"32"`
	Voter     uint32 `ssz-size:"4"`
	Signature []byte `ssz-max:"256"`
}

// HotStuffPrepareQCMsg is the wire format for a PrepareQC broadcast.
type HotStuffPrepareQCMsg struct {
	View      uint64 `ssz-size:"8"`
	BlockHash []byte `ssz-size:"32"`
	QC        []byte `ssz-max:"2048"` // encoded HotStuffQC
}

// HotStuffTimeoutMsg is the wire format for a timeout message.
type HotStuffTimeoutMsg struct {
	View      uint64 `ssz-size:"8"`
	HighQC    []byte `ssz-max:"2048"` // encoded HotStuffQC
	Sender    uint32 `ssz-size:"4"`
	Signature []byte `ssz-max:"256"`
}

// HotStuffNewViewMsg is the wire format for a NewView message.
type HotStuffNewViewMsg struct {
	View        uint64 `ssz-size:"8"`
	TimeoutCert []byte `ssz-max:"4096"` // encoded HotStuffTC
	Leader      uint32 `ssz-size:"4"`
	Signature   []byte `ssz-max:"256"`
}

// HotStuffDecide is the wire format for a Decide (commit) message.
type HotStuffDecide struct {
	View      uint64 `ssz-size:"8"`
	BlockHash []byte `ssz-size:"32"`
	CommitQC  []byte `ssz-max:"2048"` // encoded HotStuffQC
}

// HotStuffConsensusMsg is the envelope for all HotStuff consensus messages.
type HotStuffConsensusMsg struct {
	Type    uint8  `ssz-size:"1"`
	Payload []byte `ssz-max:"8192"`
}
