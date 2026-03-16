package download

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"unsafe"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/n42blockchain/N42/api/protocol/sync_proto"
	"github.com/n42blockchain/N42/api/protocol/types_pb"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/block"
	"google.golang.org/protobuf/proto"
)

type downloadChainStub struct {
	common.IBlockChain
	current block.IBlock
}

func (s *downloadChainStub) CurrentBlock() block.IBlock {
	return s.current
}

func TestRequireCurrentBlockNumberRejectsNilNumber(t *testing.T) {
	chain := &downloadChainStub{
		current: testDownloadBlock(nil),
	}

	_, err := requireCurrentBlockNumber(chain, "current block number unavailable")
	if err == nil || err.Error() != "current block number unavailable" {
		t.Fatalf("requireCurrentBlockNumber() error = %v", err)
	}
}

func TestFindAncestorRejectsNilCurrentBlockNumber(t *testing.T) {
	d := &Downloader{
		bc: &downloadChainStub{
			current: testDownloadBlock(nil),
		},
	}

	_, err := d.findAncestor()
	if err == nil || err.Error() != "current block number unavailable" {
		t.Fatalf("findAncestor() error = %v", err)
	}
}

func TestNewDownloaderAllowsNilCurrentBlockNumber(t *testing.T) {
	downloader := NewDownloader(context.Background(), &downloadChainStub{
		current: testDownloadBlock(nil),
	}, nil, nil, common.PeerMap{})

	concrete, ok := downloader.(*Downloader)
	if !ok {
		t.Fatalf("NewDownloader() type = %T", downloader)
	}
	if concrete.highestNumber.Uint64() != 0 {
		t.Fatalf("highestNumber = %d, want 0", concrete.highestNumber.Uint64())
	}
}

func TestNewDownloaderUsesPeerHeightWhenCurrentBlockNumberMissing(t *testing.T) {
	peerID := peer.ID("peer-1")
	downloader := NewDownloader(context.Background(), &downloadChainStub{
		current: testDownloadBlock(nil),
	}, nil, nil, common.PeerMap{
		peerID: {
			CurrentHeight: uint256.NewInt(9),
		},
	})

	concrete, ok := downloader.(*Downloader)
	if !ok {
		t.Fatalf("NewDownloader() type = %T", downloader)
	}
	if concrete.highestNumber.Uint64() != 9 {
		t.Fatalf("highestNumber = %d, want 9", concrete.highestNumber.Uint64())
	}
}

func TestRequireProtoBlockNumberRejectsNilHeader(t *testing.T) {
	_, err := requireProtoBlockNumber(&types_pb.Block{}, "block number unavailable")
	if err == nil || err.Error() != "header is nil" {
		t.Fatalf("requireProtoBlockNumber() error = %v", err)
	}
}

func TestProcessBodiesRejectsNilProtoBlockHeader(t *testing.T) {
	d := &Downloader{
		ctx:                 context.Background(),
		blockProcCh:         make(chan *bodyResponse, 1),
		bodyTaskPool:        make([]*blockTask, 0),
		bodyProcessingTasks: make(map[uint64]*blockTask),
		bodyResultStore:     make(map[uint256.Int]*types_pb.Block),
	}
	d.once = sync.Once{}
	d.blockProcCh <- &bodyResponse{
		taskID: 1,
		bodies: []*types_pb.Block{{}},
	}

	err := d.processBodies()
	if err == nil || err.Error() != "header is nil" {
		t.Fatalf("processBodies() error = %v", err)
	}
}

func TestConnHandlerRejectsMismatchedPayload(t *testing.T) {
	peerID := peer.ID("peer-1")
	d := &Downloader{
		ctx:          context.Background(),
		headerProcCh: make(chan *headerResponse, 1),
		blockProcCh:  make(chan *bodyResponse, 1),
		peersInfo: newPeersInfo(context.Background(), common.PeerMap{
			peerID: {},
		}),
	}

	msg := &sync_proto.SyncTask{
		Id:       1,
		SyncType: sync_proto.SyncType_HeaderRes,
		Payload: &sync_proto.SyncTask_SyncBlockResponse{
			SyncBlockResponse: &sync_proto.SyncBlockResponse{},
		},
	}
	data, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	err = d.ConnHandler(data, peerID)
	if !errors.Is(err, ErrInvalidSyncTaskPayload) {
		t.Fatalf("ConnHandler() error = %v, want %v", err, ErrInvalidSyncTaskPayload)
	}
	select {
	case <-d.headerProcCh:
		t.Fatal("headerProcCh received payload for mismatched task")
	default:
	}
}

func TestStartRejectsNilNetwork(t *testing.T) {
	downloader := NewDownloader(context.Background(), &downloadChainStub{
		current: testDownloadBlock(uint256.NewInt(1)),
	}, nil, nil, common.PeerMap{})

	err := downloader.Start()
	if !errors.Is(err, ErrInvalidNetwork) {
		t.Fatalf("Start() error = %v, want %v", err, ErrInvalidNetwork)
	}
}

func testDownloadBlock(number *uint256.Int) *block.Block {
	blk := &block.Block{}
	setDownloadBlockField(blk, "header", &block.Header{
		Number:     number,
		Difficulty: uint256.NewInt(1),
		BaseFee:    uint256.NewInt(0),
	})
	setDownloadBlockField(blk, "body", &block.Body{})
	return blk
}

func setDownloadBlockField(target interface{}, name string, value interface{}) {
	field := reflect.ValueOf(target).Elem().FieldByName(name)
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}
