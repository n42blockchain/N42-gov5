package api

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/avmtypes"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/rlp"
)

func TestRPCMarshalHeaderIncludesElements22To24(t *testing.T) {
	slot := uint64(77)
	bal := types.Hash{0x22}
	mobile := types.Hash{0x24}
	header := &block.Header{Number: uint256.NewInt(77), SlotNumber: &slot, BlockAccessListHash: &bal, MobileRegistryRoot: &mobile}

	fields := RPCMarshalHeader(header, nil)
	if got, ok := fields["slotNumber"].(hexutil.Uint64); !ok || uint64(got) != slot {
		t.Fatalf("slotNumber = %#v, want %d", fields["slotNumber"], slot)
	}
	if got := fields["blockAccessListHash"]; got != avmtypes.FromastHash(bal) {
		t.Fatalf("blockAccessListHash = %#v", got)
	}
	if got := fields["mobileRegistryRoot"]; got != avmtypes.FromastHash(mobile) {
		t.Fatalf("mobileRegistryRoot = %#v", got)
	}

	plain := RPCMarshalHeader(&block.Header{Number: uint256.NewInt(1)}, nil)
	for _, k := range []string{"slotNumber", "blockAccessListHash", "mobileRegistryRoot"} {
		if _, ok := plain[k]; ok {
			t.Fatalf("%s must be omitted when the header does not carry it", k)
		}
	}
}

func TestRPCMarshalBlockSizeIsWholeBlockEncoding(t *testing.T) {
	slot := uint64(5)
	header := &block.Header{Number: uint256.NewInt(5), SlotNumber: &slot, GasLimit: 30_000_000}
	blk := block.NewBlock(header, nil)

	size, ok := rpcBlockSize(blk)
	if !ok {
		t.Fatal("rpcBlockSize failed")
	}
	enc, err := rlp.EncodeToBytes(blk.(*block.Block))
	if err != nil {
		t.Fatal(err)
	}
	got := hexutil.Uint64(size)
	if uint64(got) != uint64(len(enc)) {
		t.Fatalf("size = %d, want %d (whole-block encoding)", got, len(enc))
	}
}
