package node

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/internal/p2p/encoder"
)

func TestReadRotorStreamWithinLimit(t *testing.T) {
	data, err := readRotorStream(bytes.NewReader([]byte("hello")))
	require.NoError(t, err)
	require.Equal(t, []byte("hello"), data)
}

func TestReadRotorStreamExceedsLimit(t *testing.T) {
	oversized := strings.NewReader(strings.Repeat("a", int(encoder.MaxGossipSize)+10))
	_, err := readRotorStream(oversized)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds")
}

func TestNewHotstuffP2PAdapterWraps(t *testing.T) {
	a := newHotstuffP2PAdapter(nil)
	require.NotNil(t, a)
	require.Nil(t, a.P2P)
}
