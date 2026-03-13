// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package rawdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/holiman/uint256"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	common2 "github.com/n42blockchain/N42/lib/common"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/modules"
)

// ReadCanonicalHash retrieves the hash assigned to a canonical block number.
func ReadCanonicalHash(db kv.Getter, number uint64) (types.Hash, error) {
	data, err := db.GetOne(modules.HeaderCanonical, modules.EncodeBlockNumber(number))
	if err != nil {
		return types.Hash{}, fmt.Errorf("failed ReadCanonicalHash: %w, number=%d", err, number)
	}
	if len(data) == 0 {
		return types.Hash{}, nil
	}
	return types.BytesToHash(data), nil
}

// WriteCanonicalHash stores the hash assigned to a canonical block number.
func WriteCanonicalHash(db kv.Putter, hash types.Hash, number uint64) error {
	if err := db.Put(modules.HeaderCanonical, modules.EncodeBlockNumber(number), hash.Bytes()); err != nil {
		return fmt.Errorf("failed to store number to hash mapping: %w", err)
	}
	return nil
}

// TruncateCanonicalHash removes all the number to hash canonical mapping from block number N
func TruncateCanonicalHash(tx kv.RwTx, blockFrom uint64, deleteHeaders bool) error {
	if err := tx.ForEach(modules.HeaderCanonical, modules.EncodeBlockNumber(blockFrom), func(k, v []byte) error {
		if deleteHeaders {
			deleteHeader(tx, types.BytesToHash(v), blockFrom)
		}
		return tx.Delete(modules.HeaderCanonical, k)
	}); err != nil {
		return fmt.Errorf("TruncateCanonicalHash: %w", err)
	}
	return nil
}

// IsCanonicalHash determines whether a header with the given hash is on the canonical chain.
func IsCanonicalHash(db kv.Getter, hash types.Hash) (bool, error) {
	number := ReadHeaderNumber(db, hash)
	if number == nil {
		return false, nil
	}
	canonicalHash, err := ReadCanonicalHash(db, *number)
	if err != nil {
		return false, err
	}
	return canonicalHash != (types.Hash{}) && canonicalHash == hash, nil
}

// ReadHeaderNumber returns the header number assigned to a hash.
func ReadHeaderNumber(db kv.Getter, hash types.Hash) *uint64 {
	data, err := db.GetOne(modules.HeaderNumber, hash.Bytes())
	if err != nil {
		log.Error("ReadHeaderNumber failed", "err", err)
	}
	if len(data) == 0 {
		return nil
	}
	if len(data) != 8 {
		log.Error("ReadHeaderNumber got wrong data len", "len", len(data))
		return nil
	}
	number := binary.BigEndian.Uint64(data)
	return &number
}

// WriteHeaderNumber stores the hash->number mapping.
func WriteHeaderNumber(db kv.Putter, hash types.Hash, number uint64) error {
	return db.Put(modules.HeaderNumber, hash[:], modules.EncodeBlockNumber(number))
}

// DeleteHeaderNumber removes hash->number mapping.
func DeleteHeaderNumber(db kv.Deleter, hash types.Hash) {
	if err := db.Delete(modules.HeaderNumber, hash[:]); err != nil {
		log.Crit("Failed to delete hash mapping", "err", err)
	}
}

// ReadHeaderRAW retrieves a block header in its raw database encoding.
func ReadHeaderRAW(db kv.Getter, hash types.Hash, number uint64) []byte {
	data, err := db.GetOne(modules.Headers, modules.HeaderKey(number, hash))
	if err != nil {
		log.Error("ReadHeaderRAW failed", "err", err)
	}
	return data
}

// HasHeader verifies the existence of a block header corresponding to the hash.
func HasHeader(db kv.Has, hash types.Hash, number uint64) bool {
	has, err := db.Has(modules.Headers, modules.HeaderKey(number, hash))
	return has && err == nil
}

// ReadHeader retrieves the block header corresponding to the hash.
func ReadHeader(db kv.Getter, hash types.Hash, number uint64) *block.Header {
	data := ReadHeaderRAW(db, hash, number)
	if len(data) == 0 {
		return nil
	}

	header := new(block.Header)
	pbHeader := new(types_pb.Header)

	if err := proto.Unmarshal(data, pbHeader); err != nil {
		log.Error("Invalid block header RAW", "hash", hash, "err", err)
		return nil
	}

	if err := header.FromProtoMessage(pbHeader); err != nil {
		log.Error("header FromProtoMessage failed", "err", err)
		return nil
	}
	return header
}

func ReadHeadersByNumber(db kv.Tx, number uint64) ([]*block.Header, error) {
	var res []*block.Header
	c, err := db.Cursor(modules.Headers)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	prefix := modules.EncodeBlockNumber(number)
	for k, v, err := c.Seek(prefix); k != nil; k, v, err = c.Next() {
		if err != nil {
			return nil, err
		}
		if !bytes.HasPrefix(k, prefix) {
			break
		}

		header := new(block.Header)
		pbHeader := new(types_pb.Header)
		if err := proto.Unmarshal(v, pbHeader); err != nil {
			return nil, fmt.Errorf("invalid block header RAW: hash=%x, err=%w", k[8:], err)
		}
		if err := header.FromProtoMessage(pbHeader); err != nil {
			return nil, fmt.Errorf("invalid block pbHeader: hash=%x, err =%w", k[8:], err)
		}
		res = append(res, header)
	}
	return res, nil
}

// WriteHeader stores a block header into the database and also stores the hash-
// to-number mapping.
func WriteHeader(db kv.Putter, header *block.Header) {
	var (
		hash   = header.Hash()
		number = header.Number.Uint64()
	)

	if err := WriteHeaderNumber(db, hash, number); err != nil {
		log.Crit("Failed to store hash to number mapping", "err", err)
	}

	// Write the encoded header
	data, err := header.Marshal()
	if err != nil {
		log.Crit("failed to Marshal header", "err", err)
	}
	if err := db.Put(modules.Headers, modules.HeaderKey(number, hash), data); err != nil {
		log.Crit("Failed to store header", "err", err)
	}
}

// deleteHeader - dangerous, use DeleteAncientBlocks/TruncateBlocks methods
func deleteHeader(db kv.Deleter, hash types.Hash, number uint64) {
	if err := db.Delete(modules.Headers, modules.HeaderKey(number, hash)); err != nil {
		log.Crit("Failed to delete header", "err", err)
	}
	if err := db.Delete(modules.HeaderNumber, hash.Bytes()); err != nil {
		log.Crit("Failed to delete hash to number mapping", "err", err)
	}
}

// ReadBodyRAW retrieves the block body (transactions and uncles) in encoding.
// Returns nil only when the body does not exist; panics on encoding errors
// to prevent silent data corruption (e.g. during freezer operations).
func ReadBodyRAW(db kv.Tx, hash types.Hash, number uint64) []byte {
	body := ReadCanonicalBodyWithTransactions(db, hash, number)
	if body == nil {
		return nil
	}
	pbBody := body.ToProtoMessage()

	bodyRaw, err := proto.Marshal(pbBody)
	if err != nil {
		log.Crit("ReadBodyRAW: failed to marshal block body", "number", number, "hash", hash, "err", err)
	}
	return bodyRaw
}

func ReadStorageBodyRAW(db kv.Getter, hash types.Hash, number uint64) []byte {
	bodyRaw, err := db.GetOne(modules.BlockBody, modules.BlockBodyKey(number, hash))
	if err != nil {
		log.Error("ReadStorageBodyRAW failed", "number", number, "hash", hash, "err", err)
	}
	return bodyRaw
}

func ReadStorageBody(db kv.Getter, hash types.Hash, number uint64) (block.BodyForStorage, error) {
	bodyRaw, err := db.GetOne(modules.BlockBody, modules.BlockBodyKey(number, hash))
	if err != nil {
		log.Error("ReadStorageBody failed", "err", err)
	}
	if len(bodyRaw) != 8+4 {
		return block.BodyForStorage{}, fmt.Errorf("invalid body raw length: expected 12, got %d", len(bodyRaw))
	}
	return block.BodyForStorage{
		BaseTxId: binary.BigEndian.Uint64(bodyRaw[:8]),
		TxAmount: binary.BigEndian.Uint32(bodyRaw[8:]),
	}, nil
}

func CanonicalTxnByID(db kv.Getter, id uint64) (*transaction.Transaction, error) {
	txIdKey := make([]byte, 8)
	binary.BigEndian.PutUint64(txIdKey, id)
	v, err := db.GetOne(modules.BlockTx, txIdKey)
	if err != nil {
		return nil, err
	}

	tx := new(transaction.Transaction)
	if err := tx.Unmarshal(v); err != nil {
		return nil, err
	}

	return tx, nil
}

func CanonicalTransactions(db kv.Getter, baseTxId uint64, amount uint32) ([]*transaction.Transaction, error) {
	if amount == 0 {
		return []*transaction.Transaction{}, nil
	}
	txIdKey := make([]byte, 8)
	txs := make([]*transaction.Transaction, amount)
	binary.BigEndian.PutUint64(txIdKey, baseTxId)
	i := uint32(0)

	if err := db.ForAmount(modules.BlockTx, txIdKey, amount, func(k, v []byte) error {
		var decodeErr error
		tx := new(transaction.Transaction)
		if decodeErr = tx.Unmarshal(v); nil != decodeErr {
			return decodeErr
		}
		txs[i] = tx
		i++
		return nil
	}); err != nil {
		return nil, err
	}
	txs = txs[:i] // user may request big "amount", but db can return small "amount". Return as much as we found.
	return txs, nil
}

func WriteTransactions(db kv.RwTx, txs []*transaction.Transaction, baseTxId uint64) error {
	txIdKey := make([]byte, 8)
	for i, tx := range txs {
		binary.BigEndian.PutUint64(txIdKey, baseTxId+uint64(i))
		data, err := tx.Marshal()
		if err != nil {
			return err
		}
		// If next Append returns KeyExists error - it means you need to open transaction
		// in App code before calling this func. Batch is also fine.
		if err := db.Append(modules.BlockTx, txIdKey, types.CopyBytes(data)); err != nil {
			return err
		}
	}
	return nil
}

func WriteRawTransactions(tx kv.RwTx, txs [][]byte, baseTxId uint64) error {
	txIdKey := make([]byte, 8)
	for i, txn := range txs {
		txId := baseTxId + uint64(i)
		binary.BigEndian.PutUint64(txIdKey, txId)
		// If next Append returns KeyExists error - it means you need to open transaction
		// in App code before calling this func. Batch is also fine.
		if err := tx.Append(modules.BlockTx, txIdKey, txn); err != nil {
			return fmt.Errorf("txId=%d, baseTxId=%d, %w", txId, baseTxId, err)
		}
	}
	return nil
}

// WriteBodyForStorage stores an encoded block body into the database.
func WriteBodyForStorage(db kv.Putter, hash types.Hash, number uint64, body *block.BodyForStorage) error {
	v := modules.BodyStorageValue(body.BaseTxId, body.TxAmount)
	return db.Put(modules.BlockBody, modules.BlockBodyKey(number, hash), v)
}

// ReadBodyByNumber - returns canonical block body
func ReadBodyByNumber(db kv.Tx, number uint64) (*block.Body, uint64, uint32, error) {
	hash, err := ReadCanonicalHash(db, number)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("failed ReadCanonicalHash: %w", err)
	}
	if hash == (types.Hash{}) {
		return nil, 0, 0, nil
	}
	body, baseTxId, txAmount := ReadBody(db, hash, number)
	return body, baseTxId, txAmount, nil
}

func ReadBodyWithTransactions(db kv.Getter, hash types.Hash, number uint64) (*block.Body, error) {
	canonicalHash, err := ReadCanonicalHash(db, number)
	if err != nil {
		return nil, fmt.Errorf("read canonical hash failed: %d, %w", number, err)
	}
	if canonicalHash == hash {
		return ReadCanonicalBodyWithTransactions(db, hash, number), nil
	}
	return nil, fmt.Errorf("mismatch hash: %v", hash)
}

func ReadCanonicalBodyWithTransactions(db kv.Getter, hash types.Hash, number uint64) *block.Body {
	body, baseTxId, txAmount := ReadBody(db, hash, number)
	if body == nil {
		return nil
	}
	var err error
	body.Txs, err = CanonicalTransactions(db, baseTxId, txAmount)
	if err != nil {
		log.Error("Failed to read transaction by hash", "hash", hash, "block", number, "err", err)
		return nil
	}

	verifies, err := ReadVerifies(db, hash, number)
	if err != nil {
		log.Error("Failed to read verifiers", "hash", hash, "block", number, "err", err)
		return nil
	}
	body.Verifiers = verifies

	rewards, err := ReadRewards(db, hash, number)
	if err != nil {
		log.Error("read reward failed", err)
		return nil
	}
	body.Rewards = rewards
	return body
}

func RawTransactionsRange(db kv.Getter, from, to uint64) (res [][]byte, err error) {
	blockKey := make([]byte, modules.NumberLength+types.HashLength)
	encNum := make([]byte, 8)
	for i := from; i < to+1; i++ {
		binary.BigEndian.PutUint64(encNum, i)
		hash, err := db.GetOne(modules.HeaderCanonical, encNum)
		if err != nil {
			return nil, err
		}
		if len(hash) == 0 {
			continue
		}

		binary.BigEndian.PutUint64(blockKey, i)
		copy(blockKey[modules.NumberLength:], hash)
		bodyRaw, err := db.GetOne(modules.BlockBody, blockKey)
		if err != nil {
			return nil, err
		}
		if len(bodyRaw) == 0 {
			continue
		}
		if len(bodyRaw) < 12 {
			continue
		}

		baseTxId := binary.BigEndian.Uint64(bodyRaw[:8])
		txAmount := binary.BigEndian.Uint32(bodyRaw[8:])

		binary.BigEndian.PutUint64(encNum, baseTxId)
		if err = db.ForAmount(modules.BlockTx, encNum, txAmount, func(k, v []byte) error {
			res = append(res, v)
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return
}

func ReadBodyForStorageByKey(db kv.Getter, k []byte) (*block.BodyForStorage, error) {
	bodyRaw, err := db.GetOne(modules.BlockBody, k)
	if err != nil {
		return nil, err
	}
	if len(bodyRaw) == 0 {
		return nil, nil
	}
	if len(bodyRaw) < 12 {
		return nil, fmt.Errorf("invalid body raw length: %d", len(bodyRaw))
	}
	return &block.BodyForStorage{
		BaseTxId: binary.BigEndian.Uint64(bodyRaw[:8]),
		TxAmount: binary.BigEndian.Uint32(bodyRaw[8:]),
	}, nil
}

func ReadBody(db kv.Getter, hash types.Hash, number uint64) (*block.Body, uint64, uint32) {
	data := ReadStorageBodyRAW(db, hash, number)
	if len(data) == 0 {
		return nil, 0, 0
	}
	if len(data) < 12 {
		log.Error("ReadBody: invalid body raw length", "number", number, "hash", hash, "len", len(data))
		return nil, 0, 0
	}
	baseTxId := binary.BigEndian.Uint64(data[:8])
	txAmount := binary.BigEndian.Uint32(data[8:])
	if txAmount < 2 {
		log.Error("ReadBody: block body has too few txs amount", "number", number, "txAmount", txAmount)
		return nil, 0, 0
	}
	// 1 system txn at the beginning of block, and 1 at the end
	return new(block.Body), baseTxId + 1, txAmount - 2
}

func ReadSenders(db kv.Getter, hash types.Hash, number uint64) ([]types.Address, error) {
	data, err := db.GetOne(modules.Senders, modules.BlockBodyKey(number, hash))
	if err != nil {
		return nil, fmt.Errorf("readSenders failed: %w", err)
	}
	senders := make([]types.Address, len(data)/types.AddressLength)
	for i := 0; i < len(senders); i++ {
		copy(senders[i][:], data[i*types.AddressLength:])
	}
	return senders, nil
}

func WriteRawBodyIfNotExists(db kv.RwTx, hash types.Hash, number uint64, body *block.RawBody) (ok bool, lastTxnNum uint64, err error) {
	exists, err := db.Has(modules.BlockBody, modules.BlockBodyKey(number, hash))
	if err != nil {
		return false, 0, err
	}
	if exists {
		return false, 0, nil
	}
	return WriteRawBody(db, hash, number, body)
}

func WriteRawBody(db kv.RwTx, hash types.Hash, number uint64, body *block.RawBody) (ok bool, lastTxnNum uint64, err error) {
	baseTxId, err := db.IncrementSequence(modules.BlockTx, uint64(len(body.Transactions))+2)
	if err != nil {
		return false, 0, err
	}
	data := block.BodyForStorage{
		BaseTxId: baseTxId,
		TxAmount: uint32(len(body.Transactions)) + 2,
	}
	if err = WriteBodyForStorage(db, hash, number, &data); err != nil {
		return false, 0, fmt.Errorf("WriteBodyForStorage: %w", err)
	}
	lastTxnNum = baseTxId + uint64(len(body.Transactions)) + 2
	if err = WriteRawTransactions(db, body.Transactions, baseTxId+1); err != nil {
		return false, 0, fmt.Errorf("WriteRawTransactions: %w", err)
	}
	return true, lastTxnNum, nil
}

func WriteBody(db kv.RwTx, hash types.Hash, number uint64, body *block.Body) error {
	// Pre-processing
	body.SendersFromTxs()
	baseTxId, err := db.IncrementSequence(modules.BlockTx, uint64(len(body.Txs))+2)
	if err != nil {
		return err
	}
	data := block.BodyForStorage{
		BaseTxId: baseTxId,
		TxAmount: uint32(len(body.Txs)) + 2,
	}
	if err := WriteBodyForStorage(db, hash, number, &data); err != nil {
		return fmt.Errorf("failed to write body: %w", err)
	}
	err = WriteTransactions(db, body.Transactions(), baseTxId+1)
	if err != nil {
		return fmt.Errorf("failed to WriteTransactions: %w", err)
	}

	if len(body.Verifiers) > 0 {
		if err := WriteVerifies(db, hash, number, body.Verifiers); err != nil {
			return err
		}
	}
	if len(body.Rewards) > 0 {
		if err := WriteRewards(db, hash, number, body.Rewards); err != nil {
			return err
		}
	}

	return nil
}

func ReadVerifies(db kv.Getter, hash types.Hash, number uint64) ([]*block.Verify, error) {
	data, err := db.GetOne(modules.BlockVerify, modules.BlockBodyKey(number, hash))
	if err != nil {
		return nil, fmt.Errorf("ReadVerifies failed: %w", err)
	}
	const recordSize = types.PublicKeyLength + types.AddressLength
	if len(data) > 0 && len(data)%recordSize != 0 {
		return nil, fmt.Errorf("ReadVerifies: invalid data length %d, not a multiple of %d", len(data), recordSize)
	}
	verifies := make([]*block.Verify, len(data)/recordSize)
	for i := range verifies {
		offset := i * recordSize
		v := &block.Verify{}
		copy(v.PublicKey[:], data[offset:offset+types.PublicKeyLength])
		copy(v.Address[:], data[offset+types.PublicKeyLength:offset+recordSize])
		verifies[i] = v
	}
	return verifies, nil
}

func WriteVerifies(db kv.Putter, hash types.Hash, number uint64, verifies []*block.Verify) error {
	const recordSize = types.PublicKeyLength + types.AddressLength
	data := make([]byte, recordSize*len(verifies))
	for i, v := range verifies {
		offset := i * recordSize
		copy(data[offset:], v.PublicKey[:])
		copy(data[offset+types.PublicKeyLength:], v.Address[:])
	}
	if err := db.Put(modules.BlockVerify, modules.BlockBodyKey(number, hash), data); err != nil {
		return fmt.Errorf("failed to store block verifies: %w", err)
	}
	return nil
}

func ReadRewards(db kv.Getter, hash types.Hash, number uint64) ([]*block.Reward, error) {
	data, err := db.GetOne(modules.BlockRewards, modules.BlockBodyKey(number, hash))
	if err != nil {
		return nil, fmt.Errorf("ReadBlockRewards failed: %w", err)
	}
	const recordSize = types.AddressLength + 32
	rewards := make([]*block.Reward, len(data)/recordSize)
	for i := range rewards {
		offset := i * recordSize
		var addr types.Address
		copy(addr[:], data[offset:offset+types.AddressLength])
		rewards[i] = &block.Reward{
			Address: addr,
			Amount:  new(uint256.Int).SetBytes(data[offset+types.AddressLength : offset+recordSize]),
		}
	}
	return rewards, nil
}

func WriteRewards(db kv.Putter, hash types.Hash, number uint64, rewards []*block.Reward) error {
	const recordSize = types.AddressLength + 32
	data := make([]byte, recordSize*len(rewards))
	for i, reward := range rewards {
		offset := i * recordSize
		copy(data[offset:], reward.Address[:])
		amountBytes := reward.Amount.Bytes32()
		copy(data[offset+types.AddressLength:], amountBytes[:])
	}
	if err := db.Put(modules.BlockRewards, modules.BlockBodyKey(number, hash), data); err != nil {
		return fmt.Errorf("failed to store block rewards: %w", err)
	}
	return nil
}

// deleteBody removes all block body data associated with a hash.
func deleteBody(db kv.Deleter, hash types.Hash, number uint64) {
	if err := db.Delete(modules.BlockBody, modules.BlockBodyKey(number, hash)); err != nil {
		log.Crit("Failed to delete block body", "err", err)
	}
}

// ReadTd retrieves a block's total difficulty corresponding to the hash.
func ReadTd(db kv.Getter, hash types.Hash, number uint64) (*uint256.Int, error) {
	data, err := db.GetOne(modules.HeaderTD, modules.HeaderKey(number, hash))
	if err != nil {
		return nil, fmt.Errorf("failed ReadTd: %w", err)
	}
	if data == nil {
		return nil, nil
	}
	td := uint256.NewInt(0).SetBytes(data)
	log.Trace("readTD", "hash", hash, "number", number, "td", td.Uint64())
	return td, nil
}

func ReadTdByHash(db kv.Getter, hash types.Hash) (*uint256.Int, error) {
	headNumber := ReadHeaderNumber(db, hash)
	if headNumber == nil {
		return nil, nil
	}
	return ReadTd(db, hash, *headNumber)
}

// WriteTd stores the total difficulty of a block into the database.
func WriteTd(db kv.Putter, hash types.Hash, number uint64, td *uint256.Int) error {
	data := td.Bytes()
	if err := db.Put(modules.HeaderTD, modules.HeaderKey(number, hash), data); err != nil {
		return fmt.Errorf("failed to store block total difficulty: %w", err)
	}
	return nil
}

// TruncateTd removes all block total difficulty from block number N
func TruncateTd(tx kv.RwTx, blockFrom uint64) error {
	if err := tx.ForEach(modules.HeaderTD, modules.EncodeBlockNumber(blockFrom), func(k, _ []byte) error {
		return tx.Delete(modules.HeaderTD, k)
	}); err != nil {
		return fmt.Errorf("TruncateTd: %w", err)
	}
	return nil
}

// LastKey - candidate on move to kv.Tx interface
func LastKey(tx kv.Tx, table string) ([]byte, error) {
	c, err := tx.Cursor(table)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	k, _, err := c.Last()
	if err != nil {
		return nil, err
	}
	return k, nil
}

// FirstKey - candidate on move to kv.Tx interface
func FirstKey(tx kv.Tx, table string) ([]byte, error) {
	c, err := tx.Cursor(table)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	k, _, err := c.First()
	if err != nil {
		return nil, err
	}
	return k, nil
}

// SecondKey - useful if table always has zero-key (for example genesis block)
func SecondKey(tx kv.Tx, table string) ([]byte, error) {
	c, err := tx.Cursor(table)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_, _, err = c.First()
	if err != nil {
		return nil, err
	}
	k, _, err := c.Next()
	if err != nil {
		return nil, err
	}
	return k, nil
}

// TruncateBlocks - delete block >= blockFrom
// does decrement sequences of kv.EthTx and kv.NonCanonicalTxs
// doesn't delete Receipts, Senders, Canonical markers, TotalDifficulty
func TruncateBlocks(ctx context.Context, tx kv.RwTx, blockFrom uint64) error {
	logEvery := time.NewTicker(20 * time.Second)
	defer logEvery.Stop()

	c, err := tx.Cursor(modules.Headers)
	if err != nil {
		return err
	}
	defer c.Close()
	if blockFrom < 1 { //protect genesis
		blockFrom = 1
	}
	sequenceTo := map[string]uint64{}
	for k, _, err := c.Last(); k != nil; k, _, err = c.Prev() {
		if err != nil {
			return err
		}
		n := binary.BigEndian.Uint64(k)
		if n < blockFrom { // [from, to)
			break
		}

		b, err := ReadBodyForStorageByKey(tx, k)
		if err != nil {
			return err
		}
		if b != nil {
			if err := tx.ForEach(modules.BlockTx, modules.EncodeBlockNumber(b.BaseTxId), func(k, _ []byte) error {
				return tx.Delete(modules.BlockTx, k)
			}); err != nil {
				return err
			}
			sequenceTo[modules.BlockTx] = b.BaseTxId
		}
		// Copying k because otherwise the same memory will be reused
		// for the next key and Delete below will end up deleting 1 more record than required
		kCopy := types.CopyBytes(k)
		if err := tx.Delete(modules.Headers, kCopy); err != nil {
			return err
		}
		if err := tx.Delete(modules.BlockBody, kCopy); err != nil {
			return err
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-logEvery.C:
			log.Info("TruncateBlocks", "block", n)
		default:
		}
	}
	return nil
}

// PruneTable has `limit` parameter to avoid too large data deletes per one sync cycle - better delete by small portions to reduce db.FreeList size
func PruneTable(tx kv.RwTx, table string, pruneTo uint64, ctx context.Context, limit int) error {
	c, err := tx.RwCursor(table)
	if err != nil {
		return fmt.Errorf("failed to create cursor for pruning: %w", err)
	}
	defer c.Close()

	i := 0
	for k, _, err := c.First(); k != nil; k, _, err = c.Next() {
		if err != nil {
			return err
		}
		i++
		if i > limit {
			break
		}

		blockNum := binary.BigEndian.Uint64(k)
		if blockNum >= pruneTo {
			break
		}
		select {
		case <-ctx.Done():
			return common2.ErrStopped
		default:
		}
		if err = c.DeleteCurrent(); err != nil {
			return fmt.Errorf("failed to remove for block %d: %w", blockNum, err)
		}
	}
	return nil
}

func PruneTableDupSort(tx kv.RwTx, table string, logPrefix string, pruneTo uint64, logEvery *time.Ticker, ctx context.Context) error {
	c, err := tx.RwCursorDupSort(table)
	if err != nil {
		return fmt.Errorf("failed to create cursor for pruning: %w", err)
	}
	defer c.Close()

	for k, _, err := c.First(); k != nil; k, _, err = c.NextNoDup() {
		if err != nil {
			return fmt.Errorf("failed to move %s cleanup cursor: %w", table, err)
		}
		blockNum := binary.BigEndian.Uint64(k)
		if blockNum >= pruneTo {
			break
		}
		select {
		case <-logEvery.C:
			log.Info(fmt.Sprintf("[%s]", logPrefix), "table", table, "block", blockNum)
		case <-ctx.Done():
			return common2.ErrStopped
		default:
		}
		if err = c.DeleteCurrentDuplicates(); err != nil {
			return fmt.Errorf("failed to remove for block %d: %w", blockNum, err)
		}
	}
	return nil
}
