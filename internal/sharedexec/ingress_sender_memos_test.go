package sharedexec

import (
	"context"
	"math/big"
	"runtime"
	"testing"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
)

func admitAllStrictFrames(t *testing.T, f strictIngressFixture) [16]byte {
	t.Helper()
	status, err := f.server.StrictIngressStatus()
	if err != nil {
		t.Fatal(err)
	}
	for _, frame := range f.frames {
		if _, err := f.server.AdmitStrictFrame(context.Background(), f.server.Identity(), status.Epoch, frame); err != nil {
			t.Fatal(err)
		}
	}
	return status.Epoch
}

func senderMemoCandidate(t *testing.T, f strictIngressFixture, txs []*transaction.Transaction) *block.Block {
	t.Helper()
	h := block.CopyHeader(f.template.Header().(*block.Header))
	h.TxHash = block.TxRootAt(txs, h.Time)
	h.ResetHashCache()
	return block.NewBlock(h, txs).(*block.Block)
}

func TestSeedLiveIngressSenderMemosRequiresExactLiveQueueEvidence(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	epoch := admitAllStrictFrames(t, f)
	var txs []*transaction.Transaction
	for _, frame := range f.frames {
		for _, raw := range strictFrameRaws(t, frame) {
			txn, err := transaction.DecodeEthereumTransaction(raw)
			if err != nil {
				t.Fatal(err)
			}
			txs = append(txs, txn)
		}
	}
	b := senderMemoCandidate(t, f, txs)
	for i, txn := range b.Transactions() {
		if txn.From() != nil {
			t.Fatalf("transaction %d unexpectedly had a sender before the helper", i)
		}
	}

	f.server.execute.Lock()
	seeded := f.server.seedLiveIngressSenderMemos(b, &epoch)
	f.server.execute.Unlock()
	if seeded != len(txs) {
		t.Fatalf("seeded %d exact queue transactions, want %d", seeded, len(txs))
	}
	header := b.Header().(*block.Header)
	signer := transaction.MakeSignerWithTimestamp(f.chain.Config(), header.Number.ToBig(), header.Time)
	for i, txn := range b.Transactions() {
		want := f.from[i/3]
		got, err := transaction.Sender(signer, txn)
		if err != nil || got != want {
			t.Fatalf("transaction %d sender memo = %s, want %s (err %v)", i, got, want, err)
		}
	}
}

func TestSeedLiveIngressSenderMemosSkipsUnprovenAndPreMemoedTransactions(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	epoch := admitAllStrictFrames(t, f)
	var txs []*transaction.Transaction
	for _, raw := range strictFrameRaws(t, f.frames[0]) {
		txn, err := transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, txn)
	}
	header := f.template.Header().(*block.Header)
	actualSigner := transaction.MakeSignerWithTimestamp(f.chain.Config(), header.Number.ToBig(), header.Time)
	fake := types.Address{19: 0xf1}
	txs[0].SetFrom(fake)
	txs[0].SetSenderMemo(actualSigner, fake)

	changed, err := transaction.SignNewTx(f.private[0], actualSigner, &transaction.LegacyTx{
		Nonce: txs[1].Nonce(), To: &f.recipient, Gas: 21000,
		GasPrice: uint256.NewInt(1), Value: uint256.NewInt(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	typed, err := transaction.SignNewTx(f.private[0], transaction.NewEIP2930Signer(f.chain.Config().ChainID), &transaction.AccessListTx{
		ChainID: uint256.MustFromBig(f.chain.Config().ChainID), To: &f.recipient,
		Gas: 21000, GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongChainSigner := transaction.NewEIP155Signer(new(big.Int).Add(f.chain.Config().ChainID, big.NewInt(1)))
	wrongChain, err := transaction.SignNewTx(f.private[0], wrongChainSigner, &transaction.LegacyTx{
		Nonce: 88, To: &f.recipient, Gas: 21000,
		GasPrice: uint256.NewInt(1), Value: uint256.NewInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	txs = append(txs, changed, typed, wrongChain)
	b := senderMemoCandidate(t, f, txs)

	f.server.execute.Lock()
	seeded := f.server.seedLiveIngressSenderMemos(b, &epoch)
	f.server.execute.Unlock()
	if seeded != 2 {
		t.Fatalf("seeded %d transactions, want only the two exact, un-memoed legacy entries", seeded)
	}
	if got, err := transaction.Sender(actualSigner, txs[0]); err != nil || got != fake {
		t.Fatalf("pre-existing memo changed: got %s, want %s (err %v)", got, fake, err)
	}
	stop := transaction.CountSenderRecoveriesForTest()
	for i, want := range []types.Address{f.from[0], f.from[0]} {
		if got, err := transaction.Sender(actualSigner, txs[i+1]); err != nil || got != want {
			t.Fatalf("exact queue transaction %d memo = %s, want %s (err %v)", i+1, got, want, err)
		}
	}
	if _, err := transaction.Sender(actualSigner, txs[3]); err != nil {
		t.Fatalf("valid non-queue transaction did not retain direct recovery: %v", err)
	}
	if got, err := transaction.Sender(actualSigner, txs[4]); err != nil || got != f.from[0] {
		t.Fatalf("unproven typed transaction did not retain direct recovery: got %s (err %v)", got, err)
	}
	if _, err := transaction.Sender(actualSigner, txs[5]); err == nil {
		t.Fatal("wrong-chain transaction unexpectedly recovered")
	}
	if got := stop(); got != 2 {
		t.Fatalf("unproven fallback recoveries = %d, want changed legacy and typed payloads", got)
	}
	for i, txn := range txs[3:] {
		if got := txn.From(); got != nil {
			t.Fatalf("unproven transaction %d received a sender memo: %s", i, *got)
		}
	}
}

func TestSeedLiveIngressSenderMemosFailsClosedOnEpochAndQueueGates(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	epoch := admitAllStrictFrames(t, f)
	var txs []*transaction.Transaction
	for _, raw := range strictFrameRaws(t, f.frames[0]) {
		txn, err := transaction.DecodeEthereumTransaction(raw)
		if err != nil {
			t.Fatal(err)
		}
		txs = append(txs, txn)
	}
	b := senderMemoCandidate(t, f, txs)
	wrongEpoch := epoch
	wrongEpoch[0] ^= 1
	zeroEpoch := [16]byte{}

	assertNoSeed := func(label string, requested *[16]byte) {
		t.Helper()
		f.server.execute.Lock()
		got := f.server.seedLiveIngressSenderMemos(b, requested)
		f.server.execute.Unlock()
		if got != 0 {
			t.Fatalf("%s gate seeded %d senders", label, got)
		}
		for i, txn := range txs {
			if txn.From() != nil {
				t.Fatalf("%s gate memoized transaction %d", label, i)
			}
		}
	}
	assertNoSeed("wrong epoch", &wrongEpoch)
	assertNoSeed("zero epoch", &zeroEpoch)

	q := f.server.ingress
	set := func(mutate func()) {
		t.Helper()
		q.mu.Lock()
		mutate()
		q.mu.Unlock()
	}
	for _, gate := range []struct {
		name string
		set  func(bool)
	}{
		{"closed", func(v bool) { set(func() { q.closed = v }) }},
		{"reconciling", func(v bool) { set(func() { q.reconciling = v }) }},
		{"bootstrap pending", func(v bool) { set(func() { q.bootstrapPending = v }) }},
	} {
		gate.set(true)
		assertNoSeed(gate.name, &epoch)
		gate.set(false)
	}
	set(func() { q.pending = 0 })
	assertNoSeed("empty queue", &epoch)
}

func TestSeedLiveIngressSenderMemosReplacesNilFromStaleMemo(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	epoch := admitAllStrictFrames(t, f)
	raw := strictFrameRaws(t, f.frames[0])[0]
	txn, err := transaction.DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	header := f.template.Header().(*block.Header)
	signer := transaction.MakeSignerWithTimestamp(f.chain.Config(), header.Number.ToBig(), header.Time)
	fake := types.Address{19: 0xf2}
	txn.SetSenderMemo(signer, fake)
	if txn.From() != nil || !txn.HasSenderMemo(signer) {
		t.Fatal("fixture did not create a memo-only candidate")
	}
	b := senderMemoCandidate(t, f, []*transaction.Transaction{txn})
	f.server.execute.Lock()
	seeded := f.server.seedLiveIngressSenderMemos(b, &epoch)
	f.server.execute.Unlock()
	if seeded != 1 || !txn.HasSenderMemo(signer) {
		t.Fatalf("exact proof did not replace the memo-only hint: seeded=%d", seeded)
	}
	got, err := transaction.Sender(signer, txn)
	if err != nil || got != f.from[0] {
		t.Fatalf("fresh queue proof retained stale sender %s, want %s (err %v)", got, f.from[0], err)
	}
}

func TestSeedLiveIngressSenderMemosParallelAndCanceledContext(t *testing.T) {
	f := newStrictIngressFixture(t, 10, 1<<20)
	epoch := admitAllStrictFrames(t, f)
	raw := strictFrameRaws(t, f.frames[0])[0]
	txn, err := transaction.DecodeEthereumTransaction(raw)
	if err != nil {
		t.Fatal(err)
	}
	txs := make([]*transaction.Transaction, 4096)
	for i := range txs {
		txs[i] = txn
	}
	b := senderMemoCandidate(t, f, txs)
	oldProcs := runtime.GOMAXPROCS(2)
	f.server.execute.Lock()
	seeded := f.server.seedLiveIngressSenderMemos(b, &epoch)
	f.server.execute.Unlock()
	runtime.GOMAXPROCS(oldProcs)
	if seeded != len(txs) {
		t.Fatalf("parallel scan seeded %d memo candidates, want %d", seeded, len(txs))
	}
	header := b.Header().(*block.Header)
	signer := transaction.MakeSignerWithTimestamp(f.chain.Config(), header.Number.ToBig(), header.Time)
	got, err := transaction.Sender(signer, txn)
	if err != nil || got != f.from[0] {
		t.Fatalf("parallel memo sender = %s, want %s (err %v)", got, f.from[0], err)
	}

	canceled := newStrictIngressFixture(t, 10, 1<<20)
	canceledEpoch := admitAllStrictFrames(t, canceled)
	canceledTxn, err := transaction.DecodeEthereumTransaction(strictFrameRaws(t, canceled.frames[0])[0])
	if err != nil {
		t.Fatal(err)
	}
	canceledBlock := senderMemoCandidate(t, canceled, []*transaction.Transaction{canceledTxn})
	canceledHeader := canceledBlock.Header().(*block.Header)
	canceledSigner := transaction.MakeSignerWithTimestamp(canceled.chain.Config(), canceledHeader.Number.ToBig(), canceledHeader.Time)
	canceled.server.Close()
	canceled.server.execute.Lock()
	seeded = canceled.server.seedLiveIngressSenderMemos(canceledBlock, &canceledEpoch)
	canceled.server.execute.Unlock()
	if seeded != 0 || canceledTxn.HasSenderMemo(canceledSigner) {
		t.Fatalf("canceled context published sender memo: seeded=%d", seeded)
	}
}

func TestQueueBuildSenderMemosPreserveSealedImportAndCertifiedCommit(t *testing.T) {
	ctx := context.Background()
	f := newStrictIngressFixture(t, 10, 1<<20)
	admitAllStrictFrames(t, f)
	request := queueRequest(t, f, f.template.Header().(*block.Header), 6)
	built, err := f.server.BuildFromQueue(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	var candidate block.Block
	if err := candidate.Unmarshal(built.BlockRLP); err != nil {
		t.Fatal(err)
	}
	sealed := make(chan block.IBlock, 1)
	if err := f.leader.Seal(f.chain, &candidate, sealed, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	b := (<-sealed).(*block.Block)
	raw, err := b.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	imported, err := f.server.Import(ctx, ImportRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), BlockRLP: raw})
	if err != nil || !imported.Durable || imported.Result != built.Result {
		t.Fatalf("queue build/import result diverged: durable=%t err=%v", imported.Durable, err)
	}
	commit := CertifiedCommitRequest{
		CommitRequest: CommitRequest{Identity: f.server.Identity(), BlockHash: b.Hash(), ParentHash: b.ParentHash(), Number: b.Number64().Uint64()},
		Certificate:   *commitProof(f.keys, b.Hash(), 5, false),
	}
	if _, err := f.server.CommitCertified(ctx, commit); err != nil {
		t.Fatal(err)
	}
	status, err := f.server.StrictIngressStatus()
	if err != nil || status.PendingTransactions != 0 {
		t.Fatalf("certified queue commit left pending entries: %+v (%v)", status, err)
	}
}
