package ancientera

import (
	"bytes"
	"testing"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
)

// The derived rows must be byte-identical to the compact per-tx log encoding
// the retired log table held, for both receipt blob formats.
func TestLogsFromReceiptBlob(t *testing.T) {
	rs := block.Receipts{
		{Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: 21000},
		{Status: block.ReceiptStatusSuccessful, CumulativeGasUsed: 42000, Logs: []*block.Log{
			{Address: types.Address{0x01}, Topics: []types.Hash{{0x0a}, {0x0b}}, Data: []byte{1, 2, 3}},
			{Address: types.Address{0x02}},
		}},
	}
	want := block.Logs(rs[1].Logs)
	wantBytes := want.MarshalCompact()

	proto, err := rs.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for name, blob := range map[string][]byte{"compact": rs.MarshalCompact(), "proto": proto} {
		got, err := LogsFromReceiptBlob(blob)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != 1 || got[0].TxID != 1 || !bytes.Equal(got[0].Data, wantBytes) {
			t.Fatalf("%s: derived rows mismatch: %+v", name, got)
		}
	}
	if got, err := LogsFromReceiptBlob(nil); err != nil || got != nil {
		t.Fatalf("empty blob: %v %v", got, err)
	}
}
