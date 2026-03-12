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

// Package encrypted implements a threshold-encrypted transaction pool
// for MEV protection. Transactions are encrypted before entering the mempool
// and only decrypted when included in a block, preventing frontrunning.
package encrypted

import (
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/holiman/uint256"
	"go.uber.org/zap"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

var (
	// ErrPoolFull is returned when the encrypted pool has reached its capacity.
	ErrPoolFull = errors.New("encrypted pool: at capacity")

	// ErrDuplicateTx is returned when a transaction with the same ID already exists.
	ErrDuplicateTx = errors.New("encrypted pool: duplicate transaction")

	// ErrInvalidTx is returned when a transaction fails basic validation.
	ErrInvalidTx = errors.New("encrypted pool: invalid transaction")

	// ErrEmptyEncryptedData is returned when encrypted data is nil or empty.
	ErrEmptyEncryptedData = errors.New("encrypted pool: empty encrypted data")

	// ErrEmptyEncryptionKey is returned when the encryption key is nil or empty.
	ErrEmptyEncryptionKey = errors.New("encrypted pool: empty encryption key")

	// ErrBlockTargetZero is returned when block target is zero.
	ErrBlockTargetZero = errors.New("encrypted pool: block target must be > 0")

	// ErrGasLimitZero is returned when gas limit is zero.
	ErrGasLimitZero = errors.New("encrypted pool: gas limit must be > 0")

	// ErrNilGasPrice is returned when gas price is nil.
	ErrNilGasPrice = errors.New("encrypted pool: gas price must not be nil")

	// ErrEncryptedDataTooLarge is returned when encrypted data exceeds the maximum allowed size.
	ErrEncryptedDataTooLarge = errors.New("encrypted pool: encrypted data exceeds 128KB limit")

	// ErrDecryptionFailed is returned when transaction decryption fails.
	ErrDecryptionFailed = errors.New("encrypted pool: decryption failed")

	// pruneWindow defines the number of blocks after blockTarget before
	// an encrypted transaction is considered expired and eligible for pruning.
	pruneWindow uint64 = 256

	// maxEncryptedDataSize is the maximum allowed size for encrypted transaction data (128KB).
	maxEncryptedDataSize = 128 * 1024
)

// EncryptedTx represents an encrypted transaction in the MEV-protected mempool.
// The transaction payload is encrypted with AES-256-GCM; the symmetric key itself
// is encrypted with the threshold public key so that only a quorum of keypers
// can reconstruct it.
type EncryptedTx struct {
	ID            types.Hash    `json:"id"`             // unique identifier
	EncryptedData []byte        `json:"encrypted_data"` // AES-256-GCM encrypted tx bytes
	EncryptionKey []byte        `json:"encryption_key"` // symmetric key encrypted with threshold public key
	Sender        types.Address `json:"sender"`         // sender (known from envelope signature)
	GasLimit      uint64        `json:"gas_limit"`      // gas limit (plaintext for ordering)
	GasPrice      *uint256.Int  `json:"gas_price"`      // gas price (plaintext for ordering)
	SubmittedAt   time.Time     `json:"submitted_at"`   // submission timestamp
	BlockTarget   uint64        `json:"block_target"`   // target block number
	Nonce         uint64        `json:"nonce"`           // sender nonce
}

// Decryptor is the interface for threshold decryption of encrypted transactions.
type Decryptor interface {
	// Decrypt decrypts the transaction payload. encryptionKey is the per-tx
	// symmetric key (itself encrypted with the threshold public key).
	Decrypt(encryptedData []byte, encryptionKey []byte) ([]byte, error)

	// PublicKey returns the current threshold public key used for encryption.
	PublicKey() []byte
}

// EncryptedPool manages encrypted transactions that are shielded from MEV
// extraction until they are included in a block.
type EncryptedPool struct {
	mu       sync.RWMutex
	pending  map[types.Hash]*EncryptedTx
	bySender map[types.Address][]*EncryptedTx

	maxSize   int
	decryptor Decryptor
	logger    *zap.Logger
}

// NewEncryptedPool creates a new encrypted transaction pool.
//
// maxSize controls the maximum number of encrypted transactions held
// concurrently. decryptor provides the threshold decryption capability.
// If logger is nil a no-op logger is used.
func NewEncryptedPool(maxSize int, decryptor Decryptor, logger *zap.Logger) *EncryptedPool {
	if maxSize <= 0 {
		maxSize = 4096
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &EncryptedPool{
		pending:   make(map[types.Hash]*EncryptedTx, maxSize),
		bySender:  make(map[types.Address][]*EncryptedTx),
		maxSize:   maxSize,
		decryptor: decryptor,
		logger:    logger,
	}
}

// Add validates and inserts an encrypted transaction into the pool.
// Returns ErrPoolFull if the pool is at capacity, ErrDuplicateTx if
// a transaction with the same ID already exists, or ErrInvalidTx if
// the transaction fails basic validation.
func (p *EncryptedPool) Add(tx *EncryptedTx) error {
	if tx == nil {
		return ErrInvalidTx
	}
	if err := validateEncryptedTx(tx); err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if len(p.pending) >= p.maxSize {
		return ErrPoolFull
	}
	if _, exists := p.pending[tx.ID]; exists {
		return ErrDuplicateTx
	}

	p.pending[tx.ID] = tx
	p.bySender[tx.Sender] = append(p.bySender[tx.Sender], tx)

	p.logger.Debug("encrypted tx added",
		zap.String("id", tx.ID.Hex()),
		zap.String("sender", tx.Sender.Hex()),
		zap.Uint64("block_target", tx.BlockTarget),
		zap.Uint64("nonce", tx.Nonce),
	)
	return nil
}

// Remove deletes an encrypted transaction from the pool by its ID.
// It is a no-op if the ID does not exist.
func (p *EncryptedPool) Remove(id types.Hash) {
	p.mu.Lock()
	defer p.mu.Unlock()

	tx, exists := p.pending[id]
	if !exists {
		return
	}
	delete(p.pending, id)
	p.removeSenderEntry(tx.Sender, id)

	p.logger.Debug("encrypted tx removed",
		zap.String("id", id.Hex()),
	)
}

// GetPending returns all encrypted transactions targeting the given block
// number, sorted by gas price descending (highest first) for fair ordering.
func (p *EncryptedPool) GetPending(blockNumber uint64) []*EncryptedTx {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*EncryptedTx
	for _, tx := range p.pending {
		if tx.BlockTarget == blockNumber {
			// Return a defensive copy to avoid callers mutating pool internals.
			cp := *tx
			if tx.GasPrice != nil {
				cp.GasPrice = new(uint256.Int).Set(tx.GasPrice)
			}
			cp.EncryptedData = make([]byte, len(tx.EncryptedData))
			copy(cp.EncryptedData, tx.EncryptedData)
			cp.EncryptionKey = make([]byte, len(tx.EncryptionKey))
			copy(cp.EncryptionKey, tx.EncryptionKey)
			result = append(result, &cp)
		}
	}

	// Sort by gas price descending for consistent ordering.
	sort.Slice(result, func(i, j int) bool {
		return result[i].GasPrice.Cmp(result[j].GasPrice) > 0
	})
	return result
}

// DecryptForBlock decrypts all encrypted transactions targeting the given
// block number and returns them as standard Transaction objects.
//
// Transactions that fail decryption or unmarshalling are logged and skipped
// rather than aborting the entire batch, because a single malformed ciphertext
// must not block production.
func (p *EncryptedPool) DecryptForBlock(blockNumber uint64) ([]*transaction.Transaction, error) {
	candidates := p.GetPending(blockNumber)
	if len(candidates) == 0 {
		return nil, nil
	}

	p.logger.Info("decrypting encrypted txs for block",
		zap.Uint64("block", blockNumber),
		zap.Int("count", len(candidates)),
	)

	var (
		result  []*transaction.Transaction
		failed  int
	)

	var decryptedIDs []types.Hash

	for _, etx := range candidates {
		plaintext, err := p.decryptor.Decrypt(etx.EncryptedData, etx.EncryptionKey)
		if err != nil {
			p.logger.Warn("failed to decrypt encrypted tx",
				zap.String("id", etx.ID.Hex()),
				zap.Error(err),
			)
			failed++
			continue
		}

		tx := new(transaction.Transaction)
		if err := tx.Unmarshal(plaintext); err != nil {
			p.logger.Warn("failed to unmarshal decrypted tx",
				zap.String("id", etx.ID.Hex()),
				zap.Error(err),
			)
			failed++
			continue
		}

		result = append(result, tx)
		decryptedIDs = append(decryptedIDs, etx.ID)
	}

	if failed > 0 {
		p.logger.Warn("some encrypted txs could not be decrypted",
			zap.Int("failed", failed),
			zap.Int("succeeded", len(result)),
		)
	}

	// Remove successfully decrypted transactions from the pool.
	if len(decryptedIDs) > 0 {
		for _, id := range decryptedIDs {
			p.Remove(id)
		}
	}

	return result, nil
}

// Len returns the current number of encrypted transactions in the pool.
func (p *EncryptedPool) Len() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.pending)
}

// Prune removes all encrypted transactions whose block target is more
// than pruneWindow (256) blocks behind currentBlock.
func (p *EncryptedPool) Prune(currentBlock uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var pruned int
	for id, tx := range p.pending {
		// Avoid underflow: if currentBlock < pruneWindow, nothing to prune.
		if currentBlock > pruneWindow && tx.BlockTarget < currentBlock-pruneWindow {
			delete(p.pending, id)
			p.removeSenderEntry(tx.Sender, id)
			pruned++
		}
	}

	if pruned > 0 {
		p.logger.Info("pruned expired encrypted txs",
			zap.Int("pruned", pruned),
			zap.Uint64("current_block", currentBlock),
			zap.Int("remaining", len(p.pending)),
		)
	}
}

// GetBySender returns all encrypted transactions from the given sender
// sorted by nonce ascending.
func (p *EncryptedPool) GetBySender(sender types.Address) []*EncryptedTx {
	p.mu.RLock()
	defer p.mu.RUnlock()

	txs := p.bySender[sender]
	if len(txs) == 0 {
		return nil
	}

	// Return a copy sorted by nonce.
	out := make([]*EncryptedTx, len(txs))
	copy(out, txs)
	sort.Slice(out, func(i, j int) bool {
		return out[i].Nonce < out[j].Nonce
	})
	return out
}

// removeSenderEntry removes a single tx from the bySender index.
// Caller must hold p.mu.
func (p *EncryptedPool) removeSenderEntry(sender types.Address, id types.Hash) {
	txs := p.bySender[sender]
	for i, tx := range txs {
		if tx.ID == id {
			// Swap with last and truncate.
			txs[i] = txs[len(txs)-1]
			txs[len(txs)-1] = nil // avoid memory leak
			p.bySender[sender] = txs[:len(txs)-1]
			if len(p.bySender[sender]) == 0 {
				delete(p.bySender, sender)
			}
			return
		}
	}
}

// validateEncryptedTx performs basic validation on an encrypted transaction.
func validateEncryptedTx(tx *EncryptedTx) error {
	if len(tx.EncryptedData) == 0 {
		return ErrEmptyEncryptedData
	}
	if len(tx.EncryptedData) > maxEncryptedDataSize {
		return ErrEncryptedDataTooLarge
	}
	if len(tx.EncryptionKey) == 0 {
		return ErrEmptyEncryptionKey
	}
	if tx.BlockTarget == 0 {
		return ErrBlockTargetZero
	}
	if tx.GasLimit == 0 {
		return ErrGasLimitZero
	}
	if tx.GasPrice == nil || tx.GasPrice.IsZero() {
		return ErrNilGasPrice
	}
	if tx.ID == (types.Hash{}) {
		return ErrInvalidTx
	}
	return nil
}
