// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Length unit for the length package.
// Protocol length constants.
// Part of the n42el consensus-layer build.

//go:build n42el

// Package length re-exports the byte-length constants from N42's
// lib/common/length so the cl/ tree's
// `github.com/n42blockchain/N42/internal/cl/depshim/length` imports resolve to the same
// values as the rest of the project.

package length

import liblength "github.com/n42blockchain/N42/lib/common/length"

const (
	Hash     = liblength.Hash
	Addr     = liblength.Addr
	BlockNum = liblength.BlockNum
	Ts       = liblength.Ts
	PeerID   = liblength.PeerID
	Bytes4   = liblength.Bytes4
	Bytes48  = liblength.Bytes48
	Bytes64  = liblength.Bytes64
	Bytes96  = liblength.Bytes96
)
