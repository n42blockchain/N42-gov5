package internal

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/vm"
)

func TestCollectDepositExecutionRequestsIgnoresNonDepositLogs(t *testing.T) {
	t.Parallel()

	transferSig := types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	receipts := block.Receipts{
		&block.Receipt{
			Logs: []*block.Log{
				{
					Address: vm.DepositContractAddress,
					Topics:  []types.Hash{transferSig},
					Data:    make([]byte, 32),
				},
			},
		},
	}

	requests, err := CollectDepositExecutionRequests(receipts)
	require.NoError(t, err)
	require.Nil(t, requests)
}

func TestCollectDepositExecutionRequestsExtractsDepositEventsAmongExtraLogs(t *testing.T) {
	t.Parallel()

	transferSig := types.HexToHash("0xddf252ad1be2c89b69c2b068fc378daa952ba7f163c4a11628f55a4df523b3ef")
	depositLogData := makeValidDepositLogData()
	expectedDeposit, err := vm.ParseDepositLog([]types.Hash{vm.DepositEventSignature}, depositLogData)
	require.NoError(t, err)

	receipts := block.Receipts{
		&block.Receipt{
			Logs: []*block.Log{
				{
					Address: vm.DepositContractAddress,
					Topics:  []types.Hash{transferSig},
					Data:    make([]byte, 32),
				},
				{
					Address: vm.DepositContractAddress,
					Topics:  []types.Hash{vm.DepositEventSignature},
					Data:    depositLogData,
				},
			},
		},
	}

	requests, err := CollectDepositExecutionRequests(receipts)
	require.NoError(t, err)
	require.Len(t, requests, 1)
	require.Len(t, requests[0], 1+vm.DepositRequestSize)
	require.Equal(t, byte(vm.DepositRequestType), requests[0][0])
	require.Equal(t, expectedDeposit.Serialize(), []byte(requests[0][1:]))
}

func makeValidDepositLogData() []byte {
	data := make([]byte, 832)
	offset := 160

	binary.BigEndian.PutUint64(data[offset+24:offset+32], 48)
	offset += 32
	copy(data[offset:offset+48], bytes.Repeat([]byte{0xaa}, 48))
	offset += 48
	offset = ((offset + 31) / 32) * 32

	binary.BigEndian.PutUint64(data[offset+24:offset+32], 32)
	offset += 32
	copy(data[offset:offset+32], bytes.Repeat([]byte{0xbb}, 32))
	offset += 32

	binary.BigEndian.PutUint64(data[offset+24:offset+32], 8)
	offset += 32
	amount := uint64(32000000000)
	for i := 0; i < 8; i++ {
		data[offset+i] = byte(amount >> (8 * i))
	}
	offset += 8
	offset = ((offset + 31) / 32) * 32

	binary.BigEndian.PutUint64(data[offset+24:offset+32], 96)
	offset += 32
	copy(data[offset:offset+96], bytes.Repeat([]byte{0xcc}, 96))
	offset += 96
	offset = ((offset + 31) / 32) * 32

	binary.BigEndian.PutUint64(data[offset+24:offset+32], 8)
	offset += 32
	index := uint64(12345)
	for i := 0; i < 8; i++ {
		data[offset+i] = byte(index >> (8 * i))
	}

	return data
}
