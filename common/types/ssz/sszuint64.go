package ssztype

import (
	"encoding/binary"
	"fmt"

	fssz "github.com/prysmaticlabs/fastssz"
)

type SSZUint64 uint64

func (s *SSZUint64) SizeSSZ() int { return 8 }

func (s *SSZUint64) MarshalSSZTo(dst []byte) ([]byte, error) {
	marshalled, err := s.MarshalSSZ()
	if err != nil {
		return nil, err
	}
	return append(dst, marshalled...), nil
}

func (s *SSZUint64) MarshalSSZ() ([]byte, error) {
	return fssz.MarshalUint64([]byte{}, uint64(*s)), nil
}

func (s *SSZUint64) UnmarshalSSZ(buf []byte) error {
	if len(buf) != s.SizeSSZ() {
		return fmt.Errorf("expected buffer of length %d received %d", s.SizeSSZ(), len(buf))
	}
	*s = SSZUint64(fssz.UnmarshallUint64(buf))
	return nil
}

func (s *SSZUint64) HashTreeRoot() ([32]byte, error) {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, uint64(*s))
	var root [32]byte
	copy(root[:], buf)
	return root, nil
}

func (s *SSZUint64) HashTreeRootWith(hh *fssz.Hasher) error {
	indx := hh.Index()
	hh.PutUint64(uint64(*s))
	hh.Merkleize(indx)
	return nil
}
