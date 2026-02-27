/*
   Copyright 2021 The Erigon contributors

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

package types

import (
	"fmt"
	"math/bits"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/rlp"
)

func EncodeSenderLengthForStorage(nonce uint64, balance uint256.Int) uint {
	var structLength uint = 1 // 1 byte for fieldset
	if !balance.IsZero() {
		structLength += uint(balance.ByteLen()) + 1
	}
	if nonce > 0 {
		structLength += uint(common.BitLenToByteLen(bits.Len64(nonce))) + 1
	}
	return structLength
}

// EncodeSender encodes the details of txn sender into the given "buffer" byte-slice that should be big enough.
func EncodeSender(nonce uint64, balance uint256.Int, buffer []byte) {
	var fieldSet = 0
	var pos = 1
	if nonce > 0 {
		fieldSet = 1
		nonceBytes := common.BitLenToByteLen(bits.Len64(nonce))
		buffer[pos] = byte(nonceBytes)
		var nonce = nonce
		for i := nonceBytes; i > 0; i-- {
			buffer[pos+i] = byte(nonce)
			nonce >>= 8
		}
		pos += nonceBytes + 1
	}

	if !balance.IsZero() {
		fieldSet |= 2
		balanceBytes := balance.ByteLen()
		buffer[pos] = byte(balanceBytes)
		pos++
		balance.WriteToSlice(buffer[pos : pos+balanceBytes])
		pos += balanceBytes //nolint
	}

	buffer[0] = byte(fieldSet)
}

// DecodeSender decodes the sender's balance and nonce from encoded byte-slice.
func DecodeSender(enc []byte) (nonce uint64, balance uint256.Int, err error) {
	if len(enc) == 0 {
		return
	}

	var fieldSet = enc[0]
	var pos = 1

	if fieldSet&1 > 0 {
		decodeLength := int(enc[pos])

		if len(enc) < pos+decodeLength+1 {
			return nonce, balance, fmt.Errorf(
				"malformed CBOR for Account.Nonce: %s, Length %d",
				enc[pos+1:], decodeLength)
		}

		nonce = bytesToUint64(enc[pos+1 : pos+decodeLength+1])
		pos += decodeLength + 1
	}

	if fieldSet&2 > 0 {
		decodeLength := int(enc[pos])

		if len(enc) < pos+decodeLength+1 {
			return nonce, balance, fmt.Errorf(
				"malformed CBOR for Account.Nonce: %s, Length %d",
				enc[pos+1:], decodeLength)
		}

		(&balance).SetBytes(enc[pos+1 : pos+decodeLength+1])
	}
	return
}

func bytesToUint64(buf []byte) (x uint64) {
	for i, b := range buf {
		x = x<<8 + uint64(b)
		if i == 7 {
			return
		}
	}
	return
}

func PeekTransactionType(serialized []byte) (byte, error) {
	dataPos, _, legacy, err := rlp.Prefix(serialized, 0)
	if err != nil {
		return LegacyTxType, fmt.Errorf("%w: size Prefix: %s", ErrParseTxn, err) //nolint
	}
	if legacy {
		return LegacyTxType, nil
	}
	return serialized[dataPos], nil
}

// UnwrapTxPlayloadRlp removes everything but the payload body from blob tx and prepends 0x3 at the beginning - no copy.
// Doesn't change non-blob tx.
func UnwrapTxPlayloadRlp(blobTxRlp []byte) ([]byte, error) {
	if blobTxRlp[0] != BlobTxType {
		return blobTxRlp, nil
	}
	dataposPrev, _, isList, err := rlp.Prefix(blobTxRlp[1:], 0)
	if err != nil || dataposPrev < 1 {
		return nil, err
	}
	if !isList {
		return blobTxRlp, nil
	}

	blobTxRlp = blobTxRlp[1:]
	datapos, datalen, err := rlp.ParseList(blobTxRlp, dataposPrev)
	if err != nil {
		return nil, err
	}
	blobTxRlp = blobTxRlp[dataposPrev-1 : datapos+datalen]
	blobTxRlp[0] = 0x3
	return blobTxRlp, nil
}
