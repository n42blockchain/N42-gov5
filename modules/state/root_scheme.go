// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package state

// RootScheme identifies the hash/trie semantics behind a state root.
type RootScheme string

const (
	// RootSchemeUnknown means the caller cannot determine the effective root semantics.
	RootSchemeUnknown RootScheme = "unknown"
	// RootSchemeLegacyKeccak is the legacy incremental Keccak hash over dirty objects.
	RootSchemeLegacyKeccak RootScheme = "legacy-keccak"
	// RootSchemeJMTBlake3 is the Jellyfish Merkle Tree root using Blake3-addressed nodes.
	RootSchemeJMTBlake3 RootScheme = "jmt-blake3"
	// RootSchemeBMTBlake3 is the Binary Merkle Tree root using Blake3 path-addressed nodes.
	RootSchemeBMTBlake3 RootScheme = "bmt-blake3"
	// RootSchemeVerkle is the Verkle tree root using IPA/Banderwagon commitments (experimental).
	RootSchemeVerkle RootScheme = "verkle"
	// RootSchemeEthereumMPT is the canonical Ethereum account/storage MPT root.
	RootSchemeEthereumMPT RootScheme = "ethereum-mpt"
	// RootSchemeQMDB is the QMDB append-only twig-forest world root (Blake3).
	RootSchemeQMDB RootScheme = "qmdb"
)

// RootSchemeReporter is an optional extension for RootComputer implementations
// that can describe the effective state-root semantics they produce.
type RootSchemeReporter interface {
	RootScheme() RootScheme
}

// DefaultScheme returns "legacy-keccak" when the config field is empty.
func DefaultScheme(s string) RootScheme {
	if s == "" {
		return RootSchemeLegacyKeccak
	}
	return RootScheme(s)
}
