// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package lthash

import (
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
)

// LtHashDigestTable is the MDBX table name for the LtHash digest.
const LtHashDigestTable = "LtHashDigest"

// ReadLtHashDigest reads the persisted LtHash digest from the database.
// Returns nil if no digest has been stored yet.
func ReadLtHashDigest(tx kv.Tx, table string) (*Digest, error) {
	data, err := tx.GetOne(table, []byte("digest"))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	if len(data) < DigestSize {
		return nil, fmt.Errorf("lthash: corrupted digest, got %d bytes, want %d", len(data), DigestSize)
	}
	d := New()
	copy(d[:], data[:DigestSize])
	return d, nil
}

// WriteLtHashDigest persists the LtHash digest to the database.
func WriteLtHashDigest(tx kv.RwTx, table string, d *Digest) error {
	return tx.Put(table, []byte("digest"), d[:])
}
