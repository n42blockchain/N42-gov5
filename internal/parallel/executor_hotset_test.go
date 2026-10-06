package parallel

import (
	"encoding/binary"
	"testing"
)

// TestExecutor_HotRecipientBlock models round 35d's block: 15,204 transfers
// from 4,000 senders (nonce chains interleaved by index) to a hot set of
// 22,857 recipients, 32 workers pinned by sender. Version-only validation
// cascaded every recipient conflict down the sender's whole chain and hit
// the 64-wave limit (aborts 611,562 of 617,414 executions); value
// validation keeps a dependant whose input bytes did not change.
func TestExecutor_HotRecipientBlock(t *testing.T) {
	const S, R, numTxs, workers = 4000, 22857, 15204, 32
	sender := func(i int) int { return i % S }
	recipient := func(i int) int { return int(uint32(i)*2654435761>>7) % R }
	key := func(tag byte, n int) LocationKey {
		var k LocationKey
		k.Field = FieldBalance
		k.Address[0] = tag
		binary.BigEndian.PutUint32(k.Address[1:5], uint32(n))
		return k
	}
	enc := func(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
	dec := func(b []byte) uint32 {
		if len(b) == 0 {
			return 0
		}
		return binary.BigEndian.Uint32(b)
	}

	var ex *Executor
	ex = NewExecutor(numTxs, workers, func(i int, rw *ReadWriteSet) error {
		read := func(k LocationKey) uint32 {
			v, wtx, winc, found := ex.MVS().Read(k, i)
			if found {
				rw.RecordReadValue(k, wtx, winc, false, v)
				return dec(v)
			}
			rw.RecordReadValue(k, -1, 0, true, nil)
			return 0
		}
		sk, rk := key(1, sender(i)), key(2, recipient(i))
		sv, rv := read(sk), read(rk)
		rw.RecordWrite(sk, enc(sv+1)) // nonce advances; independent of the recipient
		rw.RecordWrite(rk, enc(rv+1)) // credit
		return nil
	})
	ex.SetAffinity(func(i int) uint64 { return uint64(sender(i)) })
	res := ex.Run()
	for i, r := range res {
		if r.Err != nil {
			t.Fatalf("tx %d: %v", i, r.Err)
		}
	}
	if ex.FellBack() {
		e, a := ex.Stats()
		t.Fatalf("sequential fallback after %d waves (executions %d, aborts %d)", ex.Waves(), e, a)
	}
	for s := 0; s < S; s++ {
		v, _, _, _ := ex.MVS().Read(key(1, s), numTxs)
		want := uint32(numTxs / S)
		if s < numTxs%S {
			want++
		}
		if dec(v) != want {
			t.Fatalf("sender %d: expected %d, got %d", s, want, dec(v))
		}
	}
	total := uint32(0)
	for r := 0; r < R; r++ {
		v, _, _, _ := ex.MVS().Read(key(2, r), numTxs)
		total += dec(v)
	}
	if total != numTxs {
		t.Fatalf("credits: expected %d, got %d", numTxs, total)
	}
	e, a := ex.Stats()
	t.Logf("waves %d executions %d aborts %d", ex.Waves(), e, a)
	if e > 3*numTxs {
		t.Fatalf("executions %d for %d txs: the cascade is back", e, numTxs)
	}
}
