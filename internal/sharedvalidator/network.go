package sharedvalidator

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/n42blockchain/N42/common/block"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/internal/consensus/hotstuff"
	"github.com/n42blockchain/N42/internal/p2p"
	"github.com/n42blockchain/N42/internal/sharedexec"
	"github.com/n42blockchain/N42/lib/kv"
	"github.com/n42blockchain/N42/lib/rlp"
	"github.com/n42blockchain/N42/modules/rawdb"
)

const bodyProtocol = "/n42/shared-validator/blocks/1"
const (
	requestStatus byte = iota
	requestHash
	requestNumber
)
const maxReplyMetadata = 4096

type bodyReply struct {
	Identity    sharedexec.Identity         `json:"identity"`
	Header      []byte                      `json:"header_rlp"`
	Certificate *hotstuff.QuorumCertificate `json:"certificate,omitempty"`
}

// Network is the validator's metadata-only block source. The supplied real P2P
// service must already enforce performance Noise/namespace/static-peer binding.
// It never uses the normal sync path or constructs an execution BlockChain.
type Network struct {
	replica         *Replica
	p2p             *p2p.Service
	service         *hotstuff.Service
	ctx             context.Context
	cancel          context.CancelFunc
	mu              sync.Mutex
	started, closed bool
	jobs            map[types.Hash]struct{}
	streams         map[network.Stream]struct{}
	wake            chan struct{}
	syncWake        chan struct{}
	serveSlots      chan struct{}
	maxHeight       atomic.Uint64
	wg              sync.WaitGroup
	stopped         chan struct{}
}

func NewNetwork(ctx context.Context, replica *Replica, pub *p2p.Service) (*Network, error) {
	if ctx == nil || replica == nil || pub == nil || pub.Host() == nil {
		return nil, errors.New("metadata network requires actual replica and scoped P2P host")
	}
	wire := pub.WireTopic(bodyProtocol)
	if wire == bodyProtocol || !strings.Contains(wire, hex.EncodeToString(replica.identity.GenesisHash[:])) || !strings.Contains(wire, replica.identity.Namespace) {
		return nil, errors.New("metadata network requires performance genesis-bound P2P service")
	}
	ctx, cancel := context.WithCancel(ctx)
	return &Network{replica: replica, p2p: pub, ctx: ctx, cancel: cancel, jobs: make(map[types.Hash]struct{}), streams: make(map[network.Stream]struct{}), wake: make(chan struct{}, 1), syncWake: make(chan struct{}, 1), serveSlots: make(chan struct{}, 2), stopped: make(chan struct{})}, nil
}

func (n *Network) AttachService(service *hotstuff.Service) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.started || n.closed || n.service != nil {
		return errors.New("metadata network service must attach once before Start")
	}
	if err := n.replica.AttachService(service, n); err != nil {
		return err
	}
	n.service = service
	return nil
}

func (n *Network) Start() error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed || n.started || n.service == nil {
		return errors.New("metadata network requires attached service and one Start")
	}
	n.started = true
	n.p2p.Host().SetStreamHandler(protocol.ID(bodyProtocol), n.serve)
	n.wg.Add(2)
	go n.fetchLoop()
	go n.catchupLoop()
	return nil
}

func (n *Network) Stop() {
	n.mu.Lock()
	if n.closed {
		n.mu.Unlock()
		<-n.stopped
		return
	}
	n.closed = true
	n.cancel()
	n.p2p.Host().RemoveStreamHandler(protocol.ID(bodyProtocol))
	streams := make([]network.Stream, 0, len(n.streams))
	for stream := range n.streams {
		streams = append(streams, stream)
	}
	n.mu.Unlock()
	for _, stream := range streams {
		_ = stream.Reset()
	}
	n.wg.Wait()
	close(n.stopped)
}

func (n *Network) track(stream network.Stream) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.closed {
		return false
	}
	n.streams[stream] = struct{}{}
	n.wg.Add(1)
	return true
}
func (n *Network) untrack(stream network.Stream) {
	n.mu.Lock()
	delete(n.streams, stream)
	n.mu.Unlock()
	n.wg.Done()
}

func writeFrame(w io.Writer, data []byte, limit int) error {
	if len(data) > limit {
		return errors.New("metadata frame exceeds limit")
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(data)))
	if _, err := io.Copy(w, bytes.NewReader(size[:])); err != nil {
		return err
	}
	_, err := io.Copy(w, bytes.NewReader(data))
	return err
}
func readFrame(r io.Reader, limit int) ([]byte, error) {
	var size [4]byte
	if _, err := io.ReadFull(r, size[:]); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(size[:])
	if uint64(length) > uint64(limit) {
		return nil, errors.New("metadata frame exceeds limit")
	}
	data := make([]byte, int(length))
	_, err := io.ReadFull(r, data)
	return data, err
}

func (n *Network) serve(stream network.Stream) {
	if !n.track(stream) {
		_ = stream.Reset()
		return
	}
	defer n.untrack(stream)
	defer stream.Close()
	select {
	case n.serveSlots <- struct{}{}:
		defer func() { <-n.serveSlots }()
	default:
		_ = stream.Reset()
		return
	}
	_ = stream.SetDeadline(time.Now().Add(10 * time.Second))
	var request [33]byte
	if _, err := io.ReadFull(stream, request[:]); err != nil {
		return
	}
	var tail [1]byte
	if count, err := stream.Read(tail[:]); count != 0 || err != io.EOF {
		return
	}
	var b block.IBlock
	var header block.IHeader
	switch request[0] {
	case requestStatus:
		header = n.replica.currentHeader()
	case requestHash:
		var hash types.Hash
		copy(hash[:], request[1:])
		_ = n.replica.db.View(n.ctx, func(tx kv.Tx) error {
			concrete, err := rawdb.ReadBlockByHash(tx, hash)
			if concrete != nil {
				b = concrete
			}
			return err
		})
	case requestNumber:
		for _, v := range request[9:] {
			if v != 0 {
				return
			}
		}
		var err error
		b, err = n.replica.GetBlockByNumber(uint256.NewInt(binary.BigEndian.Uint64(request[1:9])))
		if err != nil {
			return
		}
	default:
		return
	}
	if b != nil {
		header = b.Header()
	}
	reply := bodyReply{Identity: n.replica.identity}
	var payload []byte
	if header != nil {
		var err error
		reply.Header, err = rlp.EncodeToBytes(header)
		if err != nil {
			return
		}
		if header.Number64().Uint64() > 0 {
			var hash types.Hash
			if b != nil {
				hash = b.Hash()
			} else {
				hash = header.Hash()
			}
			reply.Certificate, err = n.service.CertificateForBlock(n.ctx, hash)
			if err != nil {
				return
			}
		}
		if request[0] != requestStatus {
			payload, err = b.Marshal()
			if err != nil || len(payload) > sharedexec.MaxBlockBytes {
				return
			}
		}
	}
	meta, err := json.Marshal(reply)
	if err != nil {
		return
	}
	if err := writeFrame(stream, meta, maxReplyMetadata); err != nil {
		return
	}
	if err := writeFrame(stream, payload, sharedexec.MaxBlockBytes); err != nil {
		return
	}
	_ = stream.CloseWrite()
}

func (n *Network) request(ctx context.Context, id peer.ID, kind byte, hash types.Hash, number uint64) (bodyReply, []byte, error) {
	var zero bodyReply
	rpcCtx, cancel := context.WithTimeout(n.ctx, 10*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	stream, err := n.p2p.Host().NewStream(rpcCtx, id, protocol.ID(bodyProtocol))
	if err != nil {
		return zero, nil, err
	}
	if !n.track(stream) {
		_ = stream.Reset()
		return zero, nil, errors.New("metadata network stopped")
	}
	defer n.untrack(stream)
	defer stream.Close()
	deadline, _ := rpcCtx.Deadline()
	_ = stream.SetDeadline(deadline)
	var request [33]byte
	request[0] = kind
	if kind == requestHash {
		copy(request[1:], hash[:])
	} else if kind == requestNumber {
		binary.BigEndian.PutUint64(request[1:9], number)
	}
	if _, err := io.Copy(stream, bytes.NewReader(request[:])); err != nil {
		return zero, nil, err
	}
	if err := stream.CloseWrite(); err != nil {
		return zero, nil, err
	}
	meta, err := readFrame(stream, maxReplyMetadata)
	if err != nil {
		return zero, nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(meta))
	decoder.DisallowUnknownFields()
	var reply bodyReply
	if err := decoder.Decode(&reply); err != nil {
		return zero, nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return zero, nil, errors.New("metadata response trailing JSON")
	}
	if reply.Identity != n.replica.identity {
		return zero, nil, errors.New("metadata response identity mismatch")
	}
	payload, err := readFrame(stream, sharedexec.MaxBlockBytes)
	if err != nil {
		return zero, nil, err
	}
	var tail [1]byte
	if count, err := stream.Read(tail[:]); count != 0 || err != io.EOF {
		return zero, nil, errors.New("metadata response trailing frame")
	}
	return reply, payload, nil
}

func (n *Network) FetchBlockByHash(hash types.Hash) {
	if hash == (types.Hash{}) || hash == n.replica.identity.GenesisHash {
		return
	}
	n.mu.Lock()
	_, exists := n.jobs[hash]
	if !n.closed && (exists || len(n.jobs) < 128) {
		n.jobs[hash] = struct{}{}
	}
	n.mu.Unlock()
	select {
	case n.wake <- struct{}{}:
	default:
	}
}
func (n *Network) CatchUp() {
	select {
	case n.syncWake <- struct{}{}:
	default:
	}
}
func (n *Network) HeightBehind() uint64 {
	head := n.replica.currentHeader()
	if head == nil {
		return 0
	}
	local := head.Number64().Uint64()
	remote := n.maxHeight.Load()
	if remote > local {
		return remote - local
	}
	return 0
}

func (n *Network) fetchLoop() {
	defer n.wg.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
		case <-n.wake:
		}
		n.mu.Lock()
		hashes := make([]types.Hash, 0, len(n.jobs))
		for hash := range n.jobs {
			hashes = append(hashes, hash)
		}
		n.mu.Unlock()
		for _, hash := range hashes {
			for _, id := range n.p2p.Host().Network().Peers() {
				reply, raw, err := n.request(n.ctx, id, requestHash, hash, 0)
				if err != nil || len(raw) == 0 {
					continue
				}
				if n.receive(reply, raw, hash) {
					n.mu.Lock()
					delete(n.jobs, hash)
					n.mu.Unlock()
					break
				}
			}
		}
	}
}

func (n *Network) receive(reply bodyReply, raw []byte, expected types.Hash) bool {
	var b block.Block
	if err := b.Unmarshal(raw); err != nil || b.Hash() != expected {
		return false
	}
	bh, ok := b.Header().(*block.Header)
	if !ok || bh == nil || bh.Number == nil || !bh.Number.IsUint64() || bh.Number.IsZero() {
		return false
	}
	var header block.Header
	if err := rlp.DecodeBytes(reply.Header, &header); err != nil || header.Hash() != b.Hash() {
		return false
	}
	if b.Number64().Uint64() > 1 {
		parent, err := n.replica.GetHeaderByHash(b.ParentHash())
		if err != nil || parent == nil {
			n.FetchBlockByHash(b.ParentHash())
			return false
		}
	}
	result, err := n.replica.Import(n.ctx, raw)
	if err != nil || !result.Durable {
		if !n.replica.BlockApplied(b.ParentHash(), b.Number64().Uint64()-1) {
			n.FetchBlockByHash(b.ParentHash())
		}
		return false
	}
	if reply.Certificate != nil {
		if reply.Certificate.BlockHash != b.Hash() {
			return false
		}
		if err := n.service.CommitCertificate(n.ctx, reply.Certificate); err != nil {
			return false
		}
	}
	return true
}

func (n *Network) catchupLoop() {
	defer n.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-n.ctx.Done():
			return
		case <-ticker.C:
		case <-n.syncWake:
		}
		for _, id := range n.p2p.Host().Network().Peers() {
			reply, raw, err := n.request(n.ctx, id, requestStatus, types.Hash{}, 0)
			if err != nil || len(raw) != 0 || len(reply.Header) == 0 {
				continue
			}
			var head block.Header
			if err := rlp.DecodeBytes(reply.Header, &head); err != nil || head.Number == nil || !head.Number.IsUint64() {
				continue
			}
			height := head.Number.Uint64()
			if height == 0 {
				if head.Hash() != n.replica.identity.GenesisHash {
					continue
				}
			} else {
				if reply.Certificate == nil || reply.Certificate.BlockHash != head.Hash() || n.replica.engine.VerifyPerformanceCommitQC(reply.Certificate) != nil {
					continue
				}
			}
			for current := n.maxHeight.Load(); height > current && !n.maxHeight.CompareAndSwap(current, height); current = n.maxHeight.Load() {
			}
			for count := 0; count < 64; count++ {
				local := n.replica.currentHeader()
				if local == nil || local.Number64().Uint64() >= height {
					break
				}
				number := local.Number64().Uint64() + 1
				reply, raw, err := n.request(n.ctx, id, requestNumber, types.Hash{}, number)
				if err != nil || len(raw) == 0 {
					break
				}
				var next block.Header
				if err := rlp.DecodeBytes(reply.Header, &next); err != nil || next.Number == nil || !next.Number.IsUint64() || next.Number.Uint64() != number || reply.Certificate == nil || reply.Certificate.BlockHash != next.Hash() || !n.receive(reply, raw, next.Hash()) {
					break
				}
			}
		}
	}
}
