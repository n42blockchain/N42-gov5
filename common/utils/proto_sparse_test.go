package utils

import (
	"encoding/binary"
	"testing"

	"github.com/n42blockchain/N42/proto/types_pb"
)

func TestSparseProtoNumbers(t *testing.T) {
	cases := []*types_pb.H256{nil, {}, {Hi: &types_pb.H128{Hi: 1}}, {Lo: &types_pb.H128{Lo: 2}}}
	for _, p := range cases {
		hash := ConvertH256ToHash(p)
		value := ConvertH256ToUint256Int(p).Bytes32()
		if hash != value {
			t.Fatal("inconsistent sparse number conversion")
		}
	}
	p := &types_pb.H256{Hi: &types_pb.H128{Hi: 1}}
	if h := ConvertH256ToHash(p); binary.BigEndian.Uint64(h[:8]) != 1 {
		t.Fatal("lost populated limb")
	}
	for _, p := range []*types_pb.H160{nil, {}, {Lo: 1}} {
		ConvertH160toAddress(p)
	}
	for _, p := range []*types_pb.H512{nil, {}, {Hi: &types_pb.H256{}}, {Lo: &types_pb.H256{Lo: &types_pb.H128{Lo: 1}}}} {
		ConvertH512ToHash(p)
	}
	for _, p := range []*types_pb.H384{nil, {}, {Hi: &types_pb.H256{}}, {Lo: &types_pb.H128{Lo: 1}}} {
		ConvertH384ToPublicKey(p)
	}
	for _, p := range []*types_pb.H768{nil, {}, {Hi: &types_pb.H384{Hi: &types_pb.H256{}}}, {Lo: &types_pb.H384{Lo: &types_pb.H128{Lo: 1}}}} {
		ConvertH768ToSignature(p)
	}
}
