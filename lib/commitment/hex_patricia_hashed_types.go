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
	"fmt"
	"hash"
	"strings"

	"github.com/holiman/uint256"
	"golang.org/x/crypto/sha3"

	"github.com/n42blockchain/N42/lib/common/length"
)

// keccakState wraps sha3.state. In addition to the usual hash methods, it also supports
// Read to get a variable amount of data from the hash state. Read is faster than Sum
// because it doesn't copy the internal state, but also modifies the internal state.
type keccakState interface {
	hash.Hash
	Read([]byte) (int, error)
}

// HexPatriciaHashed implements commitment based on patricia merkle tree with radix 16,
// with keys pre-hashed by keccak256
type HexPatriciaHashed struct {
	root Cell // Root cell of the tree
	// How many rows (starting from row 0) are currently active and have corresponding selected columns
	// Last active row does not have selected column
	activeRows int
	// Length of the key that reflects current positioning of the grid. It maybe larger than number of active rows,
	// if an account leaf cell represents multiple nibbles in the key
	currentKeyLen int
	accountKeyLen int
	// Rows of the grid correspond to the level of depth in the patricia tree
	// Columns of the grid correspond to pointers to the nodes further from the root
	grid         [128][16]Cell // First 64 rows of this grid are for account trie, and next 64 rows are for storage trie
	currentKey   [128]byte     // For each row indicates which column is currently selected
	depths       [128]int      // For each row, the depth of cells in that row
	branchBefore [128]bool     // For each row, whether there was a branch node in the database loaded in unfold
	touchMap     [128]uint16   // For each row, bitmap of cells that were either present before modification, or modified or deleted
	afterMap     [128]uint16   // For each row, bitmap of cells that were present after modification
	keccak       keccakState
	keccak2      keccakState
	rootChecked  bool // Set to false if it is not known whether the root is empty, set to true if it is checked
	rootTouched  bool
	rootPresent  bool
	trace        bool
	// Function used to load branch node and fill up the cells
	// For each cell, it sets the cell type, clears the modified flag, fills the hash,
	// and for the extension, account, and leaf type, the `l` and `k`
	branchFn func(prefix []byte) ([]byte, error)
	// Function used to fetch account with given plain key
	accountFn func(plainKey []byte, cell *Cell) error
	// Function used to fetch storage with given plain key
	storageFn func(plainKey []byte, cell *Cell) error
	// Optional callback: immediately persist branch data during fold().
	// Without this, branch data from fold() is only available in the
	// ProcessUpdates return value, causing unfold() within the same call to fail.
	putBranchFn func(updateKey []byte, branchData []byte)

	hashAuxBuffer [128]byte     // buffer to compute cell hash or write hash-related things
	auxBuffer     *bytes.Buffer // auxiliary buffer used during branch updates encoding
}

func NewHexPatriciaHashed(accountKeyLen int,
	branchFn func(prefix []byte) ([]byte, error),
	accountFn func(plainKey []byte, cell *Cell) error,
	storageFn func(plainKey []byte, cell *Cell) error,
) *HexPatriciaHashed {
	return &HexPatriciaHashed{
		keccak:        sha3.NewLegacyKeccak256().(keccakState),
		keccak2:       sha3.NewLegacyKeccak256().(keccakState),
		accountKeyLen: accountKeyLen,
		branchFn:      branchFn,
		accountFn:     accountFn,
		storageFn:     storageFn,
		auxBuffer:     bytes.NewBuffer(make([]byte, 8192)),
	}
}

// represents state of the tree
type state struct {
	Root          []byte      // encoded root cell
	Depths        [128]int    // For each row, the depth of cells in that row
	TouchMap      [128]uint16 // For each row, bitmap of cells that were either present before modification, or modified or deleted
	AfterMap      [128]uint16 // For each row, bitmap of cells that were present after modification
	BranchBefore  [128]bool   // For each row, whether there was a branch node in the database loaded in unfold
	CurrentKey    [128]byte   // For each row indicates which column is currently selected
	CurrentKeyLen int8
	RootChecked   bool // Set to false if it is not known whether the root is empty, set to true if it is checked
	RootTouched   bool
	RootPresent   bool
}

type stateRootFlag int8

var (
	stateRootPresent stateRootFlag = 1
	stateRootChecked stateRootFlag = 2
	stateRootTouched stateRootFlag = 4
)

type Cell struct {
	Balance       uint256.Int
	Nonce         uint64
	hl            int // Length of the hash (or embedded)
	StorageLen    int
	apl           int // length of account plain key
	spl           int // length of the storage plain key
	downHashedLen int
	extLen        int
	downHashedKey [128]byte
	extension     [64]byte
	spk           [length.Addr + length.Hash]byte // storage plain key
	h             [length.Hash]byte               // cell hash
	CodeHash      [length.Hash]byte               // hash of the bytecode
	Storage       [length.Hash]byte
	apk           [length.Addr]byte // account plain key
	Delete        bool
}

var (
	EmptyRootHash, _ = hex.DecodeString("56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	EmptyCodeHash, _ = hex.DecodeString("c5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")
)

type UpdateFlags uint8

const (
	CodeUpdate    UpdateFlags = 1
	DeleteUpdate  UpdateFlags = 2
	BalanceUpdate UpdateFlags = 4
	NonceUpdate   UpdateFlags = 8
	StorageUpdate UpdateFlags = 16
)

func (uf UpdateFlags) String() string {
	var sb strings.Builder
	if uf == DeleteUpdate {
		sb.WriteString("Delete")
	} else {
		if uf&BalanceUpdate != 0 {
			sb.WriteString("+Balance")
		}
		if uf&NonceUpdate != 0 {
			sb.WriteString("+Nonce")
		}
		if uf&CodeUpdate != 0 {
			sb.WriteString("+Code")
		}
		if uf&StorageUpdate != 0 {
			sb.WriteString("+Storage")
		}
	}
	return sb.String()
}

type Update struct {
	Flags             UpdateFlags
	Balance           uint256.Int
	Nonce             uint64
	CodeHashOrStorage [length.Hash]byte
	ValLength         int
}

func (u *Update) DecodeForStorage(enc []byte) {
	u.Nonce = 0
	u.Balance.Clear()
	copy(u.CodeHashOrStorage[:], EmptyCodeHash)

	pos := 0
	nonceBytes := int(enc[pos])
	pos++
	if nonceBytes > 0 {
		u.Nonce = bytesToUint64(enc[pos : pos+nonceBytes])
		pos += nonceBytes
	}
	balanceBytes := int(enc[pos])
	pos++
	if balanceBytes > 0 {
		u.Balance.SetBytes(enc[pos : pos+balanceBytes])
		pos += balanceBytes
	}
	codeHashBytes := int(enc[pos])
	pos++
	if codeHashBytes > 0 {
		copy(u.CodeHashOrStorage[:], enc[pos:pos+codeHashBytes])
	}
}

func (u *Update) Encode(buf []byte, numBuf []byte) []byte {
	buf = append(buf, byte(u.Flags))
	if u.Flags&BalanceUpdate != 0 {
		buf = append(buf, byte(u.Balance.ByteLen()))
		buf = append(buf, u.Balance.Bytes()...)
	}
	if u.Flags&NonceUpdate != 0 {
		n := binary.PutUvarint(numBuf, u.Nonce)
		buf = append(buf, numBuf[:n]...)
	}
	if u.Flags&CodeUpdate != 0 {
		buf = append(buf, u.CodeHashOrStorage[:]...)
	}
	if u.Flags&StorageUpdate != 0 {
		n := binary.PutUvarint(numBuf, uint64(u.ValLength))
		buf = append(buf, numBuf[:n]...)
		if u.ValLength > 0 {
			buf = append(buf, u.CodeHashOrStorage[:u.ValLength]...)
		}
	}
	return buf
}

func (u *Update) Decode(buf []byte, pos int) (int, error) {
	if len(buf) < pos+1 {
		return 0, fmt.Errorf("decode Update: buffer too small for flags")
	}
	u.Flags = UpdateFlags(buf[pos])
	pos++
	if u.Flags&BalanceUpdate != 0 {
		if len(buf) < pos+1 {
			return 0, fmt.Errorf("decode Update: buffer too small for balance len")
		}
		balanceLen := int(buf[pos])
		pos++
		if len(buf) < pos+balanceLen {
			return 0, fmt.Errorf("decode Update: buffer too small for balance")
		}
		u.Balance.SetBytes(buf[pos : pos+balanceLen])
		pos += balanceLen
	}
	if u.Flags&NonceUpdate != 0 {
		var n int
		u.Nonce, n = binary.Uvarint(buf[pos:])
		if n == 0 {
			return 0, fmt.Errorf("decode Update: buffer too small for nonce")
		}
		if n < 0 {
			return 0, fmt.Errorf("decode Update: nonce overflow")
		}
		pos += n
	}
	if u.Flags&CodeUpdate != 0 {
		if len(buf) < pos+32 {
			return 0, fmt.Errorf("decode Update: buffer too small for codeHash")
		}
		copy(u.CodeHashOrStorage[:], buf[pos:pos+32])
		pos += 32
	}
	if u.Flags&StorageUpdate != 0 {
		l, n := binary.Uvarint(buf[pos:])
		if n == 0 {
			return 0, fmt.Errorf("decode Update: buffer too small for storage len")
		}
		if n < 0 {
			return 0, fmt.Errorf("decode Update: storage lee overflow")
		}
		pos += n
		if len(buf) < pos+int(l) {
			return 0, fmt.Errorf("decode Update: buffer too small for storage")
		}
		u.ValLength = int(l)
		copy(u.CodeHashOrStorage[:], buf[pos:pos+int(l)])
		pos += int(l)
	}
	return pos, nil
}

func (u *Update) String() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Flags: [%s]", u.Flags))
	if u.Flags&BalanceUpdate != 0 {
		sb.WriteString(fmt.Sprintf(", Balance: [%d]", &u.Balance))
	}
	if u.Flags&NonceUpdate != 0 {
		sb.WriteString(fmt.Sprintf(", Nonce: [%d]", u.Nonce))
	}
	if u.Flags&CodeUpdate != 0 {
		sb.WriteString(fmt.Sprintf(", CodeHash: [%x]", u.CodeHashOrStorage))
	}
	if u.Flags&StorageUpdate != 0 {
		sb.WriteString(fmt.Sprintf(", Storage: [%x]", u.CodeHashOrStorage[:u.ValLength]))
	}
	return sb.String()
}
