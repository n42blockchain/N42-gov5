package sharedexec

import (
	"bytes"
	"context"
	"math/big"
	"sync"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
)

type canonicalIngressSender struct {
	address types.Address
	nonce   uint64
	raw     []byte
}

// The caller exclusively holds execute throughout reconciliation. All three
// ingress publication paths directly verify signatures before publishing lanes.
// This snapshot is local to the current dirty queue/epoch; it never outlives the
// reconciliation. Capture slice values under mu, not mutable slab pointers.
func (s *Server) snapshotCanonicalIngressSenders(q *strictIngressQueue) map[types.Hash]canonicalIngressSender {
	if q == nil || s.ingress != q {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || !q.reconciling || q.epoch == ([16]byte{}) || q.pending < 1 || q.pending > q.maxTx {
		return nil
	}
	proofs := make(map[types.Hash]canonicalIngressSender, q.pending)
	for address, lane := range q.lanes {
		for _, entry := range lane.entries {
			if entry == nil || entry.slab == nil || entry.start < 0 || entry.end <= entry.start || entry.end > len(entry.slab.payload) {
				continue
			}
			proofs[entry.hash] = canonicalIngressSender{address: address, nonce: entry.nonce, raw: entry.slab.payload[entry.start:entry.end]}
		}
	}
	return proofs
}

// Reuse only an exact signed payload already verified by this live ingress
// queue. From, sender memos, stored senders and cached transaction encodings or
// hashes grant no authority. Every miss retains direct signature recovery.
func recoverCanonicalIngressSenders(ctx context.Context, txs []*transaction.Transaction, signer transaction.Signer, workers int, chainID *big.Int, proofs map[types.Hash]canonicalIngressSender) ([]types.Address, error) {
	if signer == nil || chainID == nil || len(proofs) == 0 || !(transaction.NewEIP155Signer(chainID).Equal(signer) || transaction.NewEIP2930Signer(chainID).Equal(signer) || transaction.NewLondonSigner(chainID).Equal(signer)) {
		return recoverCanonicalSenders(ctx, txs, signer, workers)
	}
	if ctx == nil {
		return recoverCanonicalSenders(ctx, txs, signer, workers)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	senders := make([]types.Address, len(txs))
	matched := make([]bool, len(txs))
	scan := func(start, end int) {
		for index := start; index < end; index++ {
			if ctx.Err() != nil {
				return
			}
			txn := txs[index]
			if txn != nil && txn.Type() == transaction.LegacyTxType && txn.Protected() && txn.ChainId().ToBig().Cmp(chainID) == 0 && txn.Nonce() != ^uint64(0) {
				// Encode directly from signed fields, bypassing EthEncoded/Hash caches.
				raw, err := transaction.EncodeEthereumTransaction(txn)
				if err == nil {
					proof, ok := proofs[crypto.Keccak256Hash(raw)]
					if ok && proof.nonce == txn.Nonce() && bytes.Equal(raw, proof.raw) {
						senders[index] = proof.address
						matched[index] = true
					}
				}
			}
		}
	}
	if workers < 1 {
		workers = 1
	}
	if workers > 16 {
		workers = 16
	}
	if workers > len(txs) {
		workers = len(txs)
	}
	if workers < 2 || len(txs) < 4096 {
		scan(0, len(txs))
	} else {
		// Workers only read signed fields and the immutable queue snapshot.
		// Each owns disjoint result slots; no worker touches the database.
		var wg sync.WaitGroup
		chunk := (len(txs) + workers - 1) / workers
		for start := 0; start < len(txs); start += chunk {
			end := min(start+chunk, len(txs))
			wg.Add(1)
			go func(start, end int) {
				defer wg.Done()
				scan(start, end)
			}(start, end)
		}
		wg.Wait()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var misses []*transaction.Transaction
	var indices []int
	for index, txn := range txs {
		if matched[index] {
			continue
		}
		misses = append(misses, txn)
		indices = append(indices, index)
	}
	// Original order preserves deterministic first-error ordering. The unchanged
	// helper joins all workers before returning, including on owner cancellation.
	recovered, err := recoverCanonicalSenders(ctx, misses, signer, workers)
	if err != nil {
		return nil, err
	}
	for i, sender := range recovered {
		senders[indices[i]] = sender
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return senders, nil
}
