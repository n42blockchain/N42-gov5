// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package params

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

const (
	PerformanceEd25519Version         = 2
	PerformanceTransactionsEd25519    = "ethereum-secp256k1+ed25519"
	PerformanceEd25519RuleID          = "gov5-ed25519-canonical-a-stdlib-v1"
	performanceEd25519NamespacePrefix = "gov5-performance-ed25519-v1-"
)

// PerformanceEncodingIdentity derives metadata for the offline v2 codec. This
// does not authorize runtime startup: ValidatePerformanceSelection rejects v2.
// NetworkNamespace is deliberately excluded from the digest it must equal.
func (c *ChainConfig) PerformanceEncodingIdentity() (namespace string, marker [sha256.Size]byte, err error) {
	if c == nil || c.Performance == nil {
		return "", marker, fmt.Errorf("missing explicit performance encoding configuration")
	}
	p := c.Performance
	if c.ChainID == nil || !c.ChainID.IsUint64() || c.ChainID.Uint64() != PerformanceChainID || c.Consensus != HotStuffConsensus || c.HotStuff == nil || c.StateScheme != "qmdb" {
		return "", marker, fmt.Errorf("performance encoding requires dedicated HotStuff/QMDB chain")
	}
	if p.Version != PerformanceEd25519Version || p.Mode != PerformanceModeIndependent || p.Role != PerformanceRoleLocal || p.Transactions != PerformanceTransactionsEd25519 || p.SignatureRule != PerformanceEd25519RuleID {
		return "", marker, fmt.Errorf("unsupported performance encoding descriptor")
	}
	descriptor := []byte("gov5-performance-protocol\x00")
	appendUint := func(v uint64) { descriptor = binary.BigEndian.AppendUint64(descriptor, v) }
	appendString := func(v string) {
		descriptor = binary.BigEndian.AppendUint32(descriptor, uint32(len(v)))
		descriptor = append(descriptor, v...)
	}
	appendUint(c.ChainID.Uint64())
	appendUint(p.Version)
	appendString(p.Mode)
	appendString(p.Role)
	appendString(p.Transactions)
	appendString(p.SignatureRule)
	// Always include zero. Serialization omitempty is not part of consensus identity.
	appendUint(p.Ed25519ActivationBlock)
	marker = sha256.Sum256(descriptor)
	return performanceEd25519NamespacePrefix + hex.EncodeToString(marker[:]), marker, nil
}

// ValidatePerformanceEncoding permits only explicit offline codec selection.
// Runtime entrypoints must keep using ValidatePerformanceSelection until the
// complete execution, signer, transport and admission integration is implemented.
func (c *ChainConfig) ValidatePerformanceEncoding() error {
	if c == nil || c.Performance == nil {
		return nil
	}
	if c.Performance.Version == 1 {
		profile, _ := ResolveExecutionProfile("n42")
		return c.ValidatePerformanceSelection("private", profile)
	}
	namespace, _, err := c.PerformanceEncodingIdentity()
	if err != nil {
		return err
	}
	if c.Performance.NetworkNamespace != namespace {
		return fmt.Errorf("performance namespace does not bind protocol descriptor")
	}
	return nil
}
