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

// Package hotstuff implements the HotStuff-2 Byzantine Fault Tolerant consensus protocol.
//
// HotStuff-2 achieves consensus in 2 rounds (optimistic path):
//
//	Round 1 (Prepare): Leader proposes block → validators vote → PrepareQC formed
//	Round 2 (Commit):  Leader broadcasts PrepareQC → validators commit-vote → CommitQC formed → block finalized
//
// Timeout recovery: validators broadcast TimeoutMessage → new leader forms TC → NewView
//
// Safety: A validator only votes for proposals where justify_qc.view >= locked_qc.view.
// Liveness: Exponential backoff pacemaker ensures eventual view change.
package hotstuff

import (
	"encoding/binary"

	"github.com/n42blockchain/N42/common/types"
)

// ViewNumber is a monotonically increasing round identifier.
type ViewNumber = uint64

// ValidatorIndex is the position of a validator in the ordered validator set.
type ValidatorIndex = uint32

// Phase represents the state within a consensus view.
type Phase uint8

const (
	// PhaseWaitingForProposal is the initial phase, waiting for the leader to propose.
	PhaseWaitingForProposal Phase = iota
	// PhaseVoting indicates the leader has proposed; collecting Round 1 votes.
	PhaseVoting
	// PhasePreCommit indicates PrepareQC formed; collecting Round 2 commit votes.
	PhasePreCommit
	// PhaseCommitted indicates the block is committed (terminal for this view).
	PhaseCommitted
	// PhaseTimedOut indicates the view has timed out; collecting timeout messages.
	PhaseTimedOut
)

func (p Phase) String() string {
	switch p {
	case PhaseWaitingForProposal:
		return "WaitingForProposal"
	case PhaseVoting:
		return "Voting"
	case PhasePreCommit:
		return "PreCommit"
	case PhaseCommitted:
		return "Committed"
	case PhaseTimedOut:
		return "TimedOut"
	default:
		return "Unknown"
	}
}

// QuorumCertificate proves that 2f+1 validators signed (view, block_hash).
type QuorumCertificate struct {
	View               ViewNumber
	BlockHash          types.Hash
	AggregateSignature []byte   // Aggregated BLS signature bytes
	Signers            []bool   // Bitmap of which validators signed
}

// Genesis returns a genesis QC with view 0 and zero block hash.
func GenesisQC() QuorumCertificate {
	return QuorumCertificate{
		View:      0,
		BlockHash: types.Hash{},
		Signers:   nil,
	}
}

// SignerCount returns the number of validators who signed this QC.
func (qc *QuorumCertificate) SignerCount() int {
	count := 0
	for _, signed := range qc.Signers {
		if signed {
			count++
		}
	}
	return count
}

// Clone returns a deep copy of the QC.
func (qc *QuorumCertificate) Clone() QuorumCertificate {
	clone := QuorumCertificate{
		View:      qc.View,
		BlockHash: qc.BlockHash,
	}
	if qc.AggregateSignature != nil {
		clone.AggregateSignature = make([]byte, len(qc.AggregateSignature))
		copy(clone.AggregateSignature, qc.AggregateSignature)
	}
	if qc.Signers != nil {
		clone.Signers = make([]bool, len(qc.Signers))
		copy(clone.Signers, qc.Signers)
	}
	return clone
}

// TimeoutCertificate proves that 2f+1 validators timed out in a view.
type TimeoutCertificate struct {
	View               ViewNumber
	AggregateSignature []byte
	Signers            []bool
	HighQC             QuorumCertificate // Highest QC known among timeouts
}

// Proposal is a block proposal from the leader.
type Proposal struct {
	View       ViewNumber
	BlockHash  types.Hash
	JustifyQC  QuorumCertificate        // QC justifying this proposal
	Proposer   ValidatorIndex
	Signature  []byte                   // BLS signature over (view, block_hash)
	PrepareQC  *QuorumCertificate       // Piggybacked from previous view (chained mode)
	TxRootHash types.Hash               // DA commitment: transaction root for data availability check (Baby Raptr)
}

// Vote is a Round 1 (Prepare) vote from a validator.
type Vote struct {
	View      ViewNumber
	BlockHash types.Hash
	Voter     ValidatorIndex
	Signature []byte
}

// CommitVote is a Round 2 (Commit) vote from a validator.
type CommitVote struct {
	View      ViewNumber
	BlockHash types.Hash
	Voter     ValidatorIndex
	Signature []byte
}

// PrepareQCMsg is a message from the leader broadcasting the PrepareQC.
type PrepareQCMsg struct {
	View      ViewNumber
	BlockHash types.Hash
	QC        QuorumCertificate
}

// Decide is the final commit message from the leader.
type Decide struct {
	View      ViewNumber
	BlockHash types.Hash
	CommitQC  QuorumCertificate
}

// TimeoutMessage is broadcast when a validator's view timer expires.
type TimeoutMessage struct {
	View      ViewNumber
	HighQC    QuorumCertificate
	Sender    ValidatorIndex
	Signature []byte
}

// NewViewMsg is sent by the new leader after forming a TC.
type NewViewMsg struct {
	View        ViewNumber
	TimeoutCert TimeoutCertificate
	Leader      ValidatorIndex
	Signature   []byte
}

// ConsensusMsg is an envelope for all consensus message types.
type ConsensusMsg struct {
	Type    ConsensusMsgType
	Payload interface{}
}

// ConsensusMsgType identifies the type of consensus message.
type ConsensusMsgType uint8

const (
	MsgProposal    ConsensusMsgType = 1
	MsgVote        ConsensusMsgType = 2
	MsgCommitVote  ConsensusMsgType = 3
	MsgPrepareQC   ConsensusMsgType = 4
	MsgTimeout     ConsensusMsgType = 5
	MsgNewView     ConsensusMsgType = 6
	MsgDecide      ConsensusMsgType = 7
)

// SigningMessage returns the message to sign for Round 1 votes: view (8 bytes LE) || block_hash (32 bytes).
func SigningMessage(view ViewNumber, blockHash types.Hash) []byte {
	msg := make([]byte, 40)
	binary.LittleEndian.PutUint64(msg[:8], view)
	copy(msg[8:], blockHash[:])
	return msg
}

// CommitSigningMessage returns the message to sign for Round 2 commit votes:
// "commit" (6 bytes) || view (8 bytes LE) || block_hash (32 bytes).
func CommitSigningMessage(view ViewNumber, blockHash types.Hash) []byte {
	msg := make([]byte, 46)
	copy(msg[:6], []byte("commit"))
	binary.LittleEndian.PutUint64(msg[6:14], view)
	copy(msg[14:], blockHash[:])
	return msg
}

// TimeoutSigningMessage returns the message for timeout signatures:
// "timeout" (7 bytes) || view (8 bytes LE).
func TimeoutSigningMessage(view ViewNumber) []byte {
	msg := make([]byte, 15)
	copy(msg[:7], []byte("timeout"))
	binary.LittleEndian.PutUint64(msg[7:], view)
	return msg
}

// NewViewSigningMessage returns the message for NewView signatures:
// "newview" (7 bytes) || view (8 bytes LE).
func NewViewSigningMessage(view ViewNumber) []byte {
	msg := make([]byte, 15)
	copy(msg[:7], []byte("newview"))
	binary.LittleEndian.PutUint64(msg[7:], view)
	return msg
}
