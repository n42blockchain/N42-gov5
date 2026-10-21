package sharedexec

import (
	"context"
	"errors"
	"math/big"
	"reflect"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/crypto"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
)

func admittedCanonicalSenderFixture(t *testing.T) (strictIngressFixture, []*transaction.Transaction, transaction.Signer, map[types.Hash]canonicalIngressSender) {
	t.Helper()
	f := newStrictIngressFixture(t, 10, 1<<20)
	status, _ := f.server.StrictIngressStatus()
	var txs []*transaction.Transaction
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(context.Background(), f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
		for _, raw := range strictFrameRaws(t, frame) {
			txn, err := transaction.DecodeEthereumTransaction(raw)
			if err != nil {
				t.Fatal(err)
			}
			txs = append(txs, txn)
		}
	}
	q := f.server.ingress
	q.mu.Lock()
	q.reconciling = true
	q.mu.Unlock()
	return f, txs, transaction.NewEIP155Signer(f.chain.Config().ChainID), f.server.snapshotCanonicalIngressSenders(q)
}

func TestCanonicalIngressSendersReuseOnlyLiveVerifiedSignedPayloads(t *testing.T) {
	f, txs, signer, proofs := admittedCanonicalSenderFixture(t)
	fake := types.Address{19: 99}
	for _, txn := range txs {
		txn.SetFrom(fake)
		txn.SetSenderMemo(signer, fake)
	}
	stop := transaction.CountSenderRecoveriesForTest()
	got, err := recoverCanonicalIngressSenders(context.Background(), txs, signer, 2, f.chain.Config().ChainID, proofs)
	count := stop()
	if err != nil || count != 0 || len(got) != len(txs) {
		t.Fatalf("live verified reuse: recoveries %d, senders %v, error %v", count, got, err)
	}
	for i, sender := range got {
		if sender != f.from[i/3] {
			t.Fatal("forged hint changed the admitted sender")
		}
	}
	for _, actual := range []transaction.Signer{transaction.NewEIP2930Signer(f.chain.Config().ChainID), transaction.NewLondonSigner(f.chain.Config().ChainID)} {
		stop := transaction.CountSenderRecoveriesForTest()
		got, err := recoverCanonicalIngressSenders(context.Background(), txs, actual, 2, f.chain.Config().ChainID, proofs)
		count := stop()
		if err != nil || count != 0 || got[0] != f.from[0] {
			t.Fatal("compatible canonical legacy signer missed verified payload", count, err)
		}
	}
	// Snapshot slice values keep the immutable bytes alive after slab release.
	q := f.server.ingress
	q.mu.Lock()
	for _, lane := range q.lanes {
		for _, entry := range lane.entries {
			entry.slab.payload = nil
		}
	}
	q.mu.Unlock()
	if got, err := recoverCanonicalIngressSenders(context.Background(), txs, signer, 2, f.chain.Config().ChainID, proofs); err != nil || got[0] != f.from[0] {
		t.Fatal("snapshot retained mutable slab pointers", err)
	}
}

type untrustedCanonicalSigner struct{ transaction.Signer }

func (untrustedCanonicalSigner) Equal(transaction.Signer) bool { return true }

func TestCanonicalIngressSendersMismatchAndCustomSignerRecoverDirectly(t *testing.T) {
	f, txs, signer, original := admittedCanonicalSenderFixture(t)
	raw, _ := transaction.EncodeEthereumTransaction(txs[0])
	hash := crypto.Keccak256Hash(raw)
	for _, kind := range []string{"missing", "nonce", "hash", "payload", "custom signer"} {
		t.Run(kind, func(t *testing.T) {
			proofs := make(map[types.Hash]canonicalIngressSender, len(original))
			for hash, proof := range original {
				proofs[hash] = proof
			}
			proof := proofs[hash]
			actualSigner := signer
			wantRecoveries := uint64(1)
			switch kind {
			case "missing":
				delete(proofs, hash)
			case "nonce":
				proof.nonce++
				proofs[hash] = proof
			case "hash":
				delete(proofs, hash)
				hash := hash
				hash[0] ^= 1
				proofs[hash] = proof
			case "payload":
				proof.raw = append([]byte(nil), proof.raw...)
				proof.raw[0] ^= 1
				proofs[hash] = proof
			case "custom signer":
				actualSigner = untrustedCanonicalSigner{signer}
				wantRecoveries = uint64(len(txs))
			}
			stop := transaction.CountSenderRecoveriesForTest()
			got, err := recoverCanonicalIngressSenders(context.Background(), txs, actualSigner, 2, f.chain.Config().ChainID, proofs)
			count := stop()
			if err != nil || count != wantRecoveries || got[0] != f.from[0] || got[3] != f.from[1] {
				t.Fatalf("%s: recoveries %d, senders %v, error %v", kind, count, got, err)
			}
		})
	}
}

func TestCanonicalIngressSendersFreshFieldsMixedMissesAndCancellation(t *testing.T) {
	f, txs, signer, proofs := admittedCanonicalSenderFixture(t)
	changed, err := transaction.SignNewTx(f.private[0], signer, &transaction.LegacyTx{Nonce: txs[0].Nonce(), To: &f.recipient, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(2)})
	if err != nil {
		t.Fatal(err)
	}
	mixed := []*transaction.Transaction{txs[3], changed, txs[0]}
	stop := transaction.CountSenderRecoveriesForTest()
	got, err := recoverCanonicalIngressSenders(context.Background(), mixed, signer, 2, f.chain.Config().ChainID, proofs)
	count := stop()
	if err != nil || count != 1 || !reflect.DeepEqual(got, []types.Address{f.from[1], f.from[0], f.from[0]}) {
		t.Fatalf("mixed ordered results: %v, recoveries %d, error %v", got, count, err)
	}
	resigned, err := transaction.SignNewTx(f.private[0], signer, &transaction.LegacyTx{Nonce: 1, To: &f.recipient, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(2)})
	if err != nil {
		t.Fatal(err)
	}
	stop = transaction.CountSenderRecoveriesForTest()
	got, err = recoverCanonicalIngressSenders(context.Background(), []*transaction.Transaction{resigned}, signer, 2, f.chain.Config().ChainID, proofs)
	count = stop()
	if err != nil || count != 1 || got[0] != f.from[0] {
		t.Fatal("valid re-signed next nonce did not retain direct recovery", count, err)
	}
	// Populate both caches, then mutate signed fields without clearing them.
	_ = txs[0].Hash()
	_, _ = txs[0].EthEncoded()
	txs[0].SetNonce(99)
	// A changed valid R/S may recover a different sender. The original signer
	// decides that result; a memo/cache must never turn it into a queue hit.
	want, wantErr := recoverCanonicalSenders(context.Background(), txs[:1], signer, 2)
	stop = transaction.CountSenderRecoveriesForTest()
	got, err = recoverCanonicalIngressSenders(context.Background(), txs[:1], signer, 2, f.chain.Config().ChainID, proofs)
	count = stop()
	if count != 1 || !reflect.DeepEqual(got, want) || (err != nil) != (wantErr != nil) {
		t.Fatal("fresh signed fields did not preserve direct recovery behavior")
	}
	bad := []*transaction.Transaction{txs[3], nil, changed}
	if got, err := recoverCanonicalIngressSenders(context.Background(), bad, signer, 2, f.chain.Config().ChainID, proofs); err == nil || got != nil {
		t.Fatal("mixed invalid input exposed partial senders")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := recoverCanonicalIngressSenders(ctx, mixed, signer, 2, f.chain.Config().ChainID, proofs); !errors.Is(err, context.Canceled) || got != nil {
		t.Fatal("cancellation exposed partial senders", err)
	}
}

func TestCanonicalIngressSendersTypedWrongChainAndInactiveQueueFallback(t *testing.T) {
	f, txs, signer, proofs := admittedCanonicalSenderFixture(t)
	chainID := f.chain.Config().ChainID
	typedSigner := transaction.NewEIP2930Signer(chainID)
	typed, err := transaction.SignNewTx(f.private[0], typedSigner, &transaction.AccessListTx{ChainID: uint256.MustFromBig(chainID), To: &f.recipient, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := transaction.EncodeEthereumTransaction(typed)
	proofs[crypto.Keccak256Hash(raw)] = canonicalIngressSender{address: f.from[0], raw: raw}
	stop := transaction.CountSenderRecoveriesForTest()
	got, err := recoverCanonicalIngressSenders(context.Background(), []*transaction.Transaction{typed}, typedSigner, 2, chainID, proofs)
	count := stop()
	if err != nil || count != 1 || got[0] != f.from[0] {
		t.Fatal("typed transaction did not use direct recovery", count, err)
	}
	wrong := transaction.NewEIP155Signer(new(big.Int).Add(chainID, big.NewInt(1)))
	if got, err := recoverCanonicalIngressSenders(context.Background(), txs, wrong, 2, chainID, proofs); err == nil || got != nil {
		t.Fatal("wrong chain signer reused ingress proof")
	}
	q := f.server.ingress
	q.mu.Lock()
	q.reconciling = false
	q.mu.Unlock()
	if p := f.server.snapshotCanonicalIngressSenders(q); p != nil {
		t.Fatal("snapshot authorized outside the dirty gate")
	}
	q.mu.Lock()
	q.reconciling = true
	q.closed = true
	q.mu.Unlock()
	if p := f.server.snapshotCanonicalIngressSenders(q); p != nil {
		t.Fatal("closed queue provided sender proofs")
	}
	if p := f.server.snapshotCanonicalIngressSenders(&strictIngressQueue{}); p != nil {
		t.Fatal("different queue provided sender proofs")
	}
	stop = transaction.CountSenderRecoveriesForTest()
	got, err = recoverCanonicalIngressSenders(context.Background(), txs, signer, 2, chainID, nil)
	count = stop()
	if err != nil || count != uint64(len(txs)) || got[0] != f.from[0] {
		t.Fatal("missing startup proofs bypassed direct recovery", count, err)
	}
	unprotected, err := transaction.SignNewTx(f.private[0], transaction.HomesteadSigner{}, &transaction.LegacyTx{To: &f.recipient, Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1)})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = transaction.EncodeEthereumTransaction(unprotected)
	proofs[crypto.Keccak256Hash(raw)] = canonicalIngressSender{address: f.from[0], raw: raw}
	stop = transaction.CountSenderRecoveriesForTest()
	got, err = recoverCanonicalIngressSenders(context.Background(), []*transaction.Transaction{unprotected}, signer, 2, chainID, proofs)
	count = stop()
	if err != nil || count != 1 || got[0] != f.from[0] {
		t.Fatal("unprotected legacy payload did not use direct recovery", count, err)
	}
}

func TestCanonicalIngressSendersStrictCommitReconcilesCredits(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	ctx := context.Background()
	status, _ := f.server.StrictIngressStatus()
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(ctx, f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
	}
	raws, err := f.server.ingressTransactions(10)
	if err != nil {
		t.Fatal(err)
	}
	b := strictExecute(t, f, raws)
	stop := transaction.CountSenderRecoveriesForTest()
	err = strictCommit(t, f, b, ctx)
	count := stop()
	if err != nil || count != 0 {
		t.Fatal("real strict canonical commit recovered already verified signatures", count, err)
	}
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 0 || status.RetainedEncodedBytes != 0 || status.ReservedTransactions != 0 || status.ReservedEncodedBytes != 0 || status.NewlyAdmitted != 6 || status.CryptoVerified != 6 || status.Reconciling {
		t.Fatalf("real commit changed queue credit/admission behavior: %+v", status)
	}
}

func TestCanonicalIngressSendersBoundedCommitRetainsLifetime(t *testing.T) {
	f := newBoundedExecutionFixture(t)
	ctx := context.Background()
	if err := f.server.EnableBoundedResumeIngress(ctx, f.prefixOptions()); err != nil {
		t.Fatal(err)
	}
	status, _ := f.server.StrictIngressStatus()
	for _, selected := range f.selected {
		if selected.FirstNonce != 0 {
			continue
		}
		ack, err := f.server.AdmitBoundedResumeFrame(ctx, f.server.Identity(), f.prefixID, status.Epoch, f.original[selected.ID])
		if err != nil || ack.NewlyAdmitted != int(selected.NewlyAdmitted) {
			t.Fatal("bounded original verified publication failed", err)
		}
	}
	b := executeBoundedFixtureQueue(t, f.resumeExecutionFixture, hotstuff.ViewNumber(8), 1417)
	stop := transaction.CountSenderRecoveriesForTest()
	f.commit(t, b, hotstuff.ViewNumber(8), 5)
	count := stop()
	if count != 0 {
		t.Fatal("bounded queue canonical reconciliation recovered verified signatures", count)
	}
	q := f.server.ingress
	q.mu.Lock()
	lifetime := q.resume.window.status()
	q.mu.Unlock()
	status, _ = f.server.StrictIngressStatus()
	if status.PendingTransactions != 0 || status.RetainedEncodedBytes != 0 || status.Reconciling || lifetime.SourceFrames != 4 || lifetime.SourceTransactions != 2000 || lifetime.NewTransactions != 1417 || lifetime.ReservedFrames != 0 || lifetime.ReservedNew != 0 {
		t.Fatalf("bounded commit changed credits/nonrefundable lifetime: %+v %+v", status, lifetime)
	}
}
