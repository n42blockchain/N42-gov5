// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Bytes unit for the hexutil package.
// Declares the Bytes type aliases.
// Exports helpers such as MarshalText, UnmarshalJSON, and UnmarshalText.
// Hex encoding utilities for JSON marshalling.

//go:build n42el


package hexutil

import (
	"encoding/hex"
	"encoding/json"
	"reflect"
)

var bytesT = reflect.TypeFor[Bytes]()

// Bytes marshals/unmarshals as a JSON string with 0x prefix.
// The empty slice marshals as "0x".
type Bytes []byte

const hexPrefix = `0x`

// MarshalText implements encoding.TextMarshaler
func (b Bytes) MarshalText() ([]byte, error) {
	result := make([]byte, len(b)*2+2)
	copy(result, hexPrefix)
	hex.Encode(result[2:], b)
	return result, nil
}

// UnmarshalJSON implements json.Unmarshaler.
func (b *Bytes) UnmarshalJSON(input []byte) error {
	if !isString(input) {
		return &json.UnmarshalTypeError{Value: "non-string", Type: bytesT}
	}
	return wrapTypeError(b.UnmarshalText(input[1:len(input)-1]), bytesT)
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (b *Bytes) UnmarshalText(input []byte) error {
	raw, err := checkText(input, true)
	if err != nil {
		return err
	}
	dec := make([]byte, len(raw)/2)
	_, err = hex.Decode(dec, raw)
	if err == nil {
		*b = dec
	}
	return err
}

// String returns the hex encoding of b.
func (b Bytes) String() string {
	return Encode(b)
}
