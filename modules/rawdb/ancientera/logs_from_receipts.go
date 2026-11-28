package ancientera

import (
	"fmt"

	"github.com/n42blockchain/N42/common/block"
)

// LogsFromReceiptBlob derives the per-transaction log rows of the era TblLogs
// table from a stored per-block receipt blob. Transactions without logs are
// skipped; TxID is the receipt index. Each row is the compact log encoding,
// byte-identical to what the retired per-transaction log table used to hold.
func LogsFromReceiptBlob(receiptsRaw []byte) ([]LogRec, error) {
	if len(receiptsRaw) == 0 {
		return nil, nil
	}
	var rs block.Receipts
	if err := rs.Unmarshal(receiptsRaw); err != nil {
		return nil, fmt.Errorf("decode receipts: %w", err)
	}
	var out []LogRec
	for i, r := range rs {
		if r == nil || len(r.Logs) == 0 {
			continue
		}
		logs := block.Logs(r.Logs)
		out = append(out, LogRec{TxID: uint32(i), Data: logs.MarshalCompact()})
	}
	return out, nil
}
