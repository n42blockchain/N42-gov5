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
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/crypto/ecies"
)

func (e *EvmEngine) Decrypt(req *EmitRequest) (interface{}, error) {
	params, ok := req.Val.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("empty input value")
	}

	privateKeyString, err := requireStringParam(params, "priv_key", "privateKey")
	if err != nil {
		return nil, err
	}

	privateKey, err := crypto.HexToECDSA(privateKeyString)
	if err != nil {
		return nil, fmt.Errorf("cannot decode private key")
	}

	message, err := requireStringParam(params, "msg", "msg")
	if err != nil {
		return nil, err
	}

	messageBytes, err := hex.DecodeString(message)
	if err != nil {
		return nil, fmt.Errorf("msg cannot decode to bytes")
	}

	priKey := ecies.ImportECDSA(privateKey)
	ms, err := priKey.Decrypt(messageBytes, nil, nil)
	return hex.EncodeToString(ms), err
}

func (e *EvmEngine) Encrypt(req *EmitRequest) (interface{}, error) {
	params, ok := req.Val.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("empty input value")
	}

	publicKeyString, err := requireStringParam(params, "public_key", "public_key")
	if err != nil {
		return nil, err
	}

	publicKeyBytes, err := hex.DecodeString(publicKeyString)
	if err != nil {
		return nil, fmt.Errorf("public_key cannot decode to bytes")
	}

	var publicKey *ecdsa.PublicKey
	publicKey, err = crypto.DecompressPubkey(publicKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("cannot decode public key")
	}

	message, err := requireStringParam(params, "msg", "msg")
	if err != nil {
		return nil, err
	}

	messageBytes, err := hex.DecodeString(message)
	if err != nil {
		return nil, fmt.Errorf("msg cannot decode to bytes")
	}

	pubKey := ecies.ImportECDSAPublic(publicKey)
	ct, err := ecies.Encrypt(rand.Reader, pubKey, messageBytes, nil, nil)
	return hex.EncodeToString(ct), err
}

// requireStringParam extracts a required string parameter from a map.
// fieldName is the map key, label is used in error messages.
func requireStringParam(m map[string]interface{}, fieldName, label string) (string, error) {
	v, ok := m[fieldName]
	if !ok {
		return "", fmt.Errorf("%s is empty", label)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s type is not string", label)
	}
	return s, nil
}
