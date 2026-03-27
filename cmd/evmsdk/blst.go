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

package evmsdk

import (
	"encoding/hex"
	"fmt"

	"github.com/n42blockchain/N42/crypto/bls"
)

// decodeSecretKey decodes a hex-encoded private key string and returns the
// corresponding BLS secret key. It validates that the decoded key is exactly
// 32 bytes.
func decodeSecretKey(privKeyHex string) (bls.SecretKey, error) {
	privKeyBytes, err := hex.DecodeString(privKeyHex)
	if err != nil {
		return nil, err
	}
	if len(privKeyBytes) != 32 {
		return nil, fmt.Errorf("invalid private key length: expected 32 bytes, got %d", len(privKeyBytes))
	}
	var arr [32]byte
	copy(arr[:], privKeyBytes)
	return bls.SecretKeyFromRandom32Byte(arr)
}

func BlsSign(privKey, msg string) (interface{}, error) {
	msgBytes, err := hex.DecodeString(msg)
	if err != nil {
		return nil, err
	}
	sk, err := decodeSecretKey(privKey)
	if err != nil {
		return nil, err
	}
	resBytes := sk.Sign(msgBytes).Marshal()
	return hex.EncodeToString(resBytes), nil
}

func BlsPublicKey(privKey string) (interface{}, error) {
	sk, err := decodeSecretKey(privKey)
	if err != nil {
		return nil, err
	}
	resBytes := sk.PublicKey().Marshal()
	return hex.EncodeToString(resBytes), nil
}
