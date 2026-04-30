// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

//go:build windows || linux
// +build windows linux

// Package devp2p imports github.com/ethereum/go-ethereum/p2p, which
// transitively pulls in ethereum/go-ethereum/crypto/secp256k1 — its own
// vendored fork of libsecp256k1. github.com/n42blockchain/N42/crypto
// imports github.com/erigontech/secp256k1, a different fork of the same
// C library. Both forks define identical C symbols
// (secp256k1_ecdsa_recover, secp256k1_pre_g, …). When a final binary
// links both packages (e.g. cmd/n42), GNU ld and MinGW ld reject the
// duplicates with "multiple definition of …" errors.
//
// --allow-multiple-definition tells the linker to keep the first
// definition and silently drop the rest — safe here because the two
// forks are bit-identical at the relevant function level (both vendor
// upstream libsecp256k1 r1.0.x with no behavior changes; only build
// scaffolding differs).
//
// macOS clang ld lacks this flag but also does not error on duplicate
// symbols for cgo-imported archives, so the darwin build tag is
// excluded. Linux and Windows MinGW both need it.
//
// This file is intentionally cgo-only with no Go code.

package devp2p

// #cgo LDFLAGS: -Wl,--allow-multiple-definition
import "C"
