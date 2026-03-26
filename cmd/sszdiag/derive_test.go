package main

import (
	"fmt"
	"testing"

	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/transaction"
	internalcore "github.com/n42blockchain/N42/internal"
)

func TestDeriveShaLegacyEmpty(t *testing.T) {
	empty := transaction.Transactions{}

	h1 := hash.DeriveSha(empty)
	h2 := internalcore.DeriveSha(empty)

	fmt.Printf("hash.DeriveSha(empty)     = %s (NilHash=%v)\n", h1.Hex()[:18], h1 == hash.NilHash)
	fmt.Printf("internal.DeriveSha(empty) = %s (NilHash=%v)\n", h2.Hex()[:18], h2 == hash.NilHash)

	if h1 != hash.NilHash {
		t.Fatalf("hash.DeriveSha(empty) = %s, want NilHash %s", h1.Hex(), hash.NilHash.Hex())
	}
	if h2 != hash.NilHash {
		t.Fatalf("internal.DeriveSha(empty) = %s, want NilHash %s", h2.Hex(), hash.NilHash.Hex())
	}
}
