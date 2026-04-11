// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

//go:build n42el

package solid

import (
	"encoding/json"

	"github.com/n42blockchain/N42/internal/cl/depshim/clonable"
	"github.com/n42blockchain/N42/internal/cl/depshim/common"
	"github.com/n42blockchain/N42/internal/cl/merkle_tree"
	"github.com/n42blockchain/N42/lib/types/ssz"
)

type EncodableHashableSSZ interface {
	ssz.EncodableSSZ
	ssz.HashableSSZ
}

type ListSSZ[T EncodableHashableSSZ] struct {
	list []T

	limit int
	// this needs to be set to true if the underlying schema of the object
	// includes an offset in any of its sub elements.
	static bool
	// If the underlying object has static size, aka static=true
	// then we can cache its size instead of calling EncodeSizeSSZ on
	// an always newly created object
	bytesPerElement int
	// We can keep hash_tree_root result cached
	root common.Hash
}

func NewDynamicListSSZ[T EncodableHashableSSZ](limit int) *ListSSZ[T] {
	return &ListSSZ[T]{
		list:  make([]T, 0),
		limit: limit,
	}
}

func NewStaticListSSZ[T EncodableHashableSSZ](limit int, bytesPerElement int) *ListSSZ[T] {
	return &ListSSZ[T]{
		list:            make([]T, 0),
		limit:           limit,
		static:          true,
		bytesPerElement: bytesPerElement,
	}
}

func (l ListSSZ[T]) MarshalJSON() ([]byte, error) {
	return json.Marshal(l.list)
}

func (l *ListSSZ[T]) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &l.list)
}

func NewDynamicListSSZFromList[T EncodableHashableSSZ](list []T, limit int) *ListSSZ[T] {
	return &ListSSZ[T]{
		list:  list,
		limit: limit,
	}
}

func NewStaticListSSZFromList[T EncodableHashableSSZ](list []T, limit int, bytesPerElement int) *ListSSZ[T] {
	return &ListSSZ[T]{
		list:            list,
		limit:           limit,
		static:          true,
		bytesPerElement: bytesPerElement,
	}
}

func (l *ListSSZ[T]) Static() bool {
	return false
}

func (l *ListSSZ[T]) EncodeSSZ(buf []byte) (dst []byte, err error) {
	if !l.static {
		return ssz.EncodeDynamicList(buf, l.list)

	}
	dst = buf
	for _, element := range l.list {
		if dst, err = element.EncodeSSZ(dst); err != nil {
			return
		}
	}
	return
}

func (l *ListSSZ[T]) DecodeSSZ(buf []byte, version int) (err error) {
	if l.static {
		l.list, err = ssz.DecodeStaticList[T](buf, 0, uint32(len(buf)), uint32(l.bytesPerElement), uint64(l.limit), version)
	} else {
		l.list, err = ssz.DecodeDynamicList[T](buf, 0, uint32(len(buf)), uint64(l.limit), version)
	}
	l.root = common.Hash{}
	return
}

func (l *ListSSZ[T]) EncodingSizeSSZ() (size int) {
	if l.static {
		return len(l.list) * l.bytesPerElement
	}
	size = len(l.list) * 4
	for _, element := range l.list {
		size += element.EncodingSizeSSZ()
	}
	return
}

func (l *ListSSZ[T]) HashSSZ() ([32]byte, error) {
	if (l.root != common.Hash{}) {
		return l.root, nil
	}
	var err error
	l.root, err = merkle_tree.ListObjectSSZRoot(l.list, uint64(l.limit))
	return l.root, err
}

func (l *ListSSZ[T]) Clone() clonable.Clonable {
	if l.static {
		return NewStaticListSSZ[T](l.limit, l.bytesPerElement)
	}
	return NewDynamicListSSZ[T](l.limit)
}

func (l *ListSSZ[T]) Get(index int) T {
	return l.list[index]
}

func (l *ListSSZ[T]) Range(fn func(index int, value T, length int) bool) {
	for idx, element := range l.list {
		cont := fn(idx, element, len(l.list))
		if !cont {
			break
		}
	}
}

func (l *ListSSZ[T]) Len() int {
	return len(l.list)
}

func (l *ListSSZ[T]) Append(obj T) {
	l.list = append(l.list, obj)
	l.root = common.Hash{}
}

func (l *ListSSZ[T]) Clear() {
	l.list = nil
	l.root = common.Hash{}
}

func (l *ListSSZ[T]) Truncate(length int) {
	l.list = l.list[:length]
	l.root = common.Hash{}
}

func (l *ListSSZ[T]) Cut(length int) {
	if length >= len(l.list) {
		l.list = make([]T, 0)
	} else {
		l.list = l.list[length:]
	}
	l.root = common.Hash{}
}

func (l *ListSSZ[T]) ElementProof(i int) [][32]byte {
	leaves := make([]any, l.limit)
	for i := range leaves {
		leaves[i] = make([]byte, 32)
	}
	for i, element := range l.list {
		root, err := element.HashSSZ()
		if err != nil {
			panic(err)
		}
		leaves[i] = root[:]
	}
	d := GetDepth(uint64(l.limit))
	branch, err := merkle_tree.MerkleProof(int(d), i, leaves...)
	if err != nil {
		panic(err)
	}
	return append(branch, merkle_tree.Uint64Root(uint64(len(l.list))))
}

func (l *ListSSZ[T]) ShallowCopy() *ListSSZ[T] {
	cpy := &ListSSZ[T]{
		list:            make([]T, len(l.list), cap(l.list)),
		limit:           l.limit,
		static:          l.static,
		bytesPerElement: l.bytesPerElement,
		root:            common.Hash(common.Copy(l.root[:])),
	}
	copy(cpy.list, l.list)
	return cpy
}
