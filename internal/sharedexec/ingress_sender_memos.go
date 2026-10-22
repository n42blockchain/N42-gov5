package sharedexec

import (
	"bytes"
	"runtime"
	"sync"
	"sync/atomic"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

// seedLiveIngressSenderMemos requires execute throughout. It only annotates
// newly decoded, privately owned transactions; it neither authorizes From nor
// changes the ordinary execution/validation path on a miss. A nil requestedEpoch
// is used by the owner import worker, which captures its own live queue epoch.
// QueueBuild supplies its explicit epoch. Generic Build never calls this helper.
func (s *Server) seedLiveIngressSenderMemos(b *block.Block, requestedEpoch *[16]byte) int {
	if b == nil || s.ctx.Err() != nil || len(b.Transactions()) == 0 {
		return 0
	}
	header, ok := b.Header().(*block.Header)
	if !ok || header == nil || header.Number == nil || !header.Number.IsUint64() {
		return 0
	}
	config := s.chain.Config()
	if config == nil {
		return 0
	}
	chainID := config.ChainID
	if chainID == nil {
		return 0
	}
	signer := transaction.MakeSignerWithTimestamp(config, header.Number.ToBig(), header.Time)
	// The trusted signer implementations check the actual concrete signer type.
	if !(transaction.NewEIP155Signer(chainID).Equal(signer) || transaction.NewEIP2930Signer(chainID).Equal(signer) || transaction.NewLondonSigner(chainID).Equal(signer)) {
		return 0
	}
	s.mu.Lock()
	q := s.ingress
	if s.closed || q == nil {
		s.mu.Unlock()
		return 0
	}
	q.mu.Lock()
	if q.closed || q.reconciling || q.bootstrapPending || q.epoch == ([16]byte{}) || (requestedEpoch != nil && *requestedEpoch != q.epoch) || q.pending < 1 || q.pending > q.maxTx {
		q.mu.Unlock()
		s.mu.Unlock()
		return 0
	}
	epoch, stamp := q.epoch, q.stamp
	proofs := make(map[types.Hash]canonicalIngressSender, q.pending)
	for address, lane := range q.lanes {
		for _, entry := range lane.entries {
			if entry == nil || entry.slab == nil || entry.start < 0 || entry.end <= entry.start || entry.end > len(entry.slab.payload) {
				continue
			}
			// Hold immutable slice values, never mutable slab pointers.
			proofs[entry.hash] = canonicalIngressSender{address: address, nonce: entry.nonce, raw: entry.slab.payload[entry.start:entry.end]}
		}
	}
	q.mu.Unlock()
	s.mu.Unlock()

	txs := b.Transactions()
	senders := make([]types.Address, len(txs))
	matched := make([]bool, len(txs))
	var panicked atomic.Bool
	scan := func(start, end int) {
		// Optional optimization failures leave every transaction on the original
		// path. A panic must never escape an untracked background goroutine.
		defer func() {
			if recover() != nil {
				panicked.Store(true)
			}
		}()
		for i := start; i < end; i++ {
			if s.ctx.Err() != nil {
				return
			}
			txn := txs[i]
			if txn == nil || txn.From() != nil || txn.Type() != transaction.LegacyTxType || !txn.Protected() || txn.ChainId().ToBig().Cmp(chainID) != 0 || txn.Nonce() == ^uint64(0) {
				continue
			}
			// Bypass cached Hash/EthEncoded and any existing sender memo.
			raw, err := transaction.EncodeEthereumTransaction(txn)
			if err != nil {
				continue
			}
			proof, ok := proofs[crypto.Keccak256Hash(raw)]
			if ok && proof.nonce == txn.Nonce() && bytes.Equal(raw, proof.raw) {
				senders[i], matched[i] = proof.address, true
			}
		}
	}
	workers := min(max(runtime.GOMAXPROCS(0), 1), 16, len(txs))
	if workers < 2 || len(txs) < 4096 {
		scan(0, len(txs))
	} else {
		var wg sync.WaitGroup
		chunk := (len(txs) + workers - 1) / workers
		for start := 0; start < len(txs); start += chunk {
			wg.Add(1)
			go func(start, end int) {
				defer wg.Done()
				scan(start, end)
			}(start, min(start+chunk, len(txs)))
		}
		wg.Wait()
	}
	if panicked.Load() || s.ctx.Err() != nil {
		return 0
	}
	// Recheck the exact live generation before publishing local memos. execute
	// remains held by the owner; neither snapshots nor results survive this call.
	s.mu.Lock()
	defer s.mu.Unlock()
	q.mu.Lock()
	defer q.mu.Unlock()
	if s.closed || s.ctx.Err() != nil || s.ingress != q || q.closed || q.reconciling || q.bootstrapPending || q.epoch != epoch || q.stamp != stamp {
		return 0
	}
	var seeded int
	for i, txn := range txs {
		if matched[i] {
			txn.SetSenderMemo(signer, senders[i])
			seeded++
		}
	}
	return seeded
}
