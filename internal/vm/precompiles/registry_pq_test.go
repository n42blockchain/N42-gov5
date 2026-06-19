// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package precompiles

import (
	"testing"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestPQPrecompilesGatedRegistration(t *testing.T) {
	falcon := types.BytesToAddress([]byte{0x14})
	dil2 := types.BytesToAddress([]byte{0x15})
	dil3 := types.BytesToAddress([]byte{0x16})
	sqisign := types.BytesToAddress([]byte{0x17})

	// Off: not registered.
	off := NewRegistry(&params.Rules{ChainID: nil, IsPQPrecompiles: false})
	for _, a := range []types.Address{falcon, dil2, dil3} {
		if off.Has(a) {
			t.Fatalf("PQ precompile %x registered while IsPQPrecompiles=false", a)
		}
	}

	// On: 0x14-0x16 registered, 0x17 (SQIsign, unimplemented) NOT.
	on := NewRegistry(&params.Rules{ChainID: nil, IsPQPrecompiles: true})
	for _, a := range []types.Address{falcon, dil2, dil3} {
		if !on.Has(a) {
			t.Fatalf("PQ precompile %x not registered while IsPQPrecompiles=true", a)
		}
	}
	if on.Has(sqisign) {
		t.Fatal("SQIsign 0x17 must not be registered (verify unimplemented)")
	}
}
