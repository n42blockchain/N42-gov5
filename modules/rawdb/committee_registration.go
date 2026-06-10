// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package rawdb

import (
	"encoding/binary"
	"fmt"

	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

// WriteCommitteeRegistration records a validator hand-over: pubkey (48-byte
// BLS12-381 G1) takes over committee-pool slot. Persisted so the hand-over
// survives node restart.
func WriteCommitteeRegistration(tx kv.Putter, slot int, pubkey []byte) error {
	if len(pubkey) != 48 {
		return fmt.Errorf("committee registration: pubkey must be 48 bytes, got %d", len(pubkey))
	}
	var key [4]byte
	binary.BigEndian.PutUint32(key[:], uint32(slot))
	return tx.Put(modules.CommitteeRegistration, key[:], pubkey)
}

// ReadCommitteeRegistrations loads every persisted slot→pubkey hand-over.
func ReadCommitteeRegistrations(tx kv.Tx) (map[int][]byte, error) {
	out := make(map[int][]byte)
	c, err := tx.Cursor(modules.CommitteeRegistration)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	for k, v, err := c.First(); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if len(k) != 4 || len(v) != 48 {
			continue
		}
		slot := int(binary.BigEndian.Uint32(k))
		pk := make([]byte, 48)
		copy(pk, v)
		out[slot] = pk
	}
	return out, nil
}
