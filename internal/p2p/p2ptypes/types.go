// Package p2ptypes contains P2P types required for sync that cannot be
// represented as a protobuf schema, along with their fast-ssz methods.
package p2ptypes

import (
	"fmt"

	ssz "github.com/prysmaticlabs/fastssz"
)

const rootLength = 32

const maxErrorLength = 256

// maxRequestBlocks is the maximum number of blocks in a single request.
const maxRequestBlocks = 1024

// SSZBytes is a byte slice that implements the fast-ssz Hasher interface.
type SSZBytes []byte

// HashTreeRoot computes the SSZ hash tree root of the byte slice.
func (b *SSZBytes) HashTreeRoot() ([32]byte, error) {
	return ssz.HashWithDefaultHasher(b)
}

// HashTreeRootWith computes the hash tree root using the provided hasher.
func (b *SSZBytes) HashTreeRootWith(hh *ssz.Hasher) error {
	indx := hh.Index()
	hh.PutBytes(*b)
	hh.Merkleize(indx)
	return nil
}

// BlockByRootsReq is a request for blocks identified by their roots.
type BlockByRootsReq [][rootLength]byte

// MarshalSSZTo appends the SSZ-encoded request to dst.
func (r *BlockByRootsReq) MarshalSSZTo(dst []byte) ([]byte, error) {
	encoded, err := r.MarshalSSZ()
	if err != nil {
		return nil, err
	}
	return append(dst, encoded...), nil
}

// MarshalSSZ serializes the block-by-roots request.
func (r *BlockByRootsReq) MarshalSSZ() ([]byte, error) {
	if len(*r) > maxRequestBlocks {
		return nil, fmt.Errorf("block by roots request exceeds max size: %d > %d", len(*r), maxRequestBlocks)
	}
	buf := make([]byte, 0, r.SizeSSZ())
	for _, root := range *r {
		buf = append(buf, root[:]...)
	}
	return buf, nil
}

// SizeSSZ returns the size of the SSZ-encoded representation.
func (r *BlockByRootsReq) SizeSSZ() int {
	return len(*r) * rootLength
}

// UnmarshalSSZ deserializes a block-by-roots request from buf.
func (r *BlockByRootsReq) UnmarshalSSZ(buf []byte) error {
	if len(buf) > maxRequestBlocks*rootLength {
		return fmt.Errorf("expected buffer with length up to %d but received %d", maxRequestBlocks*rootLength, len(buf))
	}
	if len(buf)%rootLength != 0 {
		return ssz.ErrIncorrectByteSize
	}
	numRoots := len(buf) / rootLength
	roots := make([][rootLength]byte, numRoots)
	for i := range roots {
		copy(roots[i][:], buf[i*rootLength:(i+1)*rootLength])
	}
	*r = roots
	return nil
}

// ErrorMessage is an SSZ-serializable RPC error message.
type ErrorMessage []byte

// MarshalSSZTo appends the SSZ-encoded error message to dst.
func (m *ErrorMessage) MarshalSSZTo(dst []byte) ([]byte, error) {
	encoded, err := m.MarshalSSZ()
	if err != nil {
		return nil, err
	}
	return append(dst, encoded...), nil
}

// MarshalSSZ serializes the error message.
func (m *ErrorMessage) MarshalSSZ() ([]byte, error) {
	if len(*m) > maxErrorLength {
		return nil, fmt.Errorf("error message exceeds max size: %d > %d", len(*m), maxErrorLength)
	}
	buf := make([]byte, m.SizeSSZ())
	copy(buf, *m)
	return buf, nil
}

// SizeSSZ returns the size of the SSZ-encoded representation.
func (m *ErrorMessage) SizeSSZ() int {
	return len(*m)
}

// UnmarshalSSZ deserializes an error message from buf.
func (m *ErrorMessage) UnmarshalSSZ(buf []byte) error {
	if len(buf) > maxErrorLength {
		return fmt.Errorf("expected buffer with length up to %d but received %d", maxErrorLength, len(buf))
	}
	msg := make([]byte, len(buf))
	copy(msg, buf)
	*m = msg
	return nil
}
