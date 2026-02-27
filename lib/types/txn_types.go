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
	"bytes"
	"errors"
	"fmt"
	"sort"

	gokzg4844 "github.com/crate-crypto/go-kzg-4844"
	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/common/length"
	"github.com/n42blockchain/N42/lib/gointerfaces/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

// Transaction type constants.
const (
	LegacyTxType     byte = 0
	AccessListTxType byte = 1 // EIP-2930
	DynamicFeeTxType byte = 2 // EIP-1559
	BlobTxType       byte = 3 // EIP-4844
	SetCodeTxType    byte = 4 // EIP-7702
)

// Sentinel errors for transaction processing.
var (
	ErrParseTxn    = fmt.Errorf("%w transaction", rlp.ErrParse)
	ErrRejected    = errors.New("rejected")
	ErrAlreadyKnown = errors.New("already known")
	ErrRlpTooBig   = errors.New("txn rlp too big")
)

type TxParseConfig struct {
	ChainID uint256.Int
}

type Signature struct {
	ChainID uint256.Int
	V       uint256.Int
	R       uint256.Int
	S       uint256.Int
}

// TxSlot contains information extracted from an Ethereum transaction, which is enough to manage it inside the transaction.
// Also, it contains some auxillary information, like ephemeral fields, and indices within priority queues
type TxSlot struct {
	Rlp            []byte      // Is set to nil after flushing to db, frees memory, later we look for it in the db, if needed
	Value          uint256.Int // Value transferred by the transaction
	Tip            uint256.Int // Maximum tip that transaction is giving to miner/block proposer
	FeeCap         uint256.Int // Maximum fee that transaction burns and gives to the miner/block proposer
	SenderID       uint64      // SenderID - require external mapping to it's address
	Nonce          uint64      // Nonce of the transaction
	DataLen        int         // Length of transaction's data (for calculation of intrinsic gas)
	DataNonZeroLen int
	AlAddrCount    int      // Number of addresses in the access list
	AlStorCount    int      // Number of storage keys in the access list
	Gas            uint64   // Gas limit of the transaction
	IDHash         [32]byte // Transaction hash for the purposes of using it as a transaction Id
	Traced         bool     // Whether transaction needs to be traced throughout transaction pool code and generate debug printing
	Creation       bool     // Set to true if "To" field of the transaction is not set
	Type           byte     // Transaction type
	Size           uint32   // Size of the payload (without the RLP string envelope for typed transactions)

	// EIP-4844: Shard Blob Transactions
	BlobFeeCap  uint256.Int // max_fee_per_blob_gas
	BlobHashes  []common.Hash
	Blobs       [][]byte
	Commitments []gokzg4844.KZGCommitment
	Proofs      []gokzg4844.KZGProof

	// EIP-7702: set code tx
	Authorizations []Signature
	AuthRaw        [][]byte // rlp encoded chainID+address+nonce, used to recover authorization address in txpool
}

// nolint
func (tx *TxSlot) PrintDebug(prefix string) {
	fmt.Printf("%s: senderID=%d,nonce=%d,tip=%d,v=%d\n", prefix, tx.SenderID, tx.Nonce, tx.Tip, tx.Value.Uint64())
}

// AccessList is an EIP-2930 access list.
type AccessList []AccessTuple

// AccessTuple is the element type of an access list.
type AccessTuple struct {
	Address     common.Address `json:"address"`
	StorageKeys []common.Hash  `json:"storageKeys"`
}

// StorageKeys returns the total number of storage keys in the access list.
func (al AccessList) StorageKeys() int {
	sum := 0
	for _, tuple := range al {
		sum += len(tuple.StorageKeys)
	}
	return sum
}

func (al AccessList) HasAddr(addr common.Address) bool {
	for _, tuple := range al {
		if tuple.Address == addr {
			return true
		}
	}
	return false
}

type PeerID *types.H512

// Hashes is a flatten list of 32-byte hashes.
type Hashes []byte

func (h Hashes) At(i int) []byte { return h[i*length.Hash : (i+1)*length.Hash] }
func (h Hashes) Len() int        { return len(h) / length.Hash }
func (h Hashes) Less(i, j int) bool {
	return bytes.Compare(h[i*length.Hash:(i+1)*length.Hash], h[j*length.Hash:(j+1)*length.Hash]) < 0
}
func (h Hashes) Swap(i, j int) {
	ii := i * length.Hash
	jj := j * length.Hash
	for k := 0; k < length.Hash; k++ {
		h[ii], h[jj] = h[jj], h[ii]
		ii++
		jj++
	}
}

// DedupCopy sorts hashes, and creates deduplicated copy
func (h Hashes) DedupCopy() Hashes {
	if len(h) == 0 {
		return h
	}
	sort.Sort(h)
	unique := 1
	for i := length.Hash; i < len(h); i += length.Hash {
		if !bytes.Equal(h[i:i+length.Hash], h[i-length.Hash:i]) {
			unique++
		}
	}
	c := make(Hashes, unique*length.Hash)
	copy(c[:], h[0:length.Hash])
	dest := length.Hash
	for i := dest; i < len(h); i += length.Hash {
		if !bytes.Equal(h[i:i+length.Hash], h[i-length.Hash:i]) {
			copy(c[dest:dest+length.Hash], h[i:i+length.Hash])
			dest += length.Hash
		}
	}
	return c
}

type Announcements struct {
	ts     []byte
	sizes  []uint32
	hashes []byte
}

func (a *Announcements) Append(t byte, size uint32, hash []byte) {
	a.ts = append(a.ts, t)
	a.sizes = append(a.sizes, size)
	a.hashes = append(a.hashes, hash...)
}

func (a *Announcements) AppendOther(other Announcements) {
	a.ts = append(a.ts, other.ts...)
	a.sizes = append(a.sizes, other.sizes...)
	a.hashes = append(a.hashes, other.hashes...)
}

func (a *Announcements) Reset() {
	a.ts = a.ts[:0]
	a.sizes = a.sizes[:0]
	a.hashes = a.hashes[:0]
}

func (a Announcements) At(i int) (byte, uint32, []byte) {
	return a.ts[i], a.sizes[i], a.hashes[i*length.Hash : (i+1)*length.Hash]
}
func (a Announcements) Len() int { return len(a.ts) }
func (a Announcements) Less(i, j int) bool {
	return bytes.Compare(a.hashes[i*length.Hash:(i+1)*length.Hash], a.hashes[j*length.Hash:(j+1)*length.Hash]) < 0
}
func (a Announcements) Swap(i, j int) {
	a.ts[i], a.ts[j] = a.ts[j], a.ts[i]
	a.sizes[i], a.sizes[j] = a.sizes[j], a.sizes[i]
	ii := i * length.Hash
	jj := j * length.Hash
	for k := 0; k < length.Hash; k++ {
		a.hashes[ii], a.hashes[jj] = a.hashes[jj], a.hashes[ii]
		ii++
		jj++
	}
}

// DedupCopy sorts hashes, and creates deduplicated copy
func (a Announcements) DedupCopy() Announcements {
	if len(a.ts) == 0 {
		return a
	}
	sort.Sort(a)
	unique := 1
	for i := length.Hash; i < len(a.hashes); i += length.Hash {
		if !bytes.Equal(a.hashes[i:i+length.Hash], a.hashes[i-length.Hash:i]) {
			unique++
		}
	}
	c := Announcements{
		ts:     make([]byte, unique),
		sizes:  make([]uint32, unique),
		hashes: make([]byte, unique*length.Hash),
	}
	copy(c.hashes, a.hashes[0:length.Hash])
	c.ts[0] = a.ts[0]
	c.sizes[0] = a.sizes[0]
	dest := length.Hash
	j := 1
	origin := length.Hash
	for i := 1; i < len(a.ts); i++ {
		if !bytes.Equal(a.hashes[origin:origin+length.Hash], a.hashes[origin-length.Hash:origin]) {
			copy(c.hashes[dest:dest+length.Hash], a.hashes[origin:origin+length.Hash])
			c.ts[j] = a.ts[i]
			c.sizes[j] = a.sizes[i]
			dest += length.Hash
			j++
		}
		origin += length.Hash
	}
	return c
}

func (a Announcements) DedupHashes() Hashes {
	if len(a.ts) == 0 {
		return Hashes{}
	}
	sort.Sort(a)
	unique := 1
	for i := length.Hash; i < len(a.hashes); i += length.Hash {
		if !bytes.Equal(a.hashes[i:i+length.Hash], a.hashes[i-length.Hash:i]) {
			unique++
		}
	}
	c := make(Hashes, unique*length.Hash)
	copy(c[:], a.hashes[0:length.Hash])
	dest := length.Hash
	j := 1
	origin := length.Hash
	for i := 1; i < len(a.ts); i++ {
		if !bytes.Equal(a.hashes[origin:origin+length.Hash], a.hashes[origin-length.Hash:origin]) {
			copy(c[dest:dest+length.Hash], a.hashes[origin:origin+length.Hash])
			dest += length.Hash
			j++
		}
		origin += length.Hash
	}
	return c
}

func (a Announcements) Hashes() Hashes {
	return Hashes(a.hashes)
}

func (a Announcements) Copy() Announcements {
	if len(a.ts) == 0 {
		return a
	}
	c := Announcements{
		ts:     common.Copy(a.ts),
		sizes:  make([]uint32, len(a.sizes)),
		hashes: common.Copy(a.hashes),
	}
	copy(c.sizes, a.sizes)
	return c
}

// Addresses is a flatten list of 20-byte addresses.
type Addresses []byte

// AddressAt returns an address at the given index in the flattened list.
func (h Addresses) AddressAt(i int) common.Address {
	return *(*[20]byte)(h[i*length.Addr : (i+1)*length.Addr])
}

func (h Addresses) At(i int) []byte { return h[i*length.Addr : (i+1)*length.Addr] }
func (h Addresses) Len() int        { return len(h) / length.Addr }

var (
	zeroAddr        = make([]byte, 20)
	addressesGrowth = make([]byte, length.Addr)
)

type TxSlots struct {
	Txs     []*TxSlot
	Senders Addresses
	IsLocal []bool
}

func (s *TxSlots) Valid() error {
	if len(s.Txs) != len(s.IsLocal) {
		return fmt.Errorf("TxSlots: expect equal len of isLocal=%d and txs=%d", len(s.IsLocal), len(s.Txs))
	}
	if len(s.Txs) != s.Senders.Len() {
		return fmt.Errorf("TxSlots: expect equal len of senders=%d and txs=%d", s.Senders.Len(), len(s.Txs))
	}
	return nil
}

// Resize internal arrays to len=targetSize, shrinks if need. It rely on `append` algorithm to realloc
func (s *TxSlots) Resize(targetSize uint) {
	for uint(len(s.Txs)) < targetSize {
		s.Txs = append(s.Txs, nil)
	}
	for uint(s.Senders.Len()) < targetSize {
		s.Senders = append(s.Senders, addressesGrowth...)
	}
	for uint(len(s.IsLocal)) < targetSize {
		s.IsLocal = append(s.IsLocal, false)
	}
	oldLen := uint(len(s.Txs))
	s.Txs = s.Txs[:targetSize]
	for i := oldLen; i < targetSize; i++ {
		s.Txs[i] = nil
	}
	s.Senders = s.Senders[:length.Addr*targetSize]
	for i := oldLen; i < targetSize; i++ {
		copy(s.Senders.At(int(i)), zeroAddr)
	}
	s.IsLocal = s.IsLocal[:targetSize]
	for i := oldLen; i < targetSize; i++ {
		s.IsLocal[i] = false
	}
}

func (s *TxSlots) Append(slot *TxSlot, sender []byte, isLocal bool) {
	n := len(s.Txs)
	s.Resize(uint(len(s.Txs) + 1))
	s.Txs[n] = slot
	s.IsLocal[n] = isLocal
	copy(s.Senders.At(n), sender)
}

type TxsRlp struct {
	Txs     [][]byte
	Senders Addresses
	IsLocal []bool
}

// Resize internal arrays to len=targetSize, shrinks if need. It rely on `append` algorithm to realloc
func (s *TxsRlp) Resize(targetSize uint) {
	for uint(len(s.Txs)) < targetSize {
		s.Txs = append(s.Txs, nil)
	}
	for uint(s.Senders.Len()) < targetSize {
		s.Senders = append(s.Senders, addressesGrowth...)
	}
	for uint(len(s.IsLocal)) < targetSize {
		s.IsLocal = append(s.IsLocal, false)
	}
	s.Txs = s.Txs[:targetSize]
	s.Senders = s.Senders[:length.Addr*targetSize]
	s.IsLocal = s.IsLocal[:targetSize]
}
