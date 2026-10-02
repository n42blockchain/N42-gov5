// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build !n42el

package eldevp2p

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/types"
)

// TestDefaultConfig checks the stub's default field values, which
// cmd/eth-el relies on when the binary is built without -tags n42el.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	require.False(t, cfg.Enabled)
	require.Equal(t, ":30303", cfg.ListenAddr)
	require.Equal(t, 50, cfg.MaxPeers)
	require.NotEmpty(t, cfg.BootNodes)
}

// TestService_DisabledStartIsNoop confirms Start on a disabled service
// returns nil (the common default-build path).
func TestService_DisabledStartIsNoop(t *testing.T) {
	s := New(Config{Enabled: false}, nil, types.Hash{}, 0)
	require.Equal(t, "el-devp2p", s.Name())
	require.NoError(t, s.Start(context.Background()))
	require.NoError(t, s.Stop())
}

// TestService_EnabledStartErrors confirms Start on an enabled service in
// the default (non-n42el) build surfaces a clear "rebuild with the tag"
// error rather than silently doing nothing.
func TestService_EnabledStartErrors(t *testing.T) {
	s := New(Config{Enabled: true}, nil, types.Hash{}, 0)
	err := s.Start(context.Background())
	require.Error(t, err)
}

// TestService_NoopSetters confirms the stub's SetEngineSyncBridge,
// SetTxPool and RequestMissingAncestor never panic even though they do
// nothing in the default build.
func TestService_NoopSetters(t *testing.T) {
	s := New(Config{}, nil, types.Hash{}, 0)
	s.SetEngineSyncBridge(nil, nil)
	s.SetTxPool(nil)
	s.RequestMissingAncestor(types.Hash{})
}
