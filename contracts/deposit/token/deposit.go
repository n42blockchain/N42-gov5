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

package token

import (
	"bytes"
	"embed"
	"math/big"

	"github.com/holiman/uint256"
	"github.com/pkg/errors"

	"github.com/n42blockchain/N42/accounts/abi"
	"github.com/n42blockchain/N42/common/crypto"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
)

//go:embed abi.json
var abiJson embed.FS
var contractAbi abi.ABI
var depositSignature = crypto.Keccak256Hash([]byte("DepositEvent(bytes,uint256,bytes)"))
var withdrawnSignature = crypto.Keccak256Hash([]byte("WithdrawnEvent(uint256)"))

func init() {
	var (
		depositAbiCode []byte
		err            error
	)
	if depositAbiCode, err = abiJson.ReadFile("abi.json"); err != nil {
		panic("Could not open abi.json")
	}

	if contractAbi, err = abi.JSON(bytes.NewReader(depositAbiCode)); err != nil {
		panic("unable to parse N42 deposit contract abi")
	}
}

type Contract struct {
}

func (Contract) WithdrawnSignature() types.Hash {
	return withdrawnSignature
}

func (Contract) DepositSignature() types.Hash {
	return depositSignature
}

func (Contract) IsDepositAction(sigdata [4]byte) bool {
	method, err := contractAbi.MethodById(sigdata[:])
	if err != nil {
		return false
	}
	return bytes.Equal(method.ID, contractAbi.Methods["deposit"].ID)
}

func (Contract) UnpackDepositLogData(data []byte) (publicKey []byte, signature []byte, depositAmount *uint256.Int, err error) {
	unpackedLogs, unpackErr := contractAbi.Unpack("DepositEvent", data)
	if unpackErr != nil {
		err = errors.Wrap(unpackErr, "unable to unpack logs")
		return
	}
	if len(unpackedLogs) < 3 {
		err = errors.New("unexpected number of log fields")
		return
	}

	bigAmount, ok := unpackedLogs[1].(*big.Int)
	if !ok {
		err = errors.New("unable to cast amount to *big.Int")
		return
	}

	var overflow bool
	if depositAmount, overflow = uint256.FromBig(bigAmount); overflow {
		err = errors.New("unable to unpack amount: overflow")
		return
	}

	pubKeyBytes, ok := unpackedLogs[0].([]byte)
	if !ok {
		err = errors.New("unable to cast publicKey to []byte")
		return
	}

	sigBytes, ok := unpackedLogs[2].([]byte)
	if !ok {
		err = errors.New("unable to cast signature to []byte")
		return
	}

	publicKey, signature = pubKeyBytes, sigBytes
	log.Debug("unpacked DepositEvent Logs", "publicKey", hexutil.Encode(pubKeyBytes), "signature", hexutil.Encode(sigBytes), "message", hexutil.Encode(depositAmount.Bytes()))
	return
}
