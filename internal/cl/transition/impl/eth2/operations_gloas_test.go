package eth2_test

import (
	"testing"

	"github.com/n42blockchain/N42/internal/cl/clparams"
	"github.com/n42blockchain/N42/internal/cl/cltypes"
	"github.com/n42blockchain/N42/internal/cl/phase1/core/state"
	"github.com/n42blockchain/N42/internal/cl/transition/impl/eth2"
	"github.com/stretchr/testify/require"
)

func TestProcessExecutionPayloadEnvelopeRejectsNilEnvelope(t *testing.T) {
	s := state.New(&clparams.MainnetBeaconConfig)
	machine := &eth2.Impl{}

	require.Error(t, machine.ProcessExecutionPayloadEnvelope(s, nil))
	require.Error(t, machine.ProcessExecutionPayloadEnvelope(s, &cltypes.SignedExecutionPayloadEnvelope{}))
	require.Error(t, machine.ProcessExecutionPayloadEnvelope(s, &cltypes.SignedExecutionPayloadEnvelope{
		Message: &cltypes.ExecutionPayloadEnvelope{},
	}))
}
