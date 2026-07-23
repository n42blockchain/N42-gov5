// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// BLS key loading from an on-disk keystore directory.
// LoadBLSKeyFromDir resolves a validator BLS secret key for a given
// address by scanning the configured keystore path, using crypto/bls
// and blscommon for decoding. Used during HotStuff engine bootstrap
// to install the local signer before joining a consensus round.

package hotstuff

import (
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto/bls"
	blscommon "github.com/n42blockchain/N42/crypto/bls/common"
)

// LoadBLSKeyFromDir loads a BLS secret key for the given address from the keystore directory.
// The key file must be named "bls_<address_hex>.key" and contain the hex-encoded 32-byte secret key.
func LoadBLSKeyFromDir(keyDir string, addr types.Address) (blscommon.SecretKey, error) {
	if keyDir == "" {
		return nil, fmt.Errorf("hotstuff: key directory not specified")
	}

	addrHex := strings.ToLower(addr.Hex())
	// Try both with and without 0x prefix in the filename.
	candidates := []string{
		filepath.Join(keyDir, fmt.Sprintf("bls_%s.key", addrHex)),
		filepath.Join(keyDir, fmt.Sprintf("bls_%s.key", strings.TrimPrefix(addrHex, "0x"))),
	}

	var data []byte
	var err error
	for _, path := range candidates {
		data, err = os.ReadFile(path)
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, fmt.Errorf("hotstuff: BLS key file not found for %s in %s: %w", addrHex, keyDir, err)
	}

	// Trim whitespace/newlines.
	hexStr := strings.TrimSpace(string(data))
	// Remove optional 0x prefix.
	hexStr = strings.TrimPrefix(hexStr, "0x")

	keyBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("hotstuff: invalid BLS key hex: %w", err)
	}

	secretKey, err := bls.SecretKeyFromBytes(keyBytes)
	if err != nil {
		return nil, fmt.Errorf("hotstuff: failed to parse BLS secret key: %w", err)
	}

	return secretKey, nil
}
