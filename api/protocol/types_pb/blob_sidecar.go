// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package types_pb

import (
	"encoding/binary"
	"errors"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/runtime/protoimpl"
)

// BlobSidecar is a gossip-compatible representation of a blob sidecar.
// It implements both proto.Message (for gossip topic registration) and
// SSZ Marshaler/Unmarshaler (for P2P encoding).
type BlobSidecar struct {
	state         protoimpl.MessageState
	sizeCache     protoimpl.SizeCache
	unknownFields protoimpl.UnknownFields

	Index         uint64 `protobuf:"varint,1,opt,name=Index,proto3" json:"Index,omitempty"`
	Blob          []byte `protobuf:"bytes,2,opt,name=Blob,proto3" json:"Blob,omitempty"`
	KzgCommitment []byte `protobuf:"bytes,3,opt,name=KzgCommitment,proto3" json:"KzgCommitment,omitempty"`
	KzgProof      []byte `protobuf:"bytes,4,opt,name=KzgProof,proto3" json:"KzgProof,omitempty"`
	BlockNumber   uint64 `protobuf:"varint,5,opt,name=BlockNumber,proto3" json:"BlockNumber,omitempty"`
	BlockHash     []byte `protobuf:"bytes,6,opt,name=BlockHash,proto3" json:"BlockHash,omitempty"`
	TxHash        []byte `protobuf:"bytes,7,opt,name=TxHash,proto3" json:"TxHash,omitempty"`
}

func (x *BlobSidecar) Reset() {
	*x = BlobSidecar{}
	// Reuse H128's message type info (index 0) for proto reflection.
	// This is safe because we only use SSZ encoding on the wire.
	mi := &file_types_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *BlobSidecar) String() string { return "BlobSidecar" }
func (x *BlobSidecar) ProtoMessage()  {}

func (x *BlobSidecar) ProtoReflect() protoreflect.Message {
	mi := &file_types_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// --- SSZ encoding ---

var (
	errBlobSSZShort    = errors.New("blob_sidecar_ssz: buffer too short")
	errBlobSSZOverflow = errors.New("blob_sidecar_ssz: length overflow")
)

func blobPutUint64(dst []byte, v uint64) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, v)
	return append(dst, b...)
}

func blobPutBytes(dst, data []byte) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, uint32(len(data)))
	dst = append(dst, b...)
	return append(dst, data...)
}

func blobReadUint64(buf []byte, offset int) (uint64, int, error) {
	if offset+8 > len(buf) {
		return 0, offset, errBlobSSZShort
	}
	return binary.LittleEndian.Uint64(buf[offset : offset+8]), offset + 8, nil
}

func blobReadBytes(buf []byte, offset int) ([]byte, int, error) {
	if offset+4 > len(buf) {
		return nil, offset, errBlobSSZShort
	}
	n := binary.LittleEndian.Uint32(buf[offset : offset+4])
	offset += 4
	if n > 2*1024*1024 {
		return nil, offset, errBlobSSZOverflow
	}
	end := offset + int(n)
	if end > len(buf) || end < offset {
		return nil, offset, errBlobSSZShort
	}
	data := make([]byte, n)
	copy(data, buf[offset:end])
	return data, end, nil
}

func blobSizeBytes(data []byte) int { return 4 + len(data) }

func (x *BlobSidecar) MarshalSSZ() ([]byte, error) {
	buf := make([]byte, 0, x.SizeSSZ())
	return x.MarshalSSZTo(buf)
}

func (x *BlobSidecar) MarshalSSZTo(buf []byte) ([]byte, error) {
	dst := buf
	dst = blobPutUint64(dst, x.Index)
	dst = blobPutBytes(dst, x.Blob)
	dst = blobPutBytes(dst, x.KzgCommitment)
	dst = blobPutBytes(dst, x.KzgProof)
	dst = blobPutUint64(dst, x.BlockNumber)
	dst = blobPutBytes(dst, x.BlockHash)
	dst = blobPutBytes(dst, x.TxHash)
	return dst, nil
}

func (x *BlobSidecar) UnmarshalSSZ(buf []byte) error {
	var err error
	off := 0
	if x.Index, off, err = blobReadUint64(buf, off); err != nil {
		return err
	}
	if x.Blob, off, err = blobReadBytes(buf, off); err != nil {
		return err
	}
	if x.KzgCommitment, off, err = blobReadBytes(buf, off); err != nil {
		return err
	}
	if x.KzgProof, off, err = blobReadBytes(buf, off); err != nil {
		return err
	}
	if x.BlockNumber, off, err = blobReadUint64(buf, off); err != nil {
		return err
	}
	if x.BlockHash, off, err = blobReadBytes(buf, off); err != nil {
		return err
	}
	if x.TxHash, _, err = blobReadBytes(buf, off); err != nil {
		return err
	}
	return nil
}

func (x *BlobSidecar) SizeSSZ() int {
	return 8 + blobSizeBytes(x.Blob) + blobSizeBytes(x.KzgCommitment) + blobSizeBytes(x.KzgProof) +
		8 + blobSizeBytes(x.BlockHash) + blobSizeBytes(x.TxHash)
}
