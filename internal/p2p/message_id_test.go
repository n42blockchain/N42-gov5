package p2p

import (
	"bytes"
	"runtime"
	"testing"

	pubsubpb "github.com/libp2p/go-libp2p-pubsub/pb"
	"github.com/n42blockchain/N42/common/hash"
	"github.com/n42blockchain/N42/common/types"
)

// concatMsgID is the previous implementation: hash the concatenation.
func concatMsgID(genesisHash types.Hash, pmsg *pubsubpb.Message) string {
	topic := pmsg.GetTopic()
	combined := make([]byte, 0, len(genesisHash)+len(topic)+len(pmsg.Data))
	combined = append(combined, genesisHash[:]...)
	combined = append(combined, topic...)
	combined = append(combined, pmsg.Data...)
	h := hash.Hash(combined)
	return string(h[:20])
}

func TestMsgIDMatchesConcatenation(t *testing.T) {
	genesis := types.BytesToHash([]byte{0x42, 0x01, 0x02})
	blockTopic := "/n42/a2d2ff5d/beacon_block/ssz_snappy"
	otherTopic := "/n42/a2d2ff5d/attestation/ssz_snappy"
	big := bytes.Repeat([]byte{0xab, 0xcd, 0xef}, 1<<20)
	cases := []*pubsubpb.Message{
		{},
		{Topic: &blockTopic},
		{Data: []byte{1}},
		{Topic: &blockTopic, Data: []byte("payload")},
		{Topic: &otherTopic, Data: []byte("payload")},
		{Topic: &blockTopic, Data: big},
	}
	for i, m := range cases {
		if got, want := MsgID(genesis, m), concatMsgID(genesis, m); got != want {
			t.Fatalf("case %d: MsgID %x, concatenation %x", i, got, want)
		}
		if len(MsgID(genesis, m)) != 20 {
			t.Fatalf("case %d: id length %d", i, len(MsgID(genesis, m)))
		}
	}
	// Topic scoping still separates identical bytes.
	if MsgID(genesis, cases[3]) == MsgID(genesis, cases[4]) {
		t.Fatal("identical data on different topics produced the same id")
	}
}

func TestMsgIDDoesNotCopyTheMessage(t *testing.T) {
	genesis := types.BytesToHash([]byte{7})
	topic := "/n42/a2d2ff5d/beacon_block/ssz_snappy"
	m := &pubsubpb.Message{Topic: &topic, Data: make([]byte, 8<<20)}
	const calls = 10
	allocated := func(f func()) uint64 {
		f() // warm the hasher pool
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		for i := 0; i < calls; i++ {
			f()
		}
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc
	}
	// The measure must be able to see a copy: the concatenation allocates
	// ~8 MB a call.
	if got := allocated(func() { _ = concatMsgID(genesis, m) }); got < calls*(8<<20) {
		t.Fatalf("the measure missed the concatenation's copies: %d bytes over %d calls", got, calls)
	}
	if got := allocated(func() { _ = MsgID(genesis, m) }); got > 1<<20 {
		t.Fatalf("MsgID allocated %d bytes over %d calls on an 8 MB message", got, calls)
	}
}
