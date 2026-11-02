package devp2p

import (
	"bytes"
	"testing"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/n42blockchain/N42/common/transaction"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/network/eth69"
)

// serveOnce runs a handler for an encoded request and returns the reply.
func serveOnce(t *testing.T, req any, handle func(gethp2p.MsgReadWriter, gethp2p.Msg) error, resp any) {
	t.Helper()
	encoded, err := rlp.EncodeToBytes(req)
	if err != nil {
		t.Fatal(err)
	}
	msg := gethp2p.Msg{Size: uint32(len(encoded)), Payload: bytes.NewReader(encoded)}
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()
	errCh := make(chan error, 1)
	go func() { errCh <- handle(rw1, msg) }()
	reply, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	if err := reply.Decode(resp); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
}

// One hash repeated in a request must not multiply the node's largest body
// without limit: each copy is retained until the whole reply is encoded.
func TestHandleGetBlockBodiesStopsAtSoftLimit(t *testing.T) {
	raw, err := rlp.EncodeToBytes(bytes.Repeat([]byte{1}, 512*1024))
	if err != nil {
		t.Fatal(err)
	}
	hash := types.HexToHash("0x01")
	provider := &servingFakeProvider{bodies: map[types.Hash]*BlockBody{
		hash: {Transactions: []rlp.RawValue{raw}, Uncles: []rlp.RawValue{}},
	}}
	h := &EthHandler{provider: provider}

	hashes := make([]types.Hash, 1024)
	for i := range hashes {
		hashes[i] = hash
	}
	var resp blockBodiesPacket
	serveOnce(t, &getBlockBodiesPacket{RequestID: 3, Hashes: hashes}, h.handleGetBlockBodies, &resp)

	if want := softResponseLimit/len(raw) + 1; len(resp.Bodies) != want {
		t.Fatalf("served %d bodies, want %d", len(resp.Bodies), want)
	}
}

func TestHandleGetPooledTransactionsStopsAtSoftLimit(t *testing.T) {
	signed := pooledTestSignedTx(t, 2)
	pool := &pooledTxPoolStub{byHash: map[types.Hash]*transaction.Transaction{signed.Hash(): signed}}
	h := &EthHandler{txpool: pool}

	encoded, err := transaction.EncodeEthereumPooledTransaction(signed)
	if err != nil {
		t.Fatal(err)
	}
	hashes := make([]types.Hash, softResponseLimit/len(encoded)+1000)
	for i := range hashes {
		hashes[i] = signed.Hash()
	}
	var resp pooledTransactionsPacket
	serveOnce(t, &eth69.GetPooledTransactionsPacket{RequestID: 9, Hashes: hashes}, h.handleGetPooledTransactions, &resp)

	size := 0
	for _, tx := range resp.Transactions {
		size += len(tx)
	}
	if size >= softResponseLimit+2*len(encoded) {
		t.Fatalf("served %d txs totalling %d bytes", len(resp.Transactions), size)
	}
}
