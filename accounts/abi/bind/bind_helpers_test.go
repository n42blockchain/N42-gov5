// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package bind

import (
	"testing"

	"github.com/n42blockchain/N42/accounts/abi"
)

func TestIsKeyWord(t *testing.T) {
	keywords := []string{"break", "case", "chan", "const", "continue", "default",
		"defer", "else", "fallthrough", "for", "func", "go", "goto", "if",
		"import", "interface", "map", "package", "range", "return", "select",
		"struct", "switch", "type", "var"}
	for _, kw := range keywords {
		if !isKeyWord(kw) {
			t.Errorf("isKeyWord(%q) = false, want true", kw)
		}
	}
	for _, notKw := range []string{"foo", "bar", "myVar", ""} {
		if isKeyWord(notKw) {
			t.Errorf("isKeyWord(%q) = true, want false", notKw)
		}
	}
}

func TestBindBasicTypeGo(t *testing.T) {
	cases := []struct {
		kind abi.Type
		want string
	}{
		{mustType(t, "address"), "common.Address"},
		{mustType(t, "uint8"), "uint8"},
		{mustType(t, "uint16"), "uint16"},
		{mustType(t, "uint32"), "uint32"},
		{mustType(t, "uint64"), "uint64"},
		{mustType(t, "int8"), "int8"},
		{mustType(t, "uint256"), "*big.Int"},
		{mustType(t, "int256"), "*big.Int"},
		{mustType(t, "bytes4"), "[4]byte"},
		{mustType(t, "bytes"), "[]byte"},
		{mustType(t, "bool"), "bool"},
		{mustType(t, "string"), "string"},
	}
	for _, c := range cases {
		if got := bindBasicTypeGo(c.kind); got != c.want {
			t.Errorf("bindBasicTypeGo(%s) = %q, want %q", c.kind.String(), got, c.want)
		}
	}
}

func TestBindTypeGoArraysAndSlices(t *testing.T) {
	structs := map[string]*tmplStruct{}

	sliceTy := mustType(t, "uint256[]")
	if got, want := bindTypeGo(sliceTy, structs), "[]*big.Int"; got != want {
		t.Errorf("bindTypeGo(uint256[]) = %q, want %q", got, want)
	}

	arrayTy := mustType(t, "uint8[4]")
	if got, want := bindTypeGo(arrayTy, structs), "[4]uint8"; got != want {
		t.Errorf("bindTypeGo(uint8[4]) = %q, want %q", got, want)
	}

	tupleTy := mustTupleType(t)
	if got := bindTypeGo(tupleTy, structs); got != "interface{}" {
		t.Errorf("bindTypeGo(unregistered tuple) = %q, want interface{}", got)
	}
}

func TestBindBasicTypeJava(t *testing.T) {
	cases := []struct {
		kind abi.Type
		want string
	}{
		{mustType(t, "address"), "Address"},
		{mustType(t, "uint256"), "BigInt"},
		{mustType(t, "int8"), "byte"},
		{mustType(t, "int16"), "short"},
		{mustType(t, "int32"), "int"},
		{mustType(t, "int64"), "long"},
		{mustType(t, "int24"), "BigInt"},
		{mustType(t, "bytes4"), "byte[]"},
		{mustType(t, "bytes"), "byte[]"},
		{mustType(t, "bool"), "boolean"},
		{mustType(t, "string"), "String"},
	}
	for _, c := range cases {
		if got := bindBasicTypeJava(c.kind); got != c.want {
			t.Errorf("bindBasicTypeJava(%s) = %q, want %q", c.kind.String(), got, c.want)
		}
	}
}

func TestPluralizeJavaType(t *testing.T) {
	cases := map[string]string{
		"boolean": "Bools",
		"String":  "Strings",
		"Address": "Addresses",
		"byte[]":  "Binaries",
		"BigInt":  "BigInts",
		"short":   "short[]",
	}
	for in, want := range cases {
		if got := pluralizeJavaType(in); got != want {
			t.Errorf("pluralizeJavaType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBindTypeJava(t *testing.T) {
	structs := map[string]*tmplStruct{}
	sliceTy := mustType(t, "bool[]")
	if got, want := bindTypeJava(sliceTy, structs), "Bools"; got != want {
		t.Errorf("bindTypeJava(bool[]) = %q, want %q", got, want)
	}

	tupleTy := mustTupleType(t)
	if got := bindTypeJava(tupleTy, structs); got != "Object" {
		t.Errorf("bindTypeJava(unregistered tuple) = %q, want Object", got)
	}
}

func TestBindTopicTypeGo(t *testing.T) {
	structs := map[string]*tmplStruct{}
	if got, want := bindTopicTypeGo(mustType(t, "string"), structs), "common.Hash"; got != want {
		t.Errorf("bindTopicTypeGo(string) = %q, want %q", got, want)
	}
	if got, want := bindTopicTypeGo(mustType(t, "bytes"), structs), "common.Hash"; got != want {
		t.Errorf("bindTopicTypeGo(bytes) = %q, want %q", got, want)
	}
	if got, want := bindTopicTypeGo(mustType(t, "uint256"), structs), "*big.Int"; got != want {
		t.Errorf("bindTopicTypeGo(uint256) = %q, want %q", got, want)
	}
}

func TestAlias(t *testing.T) {
	aliases := map[string]string{"foo": "bar"}
	if got := alias(aliases, "foo"); got != "bar" {
		t.Errorf("alias(foo) = %q, want bar", got)
	}
	if got := alias(aliases, "baz"); got != "baz" {
		t.Errorf("alias(baz) = %q, want baz (unmatched passthrough)", got)
	}
}

func TestDecapitalise(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"Foo":     "foo",
		"foo_bar": "fooBar",
		"FOO":     "fOO",
	}
	for in, want := range cases {
		if got := decapitalise(in); got != want {
			t.Errorf("decapitalise(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStructured(t *testing.T) {
	if structured(abi.Arguments{}) {
		t.Error("structured(empty) should be false")
	}
	one := abi.Arguments{{Name: "a"}}
	if structured(one) {
		t.Error("structured(single arg) should be false (needs >=2)")
	}

	anon := abi.Arguments{{Name: "a"}, {Name: ""}}
	if structured(anon) {
		t.Error("structured with an anonymous field should be false")
	}

	collide := abi.Arguments{{Name: "foo"}, {Name: "Foo"}}
	if structured(collide) {
		t.Error("structured with colliding normalized names should be false")
	}

	ok := abi.Arguments{{Name: "foo"}, {Name: "bar"}}
	if !structured(ok) {
		t.Error("structured with two distinct named args should be true")
	}
}

func TestHasStruct(t *testing.T) {
	if hasStruct(mustType(t, "uint256")) {
		t.Error("hasStruct(uint256) should be false")
	}
	if !hasStruct(mustTupleType(t)) {
		t.Error("hasStruct(tuple) should be true")
	}
	sliceOfUint := mustType(t, "uint256[]")
	if hasStruct(sliceOfUint) {
		t.Error("hasStruct(uint256 slice) should be false")
	}
}

func mustType(t *testing.T, s string) abi.Type {
	t.Helper()
	ty, err := abi.NewType(s, "", nil)
	if err != nil {
		t.Fatalf("abi.NewType(%q) failed: %v", s, err)
	}
	return ty
}

// mustTupleType builds a simple two-field tuple type (uint256 amount,
// address recipient) for exercising the TupleTy branches of the bind
// helpers, which abi.NewType cannot build from a bare type string.
func mustTupleType(t *testing.T) abi.Type {
	t.Helper()
	components := []abi.ArgumentMarshaling{
		{Name: "amount", Type: "uint256"},
		{Name: "recipient", Type: "address"},
	}
	ty, err := abi.NewType("tuple", "", components)
	if err != nil {
		t.Fatalf("abi.NewType(tuple) failed: %v", err)
	}
	return ty
}
