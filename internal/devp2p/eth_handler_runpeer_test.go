package devp2p

import (
	"errors"
	"math/big"
	"testing"

	gethp2p "github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/p2p/enode"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"

	n42block "github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/params"
)

// runPeerHeadStub is a minimal BlockProvider feeding runPeer's handshake.
type runPeerHeadStub struct {
	head     *n42block.Header
	headHash types.Hash
	err      error
}

func (p *runPeerHeadStub) CurrentHead() (*n42block.Header, types.Hash, error) {
	return p.head, p.headHash, p.err
}
func (p *runPeerHeadStub) GetHeaderByNumber(uint64) (*n42block.Header, error) { return nil, nil }
func (p *runPeerHeadStub) GetHeaderByHash(types.Hash) (*n42block.Header, error) {
	return nil, nil
}

func runPeerTestPeer() *gethp2p.Peer {
	return gethp2p.NewPeer(enode.ID{}, "runpeer-test", nil)
}

func newRunPeerHandler(t *testing.T, genesis types.Hash) *EthHandler {
	t.Helper()
	h, err := NewEthHandler(&params.ChainConfig{ChainID: big.NewInt(7)}, genesis, 0, &runPeerHeadStub{
		head: &n42block.Header{Number: uint256.NewInt(10), Time: 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestRunPeerCurrentHeadErrorIsReturned(t *testing.T) {
	genesis := types.HexToHash("0x01")
	h, err := NewEthHandler(&params.ChainConfig{ChainID: big.NewInt(7)}, genesis, 0, &runPeerHeadStub{err: assertErr{}})
	if err != nil {
		t.Fatal(err)
	}
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()
	if err := h.runPeer(runPeerTestPeer(), rw1, 69); err == nil {
		t.Fatal("expected error when CurrentHead fails")
	}
}

type assertErr struct{}

func (assertErr) Error() string { return "boom" }

func TestRunPeerEth69HandshakeAcceptsMatchingPeer(t *testing.T) {
	genesis := types.HexToHash("0x0102")
	h := newRunPeerHandler(t, genesis)
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- h.runPeer(runPeerTestPeer(), rw1, 69) }()

	// Read our own status.
	statusMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	var sent statusPacket
	if err := statusMsg.Decode(&sent); err != nil {
		t.Fatal(err)
	}
	if sent.Genesis != genesis {
		t.Fatalf("sent genesis = %s, want %s", sent.Genesis, genesis)
	}

	// Reply with a matching eth/69 status.
	reply := statusPacket{
		ProtocolVersion: 69,
		NetworkID:       sent.NetworkID,
		Genesis:         genesis,
		ForkID:          sent.ForkID,
		LatestBlock:     5,
	}
	if err := gethp2p.Send(rw2, 0, &reply); err != nil {
		t.Fatal(err)
	}

	// Close the pipe from our side to end the handler's read loop. The
	// handshake itself already succeeded (we got past both the network and
	// genesis checks above); the loop then dies on the closed pipe.
	rw2.Close()
	if err := <-errCh; !errors.Is(err, gethp2p.ErrPipeClosed) {
		t.Fatalf("runPeer() error = %v, want ErrPipeClosed (message loop ended on closed pipe)", err)
	}
}

func TestRunPeerEth69HandshakeRejectsNetworkMismatch(t *testing.T) {
	genesis := types.HexToHash("0x0102")
	h := newRunPeerHandler(t, genesis)
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- h.runPeer(runPeerTestPeer(), rw1, 69) }()

	statusMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	var sent statusPacket
	if err := statusMsg.Decode(&sent); err != nil {
		t.Fatal(err)
	}

	reply := statusPacket{
		ProtocolVersion: 69,
		NetworkID:       sent.NetworkID + 1, // mismatch
		Genesis:         genesis,
		ForkID:          sent.ForkID,
	}
	if err := gethp2p.Send(rw2, 0, &reply); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err == nil {
		t.Fatal("expected network ID mismatch error")
	}
}

func TestRunPeerEth69HandshakeRejectsGenesisMismatch(t *testing.T) {
	genesis := types.HexToHash("0x0102")
	h := newRunPeerHandler(t, genesis)
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- h.runPeer(runPeerTestPeer(), rw1, 69) }()

	statusMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	var sent statusPacket
	if err := statusMsg.Decode(&sent); err != nil {
		t.Fatal(err)
	}

	reply := statusPacket{
		ProtocolVersion: 69,
		NetworkID:       sent.NetworkID,
		Genesis:         types.HexToHash("0xdead"),
		ForkID:          sent.ForkID,
	}
	if err := gethp2p.Send(rw2, 0, &reply); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err == nil {
		t.Fatal("expected genesis mismatch error")
	}
}

func TestRunPeerEth68HandshakeAcceptsMatchingPeer(t *testing.T) {
	genesis := types.HexToHash("0x0304")
	h := newRunPeerHandler(t, genesis)
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- h.runPeer(runPeerTestPeer(), rw1, 68) }()

	statusMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	var sent statusPacket68
	if err := statusMsg.Decode(&sent); err != nil {
		t.Fatal(err)
	}
	if sent.Genesis != genesis {
		t.Fatalf("sent genesis = %s, want %s", sent.Genesis, genesis)
	}

	reply := statusPacket68{
		ProtocolVersion: 68,
		NetworkID:       sent.NetworkID,
		TD:              big.NewInt(0),
		Head:            types.HexToHash("0x9999"),
		Genesis:         genesis,
		ForkID:          sent.ForkID,
	}
	if err := gethp2p.Send(rw2, 0, &reply); err != nil {
		t.Fatal(err)
	}
	rw2.Close()
	if err := <-errCh; !errors.Is(err, gethp2p.ErrPipeClosed) {
		t.Fatalf("runPeer() error = %v, want ErrPipeClosed (message loop ended on closed pipe)", err)
	}
}

func TestRunPeerRejectsNonStatusFirstMessage(t *testing.T) {
	genesis := types.HexToHash("0x0102")
	h := newRunPeerHandler(t, genesis)
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- h.runPeer(runPeerTestPeer(), rw1, 69) }()

	statusMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	statusMsg.Discard()
	// Send something other than code 0 as the "status". runPeer rejects this
	// without reading the payload, so WriteMsg blocks until the pipe closes —
	// send asynchronously and let the deferred Close() release it.
	go gethp2p.Send(rw2, 5, &struct{}{})
	if err := <-errCh; err == nil {
		t.Fatal("expected error for non-status first message")
	}
}

func TestRunPeerRejectsUndecodableStatus(t *testing.T) {
	genesis := types.HexToHash("0x0102")
	h := newRunPeerHandler(t, genesis)
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- h.runPeer(runPeerTestPeer(), rw1, 69) }()

	statusMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	statusMsg.Discard()
	// A bare uint64 matches neither the eth/68 nor eth/69 Status struct layout.
	if err := gethp2p.Send(rw2, 0, uint64(42)); err != nil {
		t.Fatal(err)
	}
	if err := <-errCh; err == nil {
		t.Fatal("expected decode error for malformed status")
	}
}

func TestRunPeerDispatchesPostHandshakeMessages(t *testing.T) {
	genesis := types.HexToHash("0x0102")
	h := newRunPeerHandler(t, genesis)
	rw1, rw2 := gethp2p.MsgPipe()
	defer rw1.Close()
	defer rw2.Close()

	errCh := make(chan error, 1)
	go func() { errCh <- h.runPeer(runPeerTestPeer(), rw1, 69) }()

	statusMsg, err := rw2.ReadMsg()
	if err != nil {
		t.Fatal(err)
	}
	var sent statusPacket
	if err := statusMsg.Decode(&sent); err != nil {
		t.Fatal(err)
	}
	reply := statusPacket{
		ProtocolVersion: 69,
		NetworkID:       sent.NetworkID,
		Genesis:         genesis,
		ForkID:          sent.ForkID,
	}
	if err := gethp2p.Send(rw2, 0, &reply); err != nil {
		t.Fatal(err)
	}

	// Past the handshake, the message loop dispatches via handleMessage.
	// Send an unhandled code: the loop must swallow the "error" path (there is
	// none for unknown codes) and keep running until we close the pipe.
	if err := gethp2p.Send(rw2, 2, &[]rlp.RawValue{}); err != nil {
		t.Fatal(err)
	}
	rw2.Close()
	if err := <-errCh; !errors.Is(err, gethp2p.ErrPipeClosed) {
		t.Fatalf("runPeer() error = %v, want ErrPipeClosed (message loop ended on closed pipe)", err)
	}
}
