package ethel

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/n42blockchain/N42/common/account"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/kv/mdbx"
	log2 "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/modules/rawdb/freezer"
	"github.com/n42blockchain/N42/modules/state"
	"github.com/n42blockchain/N42/params"
)

// witnessReplayReader replays a witness stream, providing pre-recorded
// state values. Code is read from MDBX (witness excludes code).
type witnessReplayReader struct {
	stream []byte
	pos    int
	codeTx kv.Tx // for ReadAccountCode from MDBX
}

func (r *witnessReplayReader) ReadAccountData(address types.Address) (*account.StateAccount, error) {
	if r.pos >= len(r.stream) {
		return nil, nil
	}
	length := int(r.stream[r.pos])
	r.pos++
	if length == 0 {
		return nil, nil // absent
	}
	acc := &account.StateAccount{}
	if err := acc.DecodeForStorage(r.stream[r.pos : r.pos+length]); err != nil {
		return nil, err
	}
	r.pos += length
	return acc, nil
}

func (r *witnessReplayReader) ReadAccountStorage(address types.Address, incarnation uint16, key *types.Hash) ([]byte, error) {
	if r.pos >= len(r.stream) {
		return nil, nil
	}
	length := int(r.stream[r.pos])
	r.pos++
	if length == 0 {
		return nil, nil
	}
	val := make([]byte, length)
	copy(val, r.stream[r.pos:r.pos+length])
	r.pos += length
	return val, nil
}

func (r *witnessReplayReader) ReadAccountCode(address types.Address, incarnation uint16, codeHash types.Hash) ([]byte, error) {
	// Code from MDBX — witness doesn't record it.
	if r.codeTx == nil {
		return nil, nil
	}
	return r.codeTx.GetOne("Code", codeHash[:])
}

func (r *witnessReplayReader) ReadAccountCodeSize(address types.Address, incarnation uint16, codeHash types.Hash) (int, error) {
	code, err := r.ReadAccountCode(address, incarnation, codeHash)
	return len(code), err
}

func (r *witnessReplayReader) ReadAccountIncarnation(address types.Address) (uint16, error) {
	return 0, nil
}

// TestWitnessVerify re-executes blocks using the witness stream + MDBX code,
// and verifies the gas used matches the header.
func TestWitnessVerify(t *testing.T) {
	ancientPath := `e:\geth\geth\chaindata\ancient\chain`
	datadir := `d:\N42\verify_test`
	if _, err := os.Stat(ancientPath); err != nil {
		t.Skip("Geth ancient not found")
	}
	if _, err := os.Stat(datadir); err != nil {
		t.Skip("verify_test data not found")
	}

	// Open Geth freezer (headers + bodies).
	inputF, err := freezer.New(ancientPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer inputF.Close()

	// Open output freezer (witness data).
	outF, err := freezer.New(filepath.Join(datadir, "ancient"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer outF.Close()

	witnessTbl := outF.Table("block_witness")
	if witnessTbl == nil {
		t.Fatal("block_witness table not found")
	}
	witnessTbl.ForceBatchSize(freezer.BatchSize)

	// Open MDBX (has code from execution).
	logger := log2.New()
	db, err := mdbx.NewMDBX(logger).
		Path(datadir).
		Label(kv.ChainDB).
		DBVerbosity(kv.DBVerbosityLvl(2)).
		Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	codeTx, err := db.BeginRo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer codeTx.Rollback()

	chainCfg := params.EthereumMainnetChainConfig
	engine := NewEthReplayEngine(chainCfg)

	// Test blocks with transactions.
	// 46147: first ETH tx, 46402: second tx, 911999-912000: DAO-era complex blocks
	testBlocks := []uint64{46147, 46400, 46402, 47287, 48000, 49018, 50111, 53000, 100009, 200006, 500003, 911999}

	for _, blockNum := range testBlocks {
		if blockNum >= witnessTbl.Items() {
			t.Logf("block %d: skipped (beyond witness data)", blockNum)
			continue
		}

		// Read witness.
		witnessData, err := witnessTbl.Retrieve(blockNum)
		if err != nil {
			t.Fatalf("block %d: read witness: %v", blockNum, err)
		}
		if len(witnessData) == 0 {
			t.Logf("block %d: empty witness (no state access)", blockNum)
			continue
		}

		// Read header + body.
		headerData, err := inputF.Ancient(freezer.TableHeaders, blockNum)
		if err != nil {
			t.Fatalf("block %d: read header: %v", blockNum, err)
		}
		header, err := DecodeGethHeader(headerData)
		if err != nil {
			t.Fatalf("block %d: decode header: %v", blockNum, err)
		}

		bodyData, err := inputF.Ancient(freezer.TableBodies, blockNum)
		if err != nil {
			t.Fatalf("block %d: read body: %v", blockNum, err)
		}
		body, err := DecodeGethBody(bodyData)
		if err != nil {
			t.Fatalf("block %d: decode body: %v", blockNum, err)
		}

		if len(body.Transactions) == 0 {
			t.Logf("block %d: no transactions, skip", blockNum)
			continue
		}

		// Re-execute using witness stream + MDBX code.
		reader := &witnessReplayReader{stream: witnessData, codeTx: codeTx}
		ibs := state.New(reader)

		// Convert []*block.Header to []block.IHeader for ProcessBlock.
		uncles := make([]block.IHeader, len(body.Uncles))
		for i, u := range body.Uncles {
			uncles[i] = u
		}
		result, err := ProcessBlock(chainCfg, engine, header, body.Transactions, uncles, ibs, nil, nil)
		if err != nil {
			t.Errorf("block %d: execute: %v (witness may be incomplete for this block)", blockNum, err)
			continue
		}

		if result.GasUsed == header.GasUsed {
			t.Logf("block %d: witness OK (gas=%d, txs=%d, witnessLen=%d)",
				blockNum, result.GasUsed, len(body.Transactions), len(witnessData))
		} else {
			t.Errorf("block %d: gas mismatch: got %d, want %d",
				blockNum, result.GasUsed, header.GasUsed)
		}
	}
}
