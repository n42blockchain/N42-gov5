package ethel

import (
	"errors"
	"sort"
	"sync"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

const (
	memoryPoolPriceBump     = uint64(10)
	memoryPoolBlobPriceBump = uint64(100)
)

var errMemoryPoolReplaceUnderpriced = errors.New("replacement transaction underpriced")

// MemoryTxPool is the small in-process pool used by eth-el's Engine and public
// RPC services. It intentionally implements only the common.ITxsPool contract;
// block execution remains responsible for nonce, balance, and fee validation.
type MemoryTxPool struct {
	mu      sync.RWMutex
	pending map[types.Address][]*transaction.Transaction
	byHash  map[types.Hash]*transaction.Transaction
}

func NewMemoryTxPool() *MemoryTxPool {
	return &MemoryTxPool{
		pending: make(map[types.Address][]*transaction.Transaction),
		byHash:  make(map[types.Hash]*transaction.Transaction),
	}
}

func (p *MemoryTxPool) Stop() error { return nil }

func (p *MemoryTxPool) Has(hash types.Hash) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.byHash[hash] != nil
}

func (p *MemoryTxPool) Pending(bool) map[types.Address][]*transaction.Transaction {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return clonePendingTransactions(p.pending)
}

func (p *MemoryTxPool) GetTransaction() ([]*transaction.Transaction, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	flat := make([]*transaction.Transaction, 0, len(p.byHash))
	for _, txs := range p.pending {
		flat = append(flat, txs...)
	}
	return flat, nil
}

func (p *MemoryTxPool) GetTx(hash types.Hash) *transaction.Transaction {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.byHash[hash]
}

func (p *MemoryTxPool) AddRemotes(txs []*transaction.Transaction) []error {
	return p.AddLocals(txs)
}

func (p *MemoryTxPool) AddLocal(tx *transaction.Transaction) error {
	if tx == nil {
		return errors.New("nil transaction")
	}
	from := tx.From()
	if from == nil {
		return errors.New("transaction sender unavailable")
	}
	hash := tx.Hash()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byHash[hash] != nil {
		return nil
	}
	for i, old := range p.pending[*from] {
		if old == nil || old.Nonce() != tx.Nonce() {
			continue
		}
		if !memoryPoolReplacementAllowed(old, tx) {
			return errMemoryPoolReplaceUnderpriced
		}
		delete(p.byHash, old.Hash())
		p.byHash[hash] = tx
		p.pending[*from][i] = tx
		return nil
	}
	p.byHash[hash] = tx
	p.pending[*from] = append(p.pending[*from], tx)
	sort.SliceStable(p.pending[*from], func(i, j int) bool {
		return p.pending[*from][i].Nonce() < p.pending[*from][j].Nonce()
	})
	return nil
}

func memoryPoolReplacementAllowed(old, replacement *transaction.Transaction) bool {
	priceBump := memoryPoolPriceBump
	if old.Type() == transaction.BlobTxType || replacement.Type() == transaction.BlobTxType {
		if old.Type() != transaction.BlobTxType || replacement.Type() != transaction.BlobTxType {
			return false
		}
		priceBump = memoryPoolBlobPriceBump
		if !memoryPoolPriceBumped(old.BlobFeeCap(), replacement.BlobFeeCap(), priceBump) {
			return false
		}
	}
	return memoryPoolPriceBumped(old.GasFeeCap(), replacement.GasFeeCap(), priceBump) &&
		memoryPoolPriceBumped(old.GasTipCap(), replacement.GasTipCap(), priceBump)
}

func memoryPoolPriceBumped(old, replacement *uint256.Int, priceBump uint64) bool {
	if old == nil || replacement == nil || replacement.Cmp(old) <= 0 {
		return false
	}
	threshold, overflow := new(uint256.Int).MulDivOverflow(
		old,
		uint256.NewInt(100+priceBump),
		uint256.NewInt(100),
	)
	return !overflow && replacement.Cmp(threshold) >= 0
}

func (p *MemoryTxPool) AddLocals(txs []*transaction.Transaction) []error {
	errs := make([]error, len(txs))
	for i, tx := range txs {
		errs[i] = p.AddLocal(tx)
	}
	return errs
}

func (p *MemoryTxPool) Stats() (int, int, int, int) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.pending), len(p.byHash), 0, 0
}

func (p *MemoryTxPool) Nonce(addr types.Address) uint64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var nonce uint64
	for _, tx := range p.pending[addr] {
		if tx != nil && tx.Nonce() >= nonce {
			nonce = tx.Nonce() + 1
		}
	}
	return nonce
}

func (p *MemoryTxPool) Content() (map[types.Address][]*transaction.Transaction, map[types.Address][]*transaction.Transaction) {
	return p.Pending(false), map[types.Address][]*transaction.Transaction{}
}

func clonePendingTransactions(src map[types.Address][]*transaction.Transaction) map[types.Address][]*transaction.Transaction {
	dst := make(map[types.Address][]*transaction.Transaction, len(src))
	for addr, txs := range src {
		dst[addr] = append([]*transaction.Transaction(nil), txs...)
	}
	return dst
}
