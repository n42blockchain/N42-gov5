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

package frame

import (
	"crypto/ecdsa"
	"errors"

	"github.com/n42blockchain/N42/crypto"
)

// localKeyDomain separates the local-frame key derivation from every other
// use of the validator secret.
const localKeyDomain = "n42-local-frame-key-v1"

// DeriveLocalKey derives the secp256k1 key a validator signs its local
// frames with from its consensus secret (the HotStuff BLS secret key bytes):
// keccak256(domain || counter || secret), the first counter that yields a
// valid scalar. Deterministic, so the key survives restarts without a key
// file; the secret never leaves the node and the derived key reveals
// nothing about it.
func DeriveLocalKey(secret []byte) (*ecdsa.PrivateKey, error) {
	if len(secret) == 0 {
		return nil, errors.New("frame: empty secret for the local frame key")
	}
	for ctr := byte(0); ctr < 16; ctr++ {
		d := crypto.Keccak256([]byte(localKeyDomain), []byte{ctr}, secret)
		if k, err := crypto.ToECDSA(d); err == nil {
			return k, nil
		}
	}
	return nil, errors.New("frame: no valid local frame key")
}
