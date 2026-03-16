package rawdb

import (
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

type txLookupPutterStub struct {
	puts   int
	values [][]byte
}

func (s *txLookupPutterStub) Put(_ string, _, value []byte) error {
	s.puts++
	s.values = append(s.values, append([]byte(nil), value...))
	return nil
}

func TestWriteTxLookupEntriesSkipsNilBlockNumber(t *testing.T) {
	putter := &txLookupPutterStub{}
	blk := block.NewBlock(&block.Header{
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, nil).(*block.Block)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("WriteTxLookupEntries() panicked: %v", r)
		}
	}()

	WriteTxLookupEntries(putter, blk)

	if putter.puts != 0 {
		t.Fatalf("Put() calls = %d, want 0", putter.puts)
	}
}

func TestWriteTxLookupEntriesUsesBlockNumberBytes(t *testing.T) {
	putter := &txLookupPutterStub{}
	from := types.HexToAddress("0x1")
	to := types.HexToAddress("0x2")
	blk := block.NewBlock(&block.Header{
		Number:     uint256.NewInt(7),
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	}, []*transaction.Transaction{
		transaction.NewTransaction(0, from, &to, uint256.NewInt(1), 21000, uint256.NewInt(1), nil),
		transaction.NewTransaction(1, from, &to, uint256.NewInt(2), 21000, uint256.NewInt(1), nil),
	}).(*block.Block)

	WriteTxLookupEntries(putter, blk)

	if putter.puts != 2 {
		t.Fatalf("Put() calls = %d, want 2", putter.puts)
	}
	for i, value := range putter.values {
		if got := new(uint256.Int).SetBytes(value).Uint64(); got != 7 {
			t.Fatalf("entry %d block number = %d, want 7", i, got)
		}
	}
}
