package node

import (
	"testing"

	"github.com/n42blockchain/N42/common"
)

type ingestStatsPool struct{ common.ITxsPool }

func (ingestStatsPool) Stats() (int, int, int, int) { return 7, 700, 3, 30 }

func TestIngestAdapterCountsTransactionsForBackpressure(t *testing.T) {
	adapter := &ingestPoolAdapter{pool: ingestStatsPool{}}
	pending, pendingAddresses, queued, queuedAddresses := adapter.Stats()
	if pending != 700 || pendingAddresses != 7 || queued != 30 || queuedAddresses != 3 {
		t.Fatalf("wrong ingest counts: %d/%d/%d/%d", pending, pendingAddresses, queued, queuedAddresses)
	}
}
