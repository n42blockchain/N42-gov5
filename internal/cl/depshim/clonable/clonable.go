// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Clonable unit for the clonable package.
// Declares the Clonable type aliases.
// Clonable interface used by SSZ types.

//go:build n42el

// Package clonable is a re-export of N42's lib/types/clonable. erigon's
// common/clonable defines an identical Clonable interface — by aliasing
// rather than vendoring we ensure cl/ types implement the SAME interface
// that the rest of N42 (lib/types/ssz, lib/types) already uses.

package clonable

import libclonable "github.com/n42blockchain/N42/lib/types/clonable"

// Clonable is the interface satisfied by anything that can produce a deep
// copy of itself. Same identity as lib/types/clonable.Clonable.
type Clonable = libclonable.Clonable
