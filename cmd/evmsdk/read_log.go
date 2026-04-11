// Copyright 2021-2026 The N42 Authors
// This file is part of the N42 library.
//
// Compact binary encoding for ordered state reads. Wire format is
// byte-identical to D:\N42\n42-26\crates\n42-execution\src\read_log.rs
// (canonical spec lives there). Cross-implementation packets must
// round-trip exactly.

package evmsdk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/types"
)

// Header byte constants (mirror Rust HEADER_*).
const (
	headerAccountNotFound = 0x00
	headerStorageBase     = 0x80
	headerBlockHash       = 0xC0

	acctExistsBit     = 0x01
	acctNonceLenShift = 1
	acctNonceLenMask  = 0x0E // bits 1-3
	acctBalanceBit    = 0x10 // bit 4
	acctCodeHashBit   = 0x20 // bit 5
	acctNonce8BBit    = 0x40 // bit 6

	maxReadLogEntries = 500_000
)

// ReadLogEntry is one captured database read.
type ReadLogEntry interface {
	readLogEntry()
}

// ReadLogAccountNotFound — Database::basic returned None.
type ReadLogAccountNotFound struct{}

func (ReadLogAccountNotFound) readLogEntry() {}

// ReadLogAccount — Database::basic returned Some(AccountInfo).
type ReadLogAccount struct {
	Nonce    uint64
	Balance  *uint256.Int // never nil; zero means absent in encoding
	CodeHash types.Hash   // emptyCodeHash for EOA, real hash for contract
}

func (ReadLogAccount) readLogEntry() {}

// ReadLogStorage — Database::storage returned a value.
// Slot key is implicit from call order.
type ReadLogStorage struct {
	Value *uint256.Int
}

func (ReadLogStorage) readLogEntry() {}

// ReadLogBlockHash — Database::block_hash returned a hash.
// Block number is implicit from call order.
type ReadLogBlockHash struct {
	Hash types.Hash
}

func (ReadLogBlockHash) readLogEntry() {}

// EmptyCodeHash is keccak256(nil), the canonical EOA code hash. Exported
// so producer-side recorders (internal/streamverify) can short-circuit
// bytecode capture for EOA accounts without re-deriving the constant.
//
//	0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470
var EmptyCodeHash = types.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")

// emptyCodeHash is the unexported alias kept for in-package use to avoid
// touching every existing reference in this session.
var emptyCodeHash = EmptyCodeHash

// ErrInvalidReadLog is returned by DecodeReadLog on malformed input.
var ErrInvalidReadLog = errors.New("read_log: invalid encoding")

// significantBytesU64 returns the number of bytes needed to represent v
// in big-endian without leading zeros.
func significantBytesU64(v uint64) int {
	if v == 0 {
		return 0
	}
	// 64 - leading zero bits, ceiling-divided by 8.
	bitsUsed := 64 - bits.LeadingZeros64(v)
	return (bitsUsed + 7) / 8
}

// significantBytesBE32 returns the number of trailing bytes (big-endian)
// after stripping leading zeros from a 32-byte buffer.
func significantBytesBE32(be [32]byte) int {
	for i := 0; i < 32; i++ {
		if be[i] != 0 {
			return 32 - i
		}
	}
	return 0
}

// EncodeReadLog serializes an ordered read log to compact bytes. The
// per-entry capacity hint of 4 bytes is sized for the common case
// (storage zero, AccountNotFound, small-nonce account); larger entries
// just trigger normal slice growth.
func EncodeReadLog(log []ReadLogEntry) []byte {
	buf := make([]byte, 0, 4+len(log)*4)

	var countBuf [4]byte
	binary.LittleEndian.PutUint32(countBuf[:], uint32(len(log)))
	buf = append(buf, countBuf[:]...)

	for _, e := range log {
		switch v := e.(type) {
		case ReadLogAccountNotFound:
			buf = append(buf, headerAccountNotFound)

		case ReadLogAccount:
			isContract := v.CodeHash != emptyCodeHash
			nonceLen := significantBytesU64(v.Nonce)

			// Skip the Bytes32() copy + scan when balance is zero (the
			// majority of accounts on most blocks).
			var balanceBE [32]byte
			var balanceLen int
			hasBalance := v.Balance != nil && !v.Balance.IsZero()
			if hasBalance {
				balanceBE = v.Balance.Bytes32()
				balanceLen = significantBytesBE32(balanceBE)
				if balanceLen == 0 {
					// All-zero buffer despite IsZero() == false: defensive.
					hasBalance = false
				}
			}

			var header byte = acctExistsBit
			if nonceLen <= 7 {
				header |= byte(nonceLen) << acctNonceLenShift
			} else {
				header |= acctNonce8BBit
			}
			if hasBalance {
				header |= acctBalanceBit
			}
			if isContract {
				header |= acctCodeHashBit
			}
			buf = append(buf, header)

			if nonceLen > 0 {
				actualLen := nonceLen
				if header&acctNonce8BBit != 0 {
					actualLen = 8
				}
				var nonceBE [8]byte
				binary.BigEndian.PutUint64(nonceBE[:], v.Nonce)
				buf = append(buf, nonceBE[8-actualLen:]...)
			}

			if hasBalance {
				buf = append(buf, byte(balanceLen))
				buf = append(buf, balanceBE[32-balanceLen:]...)
			}

			if isContract {
				buf = append(buf, v.CodeHash[:]...)
			}

		case ReadLogStorage:
			value := v.Value
			if value == nil {
				value = uint256.NewInt(0)
			}
			valueBE := value.Bytes32()
			valueLen := significantBytesBE32(valueBE)
			buf = append(buf, headerStorageBase+byte(valueLen))
			if valueLen > 0 {
				buf = append(buf, valueBE[32-valueLen:]...)
			}

		case ReadLogBlockHash:
			buf = append(buf, headerBlockHash)
			buf = append(buf, v.Hash[:]...)

		default:
			// Unknown variant; encode nothing — round-trip will fail downstream.
		}
	}

	return buf
}

// DecodeReadLog deserializes a compact read log from wire bytes.
func DecodeReadLog(data []byte) ([]ReadLogEntry, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: short header", ErrInvalidReadLog)
	}
	count := binary.LittleEndian.Uint32(data[:4])
	if count > maxReadLogEntries {
		return nil, fmt.Errorf("%w: entry count %d exceeds max %d", ErrInvalidReadLog, count, maxReadLogEntries)
	}
	entries := make([]ReadLogEntry, 0, count)
	pos := 4

	ensure := func(p, n int) error {
		if p+n > len(data) {
			return fmt.Errorf("%w: truncated at offset %d (need %d, have %d)", ErrInvalidReadLog, p, n, len(data)-p)
		}
		return nil
	}

	for i := uint32(0); i < count; i++ {
		if err := ensure(pos, 1); err != nil {
			return nil, err
		}
		header := data[pos]
		pos++

		switch {
		case header == headerAccountNotFound:
			entries = append(entries, ReadLogAccountNotFound{})

		case header < headerStorageBase:
			if header&acctExistsBit == 0 {
				return nil, fmt.Errorf("%w: invalid account header 0x%02x at offset %d", ErrInvalidReadLog, header, pos-1)
			}
			var nonceLen int
			if header&acctNonce8BBit != 0 {
				nonceLen = 8
			} else {
				nonceLen = int((header & acctNonceLenMask) >> acctNonceLenShift)
			}
			if nonceLen > 8 {
				return nil, fmt.Errorf("%w: nonce length %d > 8", ErrInvalidReadLog, nonceLen)
			}

			var nonce uint64
			if nonceLen > 0 {
				if err := ensure(pos, nonceLen); err != nil {
					return nil, err
				}
				var buf [8]byte
				copy(buf[8-nonceLen:], data[pos:pos+nonceLen])
				nonce = binary.BigEndian.Uint64(buf[:])
				pos += nonceLen
			}

			balance := uint256.NewInt(0)
			if header&acctBalanceBit != 0 {
				if err := ensure(pos, 1); err != nil {
					return nil, err
				}
				balanceLen := int(data[pos])
				pos++
				if balanceLen > 32 {
					return nil, fmt.Errorf("%w: balance length %d > 32", ErrInvalidReadLog, balanceLen)
				}
				if err := ensure(pos, balanceLen); err != nil {
					return nil, err
				}
				var buf [32]byte
				copy(buf[32-balanceLen:], data[pos:pos+balanceLen])
				balance = new(uint256.Int).SetBytes(buf[:])
				pos += balanceLen
			}

			codeHash := emptyCodeHash
			if header&acctCodeHashBit != 0 {
				if err := ensure(pos, 32); err != nil {
					return nil, err
				}
				copy(codeHash[:], data[pos:pos+32])
				pos += 32
			}

			entries = append(entries, ReadLogAccount{
				Nonce:    nonce,
				Balance:  balance,
				CodeHash: codeHash,
			})

		case header <= headerStorageBase+32:
			valueLen := int(header - headerStorageBase)
			value := uint256.NewInt(0)
			if valueLen > 0 {
				if err := ensure(pos, valueLen); err != nil {
					return nil, err
				}
				var buf [32]byte
				copy(buf[32-valueLen:], data[pos:pos+valueLen])
				value = new(uint256.Int).SetBytes(buf[:])
				pos += valueLen
			}
			entries = append(entries, ReadLogStorage{Value: value})

		case header == headerBlockHash:
			if err := ensure(pos, 32); err != nil {
				return nil, err
			}
			var h types.Hash
			copy(h[:], data[pos:pos+32])
			pos += 32
			entries = append(entries, ReadLogBlockHash{Hash: h})

		default:
			return nil, fmt.Errorf("%w: unknown header byte 0x%02x at offset %d", ErrInvalidReadLog, header, pos-1)
		}
	}

	return entries, nil
}
