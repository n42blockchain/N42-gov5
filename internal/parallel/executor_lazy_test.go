package parallel

import (
	"fmt"
	"testing"
)

// TestExecutor_SharedCounterRepeated is TestExecutor_SequentialConsistency run
// many times: the lazy-validation rule had a window in which a provisional
// transaction was re-executed under an unchanged incarnation, and it showed
// in about 1 run in 30.
func TestExecutor_SharedCounterRepeated(t *testing.T) {
	for run := 0; run < 200; run++ {
		numTxs := 20
		sharedKey := balanceKey(0)
		var parExec *Executor
		parExec = NewExecutor(numTxs, 4, func(txIndex int, rw *ReadWriteSet) error {
			val, writerTx, writerInc, found := parExec.MVS().Read(sharedKey, txIndex)
			if found {
				rw.RecordRead(sharedKey, writerTx, writerInc, false)
			} else {
				rw.RecordRead(sharedKey, -1, 0, true)
				val = []byte{0}
			}
			rw.RecordWrite(sharedKey, []byte{val[0] + 1})
			return nil
		})
		parExec.Run()
		val, _, _, found := parExec.MVS().Read(sharedKey, numTxs)
		if !found || val[0] != byte(numTxs) {
			t.Fatalf("run %d: expected %d, got %v", run, numTxs, val)
		}
	}
}

// TestExecutor_SenderChainsWithAffinity models the benchmark block: S senders
// each with a nonce chain of L transfers to recipients drawn from a small
// set. With sender affinity each chain runs on one worker in order and
// never conflicts with itself; only recipient credits from different
// senders reach the validator. Correctness against the sequential result,
// and the wave count must stay far below the limit.
func TestExecutor_SenderChainsWithAffinity(t *testing.T) {
	const S, L, R = 40, 30, 8
	numTxs := S * L
	sender := func(i int) int { return i % S } // interleave senders, chain order by index
	recipient := func(i int) int { return (i * 7) % R }
	senderKey := func(s int) LocationKey { return balanceKey(byte(100 + s)) }
	recipKey := func(r int) LocationKey { return balanceKey(byte(200 + r)) }

	run := func(withAffinity bool) (map[LocationKey]byte, int64, int64) {
		var ex *Executor
		ex = NewExecutor(numTxs, 8, func(i int, rw *ReadWriteSet) error {
			read := func(k LocationKey) byte {
				v, wtx, winc, found := ex.MVS().Read(k, i)
				if found {
					rw.RecordRead(k, wtx, winc, false)
					return v[0]
				}
				rw.RecordRead(k, -1, 0, true)
				return 0
			}
			sk, rk := senderKey(sender(i)), recipKey(recipient(i))
			sv, rv := read(sk), read(rk)
			rw.RecordWrite(sk, []byte{sv + 1}) // nonce-like counter
			rw.RecordWrite(rk, []byte{rv + 1}) // credit
			return nil
		})
		if withAffinity {
			ex.SetAffinity(func(i int) uint64 { return uint64(sender(i)) })
		}
		res := ex.Run()
		for i, r := range res {
			if r.Err != nil {
				t.Fatalf("tx %d: %v", i, r.Err)
			}
		}
		out := map[LocationKey]byte{}
		for s := 0; s < S; s++ {
			v, _, _, _ := ex.MVS().Read(senderKey(s), numTxs)
			out[senderKey(s)] = v[0]
		}
		for r := 0; r < R; r++ {
			v, _, _, _ := ex.MVS().Read(recipKey(r), numTxs)
			out[recipKey(r)] = v[0]
		}
		e, a := ex.Stats()
		return out, e, a
	}
	got, execs, aborts := run(true)
	for s := 0; s < S; s++ {
		if got[senderKey(s)] != L {
			t.Fatalf("sender %d: expected %d, got %d", s, L, got[senderKey(s)])
		}
	}
	total := 0
	for r := 0; r < R; r++ {
		total += int(got[recipKey(r)])
	}
	if total != numTxs {
		t.Fatalf("credits: expected %d, got %d", numTxs, total)
	}
	if execs > int64(numTxs)*4 {
		t.Fatalf("too many executions for %d txs: %d (aborts %d)", numTxs, execs, aborts)
	}
	fmt.Printf("affinity: %d txs, %d executions, %d aborts\n", numTxs, execs, aborts)
}
