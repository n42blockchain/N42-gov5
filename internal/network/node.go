// Copyright 2022-2026 The N42 Authors
// This file is part of the N42 library.
//
// The N42 library is free software: you can redistribute it and/or modify
// it under the terms of the GNU Lesser General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The N42 library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU Lesser General Public License for more details.
//
// You should have received a copy of the GNU Lesser General Public License
// along with the N42 library. If not, see <http://www.gnu.org/licenses/>.

package network

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/holiman/uint256"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	libpeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"google.golang.org/protobuf/proto"

	"github.com/n42blockchain/N42/api/protocol/msg_proto"
	"github.com/n42blockchain/N42/common"
	"github.com/n42blockchain/N42/common/hexutil"
	"github.com/n42blockchain/N42/common/message"
	"github.com/n42blockchain/N42/common/types"
	"github.com/n42blockchain/N42/log"
	"github.com/n42blockchain/N42/utils"
)

var (
	pingPongTimeout  = 1 * time.Second
	handshakeTimeout = 2 * time.Second
)

type NodeConfig struct {
	Stream     network.Stream
	Protocol   protocol.ID
	CacheCount int
}

type NodeOption func(*NodeConfig)

func WithStream(stream network.Stream) NodeOption {
	return func(config *NodeConfig) {
		config.Stream = stream
	}
}

func WithNodeProtocol(protocol protocol.ID) NodeOption {
	return func(config *NodeConfig) {
		config.Protocol = protocol
	}
}

func WithNodeCacheCount(count int) NodeOption {
	return func(config *NodeConfig) {
		config.CacheCount = count
	}
}

type Node struct {
	host.Host

	peer    *libpeer.AddrInfo
	streams map[string]network.Stream // send and read msg stream protocolID -> stream

	sync.RWMutex

	ctx    context.Context
	cancel context.CancelFunc

	msgCh chan message.IMessage

	service *Service

	config      *NodeConfig
	msgCallback map[message.MessageType]common.ConnHandler
	msgLock     sync.RWMutex

	isOK bool
}

func NewNode(ctx context.Context, h host.Host, s *Service, peer libpeer.AddrInfo, callback map[message.MessageType]common.ConnHandler, opts ...NodeOption) (*Node, error) {
	c, cancel := context.WithCancel(ctx)
	node := &Node{
		Host:        h,
		peer:        &peer,
		streams:     make(map[string]network.Stream),
		ctx:         c,
		cancel:      cancel,
		service:     s,
		msgCallback: callback,
	}

	config := NodeConfig{
		Protocol:   MSGProtocol,
		CacheCount: 100,
	}
	for _, opt := range opts {
		opt(&config)
	}

	var stream network.Stream
	var err error

	node.msgCh = make(chan message.IMessage, config.CacheCount)

	if config.Stream != nil {
		stream = config.Stream
	} else {
		stream, err = node.NewStream(node.ctx, peer.ID, config.Protocol)
		if err != nil {
			log.Error("failed to created stream to peer", "PeerID", peer.ID, "PeerAddress", peer.Addrs, "err", err)
			return nil, err
		}
	}

	node.Lock()
	defer node.Unlock()
	node.streams[string(config.Protocol)] = stream
	node.config = &config

	return node, nil
}

func (n *Node) Start() {
	n.isOK = true
	for _, s := range n.streams {
		go n.readData(s)
		go n.writeData(s)
	}
}

// ProcessHandshake reads the peer's genesisHash and currentHeight from the handshake message.
func (n *Node) ProcessHandshake(h *msg_proto.ProtocolHandshakeMessage) error {
	stream, ok := n.streams[string(n.config.Protocol)]
	if !ok {
		return fmt.Errorf("invalid protocol %s", n.config.Protocol)
	}

	errCh := make(chan error, 1)

	go func() {
		var header Header
		reader := bufio.NewReader(stream)
		msgType, payloadLen, err := header.Decode(reader)
		if err != nil {
			log.Error("failed to decode protocol handshake msg", "ID", n.peer.ID, "err", err)
			errCh <- err
			return
		}

		if msgType != message.MsgAppHandshake {
			log.Error("unexpected message type during handshake", "got", msgType, "want", message.MsgAppHandshake)
			errCh <- errBadMsgType
			return
		}

		payload := make([]byte, payloadLen)
		if _, err = io.ReadFull(reader, payload); err != nil {
			log.Error("failed to read payload", "payloadLen", payloadLen, "err", err)
			errCh <- err
			return
		}

		var m msg_proto.MessageData
		if err = proto.Unmarshal(payload, &m); err != nil {
			log.Error("failed to unmarshal message data", "peer", stream.Conn().ID(), "err", err)
			errCh <- err
			return
		}
		if err = proto.Unmarshal(m.Payload, h); err != nil {
			log.Error("failed to unmarshal handshake message", "err", err)
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case <-n.ctx.Done():
		return n.ctx.Err()
	case <-time.After(handshakeTimeout):
		log.Warn("Peer handshake timeout", "PeerID", n.peer.ID, "StreamId", stream.ID(), "ProtocolId", n.config.Protocol)
		_ = stream.Close()
		return errors.New("handshake timeout")
	case err := <-errCh:
		return err
	}
}

func (n *Node) AcceptHandshake(h *msg_proto.ProtocolHandshakeMessage, version string, genesisHash types.Hash, currentHeight *uint256.Int) error {
	if err := n.ProcessHandshake(h); err != nil {
		return err
	}
	return n.ProtocolHandshake(h, version, genesisHash, currentHeight, false)
}

// ProtocolHandshake sends the current peer's genesisHash and height to the remote peer.
func (n *Node) ProtocolHandshake(h *msg_proto.ProtocolHandshakeMessage, version string, genesisHash types.Hash, currentHeight *uint256.Int, process bool) error {
	phm := msg_proto.ProtocolHandshakeMessage{
		Version:       version,
		GenesisHash:   utils.ConvertHashToH256(genesisHash),
		CurrentHeight: utils.ConvertUint256IntToH256(currentHeight),
	}

	b, err := proto.Marshal(&phm)
	if err != nil {
		return err
	}

	msg := P2PMessage{
		MsgType: message.MsgAppHandshake,
		Payload: b,
	}

	s, ok := n.streams[string(n.config.Protocol)]
	if !ok {
		return fmt.Errorf("invalid protocol %s", n.config.Protocol)
	}

	if err := n.writeMsg(s, &msg); err != nil {
		return err
	}

	if process {
		return n.ProcessHandshake(h)
	}

	return nil
}

func (n *Node) SetHandler(msgType message.MessageType, handler common.ConnHandler) error {
	n.Lock()
	defer n.Unlock()

	if _, ok := n.msgCallback[msgType]; ok {
		return nil
	}

	n.msgCallback[msgType] = handler

	return nil
}

func (n *Node) readData(stream network.Stream) error {
	defer func() {
		log.Debug("readData loop exiting", "PeerID", stream.Conn().RemotePeer().String())
		if n.isOK {
			n.isOK = false
			stream.Close()
			n.cancel()
			n.service.removeCh <- stream.Conn().RemotePeer()
		}
	}()

	reader := bufio.NewReader(stream)
	for {
		select {
		case <-n.ctx.Done():
			return stream.Close()
		default:
		}

		var header Header
		msgType, payloadLen, err := header.Decode(reader)
		if err != nil {
			log.Error("failed to read msg", "StreamID", stream.ID(), "PeerID", stream.Conn().RemotePeer().String(), "err", err)
			return err
		}

		if payloadLen < 0 || payloadLen > MaxPayloadSize {
			log.Error("payload size exceeds limit", "StreamID", stream.ID(), "PeerID", stream.Conn().RemotePeer().String(), "payloadLen", payloadLen, "maxPayloadSize", MaxPayloadSize)
			return fmt.Errorf("payload size %d exceeds maximum allowed size %d", payloadLen, MaxPayloadSize)
		}
		ingressTrafficMeter.Mark(int64(payloadLen))

		payload := make([]byte, payloadLen)
		if _, err = io.ReadFull(reader, payload); err != nil {
			log.Error("failed to read payload", "payloadLen", payloadLen, "err", err)
			return err
		}

		var msg msg_proto.MessageData
		if err = proto.Unmarshal(payload, &msg); err != nil {
			log.Error("failed to unmarshal message data", "peer", stream.Conn().ID(), "err", err)
			return err
		}

		switch msgType {
		case message.MsgPingReq:
			log.Tracef("receive ping msg %s", string(msg.Payload))
			pong := P2PMessage{
				MsgType: message.MsgPingResp,
				Payload: []byte("pong"),
			}
			n.Write(&pong)
		case message.MsgPingResp:
			log.Tracef("receive pong msg %s", string(msg.Payload))
		default:
			n.msgLock.RLock()
			h, ok := n.msgCallback[msgType]
			n.msgLock.RUnlock()

			if ok {
				log.Debug("receive p2p msg", "msgType", msgType, "PeerID", n.ID(), "Content", hexutil.Encode(msg.Payload))
				if err := h(msg.Payload, n.ID()); err != nil {
					log.Error("failed to dispatch message", "msgType", msgType, "err", err)
					return err
				}
			} else {
				log.Warn("received unhandled message type", "msgType", msgType)
			}
		}
	}
}

var (
	errMakeMsgSignFailed  = errors.New("failed to sign message")
	errMakeMsgPubKeyFailed = errors.New("failed to get public key")
)

func (n *Node) makeMsg(payload []byte) (proto.Message, error) {
	key := n.Peerstore().PrivKey(n.Host.ID())
	sign, err := key.Sign(payload)
	if err != nil {
		log.Error("failed to sign message", "err", err)
		return nil, fmt.Errorf("%w: %v", errMakeMsgSignFailed, err)
	}

	nodePubKey, err := crypto.MarshalPublicKey(n.Peerstore().PubKey(n.Host.ID()))
	if err != nil {
		log.Error("failed to get public key", "id", n.ID(), "err", err)
		return nil, fmt.Errorf("%w: %v", errMakeMsgPubKeyFailed, err)
	}

	return &msg_proto.MessageData{
		Timestamp:  time.Now().Unix(),
		NodeID:     n.ID().String(),
		NodePubKey: nodePubKey,
		Sign:       sign,
		Payload:    payload,
	}, nil
}

func (n *Node) writeData(stream network.Stream) error {
	defer func() {
		if n.isOK {
			n.isOK = false
			n.cancel()
			n.service.removeCh <- stream.Conn().RemotePeer()
			stream.Close()
		}
		close(n.msgCh)
	}()

	ticker := time.NewTicker(pingPongTimeout)
	defer ticker.Stop()

	sendMsg := func(msg message.IMessage) error {
		var header Header
		payload, err := msg.Encode()
		if err != nil {
			log.Error("failed to encode msg", "err", err)
			return err
		}
		msgData, err := n.makeMsg(payload)
		if err != nil {
			log.Error("failed to make message", "err", err)
			return err
		}
		data, err := proto.Marshal(msgData)
		if err != nil {
			log.Error("failed to marshal message data", "err", err)
			return err
		}
		if err := header.Encode(stream, msg.Type(), int32(len(data))); err != nil {
			log.Error("failed to send header", "msgType", msg.Type(), "dataLen", len(data), "err", err)
			return err
		}
		if _, err := stream.Write(data); err != nil {
			log.Error("failed to send payload", "node", stream.Conn().ID(), "err", err)
			return err
		}
		egressTrafficMeter.Mark(int64(len(data)))
		return nil
	}

	for {
		select {
		case <-n.ctx.Done():
			return stream.Close()
		case m, ok := <-n.msgCh:
			if !ok {
				log.Error("message channel was closed")
				return errors.New("message channel closed")
			}
			if err := sendMsg(m); err != nil {
				return err
			}
		case <-ticker.C:
			ping := P2PMessage{
				MsgType: message.MsgPingReq,
				Payload: []byte("ping"),
			}
			if err := sendMsg(&ping); err != nil {
				return err
			}
			ticker.Reset(pingPongTimeout)
		}
	}
}

func (n *Node) writeMsg(stream network.Stream, msg message.IMessage) error {
	var header Header
	payload, err := msg.Encode()
	if err != nil {
		log.Error("failed to encode msg", "err", err)
		return err
	}
	msgData, err := n.makeMsg(payload)
	if err != nil {
		log.Error("failed to make message", "err", err)
		return err
	}
	data, err := proto.Marshal(msgData)
	if err != nil {
		log.Error("failed to marshal message data", "err", err)
		return err
	}
	buf := new(bytes.Buffer)
	if err := header.Encode(buf, msg.Type(), int32(len(data))); err != nil {
		log.Error("failed to encode header", "msgType", msg.Type(), "dataLen", len(data), "err", err)
		return err
	}
	buf.Write(data)
	if _, err := stream.Write(buf.Bytes()); err != nil {
		log.Error("failed to send payload", "node", stream.Conn().ID(), "err", err)
		return err
	}
	log.Debug("sent data to peer", "PeerID", stream.Conn().RemotePeer(), "ProtocolID", stream.Protocol(), "StreamID", stream.Conn().ID(), "Direction", stream.Stat().Direction)
	return nil
}

// writeTimeout is the maximum time to wait for a message to be written to the channel
const writeTimeout = 5 * time.Second

func (n *Node) Write(msg message.IMessage) error {
	if !n.isOK {
		return errors.New("node already closed")
	}

	select {
	case n.msgCh <- msg:
		return nil
	case <-time.After(writeTimeout):
		return fmt.Errorf("write timeout: channel blocked for %v", writeTimeout)
	case <-n.ctx.Done():
		return n.ctx.Err()
	}
}

func (n *Node) WriteMsg(messageType message.MessageType, payload []byte) error {
	msg := P2PMessage{
		MsgType: messageType,
		Payload: payload,
	}
	return n.Write(&msg)
}

func (n *Node) Close() error {
	n.Lock()
	defer n.Unlock()
	if n.isOK {
		n.isOK = false
		n.cancel()
	}
	return nil
}

func (n *Node) ID() libpeer.ID {
	return n.peer.ID
}

func (n *Node) ClearHandler(msgType message.MessageType) error {
	n.msgLock.Lock()
	defer n.msgLock.Unlock()
	if _, ok := n.msgCallback[msgType]; ok {
		delete(n.msgCallback, msgType)
		return nil
	}
	return fmt.Errorf("handler not found for message type %v", msgType)
}
