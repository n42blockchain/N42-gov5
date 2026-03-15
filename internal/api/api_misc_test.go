package api

import (
	"context"
	"testing"

	"github.com/n42blockchain/N42/modules/rpc/jsonrpc"
)

func TestFeeHistoryRejectsMissingOracle(t *testing.T) {
	service := NewN42API(&API{})

	_, err := service.FeeHistory(context.Background(), jsonrpc.DecimalOrHex(1), jsonrpc.LatestBlockNumber, nil)
	if err == nil || err.Error() != "gas price oracle is unavailable" {
		t.Fatalf("FeeHistory() error = %v", err)
	}
}
