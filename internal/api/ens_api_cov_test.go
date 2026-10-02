// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package api

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/common/types"
)

func TestENSAPI_Namehash(t *testing.T) {
	e := NewENSAPI(setupStorageTestAPI(t))

	h, err := e.Namehash(context.Background(), "")
	if err != nil || h != (types.Hash{}) {
		t.Fatalf("Namehash(empty) = %v, %v, want zero hash, nil", h, err)
	}

	h2, err := e.Namehash(context.Background(), "example.eth")
	if err != nil {
		t.Fatalf("Namehash() error = %v", err)
	}
	if h2 == (types.Hash{}) {
		t.Fatal("Namehash(example.eth) = zero hash, want non-zero")
	}
}

func TestENSAPI_IsValidName(t *testing.T) {
	e := NewENSAPI(setupStorageTestAPI(t))

	ok, err := e.IsValidName(context.Background(), "example.eth")
	if err != nil || !ok {
		t.Fatalf("IsValidName(example.eth) = %v, %v, want true, nil", ok, err)
	}

	ok2, err := e.IsValidName(context.Background(), "")
	if err != nil || ok2 {
		t.Fatalf("IsValidName(empty) = %v, %v, want false, nil", ok2, err)
	}
}

func TestENSAPI_ResolveName_InvalidName(t *testing.T) {
	e := NewENSAPI(setupStorageTestAPI(t))

	if _, err := e.ResolveName(context.Background(), ""); err == nil {
		t.Fatal("ResolveName(empty) error = nil, want error")
	}
}

func TestENSAPI_ResolveName_ChainReachesEthCall(t *testing.T) {
	e := NewENSAPI(setupStorageTestAPI(t))

	// A syntactically valid name passes the name/registry checks and reaches
	// ethCall, which always errors ("requires blockchain state access") since
	// it is a stub in this codebase. This exercises registryAddress,
	// getResolver and the ethCall plumbing.
	if _, err := e.ResolveName(context.Background(), "example.eth"); err == nil {
		t.Fatal("ResolveName(example.eth) error = nil, want ethCall stub error")
	}
}

func TestENSAPI_ResolveAddress(t *testing.T) {
	e := NewENSAPI(setupStorageTestAPI(t))
	if _, err := e.ResolveAddress(context.Background(), types.Address{}); err == nil {
		t.Fatal("ResolveAddress() error = nil, want ethCall stub error")
	}
}

func TestENSAPI_GetContentHash_InvalidAndValid(t *testing.T) {
	e := NewENSAPI(setupStorageTestAPI(t))
	if _, err := e.GetContentHash(context.Background(), ""); err == nil {
		t.Fatal("GetContentHash(empty) error = nil, want error")
	}
	if _, err := e.GetContentHash(context.Background(), "example.eth"); err == nil {
		t.Fatal("GetContentHash(example.eth) error = nil, want ethCall stub error")
	}
}

func TestENSAPI_GetTextRecord(t *testing.T) {
	e := NewENSAPI(setupStorageTestAPI(t))
	if _, err := e.GetTextRecord(context.Background(), "", "url"); err == nil {
		t.Fatal("GetTextRecord(empty name) error = nil, want error")
	}
	if _, err := e.GetTextRecord(context.Background(), "example.eth", "url"); err == nil {
		t.Fatal("GetTextRecord(example.eth) error = nil, want ethCall stub error")
	}
}

func TestENSAPI_GetOwner(t *testing.T) {
	e := NewENSAPI(setupStorageTestAPI(t))
	if _, err := e.GetOwner(context.Background(), ""); err == nil {
		t.Fatal("GetOwner(empty) error = nil, want error")
	}
	if _, err := e.GetOwner(context.Background(), "example.eth"); err == nil {
		t.Fatal("GetOwner(example.eth) error = nil, want ethCall stub error")
	}
}

func TestENSAPI_GetResolver(t *testing.T) {
	e := NewENSAPI(setupStorageTestAPI(t))
	if _, err := e.GetResolver(context.Background(), ""); err == nil {
		t.Fatal("GetResolver(empty) error = nil, want error")
	}
	if _, err := e.GetResolver(context.Background(), "example.eth"); err == nil {
		t.Fatal("GetResolver(example.eth) error = nil, want ethCall stub error")
	}
}

func TestENSAPI_RegistryAddress_NilReceiver(t *testing.T) {
	var e *ENSAPI
	if _, err := e.registryAddress(); err == nil {
		t.Fatal("registryAddress(nil receiver) error = nil, want error")
	}
}
