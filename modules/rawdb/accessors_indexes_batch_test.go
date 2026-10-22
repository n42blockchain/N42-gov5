package rawdb

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"sort"
	"testing"

	"github.com/holiman/uint256"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/modules"
)

type lookupBatchPutter struct {
	handled   bool
	failure   error
	calls     int
	pointKeys [][]byte
	flat      []byte
	value     []byte
	owner     kv.Putter
}

func (p *lookupBatchPutter) Put(_ string, key, value []byte) error {
	if p.failure != nil {
		_, _ = os.Stdout.WriteString("unexpected point fallback\n")
	}
	p.pointKeys = append(p.pointKeys, append([]byte(nil), key...))
	return nil
}
func (p *lookupBatchPutter) TryPutSameValueMany(owner kv.Putter, table string, keys []byte, size int, value []byte) (bool, error) {
	if table != modules.TxLookup || size != 32 {
		panic("wrong batch table or key width")
	}
	p.calls++
	p.owner = owner
	p.flat = append([]byte(nil), keys...)
	p.value = append([]byte(nil), value...)
	return p.handled, p.failure
}
func lookupBatchBlock() *block.Block {
	from, to := types.HexToAddress("0x1"), types.HexToAddress("0x2")
	txs := make([]*transaction.Transaction, 4)
	for i := range txs {
		txs[i] = transaction.NewTransaction(uint64(i), from, &to, uint256.NewInt(uint64(i+1)), 21000, uint256.NewInt(1), nil)
	}
	return block.NewBlock(&block.Header{Number: uint256.NewInt(700), Difficulty: uint256.NewInt(1), BaseFee: uint256.NewInt(0)}, txs).(*block.Block)
}
func TestWriteTxLookupEntriesBatchSelectionAndFallback(t *testing.T) {
	b := lookupBatchBlock()
	expected := make([][]byte, len(b.Transactions()))
	for i, tx := range b.Transactions() {
		h := tx.Hash()
		expected[i] = append([]byte(nil), h[:]...)
	}
	sort.Slice(expected, func(i, j int) bool { return bytes.Compare(expected[i], expected[j]) < 0 })
	for _, mode := range []string{"legacy", "native", "declined"} {
		t.Run(mode, func(t *testing.T) {
			p := &lookupBatchPutter{handled: mode == "native"}
			used := false
			if mode == "legacy" {
				WriteTxLookupEntries(p, b)
			} else {
				used = WriteTxLookupEntriesBatch(p, b)
			}
			if used != (mode == "native") {
				t.Fatalf("batch selection %v", used)
			}
			if mode == "legacy" {
				if p.calls != 0 {
					t.Fatal("legacy writer attempted batch")
				}
			} else {
				if p.calls != 1 || p.owner != p || !bytes.Equal(p.flat, bytes.Join(expected, nil)) || !bytes.Equal(p.value, []byte{2, 188}) {
					t.Fatal("batch did not bind complete sorted keys/value/owner")
				}
			}
			if mode == "native" {
				if len(p.pointKeys) != 0 {
					t.Fatal("completed batch replayed point puts")
				}
			} else {
				if len(p.pointKeys) != len(expected) {
					t.Fatal("fallback did not write every row")
				}
				for i := range expected {
					if !bytes.Equal(expected[i], p.pointKeys[i]) {
						t.Fatal("fallback row order changed")
					}
				}
			}
		})
	}
}
func TestWriteTxLookupEntriesBatchErrorNeverFallsBack(t *testing.T) {
	if mode := os.Getenv("N42_TEST_LOOKUP_BATCH_FATAL"); mode != "" {
		p := &lookupBatchPutter{handled: mode == "selected", failure: errors.New("injected batch error")}
		WriteTxLookupEntriesBatch(p, lookupBatchBlock())
		os.Exit(9)
	}
	for _, mode := range []string{"selected", "declined-with-error"} {
		c := exec.Command(os.Args[0], "-test.run=^TestWriteTxLookupEntriesBatchErrorNeverFallsBack$")
		c.Env = append(os.Environ(), "N42_TEST_LOOKUP_BATCH_FATAL="+mode)
		out, err := c.CombinedOutput()
		e, ok := err.(*exec.ExitError)
		if !ok || e.ExitCode() != 1 || !bytes.Contains(out, []byte("injected batch error")) || bytes.Contains(out, []byte("unexpected point fallback")) {
			t.Fatalf("batch error was replayed/masked: %s: %v %s", mode, err, out)
		}
	}
}
