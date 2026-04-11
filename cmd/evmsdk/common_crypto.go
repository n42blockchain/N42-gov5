// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// ECDSA / ECIES crypto helpers exposed to the mobile SDK.
// EvmEngine.Decrypt parses a hex ECDSA private key via
// crypto.HexToECDSA and runs crypto/ecies to decrypt request
// payloads. Used by the mobile client to authenticate inbound
// verification jobs from the N42 relay.

package evmsdk

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/crypto/ecies"
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
