package dbutils

import (
	"encoding/binary"
	"errors"
	"fmt"

	libcommon "github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/length"
)

const NumberLength = 8

// EncodeBlockNumber encodes a block number as big endian uint64
func EncodeBlockNumber(number uint64) []byte {
	enc := make([]byte, NumberLength)
	binary.BigEndian.PutUint64(enc, number)
	return enc
}

var ErrInvalidSize = errors.New("bit endian number has an invalid size")

func DecodeBlockNumber(number []byte) (uint64, error) {
	if len(number) != NumberLength {
		return 0, fmt.Errorf("%w: %d", ErrInvalidSize, len(number))
	}
	return binary.BigEndian.Uint64(number), nil
}

// HeaderKey = num (uint64 big endian) + hash
func HeaderKey(number uint64, hash libcommon.Hash) []byte {
	k := make([]byte, NumberLength+length.Hash)
	binary.BigEndian.PutUint64(k, number)
	copy(k[NumberLength:], hash[:])
	return k
}

// BlockBodyKey = num (uint64 big endian) + hash
func BlockBodyKey(number uint64, hash libcommon.Hash) []byte {
	return HeaderKey(number, hash)
}

// LogKey = blockN (uint64 big endian) + txId (uint32 big endian)
func LogKey(blockNumber uint64, txId uint32) []byte {
	k := make([]byte, NumberLength+4)
	binary.BigEndian.PutUint64(k, blockNumber)
	binary.BigEndian.PutUint32(k[NumberLength:], txId)
	return k
}

// BloomBitsKey = bit (uint16 big endian) + section (uint64 big endian) + hash
func BloomBitsKey(bit uint, section uint64, hash libcommon.Hash) []byte {
	key := make([]byte, 2+NumberLength+length.Hash)
	binary.BigEndian.PutUint16(key, uint16(bit))
	binary.BigEndian.PutUint64(key[2:], section)
	copy(key[2+NumberLength:], hash[:])
	return key
}

// GenerateCompositeTrieKey = AddrHash + KeyHash (only for trie)
func GenerateCompositeTrieKey(addressHash libcommon.Hash, seckey libcommon.Hash) []byte {
	compositeKey := make([]byte, length.Hash+length.Hash)
	copy(compositeKey, addressHash[:])
	copy(compositeKey[length.Hash:], seckey[:])
	return compositeKey
}

// Key + blockNum
func CompositeKeySuffix(key []byte, timestamp uint64) (composite, encodedTS []byte) {
	encodedTS = encodeTimestamp(timestamp)
	composite = make([]byte, len(key)+len(encodedTS))
	copy(composite, key)
	copy(composite[len(key):], encodedTS)
	return composite, encodedTS
}

// encodeTimestamp has the property: if a < b, then Encoding(a) < Encoding(b) lexicographically
func encodeTimestamp(timestamp uint64) []byte {
	limit := uint64(32)
	for bytecount := 1; bytecount <= 8; bytecount++ {
		if timestamp < limit {
			suffix := make([]byte, bytecount)
			b := timestamp
			for i := bytecount - 1; i > 0; i-- {
				suffix[i] = byte(b & 0xff)
				b >>= 8
			}
			suffix[0] = byte(b) | (byte(bytecount) << 5) // 3 most significant bits of the first byte are bytecount
			return suffix
		}
		limit <<= 8
	}
	return nil
}
