// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.

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
