// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.

package miner

import (
	"bytes"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/holiman/uint256"

	"github.com/n42blockchain/N42/cmd/evmsdk"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal"
	"github.com/n42blockchain/N42/internal/streamverify"
	"github.com/n42blockchain/N42/internal/vm"
	"github.com/n42blockchain/N42/modules/state"
)

// mpSeal reproduces what the HotStuff seal does to a header: the trailing
// reserved bytes of Extra are filled in place with the signature, which changes
// the header hash (Header.Hash() covers all of Extra).
func mpSeal(t *testing.T, unsealed block.IBlock) *block.Block {
	t.Helper()
	hdr, ok := unsealed.Header().(*block.Header)
	if !ok {
		t.Fatalf("header is %T", unsealed.Header())
	}
	sealed := block.CopyHeader(hdr)
	extra := make([]byte, 40+96) // vanity/view area plus the reserved 96-byte seal
	copy(extra, sealed.Extra)
	for i := 40; i < len(extra); i++ {
		extra[i] = 0xAB
	}
	sealed.Extra = extra
	sealed.ResetHashCache()
	b, ok := unsealed.(*block.Block)
	if !ok {
		t.Fatalf("block is %T", unsealed)
	}
	return b.WithSeal(sealed)
}

func mpWaitPacket(t *testing.T, ch <-chan *evmsdk.StreamPacket) *evmsdk.StreamPacket {
	t.Helper()
	select {
	case p := <-ch:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no mobile packet was delivered")
		return nil
	}
}

// The packet must carry, and be keyed by, the hash of the SEALED block, and the
// header inside it must be the sealed header.
func TestMobilePacketJobBuildsFromSealedHeader(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))
	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase
	tk := mnTSealOneBlock(t, w, f)

	job := newMobilePacketJob(tk.block.Transactions(), streamverify.NewReadLogRecorder(nil, nil))
	unsealedHash := tk.block.Hash()
	sealed := mpSeal(t, tk.block) // WithSeal mutates in place, as the real Seal does
	if sealed.Hash() == unsealedHash {
		t.Fatal("test setup: sealing did not change the hash")
	}

	pkt, num, err := job.buildSealed(sealed)
	if err != nil {
		t.Fatalf("buildSealed: %v", err)
	}
	if pkt.BlockHash != sealed.Hash() {
		t.Fatalf("packet keyed by %x, want the sealed hash %x (unsealed %x)", pkt.BlockHash, sealed.Hash(), unsealedHash)
	}
	if num != sealed.Number64().Uint64() {
		t.Fatalf("packet number %d, want %d", num, sealed.Number64().Uint64())
	}
	var inner block.Header
	if err := inner.Unmarshal(pkt.HeaderRLP); err != nil {
		t.Fatalf("decode packet header: %v", err)
	}
	if inner.Hash() != sealed.Hash() {
		t.Fatalf("header inside the packet hashes to %x, want the sealed hash %x", inner.Hash(), sealed.Hash())
	}
}

// launch delivers once, and a second launch of the same job is ignored.
func TestMobilePacketJobLaunchesOnce(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))
	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase
	tk := mnTSealOneBlock(t, w, f)
	sealed := mpSeal(t, tk.block)

	var calls atomic.Int32
	got := make(chan *evmsdk.StreamPacket, 4)
	sink := func(p *evmsdk.StreamPacket, _ uint64) { calls.Add(1); got <- p }

	job := newMobilePacketJob(tk.block.Transactions(), streamverify.NewReadLogRecorder(nil, nil))
	job.launch(sealed, sink)
	job.launch(sealed, sink)
	mpWaitPacket(t, got)
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 1 {
		t.Fatalf("sink called %d times, want 1", n)
	}

	// Nil job and nil sink are no-ops.
	var nilJob *mobilePacketJob
	nilJob.launch(sealed, sink)
	newMobilePacketJob(nil, streamverify.NewReadLogRecorder(nil, nil)).launch(sealed, nil)
	if newMobilePacketJob(nil, nil) != nil {
		t.Fatal("a job without a recorder must be nil")
	}
}

// The whole path: commit attaches the job to the task, the seal changes the
// hash, handleSealed stores the block and the packet that reaches the sink is
// the SEALED block's.
func TestMobilePacketIsKeyedBySealedHashThroughHandleSealed(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))
	w := mnTBareWorker(t, f, &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0)})
	w.coinbase = f.Coinbase

	var mu sync.Mutex
	got := make(chan *evmsdk.StreamPacket, 4)
	w.mobilePacketSink = func(p *evmsdk.StreamPacket, _ uint64) {
		mu.Lock()
		defer mu.Unlock()
		got <- p
	}

	atomic.StoreInt32(&w.running, 1)
	env, ibs := mnTBuildFilledEnv(t, w, f)
	rec := streamverify.NewReadLogRecorder(nil, nil)
	if err := w.commit(env, state.NewNoopWriter(), ibs, time.Now(), nil, nil, rec, false, types.Hash{}, time.Time{}, time.Time{}, 0); err != nil {
		t.Fatalf("commit: %v", err)
	}
	var tk *task
	select {
	case tk = <-w.taskCh:
	default:
		t.Fatal("commit did not send a task")
	}
	if tk.mobilePacket == nil {
		t.Fatal("commit did not attach the packet job to the task")
	}
	select {
	case p := <-got:
		t.Fatalf("a packet was delivered at assembly time, before the seal (key %x)", p.BlockHash)
	case <-time.After(150 * time.Millisecond):
	}

	unsealedHash := tk.block.Hash()
	sealed := mpSeal(t, tk.block)
	if sealed.Hash() == unsealedHash {
		t.Fatal("test setup: sealing did not change the hash")
	}
	w.mu.Lock()
	w.pendingTasks[w.engine.SealHash(sealed.Header())] = tk
	w.mu.Unlock()
	before := f.Chain.CurrentBlock().Number64().Uint64()
	w.handleSealed(sealed)
	if f.Chain.CurrentBlock().Number64().Uint64() != before+1 {
		t.Fatalf("the sealed block was not stored")
	}

	pkt := mpWaitPacket(t, got)
	if pkt.BlockHash != sealed.Hash() {
		t.Fatalf("packet keyed by %x, want the sealed hash %x (unsealed %x)", pkt.BlockHash, sealed.Hash(), unsealedHash)
	}
	var inner block.Header
	if err := inner.Unmarshal(pkt.HeaderRLP); err != nil {
		t.Fatalf("decode packet header: %v", err)
	}
	if inner.Hash() != sealed.Hash() {
		t.Fatalf("header inside the packet hashes to %x, want %x", inner.Hash(), sealed.Hash())
	}
}

// The read log must hold exactly the reads a verifier makes re-executing the
// block's transactions. The stale-nonce trim reads every pending sender before
// the fill; read through the recorder, those reads opened the log (in map
// order) and shifted every later read, so verifiers failed every block with a
// transaction ("nonce too high/low").
func TestFillTransactionsReadLogMatchesReplay(t *testing.T) {
	f := mnTNewChainFixture(t)
	tx0 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(1_000))
	tx1 := f.mnTSignedTransfer(t, 0, f.Senders[1], uint256.NewInt(2_000))

	w := mnTNewIdleWorker(t, f, &mnTStubTxsPool{})
	w.setCoinbase(f.Coinbase)
	_, env := mnTPrepareEnvAndState(t, w, f)
	w.txsPool = &mnTStubTxsPool{pending: mnTByAddr(f.Senders[0], tx0, tx1)}

	roTx, err := f.DB.BeginRo(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer roTx.Rollback()
	base := state.NewPlainStateReader(roTx)
	rec := streamverify.NewReadLogRecorder(base, nil)
	env.untracedReader = base
	ibs := state.New(rec)
	getHeader := func(types.Hash, uint64) *block.Header { return nil }
	if err := w.fillTransactions(nil, env, ibs, getHeader, nil, newBuildStallWatchdog(false, ""), nil); err != nil {
		t.Fatalf("fillTransactions: %v", err)
	}
	if len(env.txs) != 2 {
		t.Fatalf("included %d txs, want 2", len(env.txs))
	}
	// Nothing was reverted: the packet ships this log as recorded, no rebuild.
	if rec.Diverged() {
		t.Fatal("a fill with no failed candidate marked the read log diverged")
	}

	replay := streamverify.NewReadLogRecorder(base, nil)
	ribs := state.New(replay)
	gp := new(common.GasPool).AddGas(env.header.GasLimit)
	var used uint64
	for i, tx := range env.txs {
		ribs.Prepare(tx.Hash(), types.Hash{}, i)
		if _, _, err := internal.ApplyTransaction(w.chainConfig, func(uint64) types.Hash { return types.Hash{} }, w.engine,
			&env.coinbase, gp, ribs, state.NewNoopWriter(), env.header, tx, &used, vm.Config{}); err != nil {
			t.Fatalf("replay tx %d: %v", i, err)
		}
	}
	got, want := rec.Log(), replay.Log()
	if len(got) != len(want) {
		t.Fatalf("leader logged %d reads, a replay of its transactions makes %d", len(got), len(want))
	}
	for i := range got {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("read %d: leader logged %+v, replay read %+v", i, got[i], want[i])
		}
	}
}

// mpTransfer signs a DynamicFeeTx with an explicit nonce and tip, so a test can
// hand the pool candidates that fail (nonce gap, unaffordable value) and price
// them ahead of the ones that are included.
func (f *mnTChainFixture) mpTransfer(t *testing.T, senderIdx int, nonce, tip uint64, to types.Address, value *uint256.Int) *transaction.Transaction {
	t.Helper()
	inner := &transaction.DynamicFeeTx{
		ChainID:   uint256.MustFromBig(f.Config.ChainID),
		Nonce:     nonce,
		GasTipCap: uint256.NewInt(tip),
		GasFeeCap: uint256.NewInt(1_000_000_000_000),
		Gas:       500_000,
		To:        &to,
		Value:     value,
	}
	signed, err := transaction.SignTx(transaction.NewTx(inner), f.Signer, f.SenderKeys[senderIdx])
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// mpReplayReadLog executes txs the way a verifier does -- block-start system
// calls, then each transaction, on a fresh IntraBlockState -- and returns the
// reads it made against the parent state.
func mpReplayReadLog(t *testing.T, w *worker, env *environment, base state.StateReader, txs []*transaction.Transaction) []evmsdk.ReadLogEntry {
	t.Helper()
	replay := streamverify.NewReadLogRecorder(base, nil)
	ribs := state.New(replay)
	if err := internal.ProcessExecutionBlockStart(env.header.ParentBeaconRoot, w.chainConfig, ribs, env.header, w.engine); err != nil {
		t.Fatalf("replay block start: %v", err)
	}
	gp := new(common.GasPool).AddGas(env.header.GasLimit)
	var used uint64
	for i, tx := range txs {
		ribs.Prepare(tx.Hash(), types.Hash{}, i)
		if _, _, err := internal.ApplyTransaction(w.chainConfig, func(uint64) types.Hash { return types.Hash{} }, w.engine,
			&env.coinbase, gp, ribs, state.NewNoopWriter(), env.header, tx, &used, vm.Config{}); err != nil {
			t.Fatalf("replay tx %d: %v", i, err)
		}
	}
	return replay.Log()
}

// mpAssertSameReadLog fails at the first read where the packet's log and the
// replay's disagree.
func mpAssertSameReadLog(t *testing.T, gotData []byte, want []evmsdk.ReadLogEntry) {
	t.Helper()
	wantData := evmsdk.EncodeReadLog(want)
	if bytes.Equal(gotData, wantData) {
		return
	}
	got, err := evmsdk.DecodeReadLog(gotData)
	if err != nil {
		t.Fatalf("decode packet read log: %v", err)
	}
	wantRT, err := evmsdk.DecodeReadLog(wantData)
	if err != nil {
		t.Fatalf("decode replay read log: %v", err)
	}
	for i := 0; i < len(got) && i < len(wantRT); i++ {
		if !reflect.DeepEqual(got[i], wantRT[i]) {
			t.Fatalf("read %d of %d: packet logs %+v, a replay of the block reads %+v", i, len(wantRT), got[i], wantRT[i])
		}
	}
	t.Fatalf("packet logs %d reads, a replay of the block makes %d", len(got), len(wantRT))
}

// mpFillWithRecorder runs block start and the fill the way commitWork does,
// with the read-log recorder over the parent state, and returns the recorder
// and the parent reader.
func mpFillWithRecorder(t *testing.T, w *worker, f *mnTChainFixture, env *environment) (*streamverify.ReadLogRecorder, state.StateReader) {
	t.Helper()
	roTx, err := f.DB.BeginRo(w.ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(roTx.Rollback)
	base := state.NewPlainStateReader(roTx)
	rec := streamverify.NewReadLogRecorder(base, nil)
	env.untracedReader = base
	ibs := state.New(rec)
	if err := internal.ProcessExecutionBlockStart(env.header.ParentBeaconRoot, w.chainConfig, ibs, env.header, w.engine); err != nil {
		t.Fatal(err)
	}
	getHeader := func(types.Hash, uint64) *block.Header { return nil }
	if err := w.fillTransactions(nil, env, ibs, getHeader, nil, newBuildStallWatchdog(false, ""), nil); err != nil {
		t.Fatalf("fillTransactions: %v", err)
	}
	return rec, base
}

// mpFailingCandidatesPool hands the fill two candidates priced first that fail
// after reading their sender (a nonce gap, a value the sender cannot afford)
// and two included transactions crediting exactly those senders.
func mpFailingCandidatesPool(t *testing.T, f *mnTChainFixture) map[types.Address][]*transaction.Transaction {
	t.Helper()
	s := f.Senders
	const high, low = 5_000_000_000, 2_000_000_000
	gap := f.mpTransfer(t, 2, 5, high, s[0], uint256.NewInt(1))
	poor := f.mpTransfer(t, 3, 0, high, s[0], new(uint256.Int).Mul(uint256.NewInt(200), uint256.NewInt(1e18)))
	inc0 := f.mpTransfer(t, 0, 0, low, s[2], uint256.NewInt(1_000))
	inc1 := f.mpTransfer(t, 1, 0, low-1, s[3], uint256.NewInt(2_000)) // distinct tip: a fixed order
	return map[types.Address][]*transaction.Transaction{s[0]: {inc0}, s[1]: {inc1}, s[2]: {gap}, s[3]: {poor}}
}

// A candidate that reads state and then fails is reverted and left out of the
// block, but its reads stayed in the log and the accounts it loaded stayed
// cached, so a later included transaction touching them read nothing. The
// verifier never runs the failed candidate: every read after it landed on the
// wrong entry and the block failed verification on every device.
func TestMobilePacketReadLogExcludesFailedCandidates(t *testing.T) {
	f := mnTNewChainFixture(t)
	w := mnTNewIdleWorker(t, f, &mnTStubTxsPool{})
	w.setCoinbase(f.Coinbase)
	_, env := mnTPrepareEnvAndState(t, w, f)
	w.txsPool = &mnTStubTxsPool{pending: mpFailingCandidatesPool(t, f)}

	rec, base := mpFillWithRecorder(t, w, f, env)
	if len(env.txs) != 2 {
		t.Fatalf("included %d txs, want 2 (the gap and the unaffordable candidate fail)", len(env.txs))
	}

	pkt := mpBuildPacket(t, w, env, rec)
	mpAssertSameReadLog(t, pkt.ReadLogData, mpReplayReadLog(t, w, env, base, env.txs))

	// And a device accepts it: the verifier's own re-execution over the
	// packet reproduces the header's receipts root.
	res, err := evmsdk.ExecuteAndVerifyV2(pkt, evmsdk.NewCodeCache(16), w.chainConfig)
	if err != nil {
		t.Fatalf("verifier rejected the packet: %v", err)
	}
	if res.TxCount != 2 {
		t.Fatalf("verifier executed %d txs, want 2", res.TxCount)
	}
}

// The read-log fix is an observer: the same fill with and without the
// recorder includes the same transactions with the same receipts and leaves
// the same state root.
func TestMobilePacketRecorderDoesNotChangeBlock(t *testing.T) {
	type out struct {
		txs      []types.Hash
		receipts types.Hash
		root     types.Hash
	}
	f := mnTNewChainFixture(t)
	pending := mpFailingCandidatesPool(t, f)
	build := func(record bool) out {
		w := mnTNewIdleWorker(t, f, &mnTStubTxsPool{})
		w.setCoinbase(f.Coinbase)
		_, env := mnTPrepareEnvAndState(t, w, f)
		// The fill consumes the map it is handed; give each build its own.
		own := make(map[types.Address][]*transaction.Transaction, len(pending))
		for a, l := range pending {
			own[a] = append([]*transaction.Transaction(nil), l...)
		}
		w.txsPool = &mnTStubTxsPool{pending: own}
		roTx, err := f.DB.BeginRo(w.ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(roTx.Rollback)
		var reader state.StateReader = state.NewPlainStateReader(roTx)
		env.untracedReader = reader
		if record {
			reader = streamverify.NewReadLogRecorder(reader, nil)
		}
		ibs := state.New(reader)
		getHeader := func(types.Hash, uint64) *block.Header { return nil }
		if err := w.fillTransactions(nil, env, ibs, getHeader, nil, newBuildStallWatchdog(false, ""), nil); err != nil {
			t.Fatalf("fillTransactions: %v", err)
		}
		o := out{receipts: internal.ReceiptsRootFor(w.chainConfig, env.header.Number.Uint64(), env.receipts), root: ibs.IntermediateRoot()}
		for _, tx := range env.txs {
			o.txs = append(o.txs, tx.Hash())
		}
		return o
	}
	plain, recorded := build(false), build(true)
	if len(plain.txs) != 2 {
		t.Fatalf("included %d txs, want 2", len(plain.txs))
	}
	if plain.root == (types.Hash{}) {
		t.Fatal("the fill computed no state root to compare")
	}
	if !reflect.DeepEqual(plain, recorded) {
		t.Fatalf("the recorder changed the block:\nwithout %+v\nwith    %+v", plain, recorded)
	}
}

// mpBuildPacket binds the build's receipts root into the header (as
// FinalizeAndAssemble does) and builds the packet through the job commit
// attaches to the task.
func mpBuildPacket(t *testing.T, w *worker, env *environment, rec *streamverify.ReadLogRecorder) *evmsdk.StreamPacket {
	t.Helper()
	env.header.ReceiptHash = internal.ReceiptsRootFor(w.chainConfig, env.header.Number.Uint64(), env.receipts)
	pkt, _, err := w.mobilePacketJobFor(env, rec).buildSealed(block.NewBlock(env.header, env.txs))
	if err != nil {
		t.Fatalf("buildSealed: %v", err)
	}
	return pkt
}

// A candidate the gas pool cannot hold fails (ErrGasLimitReached) after its
// sender was read; the account it loaded is credited by an included tx.
func TestMobilePacketReadLogExcludesGasLimitCandidate(t *testing.T) {
	f := mnTNewChainFixture(t)
	s := f.Senders
	// The log carries no keys: give the included sender a state the hog's
	// sender does not share, or a swapped order would compare equal.
	f.mnTBuildAndInsert(t, []*transaction.Transaction{f.mpTransfer(t, 0, 0, 2_000_000_000, s[1], uint256.NewInt(7))})
	hog := f.mpTransfer(t, 2, 0, 5_000_000_000, s[0], uint256.NewInt(1))
	hog = mpWithGas(t, f, 2, hog, 40_000_000) // above the block gas limit
	inc := f.mpTransfer(t, 0, 1, 2_000_000_000, s[2], uint256.NewInt(1_000))

	w := mnTNewIdleWorker(t, f, &mnTStubTxsPool{})
	w.setCoinbase(f.Coinbase)
	_, env := mnTPrepareEnvAndState(t, w, f)
	w.txsPool = &mnTStubTxsPool{pending: map[types.Address][]*transaction.Transaction{s[0]: {inc}, s[2]: {hog}}}

	rec, base := mpFillWithRecorder(t, w, f, env)
	if len(env.txs) != 1 || env.txs[0].Hash() != inc.Hash() {
		t.Fatalf("included %d txs, want only the transfer", len(env.txs))
	}
	mpAssertSameReadLog(t, mpBuildPacket(t, w, env, rec).ReadLogData, mpReplayReadLog(t, w, env, base, env.txs))
	if !rec.Diverged() {
		t.Fatal("a reverted candidate did not mark the read log diverged")
	}
}

// mpWithGas re-signs tx with a different gas limit.
func mpWithGas(t *testing.T, f *mnTChainFixture, senderIdx int, tx *transaction.Transaction, gas uint64) *transaction.Transaction {
	t.Helper()
	to := *tx.To()
	inner := &transaction.DynamicFeeTx{
		ChainID:   uint256.MustFromBig(f.Config.ChainID),
		Nonce:     tx.Nonce(),
		GasTipCap: tx.GasTipCap(),
		GasFeeCap: tx.GasFeeCap(),
		Gas:       gas,
		To:        &to,
		Value:     tx.Value(),
	}
	signed, err := transaction.SignTx(transaction.NewTx(inner), f.Signer, f.SenderKeys[senderIdx])
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// The parallel fill (N42_MINER_PARALLEL_FILL=1) reads through its workers'
// own readers, around the recorder. Its log is always rebuilt, and the
// rebuild either reproduces the replay exactly or fails the packet; it never
// ships a log a device would consume shifted.
func TestMobilePacketParallelFillNeverShipsShiftedLog(t *testing.T) {
	t.Setenv("N42_MINER_PARALLEL_FILL", "1")
	f := mnTNewChainFixture(t)
	w := mnTNewIdleWorker(t, f, &mnTStubTxsPool{})
	w.setCoinbase(f.Coinbase)
	_, env := mnTPrepareEnvAndState(t, w, f)
	w.txsPool = &mnTStubTxsPool{pending: mpFailingCandidatesPool(t, f)}

	rec, base := mpFillWithRecorder(t, w, f, env)
	if len(env.txs) == 0 {
		t.Fatal("the parallel fill included nothing")
	}
	env.header.ReceiptHash = internal.ReceiptsRootFor(w.chainConfig, env.header.Number.Uint64(), env.receipts)
	pkt, _, err := w.mobilePacketJobFor(env, rec).buildSealed(block.NewBlock(env.header, env.txs))
	if err != nil {
		t.Logf("parallel fill: packet dropped: %v", err)
	} else {
		t.Logf("parallel fill: log rebuilt (%d txs)", len(env.txs))
		mpAssertSameReadLog(t, pkt.ReadLogData, mpReplayReadLog(t, w, env, base, env.txs))
	}
	if !rec.Diverged() {
		t.Fatal("the parallel fill did not mark the read log diverged")
	}
}
