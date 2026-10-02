// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package publicrpc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/internal/ethel/rpccaps"
)

// TestParseMode covers every named mode plus the default fallback.
func TestParseMode(t *testing.T) {
	cases := map[string]rpccaps.Mode{
		"full":    rpccaps.Full,
		"m1":      rpccaps.M1,
		"M1":      rpccaps.M1,
		"m0":      rpccaps.M0,
		"M0":      rpccaps.M0,
		"archive": rpccaps.Archive,
		"":        rpccaps.Archive,
		"bogus":   rpccaps.Archive,
	}
	for in, want := range cases {
		require.Equal(t, want, ParseMode(in), "ParseMode(%q)", in)
	}
}

// TestDisabledService_LifecycleIsNoop confirms the Disabled() constructor
// yields a service whose Start/Stop/Name never touch a listener or DB.
func TestDisabledService_LifecycleIsNoop(t *testing.T) {
	s := Disabled()
	require.Equal(t, "publicRPC", s.Name())
	require.NoError(t, s.Start(context.Background()))
	require.NoError(t, s.Stop())
}

// TestDisabledService_StopIsIdempotent confirms calling Stop twice (e.g. once
// from an error path, once from normal shutdown) is safe.
func TestDisabledService_StopIsIdempotent(t *testing.T) {
	s := Disabled()
	require.NoError(t, s.Stop())
	require.NoError(t, s.Stop())
}
