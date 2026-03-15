package state

import (
	"encoding/binary"
	"fmt"
)

func decodeTxNumPrefix(context string, value []byte) (uint64, error) {
	if len(value) < 8 {
		return 0, fmt.Errorf("%s: expected txnum prefix with at least 8 bytes, got %d", context, len(value))
	}
	return binary.BigEndian.Uint64(value[:8]), nil
}

func decodeTxNumExact(context string, value []byte) (uint64, error) {
	if len(value) != 8 {
		return 0, fmt.Errorf("%s: expected 8-byte txnum, got %d", context, len(value))
	}
	return binary.BigEndian.Uint64(value), nil
}

func splitTxNumSuffix(context string, key []byte) ([]byte, uint64, error) {
	if len(key) < 8 {
		return nil, 0, fmt.Errorf("%s: expected key with 8-byte txnum suffix, got %d", context, len(key))
	}
	return key[:len(key)-8], binary.BigEndian.Uint64(key[len(key)-8:]), nil
}

func trimTxNumSuffix(context string, key []byte) ([]byte, error) {
	keyPrefix, _, err := splitTxNumSuffix(context, key)
	if err != nil {
		return nil, err
	}
	return keyPrefix, nil
}
