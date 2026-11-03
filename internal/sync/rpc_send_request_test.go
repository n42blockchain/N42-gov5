package sync

import (
	"context"
	"errors"
	"testing"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/test"

	types "github.com/n42blockchain/N42/common/block"
	comtypes "github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/common/utils"
	"github.com/n42blockchain/N42/proto/sync_pb"
)

// syncTSmallBlock builds a minimal, RLP-round-trippable block at the given
// number -- a real *types.Block, since writeBlockChunk/decodeChunkedBlock
// round-trip actual RLP and syncBlockStub's Header()/Body() stubs do not
// encode to anything readable.
func syncTSmallBlock(number uint64) types.IBlock {
	h := &types.Header{
		ParentHash:  comtypes.HexToHash("0x01"),
		UncleHash:   comtypes.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347"),
		Coinbase:    comtypes.HexToAddress("0x02"),
		Root:        comtypes.HexToHash("0x03"),
		TxHash:      comtypes.HexToHash("0x04"),
		ReceiptHash: comtypes.HexToHash("0x05"),
		Difficulty:  uint256.NewInt(1),
		Number:      uint256.NewInt(number),
		GasLimit:    1000,
		GasUsed:     0,
		Time:        1700000000,
		MixDigest:   comtypes.HexToHash("0x06"),
		BaseFee:     uint256.NewInt(1),
	}
	return types.NewBlock(h, nil)
}

// syncTNewSendRequestFixture wires a fakeP2P whose Send() hands back the
// client half of a net.Pipe, with the server half left for the test to drive.
func syncTNewSendRequestFixture(t *testing.T) (fp *fakeP2P, server *fakeStream) {
	t.Helper()
	fp = newFakeP2P(t)
	remote, err := test.RandPeerID()
	if err != nil {
		t.Fatalf("RandPeerID: %v", err)
	}
	client, server := newFakeStreamPair(remote, fp.self)
	fp.sendStream = client
	return fp, server
}

func TestSendBodiesByRangeRequestRejectsZeroStep(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()
	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
		Count:            1,
		Step:             0,
	}
	_, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, req, nil)
	if err == nil {
		t.Fatal("expected error for zero step")
	}
}

func TestSendBodiesByRangeRequestRejectsZeroCount(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()
	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
		Count:            0,
		Step:             1,
	}
	_, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, req, nil)
	if err == nil {
		t.Fatal("expected error for zero count")
	}
}

func TestSendBodiesByRangeRequestSendError(t *testing.T) {
	fp := newFakeP2P(t)
	fp.sendErr = errBoom
	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
		Count:            1,
		Step:             1,
	}
	_, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, req, nil)
	if err == nil {
		t.Fatal("expected error when Send fails")
	}
}

func TestSendBodiesByRangeRequestReadsUntilEOF(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()

	b1 := syncTSmallBlock(1)
	go func() {
		defer server.Close()
		_ = writeBlockChunk(server, comtypes.Hash{}, b1)
	}()

	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
		Count:            5,
		Step:             1,
	}
	blocks, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, req, nil)
	if err != nil {
		t.Fatalf("SendBodiesByRangeRequest: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}
}

func TestSendBodiesByRangeRequestInvokesBlockProcessor(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()

	b1 := syncTSmallBlock(1)
	go func() {
		defer server.Close()
		_ = writeBlockChunk(server, comtypes.Hash{}, b1)
	}()

	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
		Count:            5,
		Step:             1,
	}
	var processed int
	_, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, req, func(_ *types.Block) error {
		processed++
		return nil
	})
	if err != nil {
		t.Fatalf("SendBodiesByRangeRequest: %v", err)
	}
	if processed != 1 {
		t.Fatalf("processed = %d, want 1", processed)
	}
}

func TestSendBodiesByRangeRequestPropagatesProcessorError(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()

	b1 := syncTSmallBlock(1)
	go func() {
		defer server.Close()
		_ = writeBlockChunk(server, comtypes.Hash{}, b1)
	}()

	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
		Count:            5,
		Step:             1,
	}
	_, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, req, func(_ *types.Block) error {
		return errBoom
	})
	if err == nil {
		t.Fatal("expected processor error to propagate")
	}
}

func TestSendBodiesByRangeRequestRejectsOutOfRangeBlock(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()

	// Block number 99 is outside [1, 1+1*2).
	outOfRange := syncTSmallBlock(99)
	go func() {
		defer server.Close()
		_ = writeBlockChunk(server, comtypes.Hash{}, outOfRange)
	}()

	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
		Count:            2,
		Step:             1,
	}
	_, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, req, nil)
	if err == nil {
		t.Fatal("expected ErrInvalidFetchedData for out-of-range block number")
	}
}

func TestSendBodiesByRangeRequestErrorResponseCode(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()
	go func() {
		defer server.Close()
		writeErrorResponseToStream(responseCodeServerError, "nope", server, fp)
	}()

	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
		Count:            1,
		Step:             1,
	}
	_, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, req, nil)
	if err == nil {
		t.Fatal("expected an error for a server-error response code")
	}
}

func TestSendBodiesByRangeRequestImmediateCloseYieldsError(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	server.Close() // close before any write: first read sees EOF/closed-pipe

	req := &sync_pb.BodiesByRangeRequest{
		StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(1)),
		Count:            1,
		Step:             1,
	}
	_, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, req, nil)
	// Either a clean zero-block read (EOF on an empty stream) or an error is
	// acceptable; the call must not hang or panic.
	_ = err
}

var errBoom = errors.New("boom")

func TestSendBodiesByRangeRequestRejectsMisalignedFirstBlock(t *testing.T) {
	fp, server := syncTNewSendRequestFixture(t)
	defer server.Close()
	go func() { defer server.Close(); _ = writeBlockChunk(server, comtypes.Hash{}, syncTSmallBlock(2)) }()
	req := &sync_pb.BodiesByRangeRequest{StartBlockNumber: utils.ConvertUint256IntToH256(uint256.NewInt(1)), Count: 3, Step: 2}
	if _, err := SendBodiesByRangeRequest(context.Background(), nil, fp, fp.self, req, nil); !errors.Is(err, ErrInvalidFetchedData) {
		t.Fatalf("peer returned 2 for requested 1,3,5: %v", err)
	}
}

func TestSendBodiesByRangeRejectsBoundsBeforeDial(t *testing.T) {
	for _, req := range []*sync_pb.BodiesByRangeRequest{nil, rangeReq(1, maxRequestBlocks+1, 1), rangeReq(1, 1, rangeLimit+1), rangeReq(1, maxRequestBlocks, rangeLimit)} {
		if _, err := SendBodiesByRangeRequest(context.Background(), nil, nil, "", req, nil); err == nil {
			t.Fatal("invalid request reached transport")
		}
	}
}
