// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// Command n42-datc builds, verifies and serves a DATC archive: EIP-1186
// proofs of Ethereum state at any historical height. The implementation is
// the internal/datc package; see docs/ethel/datc-archive-plus.md.
package main

import "github.com/n42blockchain/N42/internal/datc"

func main() { datc.Main() }
