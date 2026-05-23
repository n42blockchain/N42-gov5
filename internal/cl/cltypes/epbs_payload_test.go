//go:build n42el

package cltypes

import (
	"testing"

	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/stretchr/testify/require"
)

func TestSignedExecutionPayloadEnvelopeCloneNilMessage(t *testing.T) {
	envelope := &SignedExecutionPayloadEnvelope{
		Signature: common.Bytes96{1, 2, 3},
	}

	cloned := envelope.Clone().(*SignedExecutionPayloadEnvelope)
	require.Nil(t, cloned.Message)
	require.Equal(t, envelope.Signature, cloned.Signature)
}
