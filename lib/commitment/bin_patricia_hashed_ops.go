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

package commitment

import (
	"encoding/binary"
	"fmt"

	"github.com/holiman/uint256"
)

func (cell *BinaryCell) fillEmpty() {
	cell.apl = 0
	cell.spl = 0
	cell.downHashedLen = 0
	cell.extLen = 0
	cell.hl = 0
	cell.Nonce = 0
	cell.Balance.Clear()
	copy(cell.CodeHash[:], EmptyCodeHash)
	cell.StorageLen = 0
	cell.Delete = false
}

func (cell *BinaryCell) fillFromUpperCell(upBinaryCell *BinaryCell, depth, depthIncrement int) {
	if upBinaryCell.downHashedLen >= depthIncrement {
		cell.downHashedLen = upBinaryCell.downHashedLen - depthIncrement
	} else {
		cell.downHashedLen = 0
	}
	if upBinaryCell.downHashedLen > depthIncrement {
		copy(cell.downHashedKey[:], upBinaryCell.downHashedKey[depthIncrement:upBinaryCell.downHashedLen])
	}
	if upBinaryCell.extLen >= depthIncrement {
		cell.extLen = upBinaryCell.extLen - depthIncrement
	} else {
		cell.extLen = 0
	}
	if upBinaryCell.extLen > depthIncrement {
		copy(cell.extension[:], upBinaryCell.extension[depthIncrement:upBinaryCell.extLen])
	}
	if depth <= halfKeySize {
		cell.apl = upBinaryCell.apl
		if upBinaryCell.apl > 0 {
			copy(cell.apk[:], upBinaryCell.apk[:cell.apl])
			cell.Balance.Set(&upBinaryCell.Balance)
			cell.Nonce = upBinaryCell.Nonce
			copy(cell.CodeHash[:], upBinaryCell.CodeHash[:])
			cell.extLen = upBinaryCell.extLen
			if upBinaryCell.extLen > 0 {
				copy(cell.extension[:], upBinaryCell.extension[:upBinaryCell.extLen])
			}
		}
	} else {
		cell.apl = 0
	}
	cell.spl = upBinaryCell.spl
	if upBinaryCell.spl > 0 {
		copy(cell.spk[:], upBinaryCell.spk[:upBinaryCell.spl])
		cell.StorageLen = upBinaryCell.StorageLen
		if upBinaryCell.StorageLen > 0 {
			copy(cell.Storage[:], upBinaryCell.Storage[:upBinaryCell.StorageLen])
		}
	}
	cell.hl = upBinaryCell.hl
	if upBinaryCell.hl > 0 {
		copy(cell.h[:], upBinaryCell.h[:upBinaryCell.hl])
	}
}

func (cell *BinaryCell) fillFromLowerBinaryCell(lowBinaryCell *BinaryCell, lowDepth int, preExtension []byte, nibble int) {
	if lowBinaryCell.apl > 0 || lowDepth < halfKeySize {
		cell.apl = lowBinaryCell.apl
	}
	if lowBinaryCell.apl > 0 {
		copy(cell.apk[:], lowBinaryCell.apk[:cell.apl])
		cell.Balance.Set(&lowBinaryCell.Balance)
		cell.Nonce = lowBinaryCell.Nonce
		copy(cell.CodeHash[:], lowBinaryCell.CodeHash[:])
	}
	cell.spl = lowBinaryCell.spl
	if lowBinaryCell.spl > 0 {
		copy(cell.spk[:], lowBinaryCell.spk[:cell.spl])
		cell.StorageLen = lowBinaryCell.StorageLen
		if lowBinaryCell.StorageLen > 0 {
			copy(cell.Storage[:], lowBinaryCell.Storage[:lowBinaryCell.StorageLen])
		}
	}
	if lowBinaryCell.hl > 0 {
		if (lowBinaryCell.apl == 0 && lowDepth < halfKeySize) || (lowBinaryCell.spl == 0 && lowDepth > halfKeySize) {
			// Extension is related to either accounts branch node, or storage branch node, we prepend it by preExtension | nibble
			if len(preExtension) > 0 {
				copy(cell.extension[:], preExtension)
			}
			cell.extension[len(preExtension)] = byte(nibble)
			if lowBinaryCell.extLen > 0 {
				copy(cell.extension[1+len(preExtension):], lowBinaryCell.extension[:lowBinaryCell.extLen])
			}
			cell.extLen = lowBinaryCell.extLen + 1 + len(preExtension)
		} else {
			// Extension is related to a storage branch node, so we copy it upwards as is
			cell.extLen = lowBinaryCell.extLen
			if lowBinaryCell.extLen > 0 {
				copy(cell.extension[:], lowBinaryCell.extension[:lowBinaryCell.extLen])
			}
		}
	}
	cell.hl = lowBinaryCell.hl
	if lowBinaryCell.hl > 0 {
		copy(cell.h[:], lowBinaryCell.h[:lowBinaryCell.hl])
	}
}

func (cell *BinaryCell) deriveHashedKeys(depth int, keccak keccakState, accountKeyLen int) error {
	extraLen := 0
	if cell.apl > 0 {
		if depth > halfKeySize {
			return fmt.Errorf("deriveHashedKeys accountPlainKey present at depth > halfKeySize")
		}
		extraLen = halfKeySize - depth
	}
	if cell.spl > 0 {
		if depth >= halfKeySize {
			extraLen = maxKeySize - depth
		} else {
			extraLen += halfKeySize
		}
	}
	if extraLen > 0 {
		if cell.downHashedLen > 0 {
			copy(cell.downHashedKey[extraLen:], cell.downHashedKey[:cell.downHashedLen])
		}
		cell.downHashedLen += extraLen
		var hashedKeyOffset, downOffset int
		if cell.apl > 0 {
			if err := binHashKey(keccak, cell.apk[:cell.apl], cell.downHashedKey[:], depth); err != nil {
				return err
			}
			downOffset = halfKeySize - depth
		}
		if cell.spl > 0 {
			if depth >= halfKeySize {
				hashedKeyOffset = depth - halfKeySize
			}
			if err := binHashKey(keccak, cell.spk[accountKeyLen:cell.spl], cell.downHashedKey[downOffset:], hashedKeyOffset); err != nil {
				return err
			}
		}
	}
	return nil
}

func (cell *BinaryCell) fillFromFields(data []byte, pos int, fieldBits PartFlags) (int, error) {
	if fieldBits&HashedKeyPart != 0 {
		l, n := binary.Uvarint(data[pos:])
		if n == 0 {
			return 0, fmt.Errorf("fillFromFields buffer too small for hashedKey len")
		} else if n < 0 {
			return 0, fmt.Errorf("fillFromFields value overflow for hashedKey len")
		}
		pos += n
		if len(data) < pos+int(l) {
			return 0, fmt.Errorf("fillFromFields buffer too small for hashedKey exp %d got %d", pos+int(l), len(data))
		}
		cell.downHashedLen = int(l)
		cell.extLen = int(l)
		if l > 0 {
			copy(cell.downHashedKey[:], data[pos:pos+int(l)])
			copy(cell.extension[:], data[pos:pos+int(l)])
			pos += int(l)
		}
	} else {
		cell.downHashedLen = 0
		cell.extLen = 0
	}
	if fieldBits&AccountPlainPart != 0 {
		l, n := binary.Uvarint(data[pos:])
		if n == 0 {
			return 0, fmt.Errorf("fillFromFields buffer too small for accountPlainKey len")
		} else if n < 0 {
			return 0, fmt.Errorf("fillFromFields value overflow for accountPlainKey len")
		}
		pos += n
		if len(data) < pos+int(l) {
			return 0, fmt.Errorf("fillFromFields buffer too small for accountPlainKey")
		}
		cell.apl = int(l)
		if l > 0 {
			copy(cell.apk[:], data[pos:pos+int(l)])
			pos += int(l)
		}
	} else {
		cell.apl = 0
	}
	if fieldBits&StoragePlainPart != 0 {
		l, n := binary.Uvarint(data[pos:])
		if n == 0 {
			return 0, fmt.Errorf("fillFromFields buffer too small for storagePlainKey len")
		} else if n < 0 {
			return 0, fmt.Errorf("fillFromFields value overflow for storagePlainKey len")
		}
		pos += n
		if len(data) < pos+int(l) {
			return 0, fmt.Errorf("fillFromFields buffer too small for storagePlainKey")
		}
		cell.spl = int(l)
		if l > 0 {
			copy(cell.spk[:], data[pos:pos+int(l)])
			pos += int(l)
		}
	} else {
		cell.spl = 0
	}
	if fieldBits&HashPart != 0 {
		l, n := binary.Uvarint(data[pos:])
		if n == 0 {
			return 0, fmt.Errorf("fillFromFields buffer too small for hash len")
		} else if n < 0 {
			return 0, fmt.Errorf("fillFromFields value overflow for hash len")
		}
		pos += n
		if len(data) < pos+int(l) {
			return 0, fmt.Errorf("fillFromFields buffer too small for hash")
		}
		cell.hl = int(l)
		if l > 0 {
			copy(cell.h[:], data[pos:pos+int(l)])
			pos += int(l)
		}
	} else {
		cell.hl = 0
	}
	return pos, nil
}

func (cell *BinaryCell) setStorage(value []byte) {
	cell.StorageLen = len(value)
	if len(value) > 0 {
		copy(cell.Storage[:], value)
	}
}

func (cell *BinaryCell) setAccountFields(codeHash []byte, balance *uint256.Int, nonce uint64) {
	copy(cell.CodeHash[:], codeHash)
	cell.Balance.SetBytes(balance.Bytes())
	cell.Nonce = nonce
}

func (cell *BinaryCell) unwrapToHexCell() (cl *Cell) {
	cl = new(Cell)
	cl.Balance = *cell.Balance.Clone()
	cl.Nonce = cell.Nonce
	cl.StorageLen = cell.StorageLen
	cl.apl = cell.apl
	cl.spl = cell.spl
	cl.hl = cell.hl

	copy(cl.apk[:], cell.apk[:])
	copy(cl.spk[:], cell.spk[:])
	copy(cl.h[:], cell.h[:])

	if cell.extLen > 0 {
		compactedExt := binToCompact(cell.extension[:cell.extLen])
		copy(cl.extension[:], compactedExt)
		cl.extLen = len(compactedExt)
	}
	if cell.downHashedLen > 0 {
		compactedDHK := binToCompact(cell.downHashedKey[:cell.downHashedLen])
		copy(cl.downHashedKey[:], compactedDHK)
		cl.downHashedLen = len(compactedDHK)
	}

	copy(cl.CodeHash[:], cell.CodeHash[:])
	copy(cl.Storage[:], cell.Storage[:])
	cl.Delete = cell.Delete
	return cl
}

func (bph *BinPatriciaHashed) deleteBinaryCell(hashedKey []byte) {
	if bph.trace {
		fmt.Printf("deleteBinaryCell, activeRows = %d\n", bph.activeRows)
	}
	var cell *BinaryCell
	if bph.activeRows == 0 {
		// Remove the root
		cell = &bph.root
		bph.rootTouched = true
		bph.rootPresent = false
	} else {
		row := bph.activeRows - 1
		if bph.depths[row] < len(hashedKey) {
			if bph.trace {
				fmt.Printf("deleteBinaryCell skipping spurious delete depth=%d, len(hashedKey)=%d\n", bph.depths[row], len(hashedKey))
			}
			return
		}
		col := int(hashedKey[bph.currentKeyLen])
		cell = &bph.grid[row][col]
		if bph.afterMap[row]&(uint16(1)<<col) != 0 {
			// Prevent "spurios deletions", i.e. deletion of absent items
			bph.touchMap[row] |= uint16(1) << col
			bph.afterMap[row] &^= uint16(1) << col
			if bph.trace {
				fmt.Printf("deleteBinaryCell setting (%d, %x)\n", row, col)
			}
		} else {
			if bph.trace {
				fmt.Printf("deleteBinaryCell ignoring (%d, %x)\n", row, col)
			}
		}
	}
	cell.extLen = 0
	cell.Balance.Clear()
	copy(cell.CodeHash[:], EmptyCodeHash)
	cell.Nonce = 0
}

func (bph *BinPatriciaHashed) updateBinaryCell(plainKey, hashedKey []byte) *BinaryCell {
	var cell *BinaryCell
	var col, depth int
	if bph.activeRows == 0 {
		cell = &bph.root
		bph.rootTouched, bph.rootPresent = true, true
	} else {
		row := bph.activeRows - 1
		depth = bph.depths[row]
		col = int(hashedKey[bph.currentKeyLen])
		cell = &bph.grid[row][col]
		bph.touchMap[row] |= uint16(1) << col
		bph.afterMap[row] |= uint16(1) << col
		if bph.trace {
			fmt.Printf("updateBinaryCell setting (%d, %x), depth=%d\n", row, col, depth)
		}
	}
	if cell.downHashedLen == 0 {
		copy(cell.downHashedKey[:], hashedKey[depth:])
		cell.downHashedLen = len(hashedKey) - depth
		if bph.trace {
			fmt.Printf("set downHasheKey=[%x]\n", cell.downHashedKey[:cell.downHashedLen])
		}
	} else {
		if bph.trace {
			fmt.Printf("left downHasheKey=[%x]\n", cell.downHashedKey[:cell.downHashedLen])
		}
	}
	if len(hashedKey) == halfKeySize { // set account key
		cell.apl = len(plainKey)
		copy(cell.apk[:], plainKey)
	} else { // set storage key
		cell.spl = len(plainKey)
		copy(cell.spk[:], plainKey)
	}
	return cell
}

func (c *BinaryCell) bytes() []byte {
	var pos = 1
	size := 1 + c.hl + 1 + c.apl + c.spl + 1 + c.downHashedLen + 1 + c.extLen + 1 // max size
	buf := make([]byte, size)

	var flags uint8
	if c.hl != 0 {
		flags |= 1
		buf[pos] = byte(c.hl)
		pos++
		copy(buf[pos:pos+c.hl], c.h[:])
		pos += c.hl
	}
	if c.apl != 0 {
		flags |= 2
		buf[pos] = byte(c.hl)
		pos++
		copy(buf[pos:pos+c.apl], c.apk[:])
		pos += c.apl
	}
	if c.spl != 0 {
		flags |= 4
		buf[pos] = byte(c.spl)
		pos++
		copy(buf[pos:pos+c.spl], c.spk[:])
		pos += c.spl
	}
	if c.downHashedLen != 0 {
		flags |= 8
		buf[pos] = byte(c.downHashedLen)
		pos++
		copy(buf[pos:pos+c.downHashedLen], c.downHashedKey[:])
		pos += c.downHashedLen
	}
	if c.extLen != 0 {
		flags |= 16
		buf[pos] = byte(c.extLen)
		pos++
		copy(buf[pos:pos+c.downHashedLen], c.downHashedKey[:])
		//pos += c.downHashedLen
	}
	buf[0] = flags
	return buf
}

func (c *BinaryCell) decodeBytes(buf []byte) error {
	if len(buf) < 1 {
		return fmt.Errorf("invalid buffer size to contain BinaryCell (at least 1 byte expected)")
	}
	c.fillEmpty()

	var pos int
	flags := buf[pos]
	pos++

	if flags&1 != 0 {
		c.hl = int(buf[pos])
		pos++
		copy(c.h[:], buf[pos:pos+c.hl])
		pos += c.hl
	}
	if flags&2 != 0 {
		c.apl = int(buf[pos])
		pos++
		copy(c.apk[:], buf[pos:pos+c.apl])
		pos += c.apl
	}
	if flags&4 != 0 {
		c.spl = int(buf[pos])
		pos++
		copy(c.spk[:], buf[pos:pos+c.spl])
		pos += c.spl
	}
	if flags&8 != 0 {
		c.downHashedLen = int(buf[pos])
		pos++
		copy(c.downHashedKey[:], buf[pos:pos+c.downHashedLen])
		pos += c.downHashedLen
	}
	if flags&16 != 0 {
		c.extLen = int(buf[pos])
		pos++
		copy(c.extension[:], buf[pos:pos+c.extLen])
		//pos += c.extLen
	}
	return nil
}
