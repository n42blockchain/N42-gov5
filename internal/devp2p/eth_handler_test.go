package devp2p

import (
	"hash/crc32"
	"math/big"
	"testing"

	"github.com/holiman/uint256"

	n42block "github.com/n42blockchain/N42/common/block"
	n42types "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

func TestNewEthHandlerUsesSuppliedGenesisHashForForkID(t *testing.T) {
	t.Parallel()

	genesisHash := n42types.HexToHash("0x34cb47b1a70a73ad1e455e97f33827d94284f5e7b819f4132e466cf3cd0a0d56")
	handler, err := NewEthHandler(&params.ChainConfig{ChainID: big.NewInt(7)}, genesisHash, 0, nil)
	if err != nil {
		t.Fatalf("NewEthHandler() error = %v", err)
	}

	head := &n42block.Header{
		Number: uint256.NewInt(0),
		Time:   0,
	}
	want := checksumToBytes(crc32.ChecksumIEEE(genesisHash.Bytes()))
	if got := handler.currentForkID(head); got.Hash != want || got.Next != 0 {
		t.Fatalf("currentForkID() = (hash=%#x next=%d), want (hash=%#x next=0)", got.Hash, got.Next, want)
	}
	if handler.genesis != genesisHash {
		t.Fatalf("handler genesis = %s, want %s", handler.genesis, genesisHash)
	}
}
