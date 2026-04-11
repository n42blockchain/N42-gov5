// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package cltypes

import (
	"encoding/json"
	"reflect"

	goethkzg "github.com/crate-crypto/go-eth-kzg"

	"github.com/n42blockchain/N42/internal/cl/depshim/clonable"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/depshim/hexutil"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	ssz2 "github.com/n42blockchain/N42/internal/cl/ssz"
)

var (
	blobT = reflect.TypeFor[Blob]()

	_ ssz2.SizedObjectSSZ = (*Blob)(nil)
	_ ssz2.SizedObjectSSZ = (*KZGProof)(nil)
)

type Blob goethkzg.Blob
type KZGProof goethkzg.KZGProof // [48]byte

const (
	// https://github.com/ethereum/consensus-specs/blob/3a2304981a3b820a22b518fe4859f4bba0ebc83b/specs/deneb/polynomial-commitments.md#custom-types
	BYTES_PER_FIELD_ELEMENT = 32
	FIELD_ELEMENTS_PER_BLOB = 4096
	BYTES_KZG_PROOF         = 48
	BYTES_PER_BLOB          = BYTES_PER_FIELD_ELEMENT * FIELD_ELEMENTS_PER_BLOB
)

type KZGCommitment goethkzg.KZGCommitment

func (b KZGCommitment) MarshalJSON() ([]byte, error) {
	return json.Marshal(common.Bytes48(b))
}

func (b *KZGCommitment) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, (*common.Bytes48)(b))
}

func (b *KZGCommitment) Copy() *KZGCommitment {
	copy := *b
	return &copy
}

func (b *KZGCommitment) EncodeSSZ(buf []byte) ([]byte, error) {
	return append(buf, b[:]...), nil
}

func (b *KZGCommitment) DecodeSSZ(buf []byte, version int) error {
	return ssz2.UnmarshalSSZ(buf, version, b[:])
}

func (b *KZGCommitment) EncodingSizeSSZ() int {
	return 48
}

func (b *KZGCommitment) HashSSZ() ([32]byte, error) {
	return merkle_tree.BytesRoot(b[:])
}

func (b *Blob) MarshalJSON() ([]byte, error) {
	return json.Marshal(hexutil.Bytes(b[:]))
}

func (b *Blob) UnmarshalJSON(in []byte) error {
	return hexutil.UnmarshalFixedJSON(blobT, in, b[:])
}

func (b *Blob) Clone() clonable.Clonable {
	return &Blob{}
}

func (b *Blob) DecodeSSZ(buf []byte, version int) error {
	copy(b[:], buf)
	return nil
}

func (b *Blob) EncodeSSZ(buf []byte) ([]byte, error) {
	return append(buf, b[:]...), nil
}

func (b *Blob) EncodingSizeSSZ() int {
	return len(b[:])
}

func (b *Blob) Static() bool {
	return true
}

func (b *Blob) HashSSZ() ([32]byte, error) {
	return merkle_tree.BytesRoot(b[:])
}

func (b *KZGProof) MarshalJSON() ([]byte, error) {
	return json.Marshal(common.Bytes48(*b))
}

func (b *KZGProof) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, (*common.Bytes48)(b))
}

func (b *KZGProof) DecodeSSZ(buf []byte, version int) error {
	return ssz2.UnmarshalSSZ(buf, version, b[:])
}

func (b *KZGProof) EncodeSSZ(buf []byte) ([]byte, error) {
	return append(buf, b[:]...), nil
}

func (b *KZGProof) EncodingSizeSSZ() int {
	return 48
}

func (b *KZGProof) HashSSZ() ([32]byte, error) {
	return merkle_tree.BytesRoot(b[:])
}

func (b *KZGProof) Clone() clonable.Clonable {
	return &KZGProof{}
}

func (b *KZGProof) Static() bool {
	return true
}
