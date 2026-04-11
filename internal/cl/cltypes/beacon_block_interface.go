//go:build n42el

package cltypes

import (
	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes/solid"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
)

type GenericBeaconBlock interface {
	Version() clparams.StateVersion
	GetSlot() uint64
	GetProposerIndex() uint64
	GetParentRoot() common.Hash
	GetBody() GenericBeaconBody
}

type GenericBeaconBody interface {
	HashSSZ() ([32]byte, error)
	GetPayloadHeader() (*Eth1Header, error)
	GetRandaoReveal() common.Bytes96
	GetEth1Data() *Eth1Data
	GetSyncAggregate() *SyncAggregate

	GetProposerSlashings() *solid.ListSSZ[*ProposerSlashing]
	GetAttesterSlashings() *solid.ListSSZ[*AttesterSlashing]
	GetAttestations() *solid.ListSSZ[*solid.Attestation]
	GetDeposits() *solid.ListSSZ[*Deposit]
	GetVoluntaryExits() *solid.ListSSZ[*SignedVoluntaryExit]
	GetBlobKzgCommitments() *solid.ListSSZ[*KZGCommitment]
	GetExecutionChanges() *solid.ListSSZ[*SignedBLSToExecutionChange]
	GetExecutionRequests() *ExecutionRequests
}
