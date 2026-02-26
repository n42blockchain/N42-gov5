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
	"bytes"
	"encoding/binary"
	"fmt"
	"math/bits"

	"github.com/n42blockchain/N42/lib/log/v3"

	"github.com/n42blockchain/N42/lib/rlp"
)

func (bph *BinPatriciaHashed) needUnfolding(hashedKey []byte) int {
	var cell *BinaryCell
	var depth int
	if bph.activeRows == 0 {
		if bph.trace {
			fmt.Printf("needUnfolding root, rootChecked = %t\n", bph.rootChecked)
		}
		if bph.rootChecked && bph.root.downHashedLen == 0 && bph.root.hl == 0 {
			// Previously checked, empty root, no unfolding needed
			return 0
		}
		cell = &bph.root
		if cell.downHashedLen == 0 && cell.hl == 0 && !bph.rootChecked {
			// Need to attempt to unfold the root
			return 1
		}
	} else {
		col := int(hashedKey[bph.currentKeyLen])
		cell = &bph.grid[bph.activeRows-1][col]
		depth = bph.depths[bph.activeRows-1]
		if bph.trace {
			fmt.Printf("needUnfolding cell (%d, %x), currentKey=[%x], depth=%d, cell.h=[%x]\n", bph.activeRows-1, col, bph.currentKey[:bph.currentKeyLen], depth, cell.h[:cell.hl])
		}
	}
	if len(hashedKey) <= depth {
		return 0
	}
	if cell.downHashedLen == 0 {
		if cell.hl == 0 {
			// cell is empty, no need to unfold further
			return 0
		}
		// unfold branch node
		return 1
	}
	cpl := commonPrefixLen(hashedKey[depth:], cell.downHashedKey[:cell.downHashedLen-1])
	if bph.trace {
		fmt.Printf("cpl=%d, cell.downHashedKey=[%x], depth=%d, hashedKey[depth:]=[%x]\n", cpl, cell.downHashedKey[:cell.downHashedLen], depth, hashedKey[depth:])
	}
	unfolding := cpl + 1
	if depth < halfKeySize && depth+unfolding > halfKeySize {
		// This is to make sure that unfolding always breaks at the level where storage subtrees start
		unfolding = halfKeySize - depth
		if bph.trace {
			fmt.Printf("adjusted unfolding=%d\n", unfolding)
		}
	}
	return unfolding
}

// unfoldBranchNode returns true if unfolding has been done
func (bph *BinPatriciaHashed) unfoldBranchNode(row int, deleted bool, depth int) (bool, error) {
	branchData, err := bph.branchFn(binToCompact(bph.currentKey[:bph.currentKeyLen]))
	if err != nil {
		return false, err
	}
	if !bph.rootChecked && bph.currentKeyLen == 0 && len(branchData) == 0 {
		// Special case - empty or deleted root
		bph.rootChecked = true
		return false, nil
	}
	if len(branchData) == 0 {
		log.Warn("got empty branch data during unfold", "row", row, "depth", depth, "deleted", deleted)
	}
	bph.branchBefore[row] = true
	bitmap := binary.BigEndian.Uint16(branchData[0:])
	pos := 2
	if deleted {
		// All cells come as deleted (touched but not present after)
		bph.afterMap[row] = 0
		bph.touchMap[row] = bitmap
	} else {
		bph.afterMap[row] = bitmap
		bph.touchMap[row] = 0
	}
	// Loop iterating over the set bits of modMask
	for bitset, j := bitmap, 0; bitset != 0; j++ {
		bit := bitset & -bitset
		nibble := bits.TrailingZeros16(bit)
		cell := &bph.grid[row][nibble]
		fieldBits := branchData[pos]
		pos++
		var err error
		if pos, err = cell.fillFromFields(branchData, pos, PartFlags(fieldBits)); err != nil {
			return false, fmt.Errorf("prefix [%x], branchData[%x]: %w", bph.currentKey[:bph.currentKeyLen], branchData, err)
		}
		if bph.trace {
			fmt.Printf("cell (%d, %x) depth=%d, hash=[%x], a=[%x], s=[%x], ex=[%x]\n", row, nibble, depth, cell.h[:cell.hl], cell.apk[:cell.apl], cell.spk[:cell.spl], cell.extension[:cell.extLen])
		}
		if cell.apl > 0 {
			bph.accountFn(cell.apk[:cell.apl], cell)
			if bph.trace {
				fmt.Printf("accountFn[%x] return balance=%d, nonce=%d code=%x\n", cell.apk[:cell.apl], &cell.Balance, cell.Nonce, cell.CodeHash[:])
			}
		}
		if cell.spl > 0 {
			bph.storageFn(cell.spk[:cell.spl], cell)
		}
		if err = cell.deriveHashedKeys(depth, bph.keccak, bph.accountKeyLen); err != nil {
			return false, err
		}
		bitset ^= bit
	}
	return true, nil
}

func (bph *BinPatriciaHashed) unfold(hashedKey []byte, unfolding int) error {
	if bph.trace {
		fmt.Printf("unfold %d: activeRows: %d\n", unfolding, bph.activeRows)
	}
	var upCell *BinaryCell
	var touched, present bool
	var col byte
	var upDepth, depth int
	if bph.activeRows == 0 {
		if bph.rootChecked && bph.root.hl == 0 && bph.root.downHashedLen == 0 {
			// No unfolding for empty root
			return nil
		}
		upCell = &bph.root
		touched = bph.rootTouched
		present = bph.rootPresent
		if bph.trace {
			fmt.Printf("unfold root, touched %t, present %t, column %d\n", touched, present, col)
		}
	} else {
		upDepth = bph.depths[bph.activeRows-1]
		col = hashedKey[upDepth-1]
		upCell = &bph.grid[bph.activeRows-1][col]
		touched = bph.touchMap[bph.activeRows-1]&(uint16(1)<<col) != 0
		present = bph.afterMap[bph.activeRows-1]&(uint16(1)<<col) != 0
		if bph.trace {
			fmt.Printf("upCell (%d, %x), touched %t, present %t\n", bph.activeRows-1, col, touched, present)
		}
		bph.currentKey[bph.currentKeyLen] = col
		bph.currentKeyLen++
	}
	row := bph.activeRows
	for i := 0; i < maxChild; i++ {
		bph.grid[row][i].fillEmpty()
	}
	bph.touchMap[row] = 0
	bph.afterMap[row] = 0
	bph.branchBefore[row] = false
	if upCell.downHashedLen == 0 {
		depth = upDepth + 1
		if unfolded, err := bph.unfoldBranchNode(row, touched && !present /* deleted */, depth); err != nil {
			return err
		} else if !unfolded {
			// Return here to prevent activeRow from being incremented
			return nil
		}
	} else if upCell.downHashedLen >= unfolding {
		depth = upDepth + unfolding
		nibble := upCell.downHashedKey[unfolding-1]
		if touched {
			bph.touchMap[row] = uint16(1) << nibble
		}
		if present {
			bph.afterMap[row] = uint16(1) << nibble
		}
		cell := &bph.grid[row][nibble]
		cell.fillFromUpperCell(upCell, depth, unfolding)
		if bph.trace {
			fmt.Printf("cell (%d, %x) depth=%d\n", row, nibble, depth)
		}
		if row >= halfKeySize {
			cell.apl = 0
		}
		if unfolding > 1 {
			copy(bph.currentKey[bph.currentKeyLen:], upCell.downHashedKey[:unfolding-1])
		}
		bph.currentKeyLen += unfolding - 1
	} else {
		// upCell.downHashedLen < unfolding
		depth = upDepth + upCell.downHashedLen
		nibble := upCell.downHashedKey[upCell.downHashedLen-1]
		if touched {
			bph.touchMap[row] = uint16(1) << nibble
		}
		if present {
			bph.afterMap[row] = uint16(1) << nibble
		}
		cell := &bph.grid[row][nibble]
		cell.fillFromUpperCell(upCell, depth, upCell.downHashedLen)
		if bph.trace {
			fmt.Printf("cell (%d, %x) depth=%d\n", row, nibble, depth)
		}
		if row >= halfKeySize {
			cell.apl = 0
		}
		if upCell.downHashedLen > 1 {
			copy(bph.currentKey[bph.currentKeyLen:], upCell.downHashedKey[:upCell.downHashedLen-1])
		}
		bph.currentKeyLen += upCell.downHashedLen - 1
	}
	bph.depths[bph.activeRows] = depth
	bph.activeRows++
	return nil
}

func (bph *BinPatriciaHashed) needFolding(hashedKey []byte) bool {
	return !bytes.HasPrefix(hashedKey, bph.currentKey[:bph.currentKeyLen])
}

// The purpose of fold is to reduce hph.currentKey[:hph.currentKeyLen]. It should be invoked
// until that current key becomes a prefix of hashedKey that we will proccess next
// (in other words until the needFolding function returns 0)
func (bph *BinPatriciaHashed) fold() (branchData BranchData, updateKey []byte, err error) {
	updateKeyLen := bph.currentKeyLen
	if bph.activeRows == 0 {
		return nil, nil, fmt.Errorf("cannot fold - no active rows")
	}
	if bph.trace {
		fmt.Printf("fold: activeRows: %d, currentKey: [%x], touchMap: %016b, afterMap: %016b\n", bph.activeRows, bph.currentKey[:bph.currentKeyLen], bph.touchMap[bph.activeRows-1], bph.afterMap[bph.activeRows-1])
	}
	// Move information to the row above
	row := bph.activeRows - 1
	var upBinaryCell *BinaryCell
	var col int
	var upDepth int
	if bph.activeRows == 1 {
		if bph.trace {
			fmt.Printf("upcell is root\n")
		}
		upBinaryCell = &bph.root
	} else {
		upDepth = bph.depths[bph.activeRows-2]
		col = int(bph.currentKey[upDepth-1])
		if bph.trace {
			fmt.Printf("upcell is (%d x %x), upDepth=%d\n", row-1, col, upDepth)
		}
		upBinaryCell = &bph.grid[row-1][col]
	}

	depth := bph.depths[bph.activeRows-1]
	updateKey = binToCompact(bph.currentKey[:updateKeyLen])
	partsCount := bits.OnesCount16(bph.afterMap[row])

	if bph.trace {
		fmt.Printf("touchMap[%d]=%016b, afterMap[%d]=%016b\n", row, bph.touchMap[row], row, bph.afterMap[row])
	}
	switch partsCount {
	case 0:
		// Everything deleted
		if bph.touchMap[row] != 0 {
			if row == 0 {
				// Root is deleted because the tree is empty
				bph.rootTouched = true
				bph.rootPresent = false
			} else if upDepth == halfKeySize {
				// Special case - all storage items of an account have been deleted, but it does not automatically delete the account, just makes it empty storage
				// Therefore we are not propagating deletion upwards, but turn it into a modification
				bph.touchMap[row-1] |= uint16(1) << col
			} else {
				// Deletion is propagated upwards
				bph.touchMap[row-1] |= uint16(1) << col
				bph.afterMap[row-1] &^= uint16(1) << col
			}
		}
		upBinaryCell.hl = 0
		upBinaryCell.apl = 0
		upBinaryCell.spl = 0
		upBinaryCell.extLen = 0
		upBinaryCell.downHashedLen = 0
		if bph.branchBefore[row] {
			branchData, _, err = EncodeBranch(0, bph.touchMap[row], 0, func(nibble int, skip bool) (*Cell, error) { return nil, nil })
			if err != nil {
				return nil, updateKey, fmt.Errorf("failed to encode leaf node update: %w", err)
			}
		}
		bph.activeRows--
		if upDepth > 0 {
			bph.currentKeyLen = upDepth - 1
		} else {
			bph.currentKeyLen = 0
		}
	case 1:
		// Leaf or extension node
		if bph.touchMap[row] != 0 {
			// any modifications
			if row == 0 {
				bph.rootTouched = true
			} else {
				// Modifiction is propagated upwards
				bph.touchMap[row-1] |= uint16(1) << col
			}
		}
		nibble := bits.TrailingZeros16(bph.afterMap[row])
		cell := &bph.grid[row][nibble]
		upBinaryCell.extLen = 0
		upBinaryCell.fillFromLowerBinaryCell(cell, depth, bph.currentKey[upDepth:bph.currentKeyLen], nibble)
		// Delete if it existed
		if bph.branchBefore[row] {
			branchData, _, err = EncodeBranch(0, bph.touchMap[row], 0, func(nibble int, skip bool) (*Cell, error) { return nil, nil })
			if err != nil {
				return nil, updateKey, fmt.Errorf("failed to encode leaf node update: %w", err)
			}
		}
		bph.activeRows--
		if upDepth > 0 {
			bph.currentKeyLen = upDepth - 1
		} else {
			bph.currentKeyLen = 0
		}
	default:
		// Branch node
		if bph.touchMap[row] != 0 {
			// any modifications
			if row == 0 {
				bph.rootTouched = true
			} else {
				// Modifiction is propagated upwards
				bph.touchMap[row-1] |= uint16(1) << col
			}
		}
		bitmap := bph.touchMap[row] & bph.afterMap[row]
		if !bph.branchBefore[row] {
			// There was no branch node before, so we need to touch even the singular child that existed
			bph.touchMap[row] |= bph.afterMap[row]
			bitmap |= bph.afterMap[row]
		}
		// Calculate total length of all hashes
		totalBranchLen := 17 - partsCount // For every empty cell, one byte
		for bitset, j := bph.afterMap[row], 0; bitset != 0; j++ {
			bit := bitset & -bitset
			nibble := bits.TrailingZeros16(bit)
			cell := &bph.grid[row][nibble]
			totalBranchLen += bph.computeBinaryCellHashLen(cell, depth)
			bitset ^= bit
		}

		bph.keccak2.Reset()
		pt := rlp.GenerateStructLen(bph.hashAuxBuffer[:], totalBranchLen)
		if _, err := bph.keccak2.Write(bph.hashAuxBuffer[:pt]); err != nil {
			return nil, nil, err
		}

		b := [...]byte{0x80}
		cellGetter := func(nibble int, skip bool) (*Cell, error) {
			if skip {
				if _, err := bph.keccak2.Write(b[:]); err != nil {
					return nil, fmt.Errorf("failed to write empty nibble to hash: %w", err)
				}
				if bph.trace {
					fmt.Printf("%x: empty(%d,%x)\n", nibble, row, nibble)
				}
				return nil, nil
			}
			cell := &bph.grid[row][nibble]
			cellHash, err := bph.computeBinaryCellHash(cell, depth, bph.hashAuxBuffer[:0])
			if err != nil {
				return nil, err
			}
			if bph.trace {
				fmt.Printf("%x: computeBinaryCellHash(%d,%x,depth=%d)=[%x]\n", nibble, row, nibble, depth, cellHash)
			}
			if _, err := bph.keccak2.Write(cellHash); err != nil {
				return nil, err
			}

			// TODO extension and downHashedKey should be encoded to hex format and vice versa, data loss due to array sizes
			return cell.unwrapToHexCell(), nil
		}

		var lastNibble int
		var err error
		_ = cellGetter

		//branchData, lastNibble, err = bph.EncodeBranchDirectAccess(bitmap, row, depth, branchData)
		branchData, lastNibble, err = EncodeBranch(bitmap, bph.touchMap[row], bph.afterMap[row], cellGetter)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to encode branch update: %w", err)
		}
		for i := lastNibble; i <= maxChild; i++ {
			if _, err := bph.keccak2.Write(b[:]); err != nil {
				return nil, nil, err
			}
			if bph.trace {
				fmt.Printf("%x: empty(%d,%x)\n", i, row, i)
			}
		}
		upBinaryCell.extLen = depth - upDepth - 1
		upBinaryCell.downHashedLen = upBinaryCell.extLen
		if upBinaryCell.extLen > 0 {
			copy(upBinaryCell.extension[:], bph.currentKey[upDepth:bph.currentKeyLen])
			copy(upBinaryCell.downHashedKey[:], bph.currentKey[upDepth:bph.currentKeyLen])
		}
		if depth < halfKeySize {
			upBinaryCell.apl = 0
		}
		upBinaryCell.spl = 0
		upBinaryCell.hl = 32
		if _, err := bph.keccak2.Read(upBinaryCell.h[:]); err != nil {
			return nil, nil, err
		}
		if bph.trace {
			fmt.Printf("} [%x]\n", upBinaryCell.h[:])
		}
		bph.activeRows--
		if upDepth > 0 {
			bph.currentKeyLen = upDepth - 1
		} else {
			bph.currentKeyLen = 0
		}
	}
	if branchData != nil {
		if bph.trace {
			fmt.Printf("fold: update key: %x, branchData: [%x]\n", CompactedKeyToHex(updateKey), branchData)
		}
	}
	return branchData, updateKey, nil
}

func (bph *BinPatriciaHashed) RootHash() ([]byte, error) {
	hash, err := bph.computeBinaryCellHash(&bph.root, 0, nil)
	if err != nil {
		return nil, err
	}
	return hash[1:], nil // first byte is 128+hash_len
}

func (bph *BinPatriciaHashed) ReviewKeys(plainKeys, hashedKeys [][]byte) (rootHash []byte, branchNodeUpdates map[string]BranchData, err error) {
	branchNodeUpdates = make(map[string]BranchData)

	stagedBinaryCell := new(BinaryCell)
	for i, hashedKey := range hashedKeys {
		plainKey := plainKeys[i]
		hashedKey = hexToBin(hashedKey)
		if bph.trace {
			fmt.Printf("plainKey=[%x], hashedKey=[%x], currentKey=[%x]\n", plainKey, hashedKey, bph.currentKey[:bph.currentKeyLen])
		}
		// Keep folding until the currentKey is the prefix of the key we modify
		for bph.needFolding(hashedKey) {
			if branchData, updateKey, err := bph.fold(); err != nil {
				return nil, nil, fmt.Errorf("fold: %w", err)
			} else if branchData != nil {
				branchNodeUpdates[string(updateKey)] = branchData
			}
		}
		// Now unfold until we step on an empty cell
		for unfolding := bph.needUnfolding(hashedKey); unfolding > 0; unfolding = bph.needUnfolding(hashedKey) {
			if err := bph.unfold(hashedKey, unfolding); err != nil {
				return nil, nil, fmt.Errorf("unfold: %w", err)
			}
		}

		// Update the cell
		stagedBinaryCell.fillEmpty()
		if len(plainKey) == bph.accountKeyLen {
			if err := bph.accountFn(plainKey, stagedBinaryCell); err != nil {
				return nil, nil, fmt.Errorf("accountFn for key %x failed: %w", plainKey, err)
			}
			if !stagedBinaryCell.Delete {
				cell := bph.updateBinaryCell(plainKey, hashedKey)
				cell.setAccountFields(stagedBinaryCell.CodeHash[:], &stagedBinaryCell.Balance, stagedBinaryCell.Nonce)

				if bph.trace {
					fmt.Printf("accountFn reading key %x => balance=%v nonce=%v codeHash=%x\n", cell.apk, cell.Balance.Uint64(), cell.Nonce, cell.CodeHash)
				}
			}
		} else {
			if err = bph.storageFn(plainKey, stagedBinaryCell); err != nil {
				return nil, nil, fmt.Errorf("storageFn for key %x failed: %w", plainKey, err)
			}
			if !stagedBinaryCell.Delete {
				bph.updateBinaryCell(plainKey, hashedKey).setStorage(stagedBinaryCell.Storage[:stagedBinaryCell.StorageLen])
				if bph.trace {
					fmt.Printf("storageFn reading key %x => %x\n", plainKey, stagedBinaryCell.Storage[:stagedBinaryCell.StorageLen])
				}
			}
		}

		if stagedBinaryCell.Delete {
			if bph.trace {
				fmt.Printf("delete cell %x hash %x\n", plainKey, hashedKey)
			}
			bph.deleteBinaryCell(hashedKey)
		}
	}
	// Folding everything up to the root
	for bph.activeRows > 0 {
		if branchData, updateKey, err := bph.fold(); err != nil {
			return nil, nil, fmt.Errorf("final fold: %w", err)
		} else if branchData != nil {
			branchNodeUpdates[string(updateKey)] = branchData
		}
	}

	rootHash, err = bph.RootHash()
	if err != nil {
		return nil, branchNodeUpdates, fmt.Errorf("root hash evaluation failed: %w", err)
	}
	return rootHash, branchNodeUpdates, nil
}

func (bph *BinPatriciaHashed) SetTrace(trace bool) { bph.trace = trace }

func (bph *BinPatriciaHashed) Variant() TrieVariant { return VariantBinPatriciaTrie }

// Reset allows BinPatriciaHashed instance to be reused for the new commitment calculation
func (bph *BinPatriciaHashed) Reset() {
	bph.rootChecked = false
	bph.root.hl = 0
	bph.root.downHashedLen = 0
	bph.root.apl = 0
	bph.root.spl = 0
	bph.root.extLen = 0
	copy(bph.root.CodeHash[:], EmptyCodeHash)
	bph.root.StorageLen = 0
	bph.root.Balance.Clear()
	bph.root.Nonce = 0
	bph.rootTouched = false
	bph.rootPresent = true
}

func (bph *BinPatriciaHashed) ResetFns(
	branchFn func(prefix []byte) ([]byte, error),
	accountFn func(plainKey []byte, cell *Cell) error,
	storageFn func(plainKey []byte, cell *Cell) error,
) {
	bph.branchFn = branchFn
	bph.accountFn = wrapAccountStorageFn(accountFn)
	bph.storageFn = wrapAccountStorageFn(storageFn)
}

// Encode current state of hph into bytes
func (bph *BinPatriciaHashed) EncodeCurrentState(buf []byte) ([]byte, error) {
	s := binState{
		CurrentKeyLen: int16(bph.currentKeyLen),
		RootChecked:   bph.rootChecked,
		RootTouched:   bph.rootTouched,
		RootPresent:   bph.rootPresent,
		Root:          make([]byte, 0),
	}

	s.Root = bph.root.bytes()
	copy(s.CurrentKey[:], bph.currentKey[:])
	copy(s.Depths[:], bph.depths[:])
	copy(s.BranchBefore[:], bph.branchBefore[:])
	copy(s.TouchMap[:], bph.touchMap[:])
	copy(s.AfterMap[:], bph.afterMap[:])

	return s.Encode(buf)
}

// buf expected to be encoded hph state. Decode state and set up hph to that state.
func (bph *BinPatriciaHashed) SetState(buf []byte) error {
	if bph.activeRows != 0 {
		return fmt.Errorf("has active rows, could not reset state")
	}

	var s state
	if err := s.Decode(buf); err != nil {
		return err
	}

	bph.Reset()

	if err := bph.root.decodeBytes(s.Root); err != nil {
		return err
	}

	bph.currentKeyLen = int(s.CurrentKeyLen)
	bph.rootChecked = s.RootChecked
	bph.rootTouched = s.RootTouched
	bph.rootPresent = s.RootPresent

	copy(bph.currentKey[:], s.CurrentKey[:])
	copy(bph.depths[:], s.Depths[:])
	copy(bph.branchBefore[:], s.BranchBefore[:])
	copy(bph.touchMap[:], s.TouchMap[:])
	copy(bph.afterMap[:], s.AfterMap[:])

	return nil
}

func (bph *BinPatriciaHashed) ProcessUpdates(plainKeys, hashedKeys [][]byte, updates []Update) (rootHash []byte, branchNodeUpdates map[string]BranchData, err error) {
	branchNodeUpdates = make(map[string]BranchData)

	for i, plainKey := range plainKeys {
		hashedKey := hashedKeys[i]
		if bph.trace {
			fmt.Printf("plainKey=[%x], hashedKey=[%x], currentKey=[%x]\n", plainKey, hashedKey, bph.currentKey[:bph.currentKeyLen])
		}
		// Keep folding until the currentKey is the prefix of the key we modify
		for bph.needFolding(hashedKey) {
			if branchData, updateKey, err := bph.fold(); err != nil {
				return nil, nil, fmt.Errorf("fold: %w", err)
			} else if branchData != nil {
				branchNodeUpdates[string(updateKey)] = branchData
			}
		}
		// Now unfold until we step on an empty cell
		for unfolding := bph.needUnfolding(hashedKey); unfolding > 0; unfolding = bph.needUnfolding(hashedKey) {
			if err := bph.unfold(hashedKey, unfolding); err != nil {
				return nil, nil, fmt.Errorf("unfold: %w", err)
			}
		}

		update := updates[i]
		// Update the cell
		if update.Flags == DeleteUpdate {
			bph.deleteBinaryCell(hashedKey)
			if bph.trace {
				fmt.Printf("key %x deleted\n", plainKey)
			}
		} else {
			cell := bph.updateBinaryCell(plainKey, hashedKey)
			if bph.trace {
				fmt.Printf("accountFn updated key %x =>", plainKey)
			}
			if update.Flags&BalanceUpdate != 0 {
				if bph.trace {
					fmt.Printf(" balance=%d", update.Balance.Uint64())
				}
				cell.Balance.Set(&update.Balance)
			}
			if update.Flags&NonceUpdate != 0 {
				if bph.trace {
					fmt.Printf(" nonce=%d", update.Nonce)
				}
				cell.Nonce = update.Nonce
			}
			if update.Flags&CodeUpdate != 0 {
				if bph.trace {
					fmt.Printf(" codeHash=%x", update.CodeHashOrStorage)
				}
				copy(cell.CodeHash[:], update.CodeHashOrStorage[:])
			}
			if bph.trace {
				fmt.Printf("\n")
			}
			if update.Flags&StorageUpdate != 0 {
				cell.setStorage(update.CodeHashOrStorage[:update.ValLength])
				if bph.trace {
					fmt.Printf("\rstorageFn filled key %x => %x\n", plainKey, update.CodeHashOrStorage[:update.ValLength])
				}
			}
		}
	}
	// Folding everything up to the root
	for bph.activeRows > 0 {
		if branchData, updateKey, err := bph.fold(); err != nil {
			return nil, nil, fmt.Errorf("final fold: %w", err)
		} else if branchData != nil {
			branchNodeUpdates[string(updateKey)] = branchData
		}
	}

	rootHash, err = bph.RootHash()
	if err != nil {
		return nil, branchNodeUpdates, fmt.Errorf("root hash evaluation failed: %w", err)
	}
	return rootHash, branchNodeUpdates, nil
}

func (s *binState) Encode(buf []byte) ([]byte, error) {
	var rootFlags stateRootFlag
	if s.RootPresent {
		rootFlags |= stateRootPresent
	}
	if s.RootChecked {
		rootFlags |= stateRootChecked
	}
	if s.RootTouched {
		rootFlags |= stateRootTouched
	}

	ee := bytes.NewBuffer(buf)
	if err := binary.Write(ee, binary.BigEndian, s.CurrentKeyLen); err != nil {
		return nil, fmt.Errorf("encode currentKeyLen: %w", err)
	}
	if err := binary.Write(ee, binary.BigEndian, int8(rootFlags)); err != nil {
		return nil, fmt.Errorf("encode rootFlags: %w", err)
	}
	if n, err := ee.Write(s.CurrentKey[:]); err != nil || n != len(s.CurrentKey) {
		return nil, fmt.Errorf("encode currentKey: %w", err)
	}
	if err := binary.Write(ee, binary.BigEndian, uint16(len(s.Root))); err != nil {
		return nil, fmt.Errorf("encode root len: %w", err)
	}
	if n, err := ee.Write(s.Root); err != nil || n != len(s.Root) {
		return nil, fmt.Errorf("encode root: %w", err)
	}
	d := make([]byte, len(s.Depths))
	for i := 0; i < len(s.Depths); i++ {
		d[i] = byte(s.Depths[i])
	}
	if n, err := ee.Write(d); err != nil || n != len(s.Depths) {
		return nil, fmt.Errorf("encode depths: %w", err)
	}
	if err := binary.Write(ee, binary.BigEndian, s.TouchMap); err != nil {
		return nil, fmt.Errorf("encode touchMap: %w", err)
	}
	if err := binary.Write(ee, binary.BigEndian, s.AfterMap); err != nil {
		return nil, fmt.Errorf("encode afterMap: %w", err)
	}

	var before1, before2 uint64
	for i := 0; i < halfKeySize; i++ {
		if s.BranchBefore[i] {
			before1 |= 1 << i
		}
	}
	for i, j := halfKeySize, 0; i < maxKeySize; i, j = i+1, j+1 {
		if s.BranchBefore[i] {
			before2 |= 1 << j
		}
	}
	if err := binary.Write(ee, binary.BigEndian, before1); err != nil {
		return nil, fmt.Errorf("encode branchBefore_1: %w", err)
	}
	if err := binary.Write(ee, binary.BigEndian, before2); err != nil {
		return nil, fmt.Errorf("encode branchBefore_2: %w", err)
	}
	return ee.Bytes(), nil
}

func (s *binState) Decode(buf []byte) error {
	aux := bytes.NewBuffer(buf)
	if err := binary.Read(aux, binary.BigEndian, &s.CurrentKeyLen); err != nil {
		return fmt.Errorf("currentKeyLen: %w", err)
	}
	var rootFlags stateRootFlag
	if err := binary.Read(aux, binary.BigEndian, &rootFlags); err != nil {
		return fmt.Errorf("rootFlags: %w", err)
	}

	if rootFlags&stateRootPresent != 0 {
		s.RootPresent = true
	}
	if rootFlags&stateRootTouched != 0 {
		s.RootTouched = true
	}
	if rootFlags&stateRootChecked != 0 {
		s.RootChecked = true
	}
	if n, err := aux.Read(s.CurrentKey[:]); err != nil || n != maxKeySize {
		return fmt.Errorf("currentKey: %w", err)
	}
	var rootSize uint16
	if err := binary.Read(aux, binary.BigEndian, &rootSize); err != nil {
		return fmt.Errorf("root size: %w", err)
	}
	s.Root = make([]byte, rootSize)
	if _, err := aux.Read(s.Root); err != nil {
		return fmt.Errorf("root: %w", err)
	}
	d := make([]byte, len(s.Depths))
	if err := binary.Read(aux, binary.BigEndian, &d); err != nil {
		return fmt.Errorf("depths: %w", err)
	}
	for i := 0; i < len(s.Depths); i++ {
		s.Depths[i] = int(d[i])
	}
	if err := binary.Read(aux, binary.BigEndian, &s.TouchMap); err != nil {
		return fmt.Errorf("touchMap: %w", err)
	}
	if err := binary.Read(aux, binary.BigEndian, &s.AfterMap); err != nil {
		return fmt.Errorf("afterMap: %w", err)
	}
	var branch1, branch2 uint64
	if err := binary.Read(aux, binary.BigEndian, &branch1); err != nil {
		return fmt.Errorf("branchBefore1: %w", err)
	}
	if err := binary.Read(aux, binary.BigEndian, &branch2); err != nil {
		return fmt.Errorf("branchBefore2: %w", err)
	}

	// TODO invalid branch encode
	for i := 0; i < halfKeySize; i++ {
		if branch1&(1<<i) != 0 {
			s.BranchBefore[i] = true
		}
	}
	for i, j := halfKeySize, 0; i < maxKeySize; i, j = i+1, j+1 {
		if branch2&(1<<j) != 0 {
			s.BranchBefore[i] = true
		}
	}
	return nil
}
