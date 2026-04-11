// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.
//
// Core type aliases and constants for the BMT package. Hash is an alias
// for common/types.Hash and NodeValue wraps the raw serialised bytes of
// a node. Stored values are self-identifying: the first byte is leafTag
// ('L' = 0x4C) or internalTag ('I' = 0x49), enabling the isLeaf / isInternal
// predicates used throughout insert, proof and walk code. EmptyHash and
// ErrNotFound supply the sentinel values returned for missing children.

package bmt

import (
	"errors"

	"github.com/n42blockchain/N42/common/types"
)

const HashSize = 32

// Node type tags — first byte of stored value distinguishes node kind.
const (
	leafTag     byte = 0x4C // 'L' — leaf node
	internalTag byte = 0x49 // 'I' — internal node
)

type Hash = types.Hash

type NodeValue []byte

var (
	EmptyHash   = Hash{}
	ErrNotFound = errors.New("bmt: not found")
)

func isLeaf(v NodeValue) bool     { return len(v) > 0 && v[0] == leafTag }
func isInternal(v NodeValue) bool { return len(v) == 1+2*HashSize && v[0] == internalTag }
