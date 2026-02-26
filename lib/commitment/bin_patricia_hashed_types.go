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
	"encoding/hex"

	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/length"
)

const (
	maxKeySize  = 512
	halfKeySize = maxKeySize / 2
	maxChild    = 2
)

type bitstring []uint8

// converts slice of nibbles (lowest 4 bits of each byte) to bitstring
func hexToBin(hex []byte) bitstring {
	bin := make([]byte, 4*len(hex))
	for i := range bin {
		if hex[i/4]&(1<<(3-i%4)) != 0 {
			bin[i] = 1
		}
	}
	return bin
}

// encodes bitstring to its compact representation
func binToCompact(bin []byte) []byte {
	compact := make([]byte, 2+common.BitLenToByteLen(len(bin)))
	binary.BigEndian.PutUint16(compact, uint16(len(bin)))
	for i := 0; i < len(bin); i++ {
		if bin[i] != 0 {
			compact[2+i/8] |= byte(1) << (i % 8)
		}
	}
	return compact
}

// decodes compact bitstring representation into actual bitstring
func compactToBin(compact []byte) []byte {
	bin := make([]byte, binary.BigEndian.Uint16(compact))
	for i := 0; i < len(bin); i++ {
		if compact[2+i/8]&(byte(1)<<(i%8)) == 0 {
			bin[i] = 0
		} else {
			bin[i] = 1
		}
	}
	return bin
}

// BinHashed implements commitment based on patricia merkle tree with radix 16,
// with keys pre-hashed by keccak256
type BinPatriciaHashed struct {
	root BinaryCell // Root cell of the tree
	// Rows of the grid correspond to the level of depth in the patricia tree
	// Columns of the grid correspond to pointers to the nodes further from the root
	grid [maxKeySize][maxChild]BinaryCell // First halfKeySize rows of this grid are for account trie, and next halfKeySize rows are for storage trie
	// How many rows (starting from row 0) are currently active and have corresponding selected columns
	// Last active row does not have selected column
	activeRows int
	// Length of the key that reflects current positioning of the grid. It maybe larger than number of active rows,
	// if a account leaf cell represents multiple nibbles in the key
	currentKeyLen int
	currentKey    [maxKeySize]byte // For each row indicates which column is currently selected
	depths        [maxKeySize]int  // For each row, the depth of cells in that row
	rootChecked   bool             // Set to false if it is not known whether the root is empty, set to true if it is checked
	rootTouched   bool
	rootPresent   bool
	branchBefore  [maxKeySize]bool   // For each row, whether there was a branch node in the database loaded in unfold
	touchMap      [maxKeySize]uint16 // For each row, bitmap of cells that were either present before modification, or modified or deleted
	afterMap      [maxKeySize]uint16 // For each row, bitmap of cells that were present after modification
	keccak        keccakState
	keccak2       keccakState
	accountKeyLen int
	trace         bool
	hashAuxBuffer [maxKeySize]byte // buffer to compute cell hash or write hash-related things
	auxBuffer     *bytes.Buffer    // auxiliary buffer used during branch updates encoding

	// Function used to load branch node and fill up the cells
	// For each cell, it sets the cell type, clears the modified flag, fills the hash,
	// and for the extension, account, and leaf type, the `l` and `k`
	branchFn func(prefix []byte) ([]byte, error)
	// Function used to fetch account with given plain key
	accountFn func(plainKey []byte, cell *BinaryCell) error
	// Function used to fetch account with given plain key
	storageFn func(plainKey []byte, cell *BinaryCell) error
}

func NewBinPatriciaHashed(accountKeyLen int,
	branchFn func(prefix []byte) ([]byte, error),
	accountFn func(plainKey []byte, cell *Cell) error,
	storageFn func(plainKey []byte, cell *Cell) error,
) *BinPatriciaHashed {
	return &BinPatriciaHashed{
		keccak:        sha3.NewLegacyKeccak256().(keccakState),
		keccak2:       sha3.NewLegacyKeccak256().(keccakState),
		accountKeyLen: accountKeyLen,
		branchFn:      branchFn,
		accountFn:     wrapAccountStorageFn(accountFn),
		storageFn:     wrapAccountStorageFn(storageFn),
		auxBuffer:     bytes.NewBuffer(make([]byte, 8192)),
	}
}

type BinaryCell struct {
	h             [length.Hash]byte               // cell hash
	hl            int                             // Length of the hash (or embedded)
	apk           [length.Addr]byte               // account plain key
	apl           int                             // length of account plain key
	spk           [length.Addr + length.Hash]byte // storage plain key
	spl           int                             // length of the storage plain key
	downHashedKey [maxKeySize]byte
	downHashedLen int
	extension     [halfKeySize]byte
	extLen        int
	Nonce         uint64
	Balance       uint256.Int
	CodeHash      [length.Hash]byte // hash of the bytecode
	Storage       [length.Hash]byte
	StorageLen    int
	Delete        bool
}

var ( // TODO REEAVL
	EmptyBinRootHash, _ = hex.DecodeString("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	EmptyBinCodeHash, _ = hex.DecodeString("c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")
)

// represents state of the tree
type binState struct {
	TouchMap      [maxKeySize]uint16 // For each row, bitmap of cells that were either present before modification, or modified or deleted
	AfterMap      [maxKeySize]uint16 // For each row, bitmap of cells that were present after modification
	CurrentKeyLen int16
	Root          []byte // encoded root cell
	RootChecked   bool   // Set to false if it is not known whether the root is empty, set to true if it is checked
	RootTouched   bool
	RootPresent   bool
	BranchBefore  [maxKeySize]bool // For each row, whether there was a branch node in the database loaded in unfold
	CurrentKey    [maxKeySize]byte // For each row indicates which column is currently selected
	Depths        [maxKeySize]int  // For each row, the depth of cells in that row
}

func wrapAccountStorageFn(fn func([]byte, *Cell) error) func(pk []byte, bc *BinaryCell) error {
	return func(pk []byte, bc *BinaryCell) error {
		cl := bc.unwrapToHexCell()

		if err := fn(pk, cl); err != nil {
			return err
		}

		bc.Balance = *cl.Balance.Clone()
		bc.Nonce = cl.Nonce
		bc.StorageLen = cl.StorageLen
		bc.apl = cl.apl
		bc.spl = cl.spl
		bc.hl = cl.hl
		copy(bc.apk[:], cl.apk[:])
		copy(bc.spk[:], cl.spk[:])
		copy(bc.h[:], cl.h[:])

		if cl.extLen > 0 {
			binExt := compactToBin(cl.extension[:cl.extLen])
			copy(bc.extension[:], binExt)
			bc.extLen = len(binExt)
		}
		if cl.downHashedLen > 0 {
			bindhk := compactToBin(cl.downHashedKey[:cl.downHashedLen])
			copy(bc.downHashedKey[:], bindhk)
			bc.downHashedLen = len(bindhk)
		}

		copy(bc.CodeHash[:], cl.CodeHash[:])
		copy(bc.Storage[:], cl.Storage[:])
		bc.Delete = cl.Delete
		return nil
	}
}
