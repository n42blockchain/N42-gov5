// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

// n42-qmdb-export reads an existing replay-v2 QMDB database without modifying
// it and emits a cross-client portable snapshot for Rust observer bootstrap.
package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/c2h5oh/datasize"
	"github.com/n42blockchain/N42/internal/ethel"
	"github.com/n42blockchain/N42/lib/kv"
	mdbxkv "github.com/n42blockchain/N42/lib/kv/mdbx"
	log "github.com/n42blockchain/N42/lib/log/v3"
	"github.com/n42blockchain/N42/lib/qmdb"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules"
	"github.com/n42blockchain/N42/modules/rawdb"
	"github.com/n42blockchain/N42/modules/state/commitment"
)

func main() {
	dbPath := flag.String("db", "", "replay-v2 chaindata directory with QMDB history")
	outputPath := flag.String("out", "", "portable snapshot output file")
	mapGB := flag.Int("map.gb", 512, "MDBX map size in GiB")
	rangeOutputPath := flag.String("range-out", "", "optional finalized-range v1 output file")
	rangeFrom := flag.Uint64("range-from", 0, "first block in --range-out (default: bounded tail ending at checkpoint)")
	rangeTo := flag.Uint64("range-to", 0, "last block in --range-out (default: canonical head/checkpoint)")
	flag.Parse()
	if *dbPath == "" || (*outputPath == "" && *rangeOutputPath == "") {
		fatalf("usage: n42-qmdb-export --db <chaindata> [--out <snapshot>] [--range-out <bundle>]")
	}

	modules.N42Init()
	kv.ChaindataTablesCfg = modules.N42TableCfg
	db, err := mdbxkv.NewMDBX(log.New()).Path(*dbPath).Label(kv.ChainDB).
		MapSize(datasize.ByteSize(*mapGB) * datasize.GB).Accede().Readonly().
		Open(context.Background())
	if err != nil {
		fatalf("open replay database: %v", err)
	}
	defer db.Close()
	tx, err := db.BeginRo(context.Background())
	if err != nil {
		fatalf("begin read-only transaction: %v", err)
	}
	defer tx.Rollback()

	genesisHash, err := rawdb.ReadCanonicalHash(tx, 0)
	if err != nil || genesisHash == ([32]byte{}) {
		fatalf("read genesis hash: %v", err)
	}
	chainConfig, err := rawdb.ReadChainConfig(tx, genesisHash)
	if err != nil || chainConfig == nil || chainConfig.ChainID == nil || !chainConfig.ChainID.IsUint64() {
		fatalf("read uint64 chain id: %v", err)
	}
	if *outputPath == "" {
		to := *rangeTo
		if to == 0 {
			headHash := rawdb.ReadHeadBlockHash(tx)
			headNumber := rawdb.ReadHeaderNumber(tx, headHash)
			if headNumber == nil {
				fatalf("canonical head is unavailable")
			}
			to = *headNumber
		}
		from := *rangeFrom
		if from == 0 && to >= qmdb.MaxFinalizedRangeBlocks {
			from = to - qmdb.MaxFinalizedRangeBlocks + 1
		}
		if err := exportFinalizedRange(tx, chainConfig.ChainID.Uint64(), genesisHash, from, to, *rangeOutputPath); err != nil {
			fatalf("export finalized range: %v", err)
		}
		return
	}

	blockNumber, blockHash, ok, err := rawdb.ReadQMDBApplied(tx)
	if err != nil {
		fatalf("read QMDB applied marker: %v", err)
	}
	if !ok {
		blockHash = rawdb.ReadHeadBlockHash(tx)
		headerNumber := rawdb.ReadHeaderNumber(tx, blockHash)
		if headerNumber == nil {
			fatalf("database has neither a QMDB applied marker nor a readable head")
		}
		blockNumber = *headerNumber
	}
	header := rawdb.ReadHeader(tx, blockHash, blockNumber)
	if header == nil {
		fatalf("checkpoint header %d/%x is missing", blockNumber, blockHash)
	}

	computer := commitment.NewQMDBRootComputer()
	if err := computer.LoadFrom(tx); err != nil {
		fatalf("load QMDB forest: %v", err)
	}
	computer.SetCold(tx)
	root := computer.Tree().Root()
	if root != qmdb.Hash(header.Root) {
		fatalf("QMDB root %x does not match checkpoint header root %x", root, header.Root)
	}
	metadata := qmdb.PortableSnapshotMetadata{
		ChainID:     chainConfig.ChainID.Uint64(),
		GenesisHash: qmdb.Hash(genesisHash),
		BlockNumber: blockNumber,
		BlockHash:   qmdb.Hash(blockHash),
		Root:        root,
		NextSlot:    computer.Tree().NextSlot(),
	}
	cursor, err := tx.Cursor(qmdb.EntryTable)
	if err != nil {
		fatalf("open QMDB entry cursor: %v", err)
	}
	defer cursor.Close()
	key, value, cursorErr := cursor.First()
	source := func(slot uint64) (qmdb.SlotEntry, error) {
		if cursorErr != nil {
			return qmdb.SlotEntry{}, cursorErr
		}
		if key == nil {
			return qmdb.SlotEntry{}, fmt.Errorf("QMDB entry row for slot %d is missing; replay must retain full --qmdb-history", slot)
		}
		if len(key) != 8 || binary.BigEndian.Uint64(key) != slot {
			return qmdb.SlotEntry{}, fmt.Errorf("QMDB entry log is not contiguous at slot %d (key=%x); replay must retain full --qmdb-history", slot, key)
		}
		if len(value) < 32 {
			return qmdb.SlotEntry{}, fmt.Errorf("QMDB entry row %d is truncated", slot)
		}
		active, ok := computer.Tree().SlotActive(slot)
		if !ok {
			return qmdb.SlotEntry{}, fmt.Errorf("QMDB tree has no liveness bit for slot %d", slot)
		}
		entry := qmdb.SlotEntry{Slot: slot, Active: active}
		copy(entry.KeyHash[:], value[:32])
		entry.Value = value[32:]
		key, value, cursorErr = cursor.Next()
		if slot > 0 && slot%1_000_000 == 0 {
			fmt.Fprintf(os.Stderr, "exported %d/%d slots\n", slot, metadata.NextSlot)
		}
		return entry, nil
	}
	written, err := writeAtomic(*outputPath, func(w io.Writer) (int64, error) {
		return qmdb.WritePortableSnapshot(w, metadata, source)
	})
	if err != nil {
		fatalf("encode portable snapshot: %v", err)
	}
	if key != nil {
		fatalf("QMDB entry table contains rows beyond next slot %d", metadata.NextSlot)
	}
	if cursorErr != nil {
		fatalf("advance QMDB entry cursor: %v", cursorErr)
	}
	if written <= 0 {
		fatalf("write portable snapshot: %v", err)
	}
	fmt.Printf("exported chain_id=%d genesis=%x block=%d hash=%x root=%x slots=%d live=%d bytes=%d to %s\n",
		metadata.ChainID, metadata.GenesisHash, blockNumber, blockHash, root, metadata.NextSlot, computer.Tree().LiveCount(), written, *outputPath)
	if *rangeOutputPath != "" {
		to := blockNumber
		if *rangeTo != 0 {
			to = *rangeTo
		}
		from := *rangeFrom
		if from == 0 && to >= qmdb.MaxFinalizedRangeBlocks {
			from = to - qmdb.MaxFinalizedRangeBlocks + 1
		}
		if err := exportFinalizedRange(tx, metadata.ChainID, genesisHash, from, to, *rangeOutputPath); err != nil {
			fatalf("export finalized range: %v", err)
		}
	}
}

func exportFinalizedRange(tx kv.Tx, chainID uint64, genesisHash [32]byte, from, to uint64, outputPath string) error {
	if from > to || to-from+1 > qmdb.MaxFinalizedRangeBlocks {
		return fmt.Errorf("range %d-%d is invalid or exceeds %d blocks", from, to, qmdb.MaxFinalizedRangeBlocks)
	}
	rangeData := &qmdb.FinalizedRange{
		ChainID: chainID, GenesisHash: qmdb.Hash(genesisHash), FromBlock: from, ToBlock: to,
		Entries: make([]qmdb.FinalizedRangeEntry, 0, to-from+1),
	}
	for number := from; number <= to; number++ {
		hash, err := rawdb.ReadCanonicalHash(tx, number)
		if err != nil || hash == ([32]byte{}) {
			return fmt.Errorf("read canonical hash at %d: %w", number, err)
		}
		header := rawdb.ReadHeader(tx, hash, number)
		blk := rawdb.ReadBlock(tx, hash, number)
		if header == nil || blk == nil {
			return fmt.Errorf("canonical block %d/%x is incomplete", number, hash)
		}
		headerRLP, err := rlp.EncodeToBytes(header)
		if err != nil {
			return fmt.Errorf("encode header %d: %w", number, err)
		}
		blockRLP, err := blk.Marshal()
		if err != nil {
			return fmt.Errorf("encode block %d: %w", number, err)
		}
		receipts := rawdb.ReadRawReceipts(tx, number)
		if receipts == nil {
			if len(blk.Transactions()) != 0 {
				return fmt.Errorf("transaction-bearing block %d has no receipts", number)
			}
		}
		receiptBytes := ethel.EncodeReceiptsCompact(receipts)
		rangeData.Entries = append(rangeData.Entries, qmdb.FinalizedRangeEntry{
			Number: number, BlockHash: qmdb.Hash(hash), ParentHash: qmdb.Hash(header.ParentHash),
			StateRoot: qmdb.Hash(header.Root), ReceiptsRoot: qmdb.Hash(header.ReceiptHash),
			TxRoot: qmdb.Hash(header.TxHash), HeaderRLP: headerRLP, BlockRLP: blockRLP, Receipts: receiptBytes,
		})
	}
	encoded, err := qmdb.MarshalFinalizedRange(rangeData)
	if err != nil {
		return err
	}
	_, err = writeAtomic(outputPath, func(w io.Writer) (int64, error) {
		n, writeErr := w.Write(encoded)
		return int64(n), writeErr
	})
	if err == nil {
		fmt.Printf("exported finalized range %d-%d blocks=%d bytes=%d to %s\n", from, to, len(rangeData.Entries), len(encoded), outputPath)
	}
	return err
}

func writeAtomic(path string, write func(io.Writer) (int64, error)) (int64, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".n42-qmdb-export-*")
	if err != nil {
		return 0, err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	buffered := bufio.NewWriterSize(tmp, 1<<20)
	written, err := write(buffered)
	if err != nil {
		return 0, err
	}
	if err := buffered.Flush(); err != nil {
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return 0, err
	}
	committed = true
	return written, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
