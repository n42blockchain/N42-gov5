package internal

import "testing"

func TestSenderSrcLogFieldsDelta(t *testing.T) {
	senderRecoverTxsLogged.Store(senderRecoverTxsTxs.Load())
	before := senderPickedUnreadyAfterGrace.Load()
	beforeLocal := senderLocalFillRecovered.Load()
	senderRecoverTxsTxs.Add(5)
	CountPickedUnreadyAfterGrace(2)
	CountPickedUnreadyAfterGrace(0)
	CountLocalFillRecovered(3)
	f := senderSrcLogFields()
	m := map[string]uint64{}
	for i := 0; i+1 < len(f); i += 2 {
		m[f[i].(string)] = f[i+1].(uint64)
	}
	if m["recoverTxsBlk"] != 5 {
		t.Fatalf("recoverTxsBlk = %d, want 5", m["recoverTxsBlk"])
	}
	if senderPickedUnreadyAfterGrace.Load()-before != 2 || senderLocalFillRecovered.Load()-beforeLocal != 3 {
		t.Fatal("counter deltas wrong")
	}
	if g := senderSrcLogFields(); g[1].(uint64) != 0 {
		t.Fatalf("second line delta = %d, want 0", g[1])
	}
}

func TestSenderJobRecoverTxsCountsNilFree(t *testing.T) {
	before := senderRecoverTxsTxs.Load()
	j := &senderJob{}
	j.recoverTxs([32]byte{}, nil)
	if senderRecoverTxsTxs.Load() != before {
		t.Fatal("empty frame must not count")
	}
}
