/*
   Copyright 2022 The Erigon contributors

   Licensed under the Apache License, Version 2.0 (the "License");
   you may not use this file except in compliance with the License.
   You may obtain a copy of the License at

       http://www.apache.org/licenses/LICENSE-2.0

   Unless required by applicable law or agreed to in writing, software
   distributed under the License is distributed on an "AS IS" BASIS,
   WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
   See the License for the specific language governing permissions and
   limitations under the License.
*/

package txpool

import (
	"errors"
	"fmt"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/crypto"
)

func RecoverSignerFromRLP(rlp []byte, yParity uint8, r uint256.Int, s uint256.Int) (*common.Address, error) {
	// from authorizations.go
	hashData := []byte{byte(0x05)}
	hashData = append(hashData, rlp...)
	hash := crypto.Keccak256Hash(hashData)

	var sig [65]byte
	rBytes := r.Bytes()
	sBytes := s.Bytes()
	copy(sig[32-len(rBytes):32], rBytes)
	copy(sig[64-len(sBytes):64], sBytes)

	if yParity == 0 || yParity == 1 {
		sig[64] = yParity
	} else {
		return nil, fmt.Errorf("invalid y parity value: %d", yParity)
	}

	if !crypto.TransactionSignatureIsValid(sig[64], &r, &s, false /* allowPreEip2s */) {
		return nil, errors.New("invalid signature")
	}

	pubkey, err := crypto.Ecrecover(hash.Bytes(), sig[:])
	if err != nil {
		return nil, err
	}
	if len(pubkey) == 0 || pubkey[0] != 4 {
		return nil, errors.New("invalid public key")
	}

	var authority common.Address
	copy(authority[:], crypto.Keccak256(pubkey[1:])[12:])
	return &authority, nil
}
