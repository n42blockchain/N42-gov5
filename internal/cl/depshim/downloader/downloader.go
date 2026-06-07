//go:build n42el

// Copyright 2026 The N42 Authors
// This file is part of the N42 library.
//
// Package downloader is a depshim for erigon's db/downloader — it provides the
// Client interface surface the Caplin antiquary takes (only Seed/Delete are
// reached; the real grpc/protobuf Download path is not used in N42's follower
// model) plus a NoopClient for follower-mode wiring (snapshot seeding disabled).

package downloader

import "context"

// Client mirrors the subset of erigon's downloader.Client that Caplin uses:
// seed/delete generated snapshot files. The protobuf Download method is omitted
// because N42 does not download erigon-format snapshots (DB-fallback model).
type Client interface {
	// Seed makes the downloader hash + seed the given files.
	Seed(ctx context.Context, paths []string) error
	// Delete removes files from the downloader.
	Delete(ctx context.Context, paths []string) error
}

// NoopClient is a Client that does nothing — used when snapshot seeding is
// disabled (the follower / DB-fallback configuration).
type NoopClient struct{}

func (NoopClient) Seed(context.Context, []string) error   { return nil }
func (NoopClient) Delete(context.Context, []string) error { return nil }
